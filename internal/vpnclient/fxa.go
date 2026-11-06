package vpnclient

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/crypto/hkdf"
	"golang.org/x/crypto/pbkdf2"
)

const (
	firefoxClientID = "5882386c6d801776"
	oauthScope      = "profile https://identity.mozilla.com/apps/vpn"
	protocolVersion = "identity.mozilla.com/picl/v1/"
	pbkdf2Rounds    = 1000
	stretchedPWLen  = 32
	hkdfLen         = 32

	// maxChallengeAttempts bounds how many times an FxA call re-solves the
	// Fastly anti-bot challenge; with rotating-exit proxies a solved cookie
	// can be rejected when the API request leaves through another IP.
	maxChallengeAttempts = 5

	// fxaCallMinBudget is the minimum wall-clock budget an FxA call gets:
	// the caller's per-request timeout is often shorter than a full
	// challenge-solving round trip.
	fxaCallMinBudget = 90 * time.Second

	// verificationMethodEmail2FA tells FxA to deliver the sign-in
	// confirmation as a code emailed to the account instead of the
	// default confirmation link: the link redirects through
	// accounts.firefox.com, which warns about non-Firefox browsers and
	// cannot be followed from this CLI client.
	verificationMethodEmail2FA = "email-2fa"

	// fxaErrnoInvalidParameter is FxA's "Invalid parameter" errno; a
	// deployment that does not accept verificationMethod in the login
	// body answers with it, so we fall back to a plain login.
	fxaErrnoInvalidParameter = 107
)

type LoginResponse struct {
	SessionToken       string `json:"sessionToken"`
	UID                string `json:"uid"`
	Verified           bool   `json:"verified"`
	AuthAt             int64  `json:"authAt"`
	VerificationMethod string `json:"verificationMethod"`
	VerificationReason string `json:"verificationReason"`
}

type TokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
	Scope        string `json:"scope"`
	TokenType    string `json:"token_type"`
}

// fxaAPIError carries a non-2xx FxA response so callers can branch on the
// API errno (e.g. invalid parameter) instead of parsing error strings.
type fxaAPIError struct {
	Status int
	Errno  int
	Body   string
}

func (e *fxaAPIError) Error() string {
	if e.Errno != 0 {
		return fmt.Sprintf("FxA error (HTTP %d, errno %d): %s", e.Status, e.Errno, e.Body)
	}
	return fmt.Sprintf("FxA request failed (HTTP %d): %s", e.Status, e.Body)
}

func newFxaAPIError(status int, body []byte) *fxaAPIError {
	var parsed struct {
		Errno int `json:"errno"`
	}
	_ = json.Unmarshal(body, &parsed)
	return &fxaAPIError{Status: status, Errno: parsed.Errno, Body: string(body)}
}

func deriveAuthPW(email, password string) ([]byte, error) {
	// FxA derives the salt from the canonical (lower-cased, trimmed) address.
	// Using the address as typed means "User@Example.com" derives a different
	// authPW from the one the server expects for that account, and a correct
	// password is rejected as incorrect.
	normalizedEmail := strings.ToLower(strings.TrimSpace(email))
	salt := []byte(protocolVersion + "quickStretch:" + normalizedEmail)
	quickStretchedPW := pbkdf2.Key([]byte(password), salt, pbkdf2Rounds, stretchedPWLen, sha256.New)

	hkdfSalt := []byte{0x00}
	info := []byte(protocolVersion + "authPW")
	hkdfReader := hkdf.New(sha256.New, quickStretchedPW, hkdfSalt, info)
	authPW := make([]byte, hkdfLen)
	if _, err := io.ReadFull(hkdfReader, authPW); err != nil {
		return nil, err
	}
	return authPW, nil
}

func deriveHawkCredentials(tokenHex, context string) (id string, key []byte, err error) {
	tokenBytes, err := hex.DecodeString(tokenHex)
	if err != nil {
		return "", nil, fmt.Errorf("invalid token hex: %w", err)
	}
	info := []byte(protocolVersion + context)
	hkdfReader := hkdf.New(sha256.New, tokenBytes, nil, info)
	out := make([]byte, 3*32)
	if _, err := io.ReadFull(hkdfReader, out); err != nil {
		return "", nil, err
	}
	return hex.EncodeToString(out[:32]), out[32:64], nil
}

