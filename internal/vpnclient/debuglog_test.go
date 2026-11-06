package vpnclient

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The sign-in debug file is where a user shares the evidence for an
// "anti-bot check could not be completed" failure. It must be written
// without requiring verbose mode, and it must never contain the password,
// authPW, tokens, or cookie values — only their lengths.
func TestDebugLogIsWrittenRedacted(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("APPDATA", dir)
	// appdir caches nothing, so the new APPDATA is picked up directly.
	// Reset the handle so the test does not reuse the developer profile.
	debugMu.Lock()
	if debugFile != nil {
		_ = debugFile.Close()
		debugFile = nil
		debugPath = ""
	}
	debugMu.Unlock()
	t.Cleanup(func() {
		debugMu.Lock()
		if debugFile != nil {
			_ = debugFile.Close()
			debugFile = nil
			debugPath = ""
		}
		debugMu.Unlock()
	})

	Debugf("[fxa] test email=%q passwordLen=%d authPWLen=%d", "a@example.com", 10, 32)
	FlushDebugLog()

	path, size := DebugLogFileInfo()
	if path == "" || size == 0 {
		t.Fatalf("debug log not written: path=%q size=%d", path, size)
	}
	if filepath.Base(path) != debugLogFileName {
		t.Fatalf("debug path = %q, want %q", path, debugLogFileName)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	txt := string(data)
	if !strings.Contains(txt, "[fxa] test email=") {
		t.Fatalf("debug line missing: %q", txt)
	}
	if strings.Contains(txt, "secret") {
		t.Fatalf("debug log leaked a secret: %q", txt)
	}
	tail := DebugLogTail(1 << 10)
	if !strings.Contains(tail, "[fxa] test") {
		t.Fatalf("DebugLogTail missed the line: %q", tail)
	}
	if got := DebugLogTail(64); got == "" {
		t.Fatal("DebugLogTail(64) should return content, got empty")
	}
}

func TestSummariesNeverIncludeValues(t *testing.T) {
	secret := "super-secret-value-1234567890"
	h := http.Header{}
	h.Set("User-Agent", "Mozilla/5.0")
	h.Set("Content-Type", "application/json")
	h.Set("Accept", "application/json")
	h.Set("Authorization", "Bearer "+secret)
	if got := SummarizeRequestHeaders(h); strings.Contains(got, secret) {
		t.Fatalf("request summary leaked Authorization value: %q", got)
	} else if !strings.Contains(got, "(len=") {
		t.Fatalf("request summary should record auth length: %q", got)
	}

	respH := http.Header{}
	respH.Set("Server", "nginx")
	respH.Set("Set-Cookie", "_fs_ch_cp_x="+secret+"; Domain=firefox.com")
	if got := SummarizeResponseHeaders(respH); strings.Contains(got, secret) {
		t.Fatalf("response summary leaked Set-Cookie value: %q", got)
	} else if !strings.Contains(got, "setCookies=1") {
		t.Fatalf("response summary should count cookies: %q", got)
	}

	cookies := []*http.Cookie{{Name: "_fs_ch_cp_x", Value: secret, Domain: "firefox.com"}}
	if got := SummarizeCookies(cookies); strings.Contains(got, secret) {
		t.Fatalf("cookie summary leaked value: %q", got)
	} else if !strings.Contains(got, "valueLen=") {
		t.Fatalf("cookie summary should record value length: %q", got)
	}
}
