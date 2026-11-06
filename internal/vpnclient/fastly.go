package vpnclient

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
)

const (
	fxaSiteURL = "https://accounts.firefox.com/"

	// fastlySolveTimeout bounds one full challenge-solving attempt. It is
	// deliberately longer than a single API call because solving involves
	// several sequential requests (page, script, challenge answer,
	// post-back, probe).
	fastlySolveTimeout = 60 * time.Second

	// maxPostBackRounds caps follow-up challenge rounds returned by
	// fst-post-back ({ch, tok} responses).
	maxPostBackRounds = 3

	// FxA is a Firefox service, so the challenge solver and the FxA API
	// present the same Firefox browser identity. The Fastly edge binds
	// the solved cookie to the browser identity that earned it; a
	// non-browser User-Agent on the API request (for example
	// MozillaVPN/...) is rejected with HTTP 406 even with a valid
	// cookie. This matches the upstream firefox-vpn-client default
	// (Firefox 159).
	fastlySolverUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:159.0) Gecko/20100101 Firefox/159.0"
)

var (
	fastlyChallengePrefixRe = regexp.MustCompile(`/_fs-ch-[A-Za-z0-9]+`)
	fastlyInitCallRe        = regexp.MustCompile(`init\((\[[^\]]*\]),\s*"([^"]+)",\s*"([^"]+)"`)

	// powAlphabet is the character set the Fastly proof-of-work solver
	// brute-forces; it mirrors the challenge script's own alphabet.
	powAlphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"

	// challengeSolveMu serializes solving attempts: two concurrent solvers
	// would race on the control plane client's cookie jar and on the
	// exit-IP-bound cookie.
	challengeSolveMu sync.Mutex

	// errNoChallengePage signals that a host served regular content
	// instead of a Fastly challenge page.
	errNoChallengePage = errors.New("host did not serve a Fastly challenge page")

	// errChallengeNotAccepted signals that the solved cookie was rejected
	// on the probe request, typically because the exit IP rotated between
	// solving and probing.
	errChallengeNotAccepted = errors.New("challenge cookie not accepted on this exit IP")
)

type fastlyChallenge struct {
	Ty   string          `json:"ty"`
	Data json.RawMessage `json:"data"`
}

type fastlyPowData struct {
	Base    string `json:"base"`
	Expires string `json:"expires"`
	HMAC    string `json:"hmac"`
	Hash    string `json:"hash"`
}

type fastlyPostBack struct {
	Token string        `json:"token"`
	Data  []interface{} `json:"data"`
}

type fastlyPostBackResponse struct {
	Status string            `json:"status"`
	Ch     []fastlyChallenge `json:"ch"`
	Tok    string            `json:"tok"`
}

// solveFastlyChallenge performs the Fastly client-challenge handshake and
// installs the resulting anti-bot cookie into the control plane client's
// cookie jar. The solver shares the control plane client's transport so that
// the request exits through the same proxy (and therefore the same exit IP)
// as the subsequent API call: the issued cookie is bound to the exit IP that
// solved the challenge.
func solveFastlyChallenge(ctx context.Context) error {
	Debugf("[fastly] solve start ua=%q", fastlySolverUserAgent)
	started := time.Now()
	challengeSolveMu.Lock()
	defer challengeSolveMu.Unlock()

	ctx, cancel := contextWithMinDeadline(ctx, fastlySolveTimeout)
	defer cancel()

	// Prefer the host that actually rejected the API request, but fall
	// back to the site host: the API host usually serves no challenge
	// page at all and the cookie it needs is issued with Domain=firefox.com.
	var lastErr error
	for _, base := range []string{fxaAPIHost, strings.TrimRight(fxaSiteURL, "/")} {
		Debugf("[fastly] solve host %s", base)
		err := solveChallengeOnHost(ctx, base)
		if err == nil {
			Debugf("[fastly] solve ok host %s in %s", base, time.Since(started).Round(time.Millisecond))
			return nil
		}
		Debugf("[fastly] solve host %s failed: %v", base, err)
		lastErr = err
		if errors.Is(err, errNoChallengePage) {
			continue
		}
	}
	if lastErr == nil {
		lastErr = errNoChallengePage
	}
	Debugf("[fastly] solve failed after %s: %v", time.Since(started).Round(time.Millisecond), lastErr)
	return lastErr
}

