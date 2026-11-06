package vpnclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// The Fastly edge in front of /v1/oauth/token rejects HTTP 406 for the
// lowercase VPN scope (https://identity.mozilla.com/apps/vpn with
// client_id 5882386c6d801776) when the TLS ClientHello is Go's, even
// with a valid challenge cookie. The same request with Windows Schannel
// (curl.exe) reaches the origin (400/401 for a fake Hawk, 200 for a real
// one) — verified 2026-11 with curl vs Go on the same body. Login and
// verify_code pass with Go TLS, so only the OAuth token exchange needs
// this path. It uses curl.exe from System32 (present since Windows 10),
// which speaks Schannel TLS. The solved Fastly cookie is not needed on
// this path; curl without a cookie already reaches the origin.
func doOAuthViaCurl(ctx context.Context, tokenURL string, bodyJSON []byte, hawkHeader string) (*TokenResponse, int, []byte, error) {
	curlPath, err := exec.LookPath("curl.exe")
	if err != nil {
		if p, err2 := exec.LookPath("curl"); err2 == nil {
			curlPath = p
		} else {
			return nil, 0, nil, fmt.Errorf("curl not found: %w", err)
		}
	}
	args := []string{"-s", "-S", "--http1.1", "--connect-timeout", "15", "--max-time", "30", "-X", "POST", tokenURL}
	args = append(args, "-H", "User-Agent: "+fastlySolverUserAgent)
	args = append(args, "-H", "Content-Type: application/json")
	args = append(args, "-H", "Accept: application/json")
	if hawkHeader != "" {
		args = append(args, "-H", "Authorization: "+hawkHeader)
	}
	args = append(args, "--data-binary", "@-")
	args = append(args, "-w", "\n__HTTP_CODE:%{http_code}")

	started := time.Now()
	Debugf("[fxa] oauth via curl start curl=%q bodyLen=%d hasHawk=%v", curlPath, len(bodyJSON), hawkHeader != "")
	cmd := exec.CommandContext(ctx, curlPath, args...)
	cmd.Stdin = bytes.NewReader(bodyJSON)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		Debugf("[fxa] oauth via curl failed after %s: %v stderr=%s", time.Since(started).Round(time.Millisecond), err, truncate(strings.TrimSpace(stderr.String()), 512))
		return nil, 0, nil, fmt.Errorf("curl oauth: %w (stderr: %s)", err, truncate(strings.TrimSpace(stderr.String()), 256))
	}
	out := stdout.Bytes()
	// Split off the status marker appended by -w.
	idx := bytes.LastIndex(out, []byte("\n__HTTP_CODE:"))
	if idx < 0 {
		Debugf("[fxa] oauth via curl bad output len=%d stderr=%s", len(out), truncate(strings.TrimSpace(stderr.String()), 256))
		return nil, 0, nil, fmt.Errorf("curl oauth: missing status in output")
	}
	statusStr := strings.TrimSpace(string(out[idx+len("\n__HTTP_CODE:"):]))
	status, parseErr := strconv.Atoi(statusStr)
	if parseErr != nil {
		Debugf("[fxa] oauth via curl bad status %q", statusStr)
		return nil, 0, nil, fmt.Errorf("curl oauth: bad status %q", statusStr)
	}
	body := bytes.TrimSuffix(out[:idx], []byte("\r\n"))
	body = bytes.TrimSuffix(body, []byte("\n"))
	Debugf("[fxa] oauth via curl -> %d bodyLen=%d in %s", status, len(body), time.Since(started).Round(time.Millisecond))
	if status != 200 {
		Debugf("[fxa] oauth via curl error body=%s", truncate(string(body), 1024))
		return nil, status, body, newFxaAPIError(status, body)
	}
	var tok TokenResponse
	if err := json.Unmarshal(body, &tok); err != nil {
		Debugf("[fxa] oauth via curl parse failed: %v bodyLen=%d", err, len(body))
		return nil, status, body, fmt.Errorf("parsing token response: %w", err)
	}
	Debugf("[fxa] oauth via curl ok accessLen=%d refreshLen=%d expiresIn=%d scope=%q", len(tok.AccessToken), len(tok.RefreshToken), tok.ExpiresIn, tok.Scope)
	return &tok, status, body, nil
}
