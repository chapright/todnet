package vpnclient

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const tokenFileName = "tokens.json"

type SavedTokens struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token"`
	Scope        string    `json:"scope"`
	ObtainedAt   time.Time `json:"obtained_at"`
	ExpiresIn    int       `json:"expires_in"`
	// Email is kept next to the tokens so the UI can still name the account
	// after a restart. It is not part of the token exchange, hence omitempty.
	Email string `json:"email,omitempty"`
}

func (s *SavedTokens) AccessTokenValid() bool {
	if s.AccessToken == "" {
		return false
	}
	expiry := s.ObtainedAt.Add(time.Duration(s.ExpiresIn) * time.Second)
	return time.Now().Before(expiry.Add(-60 * time.Second))
}

// TokenFilePath reports where the refresh token is persisted. On Windows this
// is %APPDATA%\Todnet\tokens.json, which is per-user and survives upgrades.
func tokenFilePath() string {
	dir, err := configDir()
	if err != nil {
		return tokenFileName
	}
	return filepath.Join(dir, tokenFileName)
}

func loadTokens() (*SavedTokens, error) {
	data, err := os.ReadFile(tokenFilePath())
	if err != nil {
		return nil, err
	}
	var tokens SavedTokens
	if err := json.Unmarshal(data, &tokens); err != nil {
		return nil, err
	}
	return &tokens, nil
}

func saveTokens(tok *TokenResponse) error {
	saved := SavedTokens{
		AccessToken:  tok.AccessToken,
		RefreshToken: tok.RefreshToken,
		Scope:        tok.Scope,
		ObtainedAt:   time.Now(),
		ExpiresIn:    tok.ExpiresIn,
	}
	data, err := json.MarshalIndent(saved, "", "  ")
	if err != nil {
		return err
	}
	path := tokenFilePath()
	if err := writePrivateFile(path, data); err != nil {
		return fmt.Errorf("saving tokens to %s: %w", path, err)
	}
	return nil
}

func writePrivateFile(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)

	if err := tmp.Chmod(0600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}

	if err := os.Rename(tmpPath, path); err != nil {
		if removeErr := os.Remove(path); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			return err
		}
		if retryErr := os.Rename(tmpPath, path); retryErr != nil {
			return retryErr
		}
	}
	return os.Chmod(path, 0600)
}

func deleteTokens() {
	os.Remove(tokenFilePath())
}

func TokenFilePath() string {
	return tokenFilePath()
}

func LoadTokens() (*SavedTokens, error) {
	return loadTokens()
}

// SaveTokens persists a refreshed token for the active account. Callers are on
// the connect path, which only ever runs for whichever account is active, so
// the store resolves the owner rather than threading an email through the
// engine.
func SaveTokens(tok *TokenResponse) error {
	store, err := LoadAccounts()
	if err != nil {
		return err
	}
	acc := store.ActiveAccount()
	if acc == nil {
		// No account list yet (first run of an older profile): keep the legacy
		// single-account file so the migration still finds the session.
		return saveTokens(tok)
	}
	applyToken(acc, tok)
	return store.Save()
}

// ApplyToken copies a token response onto an account, keeping the previous
// refresh token when the server does not rotate it.
func ApplyToken(acc *Account, tok *TokenResponse) {
	applyToken(acc, tok)
}

func applyToken(acc *Account, tok *TokenResponse) {
	refresh := tok.RefreshToken
	if refresh == "" {
		refresh = acc.Tokens.RefreshToken
	}
	acc.Tokens = SavedTokens{
		AccessToken:  tok.AccessToken,
		RefreshToken: refresh,
		Scope:        tok.Scope,
		ObtainedAt:   time.Now(),
		ExpiresIn:    tok.ExpiresIn,
		Email:        acc.Email,
	}
}

func DeleteTokens() {
	deleteTokens()
}