// solveChallengeOnHost runs the whole challenge flow against one host using
// a throw-away client with a fresh cookie jar: re-solving with a client that
// already holds a valid challenge cookie fails because Fastly then serves
// the real page instead of the challenge page.
func solveChallengeOnHost(ctx context.Context, base string) error {
	jar, err := cookiejar.New(nil)
	if err != nil {
		return err
	}
	client := &http.Client{
		Transport: controlPlaneHTTPClient.Transport,
		Jar:       jar,
		Timeout:   fastlySolveTimeout,
	}
	pageURL := base + "/"

	page, isChallenge, err := fetchChallengePage(ctx, client, pageURL)
	if err != nil {
		Debugf("[fastly] host %s page fetch failed: %v", base, err)
		return err
	}
	Debugf("[fastly] host %s page len=%d isChallenge=%v", base, len(page), isChallenge)
	if !isChallenge {
		return errNoChallengePage
	}

	prefix := fastlyChallengePrefixRe.FindString(page)
	if prefix == "" {
		Debugf("[fastly] host %s challenge prefix not found pageLen=%d", base, len(page))
		return fmt.Errorf("challenge asset prefix not found on %s", base)
	}
	Debugf("[fastly] host %s prefix=%q", base, prefix)

	script, err := fetchText(ctx, client, base+prefix+"/script.js?reload=true")
	if err != nil {
		Debugf("[fastly] host %s script fetch failed: %v", base, err)
		return fmt.Errorf("fetching challenge script: %w", err)
	}
	Debugf("[fastly] host %s script len=%d", base, len(script))

	challenges, token, err := parseChallengeInit(script)
	if err != nil {
		Debugf("[fastly] host %s parse init failed: %v scriptLen=%d", base, err, len(script))
		return err
	}
	Debugf("[fastly] host %s init challenges=%d tokenLen=%d", base, len(challenges), len(token))
	for i, ch := range challenges {
		Debugf("[fastly] host %s challenge[%d] ty=%q dataLen=%d", base, i, ch.Ty, len(ch.Data))
	}

	for round := 0; round < maxPostBackRounds; round++ {
		Debugf("[fastly] host %s round %d/%d tokenLen=%d challenges=%d", base, round+1, maxPostBackRounds, len(token), len(challenges))
		answers := make([]interface{}, 0, len(challenges))
		for _, ch := range challenges {
			answer, err := answerChallenge(ctx, client, base+prefix, token, ch)
			if err != nil {
				Debugf("[fastly] host %s round %d answer %q failed: %v", base, round+1, ch.Ty, err)
				return err
			}
			Debugf("[fastly] host %s round %d answer %q ok", base, round+1, ch.Ty)
			answers = append(answers, answer)
		}

		postBack, err := json.Marshal(fastlyPostBack{Token: token, Data: answers})
		if err != nil {
			Debugf("[fastly] host %s round %d marshal post-back failed: %v", base, round+1, err)
			return err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+prefix+"/fst-post-back", strings.NewReader(string(postBack)))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json")
		req.Header.Set("User-Agent", fastlySolverUserAgent)
		req.Header.Set("Origin", baseOrigin(base+prefix))

		Debugf("[fastly] host %s round %d post-back bodyLen=%d", base, round+1, len(postBack))
		resp, err := client.Do(req)
		if err != nil {
			Debugf("[fastly] host %s round %d post-back transport failed: %v", base, round+1, err)
			return fmt.Errorf("challenge post-back: %w", err)
		}
		data, readErr := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		if readErr != nil {
			Debugf("[fastly] host %s round %d post-back read failed: %v", base, round+1, readErr)
			return readErr
		}
		Debugf("[fastly] host %s round %d post-back -> %d bodyLen=%d %s", base, round+1, resp.StatusCode, len(data), SummarizeResponseHeaders(resp.Header))
		if resp.StatusCode != http.StatusOK {
			Debugf("[fastly] host %s round %d post-back body=%s", base, round+1, truncate(string(data), 512))
			return fmt.Errorf("challenge post-back returned HTTP %d: %s", resp.StatusCode, truncate(string(data), 256))
		}

		var pbResp fastlyPostBackResponse
		if err := json.Unmarshal(data, &pbResp); err != nil {
			Debugf("[fastly] host %s round %d parse post-back failed: %v body=%s", base, round+1, err, truncate(string(data), 512))
			return fmt.Errorf("parsing post-back response: %w", err)
		}
		Debugf("[fastly] host %s round %d post-back status=%q next=%d tokLen=%d", base, round+1, pbResp.Status, len(pbResp.Ch), len(pbResp.Tok))
		if pbResp.Status == "success" {
			// Probe before installing the cookie: with rotating-exit
			// proxies the cookie may be bound to an IP the API call
			// will not reuse, and the probe catches that immediately.
			if _, stillChallenged, err := fetchChallengePage(ctx, client, pageURL); err != nil {
				Debugf("[fastly] host %s post-success probe failed: %v", base, err)
				return err
			} else if stillChallenged {
				Debugf("[fastly] host %s post-success probe still challenged (cookie not accepted)", base)
				return errChallengeNotAccepted
			}
			Debugf("[fastly] host %s post-success probe ok, installing cookies", base)
			return installChallengeCookies(jar, pageURL)
		}
		if len(pbResp.Ch) == 0 || pbResp.Tok == "" {
			Debugf("[fastly] host %s round %d unexpected post-back body=%s", base, round+1, truncate(string(data), 512))
			return fmt.Errorf("unexpected post-back response: %s", truncate(string(data), 256))
		}
		for i, ch := range pbResp.Ch {
			Debugf("[fastly] host %s next challenge[%d] ty=%q dataLen=%d", base, i, ch.Ty, len(ch.Data))
		}
		challenges, token = pbResp.Ch, pbResp.Tok
	}
	Debugf("[fastly] host %s did not complete within %d rounds", base, maxPostBackRounds)
	return fmt.Errorf("Fastly challenge did not complete within %d rounds", maxPostBackRounds)
}

