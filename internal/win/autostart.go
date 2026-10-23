//go:build windows

package win

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/windows/registry"
)

const runKey = `Software\Microsoft\Windows\CurrentVersion\Run`

// appRunName is the value name used in the per-user Run key.
const appRunName = "Todnet"

// legacyRunName is the value the pre-rename build wrote. A stale entry
// would point Windows at an executable that no longer exists.
const legacyRunName = "FoxyVPN"

// RemoveLegacyAutostart deletes the startup value written under the old
// product name. Missing key or value is not an error: most users never had
// "Launch at startup" turned on.
func RemoveLegacyAutostart() {
	key, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.SET_VALUE)
	if err != nil {
		return
	}
	defer key.Close()
	_ = key.DeleteValue(legacyRunName)
}

// SetLaunchAtStartup registers or removes this executable from the per-user
// Run key. It never needs administrator rights.
func SetLaunchAtStartup(enabled bool) error {
	key, _, err := registry.CreateKey(registry.CURRENT_USER, runKey, registry.SET_VALUE|registry.QUERY_VALUE)
	if err != nil {
		return fmt.Errorf("opening Run key: %w", err)
	}
	defer key.Close()

	if !enabled {
		if err := key.DeleteValue(appRunName); err != nil && !errors.Is(err, registry.ErrNotExist) {
			return fmt.Errorf("removing startup entry: %w", err)
		}
		return nil
	}

	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locating executable: %w", err)
	}
	exe, err = filepath.Abs(exe)
	if err != nil {
		return err
	}
	// Quote the path so spaces survive; --minimized keeps the window hidden.
	command := fmt.Sprintf(`"%s" --minimized`, exe)
	if err := key.SetStringValue(appRunName, command); err != nil {
		return fmt.Errorf("writing startup entry: %w", err)
	}
	return nil
}

// LaunchAtStartup reports whether the Run key currently points at this app.
func LaunchAtStartup() bool {
	key, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer key.Close()
	_, _, err = key.GetStringValue(appRunName)
	return err == nil
}
