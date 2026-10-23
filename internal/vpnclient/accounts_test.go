package vpnclient

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// withTempProfile points the store at a throwaway directory for one test.
func withTempProfile(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	prev := configDirOverride
	configDirOverride = dir
	t.Cleanup(func() { configDirOverride = prev })
	return dir
}

func writeLegacyTokens(t *testing.T, dir, email string) {
	t.Helper()
	saved := SavedTokens{
		AccessToken:  "access-1",
		RefreshToken: "refresh-1",
		ObtainedAt:   time.Now(),
		ExpiresIn:    3600,
		Email:        email,
	}
	data, err := json.MarshalIndent(saved, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, tokenFileName), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestLoadAccountsMigratesLegacyTokens(t *testing.T) {
	dir := withTempProfile(t)
	writeLegacyTokens(t, dir, "one@example.com")

	store, err := LoadAccounts()
	if err != nil {
		t.Fatalf("LoadAccounts: %v", err)
	}
	if len(store.Accounts) != 1 {
		t.Fatalf("want 1 account, got %d", len(store.Accounts))
	}
	acc := store.Accounts[0]
	if acc.Email != "one@example.com" {
		t.Errorf("email = %q", acc.Email)
	}
	if acc.Tokens.RefreshToken != "refresh-1" {
		t.Errorf("refresh token = %q", acc.Tokens.RefreshToken)
	}
	if store.Active != "one@example.com" {
		t.Errorf("active = %q", store.Active)
	}

	if _, err := os.Stat(filepath.Join(dir, accountsFileName)); err != nil {
		t.Errorf("accounts.json not written: %v", err)
	}
	// The credentials now live in accounts.json; the legacy copy must be gone
	// so they are not kept in two places.
	if _, err := os.Stat(filepath.Join(dir, tokenFileName)); !os.IsNotExist(err) {
		t.Errorf("tokens.json should have been removed, stat err = %v", err)
	}
}

func TestLoadAccountsWithoutAnySession(t *testing.T) {
	withTempProfile(t)

	store, err := LoadAccounts()
	if err != nil {
		t.Fatalf("LoadAccounts: %v", err)
	}
	if len(store.Accounts) != 0 || store.Active != "" {
		t.Fatalf("want empty store, got %+v", store)
	}
	if store.ActiveAccount() != nil {
		t.Error("empty store should have no active account")
	}
}

// A session carried over from a build that did not persist the email has an
// empty address. It must still resolve as the active account, otherwise the
// app looks signed out with a valid token sitting on disk.
func TestActiveAccountWithNamelessMigration(t *testing.T) {
	dir := withTempProfile(t)
	writeLegacyTokens(t, dir, "")

	store, err := LoadAccounts()
	if err != nil {
		t.Fatalf("LoadAccounts: %v", err)
	}
	acc := store.ActiveAccount()
	if acc == nil {
		t.Fatal("ActiveAccount returned nil for a migrated nameless account")
	}
	if acc.Label() != "Firefox account" {
		t.Errorf("label = %q", acc.Label())
	}
	if acc.Tokens.RefreshToken != "refresh-1" {
		t.Errorf("refresh token = %q", acc.Tokens.RefreshToken)
	}
}

func TestUpsertKeepsKnownFields(t *testing.T) {
	store := &AccountStore{}
	store.Upsert(Account{
		Email:      "a@example.com",
		UID:        42,
		Subscribed: true,
		MaxBytes:   1000,
		QuotaLeft:  250,
		QuotaMax:   1000,
		QuotaAt:    time.Now(),
		AddedAt:    time.Now().Add(-time.Hour),
		Tokens:     SavedTokens{RefreshToken: "r1"},
	})

	// A re-login only knows the tokens; it must not wipe the plan or the
	// allowance snapshot the switcher shows.
	store.Upsert(Account{
		Email:  "a@example.com",
		Tokens: SavedTokens{RefreshToken: "r2"},
	})

	if len(store.Accounts) != 1 {
		t.Fatalf("want 1 account, got %d", len(store.Accounts))
	}
	acc := store.Accounts[0]
	if acc.Tokens.RefreshToken != "r2" {
		t.Errorf("refresh token = %q, want r2", acc.Tokens.RefreshToken)
	}
	if acc.UID != 42 || !acc.Subscribed || acc.MaxBytes != 1000 {
		t.Errorf("plan fields lost: %+v", acc)
	}
	if acc.QuotaLeft != 250 || acc.QuotaAt.IsZero() {
		t.Errorf("allowance lost: %+v", acc)
	}
}

func TestUpsertIsCaseInsensitive(t *testing.T) {
	store := &AccountStore{}
	store.Upsert(Account{Email: "A@Example.com", Tokens: SavedTokens{RefreshToken: "r1"}})
	store.Upsert(Account{Email: "a@example.com", Tokens: SavedTokens{RefreshToken: "r2"}})

	if len(store.Accounts) != 1 {
		t.Fatalf("want 1 account, got %d", len(store.Accounts))
	}
	if store.Accounts[0].Tokens.RefreshToken != "r2" {
		t.Errorf("second upsert did not win: %q", store.Accounts[0].Tokens.RefreshToken)
	}
}

// An account imported from an older build has no email. Signing in with the
// real address should adopt that entry instead of leaving a nameless ghost.
func TestUpsertAdoptsNamelessMigration(t *testing.T) {
	store := &AccountStore{Accounts: []Account{{
		Tokens: SavedTokens{RefreshToken: "same"},
	}}}

	store.Upsert(Account{Email: "real@example.com", Tokens: SavedTokens{RefreshToken: "same"}})

	if len(store.Accounts) != 1 {
		t.Fatalf("want 1 account, got %d", len(store.Accounts))
	}
	if store.Accounts[0].Email != "real@example.com" {
		t.Errorf("email = %q", store.Accounts[0].Email)
	}
	if store.Active != "real@example.com" {
		t.Errorf("active = %q", store.Active)
	}
}

func TestRemoveMovesActiveToMostRecent(t *testing.T) {
	now := time.Now()
	store := &AccountStore{
		Active: "old@example.com",
		Accounts: []Account{
			{Email: "old@example.com", LastUsed: now.Add(-48 * time.Hour)},
			{Email: "new@example.com", LastUsed: now},
			{Email: "mid@example.com", LastUsed: now.Add(-24 * time.Hour)},
		},
	}

	if !store.Remove("old@example.com") {
		t.Fatal("Remove reported the account was not found")
	}
	if store.Active != "new@example.com" {
		t.Errorf("active = %q, want the most recently used survivor", store.Active)
	}
	if store.Find("old@example.com") != nil {
		t.Error("removed account is still present")
	}
}

func TestRemoveLastAccountClearsActive(t *testing.T) {
	store := &AccountStore{
		Active:   "only@example.com",
		Accounts: []Account{{Email: "only@example.com"}},
	}
	store.Remove("only@example.com")

	if store.Active != "" {
		t.Errorf("active = %q, want empty", store.Active)
	}
	if store.ActiveAccount() != nil {
		t.Error("ActiveAccount should be nil")
	}
}

func TestRemoveUnknownAccount(t *testing.T) {
	store := &AccountStore{Accounts: []Account{{Email: "a@example.com"}}}
	if store.Remove("b@example.com") {
		t.Error("Remove reported success for an unknown account")
	}
	if len(store.Accounts) != 1 {
		t.Errorf("account list changed: %+v", store.Accounts)
	}
}

func TestNormalizeDedupesAndRepairsActive(t *testing.T) {
	store := &AccountStore{
		Active: "missing@example.com",
		Accounts: []Account{
			{Email: "a@example.com", UID: 1},
			{Email: "A@example.com", Subscribed: true},
			{Email: "b@example.com", LastUsed: time.Now()},
		},
	}
	store.normalize()

	if len(store.Accounts) != 2 {
		t.Fatalf("want 2 accounts after dedupe, got %d", len(store.Accounts))
	}
	merged := store.Find("a@example.com")
	if merged == nil || merged.UID != 1 || !merged.Subscribed {
		t.Errorf("duplicate merge lost fields: %+v", merged)
	}
	if store.Find(store.Active) == nil {
		t.Errorf("active %q does not name a saved account", store.Active)
	}
}

func TestOrderedPutsActiveFirst(t *testing.T) {
	now := time.Now()
	store := &AccountStore{
		Active: "mid@example.com",
		Accounts: []Account{
			{Email: "old@example.com", LastUsed: now.Add(-48 * time.Hour)},
			{Email: "mid@example.com", LastUsed: now.Add(-24 * time.Hour)},
			{Email: "new@example.com", LastUsed: now},
		},
	}
	ordered := store.Ordered()

	if ordered[0].Email != "mid@example.com" {
		t.Errorf("first row = %q, want the active account", ordered[0].Email)
	}
	if ordered[1].Email != "new@example.com" || ordered[2].Email != "old@example.com" {
		t.Errorf("remaining order = %q, %q", ordered[1].Email, ordered[2].Email)
	}
	// Ordered returns a copy: mutating it must not touch the store.
	ordered[0].Email = "mutated@example.com"
	if store.Accounts[1].Email != "mid@example.com" {
		t.Error("Ordered aliased the store's backing array")
	}
}

func TestAdoptEmailRenamesNameless(t *testing.T) {
	store := &AccountStore{
		Active: "",
		Accounts: []Account{{
			Tokens: SavedTokens{RefreshToken: "r1", ObtainedAt: time.Now()},
		}},
	}

	if !store.AdoptEmail("me@example.com") {
		t.Fatal("AdoptEmail returned false")
	}
	if len(store.Accounts) != 1 {
		t.Fatalf("want 1 account, got %d", len(store.Accounts))
	}
	if store.Accounts[0].Email != "me@example.com" {
		t.Errorf("email = %q", store.Accounts[0].Email)
	}
	if store.Accounts[0].Tokens.Email != "me@example.com" {
		t.Errorf("token email = %q", store.Accounts[0].Tokens.Email)
	}
	if store.Active != "me@example.com" {
		t.Errorf("active = %q, want the adopted address", store.Active)
	}
	if store.ActiveAccount() == nil {
		t.Error("ActiveAccount is nil after adoption")
	}
}

// Signing in again to a session carried over from an older build leaves the
// same account saved twice: once nameless, once with an address. Naming the
// nameless one must merge them rather than show two identical rows.
func TestAdoptEmailMergesDuplicate(t *testing.T) {
	now := time.Now()
	store := &AccountStore{
		Active: "",
		Accounts: []Account{
			{
				// The nameless leftover, with the older token set.
				Tokens: SavedTokens{AccessToken: "old", RefreshToken: "r-old", ObtainedAt: now.Add(-time.Hour)},
			},
			{
				Email:      "me@example.com",
				Subscribed: true,
				MaxBytes:   500,
				QuotaLeft:  100,
				QuotaMax:   500,
				QuotaAt:    now,
				Tokens:     SavedTokens{AccessToken: "new", RefreshToken: "r-new", ObtainedAt: now},
			},
		},
	}

	if !store.AdoptEmail("me@example.com") {
		t.Fatal("AdoptEmail returned false")
	}
	if len(store.Accounts) != 1 {
		t.Fatalf("want the duplicate merged, got %d accounts", len(store.Accounts))
	}
	acc := store.Accounts[0]
	if acc.Email != "me@example.com" {
		t.Errorf("email = %q", acc.Email)
	}
	if acc.Tokens.AccessToken != "new" {
		t.Errorf("kept the stale token: %q", acc.Tokens.AccessToken)
	}
	if !acc.Subscribed || acc.QuotaLeft != 100 || acc.MaxBytes != 500 {
		t.Errorf("merged record lost fields: %+v", acc)
	}
	if store.Active != "me@example.com" {
		t.Errorf("active = %q", store.Active)
	}
}

func TestAdoptEmailWithoutNamelessAccount(t *testing.T) {
	store := &AccountStore{
		Active:   "a@example.com",
		Accounts: []Account{{Email: "a@example.com"}},
	}
	if store.AdoptEmail("b@example.com") {
		t.Error("AdoptEmail succeeded with no nameless account")
	}
	if len(store.Accounts) != 1 || store.Accounts[0].Email != "a@example.com" {
		t.Errorf("store changed: %+v", store.Accounts)
	}
}

func TestAdoptEmailIgnoresBlankAddress(t *testing.T) {
	store := &AccountStore{Accounts: []Account{{Email: ""}}}
	if store.AdoptEmail("   ") {
		t.Error("AdoptEmail accepted a blank address")
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	withTempProfile(t)

	store := &AccountStore{Active: "b@example.com"}
	store.Upsert(Account{
		Email:      "a@example.com",
		Subscribed: true,
		QuotaLeft:  100,
		QuotaMax:   200,
		QuotaAt:    time.Now().Truncate(time.Second),
		Tokens:     SavedTokens{AccessToken: "at", RefreshToken: "rt", ExpiresIn: 60},
	})
	store.Upsert(Account{Email: "b@example.com", Tokens: SavedTokens{RefreshToken: "rt-b"}})
	if err := store.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	reloaded, err := LoadAccounts()
	if err != nil {
		t.Fatalf("LoadAccounts: %v", err)
	}
	if reloaded.Active != "b@example.com" {
		t.Errorf("active = %q", reloaded.Active)
	}
	acc := reloaded.Find("a@example.com")
	if acc == nil {
		t.Fatal("account a disappeared")
	}
	if acc.QuotaLeft != 100 || acc.QuotaMax != 200 {
		t.Errorf("allowance = %d/%d", acc.QuotaLeft, acc.QuotaMax)
	}
	if acc.Tokens.RefreshToken != "rt" {
		t.Errorf("refresh token = %q", acc.Tokens.RefreshToken)
	}
}

// The connect path saves refreshed tokens without knowing the email; they must
// land on whichever account is active.
func TestSaveTokensTargetsActiveAccount(t *testing.T) {
	withTempProfile(t)

	store := &AccountStore{Active: "b@example.com"}
	store.Upsert(Account{Email: "a@example.com", Tokens: SavedTokens{RefreshToken: "rt-a"}})
	store.Upsert(Account{Email: "b@example.com", Tokens: SavedTokens{RefreshToken: "rt-b"}})
	if err := store.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	if err := SaveTokens(&TokenResponse{AccessToken: "new-at", ExpiresIn: 3600}); err != nil {
		t.Fatalf("SaveTokens: %v", err)
	}

	reloaded, err := LoadAccounts()
	if err != nil {
		t.Fatalf("LoadAccounts: %v", err)
	}
	b := reloaded.Find("b@example.com")
	if b == nil || b.Tokens.AccessToken != "new-at" {
		t.Fatalf("active account did not get the new token: %+v", b)
	}
	// The response carried no refresh token, so the previous one must survive.
	if b.Tokens.RefreshToken != "rt-b" {
		t.Errorf("refresh token = %q, want rt-b", b.Tokens.RefreshToken)
	}
	a := reloaded.Find("a@example.com")
	if a == nil || a.Tokens.AccessToken != "" {
		t.Errorf("inactive account was modified: %+v", a)
	}
}