// fetchChallengePage fetches pageURL and reports whether the response is a
// Fastly client-challenge page.
func fetchChallengePage(ctx context.Context, client *http.Client, pageURL string) (string, bool, error) {
	body, err := fetchText(ctx, client, pageURL)
	if err != nil {
		return "", false, err
	}
	isChallenge := strings.Contains(body, "/_fs-ch-") && strings.Contains(body, "Client Challenge")
	return body, isChallenge, nil
}

func fetchText(ctx context.Context, client *http.Client, rawURL string) (string, error) {
	host := rawURL
	if u, err := url.Parse(rawURL); err == nil {
		host = u.Host + u.Path
	}
	started := time.Now()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		Debugf("[fastly] GET %s newRequest failed: %v", host, err)
		return "", err
	}
	req.Header.Set("User-Agent", fastlySolverUserAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")

	resp, err := client.Do(req)
	if err != nil {
		Debugf("[fastly] GET %s transport failed after %s: %v", host, time.Since(started).Round(time.Millisecond), err)
		return "", err
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	resp.Body.Close()
	if err != nil {
		Debugf("[fastly] GET %s read failed: %v", host, err)
		return "", err
	}
	Debugf("[fastly] GET %s -> %d len=%d in %s %s", host, resp.StatusCode, len(data), time.Since(started).Round(time.Millisecond), SummarizeResponseHeaders(resp.Header))
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GET %s returned HTTP %d", rawURL, resp.StatusCode)
	}
	return string(data), nil
}

