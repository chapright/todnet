//go:build windows

package tun

import (
	"encoding/binary"
	"net"
	"time"
)

// In TUN mode every connection is addressed by IP, so the log could only ever
// say "43.156.86.196:443" — useless when the question is "which app is
// failing". The TLS ClientHello carries the hostname the client actually
// wants, so read it before splicing and put it in the log.

const (
	// A ClientHello is far smaller than this; the cap only bounds a hostile
	// or truncated stream.
	maxClientHelloBytes = 4096
	// Only peek at ports that speak TLS first. Other protocols (SMTP, FTP,
	// SSH) are server-first, and waiting on them would add latency for
	// nothing.
	sniPeekTimeout = 250 * time.Millisecond
)

// tlsFirstPorts are the ports where the client speaks first and the first
// bytes are a TLS record.
func tlsFirstPort(port uint16) bool {
	switch port {
	case 443, 8443, 9443, 2053, 2083, 2087, 2096:
		return true
	}
	return false
}

// peekServerName reads the start of the stream, extracts the SNI hostname if
// one is present, and returns a connection that replays every byte it read.
// The returned connection must always be used in place of the original.
func peekServerName(conn net.Conn, timeout time.Duration) (string, net.Conn) {
	if timeout <= 0 {
		timeout = sniPeekTimeout
	}
	_ = conn.SetReadDeadline(time.Now().Add(timeout))

	buf := make([]byte, 0, 1024)
	chunk := make([]byte, 512)
	var name string

	for len(buf) < maxClientHelloBytes {
		n, err := conn.Read(chunk)
		if n > 0 {
			buf = append(buf, chunk[:n]...)
			// Once the first byte is known not to be a TLS handshake there
			// is nothing to parse; stop immediately rather than waiting out
			// the deadline.
			if len(buf) > 0 && buf[0] != 0x16 {
				break
			}
			if parsed, ok := parseClientHelloServerName(buf); ok {
				name = parsed
				break
			}
			// A complete record that simply has no SNI (resumption, IP
			// literal, ECH) will never gain one.
			if done, _ := clientHelloComplete(buf); done {
				break
			}
		}
		if err != nil {
			break
		}
	}

	_ = conn.SetReadDeadline(time.Time{})
	if len(buf) == 0 {
		return "", conn
	}
	return name, &prefixConn{Conn: conn, prefix: buf}
}

// prefixConn replays bytes that were consumed while peeking.
type prefixConn struct {
	net.Conn
	prefix []byte
}

func (c *prefixConn) Read(p []byte) (int, error) {
	if len(c.prefix) > 0 {
		n := copy(p, c.prefix)
		c.prefix = c.prefix[n:]
		return n, nil
	}
	return c.Conn.Read(p)
}

// clientHelloComplete reports whether buf holds a whole ClientHello record.
func clientHelloComplete(buf []byte) (bool, bool) {
	if len(buf) < 5 {
		return false, false
	}
	if buf[0] != 0x16 {
		return true, false
	}
	recordLen := int(binary.BigEndian.Uint16(buf[3:5]))
	return len(buf) >= 5+recordLen, true
}

// parseClientHelloServerName returns the SNI hostname, if the buffer holds a
// complete TLS ClientHello that carries one.
func parseClientHelloServerName(buf []byte) (string, bool) {
	if len(buf) < 5 || buf[0] != 0x16 {
		return "", false
	}
	recordLen := int(binary.BigEndian.Uint16(buf[3:5]))
	if len(buf) < 5+recordLen {
		return "", false
	}
	b := buf[5 : 5+recordLen]

	// Handshake header: type(1) + length(3).
	if len(b) < 4 || b[0] != 0x01 {
		return "", false
	}
	hsLen := int(b[1])<<16 | int(b[2])<<8 | int(b[3])
	if len(b) < 4+hsLen {
		return "", false
	}
	b = b[4 : 4+hsLen]

	// client_version(2) + random(32)
	if len(b) < 34 {
		return "", false
	}
	b = b[34:]

	// session_id
	if len(b) < 1 {
		return "", false
	}
	sidLen := int(b[0])
	b = b[1:]
	if len(b) < sidLen {
		return "", false
	}
	b = b[sidLen:]

	// cipher_suites
	if len(b) < 2 {
		return "", false
	}
	csLen := int(binary.BigEndian.Uint16(b[:2]))
	b = b[2:]
	if len(b) < csLen {
		return "", false
	}
	b = b[csLen:]

	// compression_methods
	if len(b) < 1 {
		return "", false
	}
	cmLen := int(b[0])
	b = b[1:]
	if len(b) < cmLen {
		return "", false
	}
	b = b[cmLen:]

	// extensions
	if len(b) < 2 {
		return "", false
	}
	extLen := int(binary.BigEndian.Uint16(b[:2]))
	b = b[2:]
	if len(b) < extLen {
		return "", false
	}
	b = b[:extLen]

	for len(b) >= 4 {
		extType := binary.BigEndian.Uint16(b[:2])
		length := int(binary.BigEndian.Uint16(b[2:4]))
		b = b[4:]
		if len(b) < length {
			return "", false
		}
		if extType != 0x0000 { // server_name
			b = b[length:]
			continue
		}
		// server_name_list: length(2), then entries of type(1)+length(2)+name
		list := b[:length]
		if len(list) < 2 {
			return "", false
		}
		list = list[2:]
		for len(list) >= 3 {
			nameType := list[0]
			nameLen := int(binary.BigEndian.Uint16(list[1:3]))
			list = list[3:]
			if len(list) < nameLen {
				return "", false
			}
			if nameType == 0 && nameLen > 0 {
				return string(list[:nameLen]), true
			}
			list = list[nameLen:]
		}
		return "", false
	}
	return "", false
}
