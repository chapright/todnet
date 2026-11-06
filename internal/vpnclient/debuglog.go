package vpnclient

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"todnet/internal/appdir"
)

// Sign-in debugging writes to signin-debug.log under %APPDATA%\Todnet so a
// user can reproduce "Firefox's anti-bot check could not be completed" and
// hand the file to whoever is debugging, without having to enable verbose
// logging or assemble the report by hand.
//
// The file is separate from app.log on purpose: app.log is capped at 2MB and
// rotated on startup, while this file is the complete, ordered transcript of
// every FxA/Fastly request and challenge step for the last few sign-in
// attempts. It is always written (independent of engine verbose mode) so the
// user does not have to find a settings toggle before the first attempt.
//
// Redaction rules (see Debugf callers): never log the password, the derived
// authPW hex, session/refresh/access tokens, Hawk keys, PAT auth values, or
// cookie values. Log token/cookie presence, lengths, domains and expiry,
// request/response status, challenge types and error bodies (FxA error
// bodies contain only errno/message, no tokens). Email is logged as the
// existing app.log already logs it ("signing in as %s"); the user who shares
// this file controls that disclosure.
const debugLogFileName = "signin-debug.log"

const debugLogMaxBytes = 2 << 20

var (
	debugMu   sync.Mutex
	debugFile *os.File
	debugPath string
)

// DebugLogPath returns the path of the sign-in debug file. It is exported
// for diagnostics and for the startup line that tells the user where to find
// it.
func DebugLogPath() string {
	path, err := appdir.Path(debugLogFileName)
	if err != nil {
		return ""
	}
	return path
}

// DebugLogFileInfo returns the debug-log path and its size for diagnostics.
func DebugLogFileInfo() (path string, size int64) {
	path = DebugLogPath()
	if path == "" {
		return "", 0
	}
	if st, err := os.Stat(path); err == nil {
		return path, st.Size()
	}
	return path, 0
}

// FlushDebugLog forces pending writes to disk. Callers invoke it at the end
// of every sign-in attempt so the user can copy the file while the app is
// still running.
func FlushDebugLog() {
	debugMu.Lock()
	f := debugFile
	debugMu.Unlock()
	if f != nil {
		_ = f.Sync()
	}
}

// DebugLogTail returns the last maxBytes of the debug file, reading from
// disk so history from before a restart is included. Missing file returns "".
func DebugLogTail(maxBytes int64) string {
	path := DebugLogPath()
	if path == "" || maxBytes <= 0 {
		return ""
	}
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || st.Size() == 0 {
		return ""
	}
	offset := int64(0)
	if st.Size() > maxBytes {
		offset = st.Size() - maxBytes
		if _, err := f.Seek(offset, 0); err != nil {
			return ""
		}
		buf := make([]byte, 4096)
		n, _ := f.Read(buf)
		if idx := indexByteDebug(buf[:n], '\n'); idx >= 0 {
			// Only align to the next line when there is still content
			// after it; otherwise the tail would be empty (single
			// short line, or offset already near EOF).
			if offset+int64(idx)+1 < st.Size() {
				offset += int64(idx) + 1
			}
		}
		if _, err := f.Seek(offset, 0); err != nil {
			return ""
		}
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return ""
	}
	return string(data)
}

func indexByteDebug(b []byte, c byte) int {
	for i, v := range b {
		if v == c {
			return i
		}
	}
	return -1
}

func openDebugFileLocked() error {
	path, err := appdir.Path(debugLogFileName)
	if err != nil {
		return err
	}
	// Reopen when the profile directory changes (tests point APPDATA at a
	// temp dir). In production the path never changes.
	if debugFile != nil && debugPath == path {
		return nil
	}
	if debugFile != nil {
		_ = debugFile.Close()
		debugFile = nil
		debugPath = ""
	}
	if st, statErr := os.Stat(path); statErr == nil && st.Size() > debugLogMaxBytes {
		_ = os.Rename(path, path+".old")
	}
	_ = os.MkdirAll(dirOf(path), 0o700)
	f, openErr := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if openErr != nil {
		return openErr
	}
	debugFile = f
	debugPath = path
	return nil
}

func dirOf(path string) string {
	if i := strings.LastIndex(path, `\`); i >= 0 {
		return path[:i]
	}
	if i := strings.LastIndex(path, `/`); i >= 0 {
		return path[:i]
	}
	return "."
}

// Debugf appends one timestamped line to signin-debug.log. It never panics
// when the file cannot be opened (headless tests, read-only profile): the
// sign-in itself must not fail because the debug file did not.
// Callers pass only redacted values (see the package comment).
func Debugf(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	line := fmt.Sprintf("%s %s\n", time.Now().Format("2006-01-02 15:04:05.000"), strings.TrimRight(msg, "\n"))
	debugMu.Lock()
	err := openDebugFileLocked()
	if err != nil {
		debugMu.Unlock()
		return
	}
	_, _ = debugFile.WriteString(line)
	debugMu.Unlock()
}

// SummarizeCookies renders a redacted cookie list for the debug log:
// name, domain, path, max-age and value length. Values are never included.
func SummarizeCookies(cookies []*http.Cookie) string {
	if len(cookies) == 0 {
		return "cookies=0"
	}
	parts := make([]string, 0, len(cookies))
	for _, c := range cookies {
		parts = append(parts, fmt.Sprintf("%s(domain=%q path=%q maxAge=%d valueLen=%d)", c.Name, c.Domain, c.Path, c.MaxAge, len(c.Value)))
	}
	return strings.Join(parts, " ")
}

// SummarizeSetCookies renders the same without values, for Set-Cookie headers.
func SummarizeSetCookies(values []string) string {
	if len(values) == 0 {
		return "setCookies=0"
	}
	parts := make([]string, 0, len(values))
	for _, raw := range values {
		name := raw
		if i := strings.Index(raw, "="); i >= 0 {
			name = raw[:i]
		}
		if i := strings.Index(name, ";"); i >= 0 {
			name = name[:i]
		}
		parts = append(parts, fmt.Sprintf("%s(len=%d)", strings.TrimSpace(name), len(raw)))
	}
	return fmt.Sprintf("setCookies=%d [%s]", len(values), strings.Join(parts, " "))
}

// SummarizeResponseHeaders keeps the fields that distinguish an edge 406
// from an origin 4xx: Server (nginx = origin, absent = edge), Via, X-Cache
// and X-Served-By. Other headers are omitted to keep the log readable.
func SummarizeResponseHeaders(h http.Header) string {
	return fmt.Sprintf("server=%q via=%q xCache=%q servedBy=%q %s",
		h.Get("Server"), h.Get("Via"), h.Get("X-Cache"), h.Get("X-Served-By"),
		SummarizeSetCookies(h.Values("Set-Cookie")))
}

// SummarizeRequestHeaders keeps the identity fields the edge validates.
// Authorization is reduced to presence + length.
func SummarizeRequestHeaders(h http.Header) string {
	auth := h.Get("Authorization")
	authLen := 0
	if auth != "" {
		// "Bearer xxx" / "Hawk id=..., mac=..." — only the scheme and
		// length are safe to log.
		if i := strings.Index(auth, " "); i >= 0 {
			authLen = len(auth)
			auth = auth[:i] + " (len=" + fmt.Sprintf("%d", authLen) + ")"
		} else {
			authLen = len(auth)
			auth = fmt.Sprintf("(len=%d)", authLen)
		}
	}
	return fmt.Sprintf("ua=%q contentType=%q accept=%q auth=%q", h.Get("User-Agent"), h.Get("Content-Type"), h.Get("Accept"), auth)
}
