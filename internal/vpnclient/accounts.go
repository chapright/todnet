package vpnclient

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"todnet/internal/appdir"
)

const accountsFileName = "accounts.json"

// Account is one saved Firefox account. Tokens are kept per account so
// switching does not require signing in again, which is the whole point: an
// account that has burned through its monthly allowance can be parked and
// another one used, then picked back up next month.
type Account struct {
	Email      string `json:"email"`
	UID        int    `json:"uid,omitempty"`
	Subscribed bool   `json:"subscribed,omitempty"`
	MaxBytes   int64  `json:"max_bytes,omitempty"`

	// Allowance as last observed from the Guardian proxy-pass headers. It is
	// only knowable by asking the server, so it is a snapshot with a time.
	QuotaLeft int64     `json:"quota_left,omitempty"`
	QuotaMax  int64     `json:"quota_max,omitempty"`
	QuotaAt   time.Time `json:"quota_at,omitempty"`

	AddedAt  time.Time `json:"added_at"`
	LastUsed time.Time `json:"last_used,omitempty"`

	Tokens SavedTokens `json:"tokens"`
}

// Label is what the UI shows when an account predates email persistence.
func (a *Account) Label() string {
	if a.Email != "" {
		return a.Email
	}
	return "Firefox account"
}

// AccountStore is the on-disk list of accounts plus the active pointer.
type AccountStore struct {
	Active   string    `json:"active"`
	Accounts []Account `json:"accounts"`
}

// configDirOverride lets tests point the store at a temporary directory
// instead of the real %APPDATA%\Todnet profile.
var configDirOverride string

func configDir() (string, error) {
	if configDirOverride != "" {
		return configDirOverride, nil
	}
	return appdir.Dir()
}

func accountsFilePath() string {
	dir, err := configDir()
	if err != nil {
		return accountsFileName
	}
	return filepath.Join(dir, accountsFileName)
}

// LoadAccounts reads the account list, migrating the single-account
// tokens.json layout from older builds the first time it runs.
func LoadAccounts() (*AccountStore, error) {
	path := accountsFilePath()
	data, err := os.ReadFile(path)
	if err == nil {
		var store AccountStore
		if err := json.Unmarshal(data, &store); err != nil {
			return nil, fmt.Errorf("parsing %s: %w", path, err)
		}
		store.normalize()
		return &store, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}

	store := &AccountStore{}
	saved, mErr := loadTokens()
	if mErr != nil || saved == nil || (saved.AccessToken == "" && saved.RefreshToken == "") {
		return store, nil
	}
	store.Accounts = []Account{{
		Email:   saved.Email,
		AddedAt: time.Now(),
		Tokens:  *saved,
	}}
	store.Active = store.Accounts[0].Email
	if wErr := store.Save(); wErr != nil {
		// Leave tokens.json alone: it is still the only copy of the session.
		return store, fmt.Errorf("migrating tokens.json: %w", wErr)
	}
	// The same credentials now live in accounts.json; keeping a second copy
	// on disk only widens the blast radius if the profile leaks.
	deleteTokens()
	return store, nil
}

// Save writes the store with the same private permissions as the token file.
func (s *AccountStore) Save() error {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return writePrivateFile(accountsFilePath(), data)
}

// Find returns a pointer into the slice so callers can update the account in
// place; it is nil when the email is unknown.
func (s *AccountStore) Find(email string) *Account {
	for i := range s.Accounts {
		if strings.EqualFold(s.Accounts[i].Email, email) {
			return &s.Accounts[i]
		}
	}
	return nil
}

// Active returns the active account, or nil when there is none. An empty
// Active string is not "nothing active": an account migrated from a build that
// did not store emails has an empty address, and it must still resolve.
func (s *AccountStore) ActiveAccount() *Account {
	if s.Active == "" && len(s.Accounts) == 0 {
		return nil
	}
	return s.Find(s.Active)
}

// Upsert adds the account or merges it into the existing entry. Fields the
// caller does not know (allowance, plan, original add time) are preserved
// rather than wiped by a re-login.
func (s *AccountStore) Upsert(acc Account) {
	// An account imported from an older build may have no email; adopt the
	// real address instead of leaving a nameless duplicate behind.
	if acc.Email != "" && acc.Tokens.RefreshToken != "" {
		for i := range s.Accounts {
			if s.Accounts[i].Email == "" &&
				s.Accounts[i].Tokens.RefreshToken == acc.Tokens.RefreshToken {
				s.Accounts[i] = mergeAccount(s.Accounts[i], acc)
				if s.Active == "" {
					s.Active = acc.Email
				}
				return
			}
		}
	}

	for i := range s.Accounts {
		if strings.EqualFold(s.Accounts[i].Email, acc.Email) {
			s.Accounts[i] = mergeAccount(s.Accounts[i], acc)
			return
		}
	}
	if acc.AddedAt.IsZero() {
		acc.AddedAt = time.Now()
	}
	s.Accounts = append(s.Accounts, acc)
}

