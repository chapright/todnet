// Package appdir resolves the per-user data directory and migrates a profile
// left behind by the pre-rename build.
//
// The app used to keep its state in %APPDATA%\FoxyVPN. Every file it owns —
// settings, accounts, the proxy snapshot, the window rectangle — has to move
// to %APPDATA%\Todnet, or a rename would silently sign the user out and forget
// their preferences. The WebView2 profile (%APPDATA%\FoxyVPN.exe) moves too,
// because that is where the interface keeps saved locations.
package appdir

import (
	"os"
	"path/filepath"
	"sync"
)

const (
	// Name is the folder under %APPDATA% that belongs to this product.
	Name = "Todnet"

	// legacyName is what the folder was called before the rename. It is
	// read once, on the first launch after the rename, and then removed.
	legacyName = "FoxyVPN"

	// legacyExeName is the executable name the vendored WebView2 host uses
	// for its profile folder (%APPDATA%\FoxyVPN.exe). Moving that folder
	// keeps localStorage — favourites and recent locations — across the
	// rename as well.
	legacyExeName = "FoxyVPN.exe"
)

// migrateMu serialises the one-time move: several startup paths ask for the
// directory at once, and two migrations racing would each see the legacy
// folder before either had claimed it.
var migrateMu sync.Mutex

// Dir returns the per-user data directory, creating it if it does not exist.
// Every call also looks for a legacy profile, so callers never have to
// remember to run the migration themselves. The lookup is two stats against
// a missing folder once the move has happened, which is cheap enough for the
// handful of paths that ask for it.
func Dir() (string, error) {
	root, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(root, Name)

	migrateMu.Lock()
	migErr := migrateLegacy(root, dir)
	migrateWebViewProfile(root)
	migrateMu.Unlock()
	if migErr != nil {
		return "", migErr
	}

	if mkErr := os.MkdirAll(dir, 0o700); mkErr != nil {
		return "", mkErr
	}
	return dir, nil
}

// Path returns a file inside the per-user data directory.
func Path(name string) (string, error) {
	base, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, name), nil
}

// legacyProfileFiles are the files that carry the user's state. The log, the
// port file and the unpacked wintun.dll are deliberately absent: they are
// rebuilt on the next launch, and a log held open by a running copy would
// defeat the directory move for no benefit.
var legacyProfileFiles = []string{
	"settings.json",
	"accounts.json",
	"tokens.json",
	"proxy-restore.json",
	"window.json",
}

// migrateLegacy moves %APPDATA%\FoxyVPN to %APPDATA%\Todnet. It is a no-op
// when there is no legacy profile, or when the new folder already exists: a
// half-migrated state must not overwrite data the new build has written.
func migrateLegacy(root, newDir string) error {
	oldDir := filepath.Join(root, legacyName)
	if _, statErr := os.Stat(oldDir); statErr != nil {
		return nil
	}
	if _, statErr := os.Stat(newDir); statErr == nil {
		return nil
	}

	// The common case: nothing has the old folder open, so the whole
	// directory moves in one step and no file is ever duplicated.
	if renameErr := os.Rename(oldDir, newDir); renameErr == nil {
		return nil
	}

	// A log or the DLL can still be held by a running copy, which makes a
	// directory rename fail on Windows even though the profile files are
	// free. Move those individually instead of giving up on the user's
	// settings.
	if mkErr := os.MkdirAll(newDir, 0o700); mkErr != nil {
		return mkErr
	}
	for _, name := range legacyProfileFiles {
		if moveErr := moveFile(
			filepath.Join(oldDir, name),
			filepath.Join(newDir, name),
		); moveErr != nil {
			return moveErr
		}
	}
	// Best effort: what is left is rebuildable state held open elsewhere.
	_ = os.RemoveAll(oldDir)
	return nil
}

// migrateWebViewProfile moves the profile folder the vendored WebView2 host
// derives from the executable name. It is best effort: a profile held open by
// the old build only costs the locally cached preferences, never the settings
// or the accounts, so a failure must not stop the app from starting.
func migrateWebViewProfile(root string) {
	oldDir := filepath.Join(root, legacyExeName)
	newDir := filepath.Join(root, Name+".exe")
	if _, err := os.Stat(oldDir); err != nil {
		return
	}
	if _, err := os.Stat(newDir); err == nil {
		return
	}
	_ = os.Rename(oldDir, newDir)
}

// moveFile renames one file, falling back to a copy when the rename is
// refused. A missing source is not an error: not every profile has every
// file.
func moveFile(from, to string) error {
	if _, statErr := os.Stat(to); statErr == nil {
		return nil
	}
	if _, statErr := os.Stat(from); statErr != nil {
		return nil
	}
	if renameErr := os.Rename(from, to); renameErr == nil {
		return nil
	}

	data, readErr := os.ReadFile(from)
	if readErr != nil {
		return readErr
	}
	if writeErr := os.WriteFile(to, data, 0o600); writeErr != nil {
		return writeErr
	}
	// The source is removed after the copy so credentials do not exist in
	// two places at once. If something still holds it, the next launch
	// leaves the new folder alone and the stale copy is harmless.
	_ = os.Remove(from)
	return nil
}
