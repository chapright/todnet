package engine

import (
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

const (
	// tunnelWriteBufSize bounds how far a writer may run ahead of the HTTP
	// transport. A pipe would serialize every write behind a goroutine
	// hand-off and cap upload throughput; this buffer absorbs bursts.
	tunnelWriteBufSize = 1 << 20

	// tunnelDrainTimeout is how long a half-closed tunnel may keep the other
	// direction alive. Two minutes was the old value and it is what made a
	// disconnect look stuck when a client vanished mid-transfer.
	tunnelDrainTimeout = 20 * time.Second
)

var (
	errTunnelDeadline = errors.New("deadlines are not supported for HTTP CONNECT tunnel streams")
)

// tunnelWriteBuffer is a bounded, concurrency-safe ring buffer used as the
// request body of a CONNECT tunnel.
type tunnelWriteBuffer struct {
	mu       sync.Mutex
	notEmpty *sync.Cond
	notFull  *sync.Cond
	buf      []byte
	n        int
	off      int
	closed   bool
	readErr  error
}

func newTunnelWriteBuffer(size int) *tunnelWriteBuffer {
	if size < 1 {
		size = tunnelWriteBufSize
	}
	b := &tunnelWriteBuffer{buf: make([]byte, size)}
	b.notEmpty = sync.NewCond(&b.mu)
	b.notFull = sync.NewCond(&b.mu)
	return b
}

// Read drains buffered data, blocking while the buffer is empty and still
// open. A read that reaches the end of the ring returns a short read rather
// than wrapping, which io.Reader permits.
func (b *tunnelWriteBuffer) Read(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	for b.n == 0 && b.readErr == nil && !b.closed {
		b.notEmpty.Wait()
	}
	if b.n == 0 {
		if b.readErr != nil {
			return 0, b.readErr
		}
		return 0, io.EOF
	}

	end := b.off + b.n
	if end > len(b.buf) {
		end = len(b.buf)
	}
	n := copy(p, b.buf[b.off:end])
	b.off = (b.off + n) % len(b.buf)
	b.n -= n
	b.notFull.Broadcast()
	return n, nil
}

// Write blocks only while the buffer is full, so bursts smaller than the
// buffer complete without waiting for the reader.
func (b *tunnelWriteBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	total := 0
	for len(p) > 0 {
		for b.n == len(b.buf) && b.readErr == nil && !b.closed {
			b.notFull.Wait()
		}
		if b.readErr != nil {
			return total, b.readErr
		}
		if b.closed {
			return total, io.ErrClosedPipe
		}

		space := len(b.buf) - b.n
		head := (b.off + b.n) % len(b.buf)
		writable := len(b.buf) - head
		if space < writable {
			writable = space
		}
		if writable > len(p) {
			writable = len(p)
		}
		copy(b.buf[head:], p[:writable])
		b.n += writable
		p = p[writable:]
		total += writable
		b.notEmpty.Broadcast()
	}
	return total, nil
}

// Close signals end of stream. Buffered data stays readable so a half-closed
// tunnel still flushes what the client already wrote.
func (b *tunnelWriteBuffer) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return nil
	}
	b.closed = true
	b.notEmpty.Broadcast()
	b.notFull.Broadcast()
	return nil
}

// failRead makes pending and future reads fail with err, unblocking the HTTP/2
// request body writer when the tunnel is torn down.
func (b *tunnelWriteBuffer) failRead(err error) {
	b.mu.Lock()
	if b.readErr == nil {
		b.readErr = err
	}
	b.closed = true
	b.notEmpty.Broadcast()
	b.notFull.Broadcast()
	b.mu.Unlock()
}

// tunnelConn adapts an HTTP CONNECT stream to net.Conn.
type tunnelConn struct {
	reader  io.ReadCloser
	writer  *tunnelWriteBuffer
	reqBody *tunnelWriteBuffer
	cancel  context.CancelFunc
	name    tunnelAddr

	// session lets Close deregister this stream, and lets the session reach
	// back in to fail it when the connection it rides on dies.
	session tunnelOwner

	closeOnce sync.Once
	readOnce  sync.Once
	writeOnce sync.Once
	closeErr  error
	readErr   error
	writeErr  error

	failMu  sync.Mutex
	failErr error
	failed  atomic.Bool
}

// tunnelOwner is the part of a session a tunnel needs: it can forget the
// stream, and it is what keeps a dead session from leaving streams blocked.
type tunnelOwner interface {
	untrackTunnel(*tunnelConn)
	failTunnels(error)
}

func (c *tunnelConn) Read(p []byte) (int, error) {
	n, err := c.reader.Read(p)
	if err != nil && c.failed.Load() {
		if fe := c.failure(); fe != nil {
			return n, fe
		}
	}
	return n, err
}

func (c *tunnelConn) Write(p []byte) (int, error) { return c.writer.Write(p) }