func hawkHeader(method, rawURL, hawkID string, hawkKey []byte, payload string) (string, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", err
	}

	nonce := make([]byte, 6)
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("generating nonce: %w", err)
	}
	nonceStr := hex.EncodeToString(nonce)
	ts := fmt.Sprintf("%d", time.Now().Unix())

	port := u.Port()
	if port == "" {
		if u.Scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	}

	var payloadHash string
	if payload != "" {
		h := sha256.New()
		h.Write([]byte("hawk.1.payload\napplication/json\n"))
		h.Write([]byte(payload))
		h.Write([]byte("\n"))
		payloadHash = hex.EncodeToString(h.Sum(nil))
	}

	normalized := strings.Join([]string{
		"hawk.1.header",
		ts,
		nonceStr,
		strings.ToUpper(method),
		u.RequestURI(),
		u.Hostname(),
		port,
		payloadHash,
		"",
		"",
	}, "\n")

	mac := hmac.New(sha256.New, hawkKey)
	mac.Write([]byte(normalized))
	macStr := hex.EncodeToString(mac.Sum(nil))

	header := fmt.Sprintf(`Hawk id="%s", ts="%s", nonce="%s", mac="%s"`, hawkID, ts, nonceStr, macStr)
	if payloadHash != "" {
		header += fmt.Sprintf(`, hash="%s"`, payloadHash)
	}
	return header, nil
}

// fxaDo sends an FxA API request built by newRequest, transparently solving
// the Fastly anti-bot client challenge when the edge answers HTTP 406 (empty
// body, no FxA error payload). The request factory is invoked again after
// each successful solve because request bodies are single-use readers.
//
// Every attempt is written to signin-debug.log (Debugf) with the method,
// host+path, User-Agent, auth presence and the response status/headers, so
// an "anti-bot check could not be completed" report can be diagnosed
// without the password or tokens.
func fxaDo(ctx context.Context, newRequest func() (*http.Request, error)) (*http.Response, error) {
	ctx, cancel := contextWithMinDeadline(ctx, fxaCallMinBudget)
	defer cancel()

	for attempt := 1; attempt <= maxChallengeAttempts; attempt++ {
		req, err := newRequest()
		if err != nil {
			Debugf("[fxa] attempt %d/%d newRequest failed: %v", attempt, maxChallengeAttempts, err)
			return nil, err
		}
		host := ""
		path := ""
		if req.URL != nil {
			host = req.URL.Host
			path = req.URL.Path
		}
		Debugf("[fxa] attempt %d/%d %s %s%s %s", attempt, maxChallengeAttempts, req.Method, host, path, SummarizeRequestHeaders(req.Header))
		started := time.Now()
		resp, err := doControlPlane(req)
		elapsed := time.Since(started).Round(time.Millisecond)
		if err != nil {
			Debugf("[fxa] attempt %d/%d %s %s%s transport error after %s: %v", attempt, maxChallengeAttempts, req.Method, host, path, elapsed, err)
			return nil, err
		}
		Debugf("[fxa] attempt %d/%d %s %s%s -> %d %s in %s", attempt, maxChallengeAttempts, req.Method, host, path, resp.StatusCode, SummarizeResponseHeaders(resp.Header), elapsed)
		if resp.StatusCode != http.StatusNotAcceptable {
			return resp, nil
		}
		io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		resp.Body.Close()

		Debugf("[fxa] attempt %d/%d got 406, solving Fastly challenge", attempt, maxChallengeAttempts)
		if err := solveFastlyChallenge(ctx); err != nil {
			Debugf("[fxa] attempt %d/%d challenge solve failed: %v", attempt, maxChallengeAttempts, err)
			return nil, fmt.Errorf("Fastly anti-bot challenge: %w", err)
		}
		Debugf("[fxa] attempt %d/%d challenge solved, retrying", attempt, maxChallengeAttempts)
	}
	Debugf("[fxa] still failing after %d attempts (HTTP 406)", maxChallengeAttempts)
	return nil, fmt.Errorf("Fastly anti-bot challenge still failing after %d attempts (HTTP 406)", maxChallengeAttempts)
}

func fxaLogin(ctx context.Context, email, password string) (*LoginResponse, error) {
	loginResp, err := fxaLoginAttempt(ctx, email, password, verificationMethodEmail2FA)
	if err != nil {
		var apiErr *fxaAPIError
		if errors.As(err, &apiErr) && apiErr.Errno == fxaErrnoInvalidParameter {
			return fxaLoginAttempt(ctx, email, password, "")
		}
		return nil, err
	}
	return loginResp, nil
}

