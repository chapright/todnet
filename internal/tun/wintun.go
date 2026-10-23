//go:build windows

package tun

import (
	_ "embed"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"golang.org/x/sys/windows"
	"golang.zx2c4.com/wintun"

	"todnet/internal/appdir"
)

// Wintun is the virtual adapter driver WireGuard publishes: a signed kernel
// driver with no installer. The DLL creates the adapter on demand and the
// adapter disappears when the last handle closes.
//
// The DLL is embedded and unpacked into the app data folder, because
// golang.zx2c4.com/wintun asks the loader for "wintun.dll" with
// LOAD_LIBRARY_SEARCH_APPLICATION_DIR — which ignores SetDllDirectory and
// fails when Todnet.exe sits in a read-only folder. Loading it once by
// absolute path puts it in the process module list, and Windows resolves the
// later bare-name request against that list.
//
//go:embed wintun_amd64.dll
var wintunDLLBytes []byte

const (
	dllFileName    = "wintun.dll"
	adapterName    = "Todnet"
	tunnelType     = "Todnet"
	sessionRingCap = 0x400000 // 4 MiB
)

var (
	dllOnce sync.Once
	dllErr  error
)

// prepareDLL unpacks the embedded driver and loads it.
func prepareDLL() error {
	dllOnce.Do(func() {
		path, err := ensureWintunDLL()
		if err != nil {
			dllErr = err
			return
		}
		if _, err := windows.LoadLibrary(path); err != nil {
			dllErr = fmt.Errorf("loading %s: %w", path, err)
		}
	})
	return dllErr
}

func ensureWintunDLL() (string, error) {
	dir, err := appdir.Dir()
	if err != nil {
		return "", fmt.Errorf("locating app data: %w", err)
	}

	path := filepath.Join(dir, dllFileName)
	if st, statErr := os.Stat(path); statErr == nil && st.Size() == int64(len(wintunDLLBytes)) {
		return path, nil
	}

	tmp := path + ".new"
	if err := os.WriteFile(tmp, wintunDLLBytes, 0o600); err != nil {
		return "", fmt.Errorf("unpacking %s: %w", dllFileName, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		// Another instance may have the DLL loaded, which locks the file.
		// An existing copy is almost certainly the same build.
		_ = os.Remove(tmp)
		if _, statErr := os.Stat(path); statErr == nil {
			return path, nil
		}
		return "", fmt.Errorf("installing %s: %w", dllFileName, err)
	}
	return path, nil
}

// createAdapter creates (or reuses) the virtual adapter.
func createAdapter() (*wintun.Adapter, error) {
	if err := prepareDLL(); err != nil {
		return nil, err
	}
	adapter, err := wintun.CreateAdapter(adapterName, tunnelType, nil)
	if err != nil {
		return nil, adapterError(err)
	}
	return adapter, nil
}

// adapterError turns the two failures users actually hit into something
// actionable.
func adapterError(err error) error {
	switch {
	case errors.Is(err, windows.ERROR_ACCESS_DENIED):
		return errors.New("TUN mode needs administrator rights (Windows refused access to the virtual adapter)")
	case errors.Is(err, windows.ERROR_FILE_NOT_FOUND):
		return fmt.Errorf("the Wintun driver could not start: %w", err)
	default:
		return fmt.Errorf("creating the virtual adapter: %w", err)
	}
}

// waitForPacket blocks until the adapter has data or the timeout elapses.
func waitForPacket(evt windows.Handle, timeout time.Duration) bool {
	if evt == 0 {
		time.Sleep(timeout)
		return true
	}
	ms := uint32(timeout / time.Millisecond)
	if ms == 0 {
		ms = 1
	}
	state, err := windows.WaitForSingleObject(evt, ms)
	if err != nil {
		return false
	}
	return state == uint32(windows.WAIT_OBJECT_0) || state == uint32(windows.WAIT_TIMEOUT)
}