func mergeAccount(prev, next Account) Account {
	if next.AddedAt.IsZero() {
		next.AddedAt = prev.AddedAt
	}
	if next.UID == 0 {
		next.UID = prev.UID
	}
	if !next.Subscribed {
		next.Subscribed = prev.Subscribed
	}
	if next.MaxBytes == 0 {
		next.MaxBytes = prev.MaxBytes
	}
	if next.QuotaAt.IsZero() {
		next.QuotaLeft, next.QuotaMax, next.QuotaAt = prev.QuotaLeft, prev.QuotaMax, prev.QuotaAt
	}
	if next.LastUsed.IsZero() {
		next.LastUsed = prev.LastUsed
	}
	return next
}

// AdoptEmail gives a nameless account the real address it was saved without.
// If another account already uses that address the two are merged, because
// they are the same account stored twice — which is exactly what happens when
// someone signs in again to a session carried over from an older build.
func (s *AccountStore) AdoptEmail(email string) bool {
	email = strings.TrimSpace(email)
	if email == "" {
		return false
	}

	idx := -1
	for i := range s.Accounts {
		if s.Accounts[i].Email == "" {
			idx = i
			break
		}
	}
	if idx < 0 {
		return false
	}

	// An empty Active names the nameless account, since it is the only entry
	// without an address to point at.
	wasActive := s.Active == ""

	adopted := s.Accounts[idx]
	adopted.Email = email
	adopted.Tokens.Email = email

	if existing := s.Find(email); existing != nil {
		merged := mergeAccount(*existing, adopted)
		// Both sides hold tokens for the same account; keep the fresher set.
		if existing.Tokens.ObtainedAt.After(adopted.Tokens.ObtainedAt) {
			merged.Tokens = existing.Tokens
			merged.Tokens.Email = email
		}
		*existing = merged
		s.Accounts = append(s.Accounts[:idx], s.Accounts[idx+1:]...)
	} else {
		s.Accounts[idx] = adopted
	}

	if wasActive {
		s.Active = email
	}
	return true
}

// Remove deletes an account and reports whether it existed. If it was the
// active one the pointer moves to the most recently used survivor.
func (s *AccountStore) Remove(email string) bool {
	for i := range s.Accounts {
		if !strings.EqualFold(s.Accounts[i].Email, email) {
			continue
		}
		wasActive := strings.EqualFold(s.Active, s.Accounts[i].Email)
		s.Accounts = append(s.Accounts[:i], s.Accounts[i+1:]...)
		if wasActive {
			s.Active = ""
			if next := s.mostRecent(); next != nil {
				s.Active = next.Email
			}
		}
		return true
	}
	return false
}

func (s *AccountStore) mostRecent() *Account {
	var best *Account
	for i := range s.Accounts {
		if best == nil || s.Accounts[i].LastUsed.After(best.LastUsed) {
			best = &s.Accounts[i]
		}
	}
	return best
}

// normalize repairs the invariants the rest of the app relies on: unique
// emails, and an active pointer that actually names a saved account.
func (s *AccountStore) normalize() {
	seen := make(map[string]int, len(s.Accounts))
	kept := s.Accounts[:0]
	for _, acc := range s.Accounts {
		key := strings.ToLower(acc.Email)
		if idx, dup := seen[key]; dup {
			kept[idx] = mergeAccount(kept[idx], acc)
			continue
		}
		seen[key] = len(kept)
		kept = append(kept, acc)
	}
	s.Accounts = kept

	if s.Active != "" && s.Find(s.Active) == nil {
		s.Active = ""
	}
	if s.Active == "" && len(s.Accounts) > 0 {
		if best := s.mostRecent(); best != nil {
			s.Active = best.Email
		} else {
			s.Active = s.Accounts[0].Email
		}
	}
}

// Ordered returns the accounts with the active one first and the rest by most
// recent use, which is the order the switcher shows.
func (s *AccountStore) Ordered() []Account {
	out := make([]Account, len(s.Accounts))
	copy(out, s.Accounts)
	sort.SliceStable(out, func(i, j int) bool {
		iActive := strings.EqualFold(out[i].Email, s.Active)
		jActive := strings.EqualFold(out[j].Email, s.Active)
		if iActive != jActive {
			return iActive
		}
		return out[i].LastUsed.After(out[j].LastUsed)
	})
	return out
}