func fxaLoginAttempt(ctx context.Context, email, password, verificationMethod string) (*LoginResponse, error) {
	Debugf("[fxa] login start email=%q verificationMethod=%q", email, verificationMethod)
	authPW, err := deriveAuthPW(email, password)
	if err != nil {
		Debugf("[fxa] login deriveAuthPW failed: %v", err)
		return nil, fmt.Errorf("deriving authPW: %w", err)
	}
	Debugf("[fxa] login authPW derived bytes=%d hexLen=%d (password and authPW never logged)", len(authPW), len(hex.EncodeToString(authPW)))

	body := map[string]string{
		"email":  email,
		"authPW": hex.EncodeToString(authPW),
	}
	if verificationMethod != "" {
		body["verificationMethod"] = verificationMethod
	}
	bodyJSON, _ := json.Marshal(body)

	loginURL := fxaAuthServer + "/account/login"

	resp, err := fxaDo(ctx, func() (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, loginURL, strings.NewReader(string(bodyJSON)))
		if err != nil {
			return nil, fmt.Errorf("creating login request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")
		applyFxaHeaders(req)
		return req, nil
	})
	if err != nil {
		Debugf("[fxa] login request failed: %v", err)
		return nil, fmt.Errorf("login request: %w", err)
	}
	defer resp.Body.Close()

	data, _ := io.ReadAll(resp.Body)
	Debugf("[fxa] login response status=%d bodyLen=%d %s", resp.StatusCode, len(data), SummarizeResponseHeaders(resp.Header))
	if resp.StatusCode != http.StatusOK {
		Debugf("[fxa] login error body=%s", truncate(string(data), 1024))
		return nil, newFxaAPIError(resp.StatusCode, data)
	}

	var loginResp LoginResponse
	if err := json.Unmarshal(data, &loginResp); err != nil {
		Debugf("[fxa] login parsing response failed: %v bodyLen=%d", err, len(data))
		return nil, fmt.Errorf("parsing login response: %w", err)
	}
	Debugf("[fxa] login ok verified=%v method=%q reason=%q sessionTokenPresent=%v sessionTokenLen=%d uidPresent=%v", loginResp.Verified, loginResp.VerificationMethod, loginResp.VerificationReason, loginResp.SessionToken != "", len(loginResp.SessionToken), loginResp.UID != "")
	return &loginResp, nil
}

