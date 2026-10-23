package engine

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
)

// Level is the severity of a log line.
type Level string

const (
	LevelDebug Level = "DEBUG"
	LevelInfo  Level = "INFO"
	LevelWarn  Level = "WARN"
	LevelError Level = "ERROR"
)

// LogEntry is one structured log record surfaced to the UI.
type LogEntry struct {
	Time    time.Time `json:"time"`
	Level   Level     `json:"level"`
	Message string    `json:"message"`
}

var (
	logMu      sync.RWMutex
	logSinks   []func(LogEntry)
	logRing    []LogEntry
	logVerbose bool
	logFile    *os.File
)

const logRingSize = 500

// logFileMaxBytes caps the on-disk log so it cannot grow without bound. On
// startup a larger file is moved aside rather than truncated, so the previous
// session stays available for diagnosis.
const logFileMaxBytes = 2 << 20

// EnableFileLogging mirrors every log record to path. This is what makes a
// failure diagnosable after the fact: a windowed app has no console, so
// without this the only evidence of a crash is that the process vanished.
func EnableFileLogging(path string) error {
	if st, err := os.Stat(path); err == nil && st.Size() > logFileMaxBytes {
		_ = os.Rename(path, path+".old")
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}

	logMu.Lock()
	prev := logFile
	logFile = f
	logMu.Unlock()
	if prev != nil {
		_ = prev.Close()
	}
	return nil
}

// CloseFileLogging flushes and closes the on-disk log.
func CloseFileLogging() {
	logMu.Lock()
	f := logFile
	logFile = nil
	logMu.Unlock()
	if f != nil {
		_ = f.Sync()
		_ = f.Close()
	}
}

// SetLogSink installs the destination for log records, replacing any previous
// one. The most recent records are replayed so a late-attaching UI still sees
// history.
func SetLogSink(fn func(LogEntry)) {
	logMu.Lock()
	if fn == nil {
		logSinks = nil
	} else {
		logSinks = []func(LogEntry){fn}
	}
	replay := append([]LogEntry(nil), logRing...)
	logMu.Unlock()
	if fn != nil {
		for _, e := range replay {
			fn(e)
		}
	}
}

// AddLogSink adds a destination without disturbing existing ones. Headless mode
// uses this so printing to stdout does not stop the control API from serving
// the same records.
func AddLogSink(fn func(LogEntry)) {
	if fn == nil {
		return
	}
	logMu.Lock()
	logSinks = append(logSinks, fn)
	replay := append([]LogEntry(nil), logRing...)
	logMu.Unlock()
	for _, e := range replay {
		fn(e)
	}
}

// SetVerbose enables per-connection debug logging, which includes CONNECT
// target hostnames.
func SetVerbose(v bool) {
	logMu.Lock()
	logVerbose = v
	logMu.Unlock()
}

// RecentLogs returns a copy of the in-memory log buffer.
func RecentLogs() []LogEntry {
	logMu.RLock()
	defer logMu.RUnlock()
	return append([]LogEntry(nil), logRing...)
}

func logEvent(level Level, format string, args ...any) {
	if level == LevelDebug {
		logMu.RLock()
		verbose := logVerbose
		logMu.RUnlock()
		if !verbose {
			return
		}
	}
	entry := LogEntry{
		Time:    time.Now(),
		Level:   level,
		Message: strings.TrimRight(fmt.Sprintf(format, args...), "\n"),
	}
	logMu.Lock()
	logRing = append(logRing, entry)
	if len(logRing) > logRingSize {
		logRing = logRing[len(logRing)-logRingSize:]
	}
	sinks := append([]func(LogEntry){}, logSinks...)
	f := logFile
	if f != nil {
		fmt.Fprintf(f, "%s %-5s %s\n", entry.Time.Format("2006-01-02 15:04:05.000"), entry.Level, entry.Message)
	}
	logMu.Unlock()
	for _, sink := range sinks {
		sink(entry)
	}
}

// LogDebug logs at debug level; suppressed unless verbose logging is on.
func LogDebug(format string, args ...any) { logEvent(LevelDebug, format, args...) }

// LogInfo logs at info level.
func LogInfo(format string, args ...any) { logEvent(LevelInfo, format, args...) }

// LogWarn logs at warn level.
func LogWarn(format string, args ...any) { logEvent(LevelWarn, format, args...) }

// LogError logs at error level.
func LogError(format string, args ...any) { logEvent(LevelError, format, args...) }

/* ---------------- repeat suppression ---------------- */

// A tunnel carries hundreds of connections a minute, and a failing
// destination fails once per connection. Logging each one buries the single
// line that explains the problem — the log from the field had fifty
// identical "Destination IP Prohibited" lines and nothing else in it.
//
// LogThrottled keeps the first occurrence of a key verbatim, counts the rest,
// and emits an updated line once per window so a running total stays visible.
const throttleWindow = 30 * time.Second

type throttleEntry struct {
	last       time.Time
	suppressed int
	level      Level
	format     string
	args       []any
}

var (
	throttleMu sync.Mutex
	throttle   = map[string]*throttleEntry{}
)

// LogThrottled logs at most once per key per 30 seconds.
func LogThrottled(level Level, key, format string, args ...any) {
	throttleMu.Lock()
	e := throttle[key]
	if e == nil {
		e = &throttleEntry{}
		throttle[key] = e
	}
	now := time.Now()
	if !e.last.IsZero() && now.Sub(e.last) < throttleWindow {
		e.suppressed++
		e.level, e.format, e.args = level, format, args
		throttleMu.Unlock()
		return
	}
	suppressed := e.suppressed
	e.suppressed = 0
	e.last = now
	e.level, e.format, e.args = level, format, args
	throttleMu.Unlock()

	if suppressed > 0 {
		logEvent(level, "%s (+%d identical in the last %s)",
			fmt.Sprintf(format, args...), suppressed, throttleWindow)
		return
	}
	logEvent(level, format, args...)
}

// FlushThrottled reports what was suppressed and forgets the counters. It is
// called when a tunnel goes down so the totals of a session are not lost.
func FlushThrottled(reason string) {
	throttleMu.Lock()
	pending := make([]*throttleEntry, 0, len(throttle))
	var total int
	for key, e := range throttle {
		if e.suppressed > 0 {
			pending = append(pending, e)
			total += e.suppressed
		}
		delete(throttle, key)
	}
	throttleMu.Unlock()

	if total == 0 {
		return
	}
	if reason == "" {
		reason = "since the last report"
	}
	logEvent(LevelInfo, "suppressed %d repeated log lines %s", total, reason)
	for _, e := range pending {
		logEvent(LevelDebug, "  suppressed: %s", fmt.Sprintf(e.format, e.args...))
	}
}