// fail unblocks the stream with a reason the caller can report. Closing the
// response body is what actually wakes a Read parked in the transport.
func (c *tunnelConn) fail(err error) {
	c.failMu.Lock()
	if c.failErr == nil {
		c.failErr = err
	}
	c.failMu.Unlock()
	c.failed.Store(true)
	if c.reqBody != nil {
		c.reqBody.failRead(err)
	}
	if c.reader != nil {
		_ = c.reader.Close()
	}
}

func (c *tunnelConn) failure() error {
	c.failMu.Lock()
	defer c.failMu.Unlock()
	return c.failErr
}

func (c *tunnelConn) Close() error {
	c.closeOnce.Do(func() {
		if c.session != nil {
			c.session.untrackTunnel(c)
		}
		writeErr := c.CloseWrite()
		readErr := c.CloseRead()
		if c.reqBody != nil {
			// Unblock the transport if it is still reading the request body,
			// and discard anything the client wrote but never got flushed.
			c.reqBody.failRead(io.ErrClosedPipe)
		}
		if c.cancel != nil {
			c.cancel()
		}
		if writeErr != nil {
			c.closeErr = writeErr
		} else {
			c.closeErr = readErr
		}
	})
	return c.closeErr
}

func (c *tunnelConn) CloseRead() error {
	c.readOnce.Do(func() {
		if c.reader != nil {
			c.readErr = c.reader.Close()
		}
	})
	return c.readErr
}

func (c *tunnelConn) CloseWrite() error {
	c.writeOnce.Do(func() {
		if c.writer != nil {
			c.writeErr = c.writer.Close()
		}
	})
	return c.writeErr
}

func (c *tunnelConn) LocalAddr() net.Addr              { return c.name }
func (c *tunnelConn) RemoteAddr() net.Addr             { return c.name }
func (c *tunnelConn) SetDeadline(time.Time) error      { return errTunnelDeadline }
func (c *tunnelConn) SetReadDeadline(time.Time) error  { return errTunnelDeadline }
func (c *tunnelConn) SetWriteDeadline(time.Time) error { return errTunnelDeadline }

type tunnelAddr string

func (a tunnelAddr) Network() string { return "tcp" }
func (a tunnelAddr) String() string  { return string(a) }

var copyBufferPool = sync.Pool{
	New: func() any { return make([]byte, 64*1024) },
}

// proxyBidirectional splices a client connection to an upstream tunnel,
// propagating half-closes in both directions. It returns the error that ended
// the relay, so the caller can say why a stream closed instead of only that it
// did.
func proxyBidirectional(clientConn, upstreamConn net.Conn, idleTimeout time.Duration) error {
	var activity func()
	done := make(chan struct{})

	if idleTimeout > 0 {
		activityCh := make(chan struct{}, 1)
		activity = func() {
			select {
			case activityCh <- struct{}{}:
			default:
			}
		}
		go closeIdleConns(clientConn, upstreamConn, idleTimeout, activityCh, done)
	} else {
		activity = func() {}
	}

	results := make(chan error, 2)
	go func() {
		err := copyConn(upstreamConn, clientConn, activity)
		if err == nil {
			closeWrite(upstreamConn)
		}
		results <- err
	}()

	go func() {
		err := copyConn(clientConn, upstreamConn, activity)
		if err == nil {
			closeWrite(clientConn)
		}
		results <- err
	}()

	firstErr := <-results
	if firstErr != nil {
		_ = clientConn.Close()
		_ = upstreamConn.Close()
	}
	var secondErr error
	if firstErr == nil {
		drainTimer := time.NewTimer(tunnelDrainTimeout)
		select {
		case secondErr = <-results:
			if !drainTimer.Stop() {
				<-drainTimer.C
			}
		case <-drainTimer.C:
			_ = clientConn.Close()
			_ = upstreamConn.Close()
			secondErr = <-results
		}
	} else {
		secondErr = <-results
	}
	if secondErr != nil {
		_ = clientConn.Close()
		_ = upstreamConn.Close()
	}
	close(done)
	if firstErr != nil {
		return firstErr
	}
	return secondErr
}

func closeIdleConns(clientConn, upstreamConn net.Conn, idleTimeout time.Duration, activity <-chan struct{}, done <-chan struct{}) {
	timer := time.NewTimer(idleTimeout)
	defer timer.Stop()
	for {
		select {
		case <-activity:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(idleTimeout)
		case <-timer.C:
			_ = clientConn.Close()
			_ = upstreamConn.Close()
			return
		case <-done:
			return
		}
	}
}

func copyConn(dst, src net.Conn, activity func()) error {
	buf := copyBufferPool.Get().([]byte)
	defer copyBufferPool.Put(buf)

	for {
		nr, readErr := src.Read(buf)
		if nr > 0 {
			activity()
			nw, writeErr := dst.Write(buf[:nr])
			if nw > 0 {
				activity()
			}
			if writeErr != nil {
				return writeErr
			}
			if nw != nr {
				return io.ErrShortWrite
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				return nil
			}
			return readErr
		}
	}
}

func closeWrite(conn net.Conn) {
	if closeWriter, ok := conn.(interface{ CloseWrite() error }); ok {
		_ = closeWriter.CloseWrite()
	}
}
