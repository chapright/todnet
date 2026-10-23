package appdir

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestDirMigratesAndFollowsTheConfigDirectory(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("os.UserConfigDir reads %APPDATA% on Windows")
	}

	first := t.TempDir()
	t.Setenv("APPDATA", first)
	oldDir := filepath.Join(first, legacyName)
	if err := os.MkdirAll(oldDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(oldDir, "settings.json"), []byte("kept"), 0o600); err != nil {
		t.Fatal(err)
	}

	dir, err := Dir()
	if err != nil {
		t.Fatalf("Dir: %v", err)
	}
	if dir != filepath.Join(first, Name) {
		t.Fatalf("Dir = %q, want %q", dir, filepath.Join(first, Name))
	}
	if data, err := os.ReadFile(filepath.Join(dir, "settings.json")); err != nil || string(data) != "kept" {
		t.Fatalf("the profile did not follow the rename: %q, %v", data, err)
	}

	// A test (or a user with a redirected profile) can change the config
	// root between calls; the answer must follow it rather than being
	// cached from the first call.
	second := t.TempDir()
	t.Setenv("APPDATA", second)
	next, err := Dir()
	if err != nil {
		t.Fatalf("Dir after changing APPDATA: %v", err)
	}
	if next != filepath.Join(second, Name) {
		t.Fatalf("Dir = %q, want %q", next, filepath.Join(second, Name))
	}
}

func TestLegacyProfileIsMovedToTheNewFolder(t *testing.T) {
	root := t.TempDir()
	oldDir := filepath.Join(root, legacyName)
	newDir := filepath.Join(root, Name)
	if err := os.MkdirAll(oldDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(oldDir, "settings.json"), []byte(`{"language":"fa"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := migrateLegacy(root, newDir); err != nil {
		t.Fatalf("migrateLegacy: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(newDir, "settings.json"))
	if err != nil {
		t.Fatalf("settings did not move: %v", err)
	}
	if string(data) != `{"language":"fa"}` {
		t.Fatalf("settings changed during the move: %s", data)
	}
	if _, err := os.Stat(oldDir); !os.IsNotExist(err) {
		t.Fatalf("the legacy folder should be gone, stat error: %v", err)
	}
}

func TestLegacyWebViewProfileIsMoved(t *testing.T) {
	root := t.TempDir()
	oldDir := filepath.Join(root, legacyExeName)
	newDir := filepath.Join(root, Name+".exe")
	prefsDir := filepath.Join(oldDir, "EBWebView", "Default", "Local Storage", "leveldb")
	if err := os.MkdirAll(prefsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(prefsDir, "000003.log"), []byte("favourites"), 0o600); err != nil {
		t.Fatal(err)
	}

	migrateWebViewProfile(root)

	data, err := os.ReadFile(filepath.Join(newDir, "EBWebView", "Default", "Local Storage", "leveldb", "000003.log"))
	if err != nil {
		t.Fatalf("the WebView2 profile did not move: %v", err)
	}
	if string(data) != "favourites" {
		t.Fatalf("the profile changed during the move: %q", data)
	}
	if _, err := os.Stat(oldDir); !os.IsNotExist(err) {
		t.Fatalf("the legacy WebView2 profile should be gone, stat error: %v", err)
	}
}

func TestAnExistingProfileIsNeverOverwritten(t *testing.T) {
	root := t.TempDir()
	oldDir := filepath.Join(root, legacyName)
	newDir := filepath.Join(root, Name)
	for _, dir := range []string{oldDir, newDir} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(oldDir, "settings.json"), []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(newDir, "settings.json"), []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := migrateLegacy(root, newDir); err != nil {
		t.Fatalf("migrateLegacy: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(newDir, "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "new" {
		t.Fatalf("the current profile was overwritten: %q", data)
	}
}

func TestNoLegacyProfileIsANoOp(t *testing.T) {
	root := t.TempDir()
	newDir := filepath.Join(root, Name)

	if err := migrateLegacy(root, newDir); err != nil {
		t.Fatalf("migrateLegacy: %v", err)
	}
	if _, err := os.Stat(newDir); !os.IsNotExist(err) {
		t.Fatalf("migration should not create the folder, stat error: %v", err)
	}
}