// fxaVerifySession submits the email confirmation code for an unverified
// session created by fxaLogin; FxA sends the code to the account email at
// login time.
func fxaVerifySession(ctx context.Context, sessionToken, code string) error {
	Debugf("[fxa] verify start sessionTokenLen=%d codeLen=%d (values never logged)", len(sessionToken), len(code))
	hawkID, hawkKey, err := deriveHawkCredentials(sessionToken, "sessionToken")
	if err != nil {
		Debugf("[fxa] verify deriveHawk failed: %v", err)
		return fmt.Errorf("deriving hawk credentials: %w", err)
	}
	Debugf("[fxa] verify hawk idLen=%d keyLen=%d", len(hawkID), len(hawkKey))

	body := map[string]string{"code": code}
	bodyJSON, _ := json.Marshal(body)
	verifyURL := fxaAuthServer + "/session/verify_code"

	resp, err := fxaDo(ctx, func() (*http.Request, error) {
		authHeader, err := hawkHeader("POST", verifyURL, hawkID, hawkKey, string(bodyJSON))
		if err != nil {
			return nil, fmt.Errorf("generating hawk header: %w", err)
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, verifyURL, strings.NewReader(string(bodyJSON)))
		if err != nil {
			return nil, fmt.Errorf("creating verify code request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", authHeader)
		applyFxaHeaders(req)
		return req, nil
	})
	if err != nil {
		Debugf("[fxa] verify request failed: %v", err)
		return fmt.Errorf("verify code request: %w", err)
	}
	defer resp.Body.Close()

	data, _ := io.ReadAll(resp.Body)
	Debugf("[fxa] verify response status=%d bodyLen=%d %s", resp.StatusCode, len(data), SummarizeResponseHeaders(resp.Header))
	if resp.StatusCode != http.StatusOK {
		Debugf("[fxa] verify error body=%s", truncate(string(data), 1024))
		return fmt.Errorf("verification failed (HTTP %d): %s", resp.StatusCode, truncate(string(data), 256))
	}
	Debugf("[fxa] verify ok")
	return nil
}

func fxaOAuthToken(ctx context.Context, sessionToken string) (*TokenResponse, error) {
	Debugf("[fxa] oauth start grant=fxa-credentials client_id=%s scope=%q sessionTokenLen=%d", firefoxClientID, oauthScope, len(sessionToken))
	hawkID, hawkKey, err := deriveHawkCredentials(sessionToken, "sessionToken")
	if err != nil {
		Debugf("[fxa] oauth deriveHawk failed: %v", err)
		return nil, fmt.Errorf("deriving hawk credentials: %w", err)
	}
	Debugf("[fxa] oauth hawk idLen=%d keyLen=%d (key never logged)", len(hawkID), len(hawkKey))

	body := map[string]interface{}{
		"client_id":   firefoxClientID,
		"grant_type":  "fxa-credentials",
		"scope":       oauthScope,
		"access_type": "offline",
	}
	bodyJSON, _ := json.Marshal(body)

	tokenURL := fxaAuthServer + "/oauth/token"

	authHeader, err := hawkHeader("POST", tokenURL, hawkID, hawkKey, string(bodyJSON))
	if err != nil {
		Debugf("[fxa] oauth hawk failed: %v", err)
		return nil, fmt.Errorf("generating hawk header: %w", err)
	}
	// Go TLS is rejected by the edge for the lowercase VPN scope on this
	// endpoint (406 even with a solved cookie). curl.exe (Schannel) for the
	// same request reaches the origin (401 for a fake Hawk proves it —
	// the edge no longer blocks). Keep this on the Schannel path: only a
	// 406 from curl (edge still blocks even Schannel) or a transport
	// failure falls back to Go/challenge-solving. A 4xx from curl with a
	// JSON body (e.g. 401 errno 110 for a bad Hawk) is the origin's
	// definitive answer and must not be retried over Go, which would
	// only turn it into a misleading 406.
	if tok, status, _, err := doOAuthViaCurl(ctx, tokenURL, bodyJSON, authHeader); err == nil {
		Debugf("[fxa] oauth ok via curl status=%d accessTokenPresent=%v len=%d refreshPresent=%v len=%d expiresIn=%d scope=%q type=%q", status, tok.AccessToken != "", len(tok.AccessToken), tok.RefreshToken != "", len(tok.RefreshToken), tok.ExpiresIn, tok.Scope, tok.TokenType)
		return tok, nil
	} else if status != 0 && status != 406 {
		Debugf("[fxa] oauth via curl origin answered %d, not retrying over Go: %v", status, err)
		return nil, fmt.Errorf("oauth token request: %w", err)
	} else {
		Debugf("[fxa] oauth via curl failed (%v), falling back to Go challenge path", err)
	}
	resp, err := fxaDo(ctx, func() (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(string(bodyJSON)))
		if err != nil {
			return nil, fmt.Errorf("creating oauth token request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", authHeader)
		applyFxaHeaders(req)
		return req, nil
	})
	if err != nil {
		Debugf("[fxa] oauth request failed: %v", err)
		return nil, fmt.Errorf("oauth token request: %w", err)
	}
	defer resp.Body.Close()

	Debugf("[fxa] oauth response status=%d %s", resp.StatusCode, SummarizeResponseHeaders(resp.Header))
	if resp.StatusCode != http.StatusOK {
		body := readErrorBody(resp.Body)
		Debugf("[fxa] oauth error body=%s", truncate(body, 1024))
		return nil, newFxaAPIError(resp.StatusCode, []byte(body))
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		Debugf("[fxa] oauth reading response failed: %v", err)
		return nil, fmt.Errorf("reading token response: %w", err)
	}

	var tok TokenResponse
	if err := json.Unmarshal(data, &tok); err != nil {
		Debugf("[fxa] oauth parsing response failed: %v bodyLen=%d", err, len(data))
		return nil, fmt.Errorf("parsing token response: %w", err)
	}
	Debugf("[fxa] oauth ok accessTokenPresent=%v len=%d refreshPresent=%v len=%d expiresIn=%d scope=%q type=%q", tok.AccessToken != "", len(tok.AccessToken), tok.RefreshToken != "", len(tok.RefreshToken), tok.ExpiresIn, tok.Scope, tok.TokenType)
	return &tok, nil
}

func fxaRefreshToken(ctx context.Context, refreshToken string) (*TokenResponse, error) {
	Debugf("[fxa] refresh start grant=refresh_token client_id=%s scope=%q refreshTokenLen=%d", firefoxClientID, oauthScope, len(refreshToken))
	body := map[string]interface{}{
		"client_id":     firefoxClientID,
		"grant_type":    "refresh_token",
		"refresh_token": refreshToken,
		"scope":         oauthScope,
	}
	bodyJSON, _ := json.Marshal(body)

	tokenURL := fxaAuthServer + "/oauth/token"

	if tok, status, _, err := doOAuthViaCurl(ctx, tokenURL, bodyJSON, ""); err == nil {
		Debugf("[fxa] refresh ok via curl status=%d accessLen=%d refreshLen=%d expiresIn=%d scope=%q", status, len(tok.AccessToken), len(tok.RefreshToken), tok.ExpiresIn, tok.Scope)
		if tok.RefreshToken == "" {
			tok.RefreshToken = refreshToken
		}
		return tok, nil
	} else if status != 0 && status != 406 {
		Debugf("[fxa] refresh via curl origin answered %d, not retrying over Go: %v", status, err)
		return nil, fmt.Errorf("refresh request: %w", err)
	} else {
		Debugf("[fxa] refresh via curl failed (%v), falling back to Go challenge path", err)
	}
	resp, err := fxaDo(ctx, func() (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(string(bodyJSON)))
		if err != nil {
			return nil, fmt.Errorf("creating refresh request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")
		applyFxaHeaders(req)
		return req, nil
	})
	if err != nil {
		Debugf("[fxa] refresh request failed: %v", err)
		return nil, fmt.Errorf("refresh request: %w", err)
	}
	defer resp.Body.Close()

	Debugf("[fxa] refresh response status=%d %s", resp.StatusCode, SummarizeResponseHeaders(resp.Header))
	if resp.StatusCode != http.StatusOK {
		body := readErrorBody(resp.Body)
		Debugf("[fxa] refresh error body=%s", truncate(body, 1024))
		return nil, newFxaAPIError(resp.StatusCode, []byte(body))
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		Debugf("[fxa] refresh reading response failed: %v", err)
		return nil, fmt.Errorf("reading refresh response: %w", err)
	}

	var tok TokenResponse
	if err := json.Unmarshal(data, &tok); err != nil {
		Debugf("[fxa] refresh parsing response failed: %v bodyLen=%d", err, len(data))
		return nil, fmt.Errorf("parsing refresh response: %w", err)
	}
	// Refresh response may not include a new refresh_token; keep the old one
	if tok.RefreshToken == "" {
		tok.RefreshToken = refreshToken
	}
	return &tok, nil
}

func FxaLogin(ctx context.Context, email, password string) (*LoginResponse, error) {
	return fxaLogin(ctx, email, password)
}

func FxaVerifySession(ctx context.Context, sessionToken, code string) error {
	return fxaVerifySession(ctx, sessionToken, code)
}

func FxaOAuthToken(ctx context.Context, sessionToken string) (*TokenResponse, error) {
	return fxaOAuthToken(ctx, sessionToken)
}

func FxaRefreshToken(ctx context.Context, refreshToken string) (*TokenResponse, error) {
	return fxaRefreshToken(ctx, refreshToken)
}

// FetchProfileEmail reads the address behind an access token. The login flow
// asks for the "profile" scope, so this recovers the email for a session saved
// by a build that did not store it.
func FetchProfileEmail(ctx context.Context, accessToken string) (string, error) {
	if accessToken == "" {
		return "", errors.New("no access token")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, profileServer+"/v1/profile", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	applyFxaHeaders(req)

	resp, err := doControlPlane(req)
	if err != nil {
		return "", fmt.Errorf("profile request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return "", ErrTokenInvalid
	}
	if resp.StatusCode != http.StatusOK {
		return "", &GuardianHTTPError{
			Operation:  "profile",
			StatusCode: resp.StatusCode,
			Body:       readErrorBody(resp.Body),
		}
	}

	var profile struct {
		Email string `json:"email"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&profile); err != nil {
		return "", fmt.Errorf("parsing profile response: %w", err)
	}
	return strings.TrimSpace(profile.Email), nil
}