// parseChallengeInit extracts the challenge list and the challenge token
// from the trailing init([...], "token", "prefix", ...) call in the script.
func parseChallengeInit(script string) ([]fastlyChallenge, string, error) {
	matches := fastlyInitCallRe.FindAllStringSubmatch(script, -1)
	if len(matches) == 0 {
		return nil, "", fmt.Errorf("challenge init call not found in script")
	}
	m := matches[len(matches)-1]

	var challenges []fastlyChallenge
	if err := json.Unmarshal([]byte(m[1]), &challenges); err != nil {
		return nil, "", fmt.Errorf("parsing challenge list: %w", err)
	}
	if len(challenges) == 0 {
		return nil, "", fmt.Errorf("empty challenge list")
	}
	return challenges, m[2], nil
}

// answerChallenge produces the fst-post-back answer payload for one
// challenge task.
func answerChallenge(ctx context.Context, client *http.Client, prefixURL, token string, ch fastlyChallenge) (interface{}, error) {
	switch ch.Ty {
	case "pow":
		var d fastlyPowData
		if err := json.Unmarshal(ch.Data, &d); err != nil {
			return nil, fmt.Errorf("parsing pow challenge: %w", err)
		}
		answer, ok := solvePow(d.Base, d.Hash)
		if !ok {
			Debugf("[fastly] pow baseLen=%d hashLen=%d no solution", len(d.Base), len(d.Hash))
			return nil, fmt.Errorf("no proof-of-work solution found for base %q", d.Base)
		}
		Debugf("[fastly] pow baseLen=%d answerLen=%d hmacLen=%d expires=%q", len(d.Base), len(answer), len(d.HMAC), d.Expires)
		return map[string]interface{}{
			"ty":      "pow",
			"base":    d.Base,
			"answer":  answer,
			"hmac":    d.HMAC,
			"expires": d.Expires,
		}, nil
	case "pat":
		auth, err := fetchPAT(ctx, client, prefixURL, token)
		if err != nil {
			return nil, err
		}
		return map[string]interface{}{"ty": "pat", "auth": auth}, nil
	case "clientmetrics":
		return clientMetricsAnswer(), nil
	default:
		return nil, fmt.Errorf("unsupported Fastly challenge type %q (captcha cannot be solved automatically)", ch.Ty)
	}
}

// solvePow brute-forces the two-character suffix the challenge script looks
// for: SHA256(base + suffix) must equal the target hash. The search space
// is 62*62, so this is instant.
func solvePow(base, targetHex string) (string, bool) {
	target, err := hex.DecodeString(targetHex)
	if err != nil || len(target) != sha256.Size {
		return "", false
	}
	buf := make([]byte, len(base)+2)
	copy(buf, base)
	for i := 0; i < len(powAlphabet); i++ {
		buf[len(base)] = powAlphabet[i]
		for j := 0; j < len(powAlphabet); j++ {
			buf[len(base)+1] = powAlphabet[j]
			sum := sha256.Sum256(buf)
			if bytes.Equal(sum[:], target) {
				return string(buf[len(base):]), true
			}
		}
	}
	return "", false
}

// fetchPAT requests the Private Access Token for a "pat" challenge. A 401
// with a PrivateToken header means the client cannot mint a PAT (a browser
// platform feature); the challenge script aborts in that case and posts
// back an empty auth, which makes Fastly fall back to solvable challenges.
func fetchPAT(ctx context.Context, client *http.Client, prefixURL, token string) (string, error) {
	patURL := prefixURL + "/pat?token=" + url.QueryEscape(token)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, patURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "text/plain")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", fastlySolverUserAgent)
	req.Header.Set("Origin", baseOrigin(prefixURL))

	Debugf("[fastly] PAT POST tokenLen=%d", len(token))
	resp, err := client.Do(req)
	if err != nil {
		Debugf("[fastly] PAT transport failed: %v", err)
		return "", fmt.Errorf("PAT request: %w", err)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	resp.Body.Close()
	if err != nil {
		Debugf("[fastly] PAT read failed: %v", err)
		return "", err
	}
	Debugf("[fastly] PAT -> %d len=%d %s", resp.StatusCode, len(data), SummarizeResponseHeaders(resp.Header))
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusBadRequest {
		// Aborted PAT (servers answer 401 or 400 depending on client
		// fingerprint): empty auth triggers the PoW fallback server-side.
		Debugf("[fastly] PAT aborted (status %d), empty auth", resp.StatusCode)
		return "", nil
	}
	if resp.StatusCode != http.StatusOK {
		Debugf("[fastly] PAT body=%s", truncate(string(data), 512))
		return "", fmt.Errorf("PAT request returned HTTP %d: %s", resp.StatusCode, truncate(string(data), 256))
	}

	var out struct {
		Auth string `json:"auth"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		Debugf("[fastly] PAT parse failed: %v bodyLen=%d", err, len(data))
		return "", fmt.Errorf("parsing PAT response: %w", err)
	}
	if out.Auth == "" {
		Debugf("[fastly] PAT empty auth bodyLen=%d", len(data))
		return "", fmt.Errorf("empty PAT auth token")
	}
	Debugf("[fastly] PAT ok authLen=%d (value never logged)", len(out.Auth))
	return out.Auth, nil
}

// clientMetricsAnswer fabricates a benign browser-metrics payload; Mozilla's
// configuration currently issues only PAT/PoW challenges, so this path is a
// best-effort fallback.
func clientMetricsAnswer() map[string]interface{} {
	return map[string]interface{}{
		"ty":                   "clientmetrics",
		"webdriver":            false,
		"bot_detection_result": map[string]interface{}{"bot_detected": false, "bot_kind": nil},
		"browser_metrics":      map[string]interface{}{"client_data": "{}", "error_trace": nil},
		"detector_results":     map[string]interface{}{},
		"v":                    2,
	}
}

// installChallengeCookies copies the cookies earned by the solver into the
// control plane client's jar so subsequent FxA API calls carry them. The
// cookie is issued with Domain=firefox.com, which covers
// api.accounts.firefox.com.
func installChallengeCookies(solverJar http.CookieJar, pageURL string) error {
	parsed, err := url.Parse(pageURL)
	if err != nil {
		return err
	}
	cookies := solverJar.Cookies(parsed)
	Debugf("[fastly] install solver %s", SummarizeCookies(cookies))
	if len(cookies) == 0 {
		return fmt.Errorf("challenge completed but no cookies were issued")
	}

	if controlPlaneHTTPClient.Jar == nil {
		jar, err := cookiejar.New(nil)
		if err != nil {
			return err
		}
		controlPlaneHTTPClient.Jar = jar
	}
	apiURL, err := url.Parse(fxaAuthServer + "/")
	if err != nil {
		return err
	}
	controlPlaneHTTPClient.Jar.SetCookies(apiURL, cookies)
	parsed2, _ := url.Parse(fxaSiteURL)
	controlPlaneHTTPClient.Jar.SetCookies(parsed2, cookies)
	Debugf("[fastly] install control jar api %s site %s", SummarizeCookies(controlPlaneHTTPClient.Jar.Cookies(apiURL)), SummarizeCookies(controlPlaneHTTPClient.Jar.Cookies(parsed2)))
	return nil
}

// contextWithMinDeadline returns a context with at least d of deadline left.
// When the parent already has a longer deadline it is returned unchanged;
// otherwise the deadline is extended while the parent's cancellation still
// propagates.
func contextWithMinDeadline(ctx context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	if deadline, ok := ctx.Deadline(); ok && time.Until(deadline) >= d {
		return ctx, func() {}
	}
	extended, cancel := context.WithTimeout(context.WithoutCancel(ctx), d)
	watcherDone := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			cancel()
		case <-watcherDone:
		}
	}()
	return extended, func() {
		close(watcherDone)
		cancel()
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// baseOrigin derives the Origin header value for challenge requests from a
// "/_fs-ch-..." prefixed URL.
func baseOrigin(prefixURL string) string {
	if idx := strings.Index(prefixURL, "/_fs-ch-"); idx > 0 {
		return prefixURL[:idx]
	}
	return prefixURL
}
