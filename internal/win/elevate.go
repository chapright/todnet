//go:build windows

package win

import (
	"errors"
	"fmt"
	"os"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// IsElevated reports whether the process is running with an elevated token.
// TUN mode needs it: creating a virtual adapter is an administrator operation.
func IsElevated() bool {
	return windows.GetCurrentProcessToken().IsElevated()
}

// RelaunchElevated starts another copy of the app through the UAC prompt. The
// caller is expected to exit afterwards.
func RelaunchElevated(args string) error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locating the executable: %w", err)
	}
	verb, err := windows.UTF16PtrFromString("runas")
	if err != nil {
		return err
	}
	file, err := windows.UTF16PtrFromString(exe)
	if err != nil {
		return err
	}
	var params uintptr
	if args != "" {
		p, perr := windows.UTF16PtrFromString(args)
		if perr != nil {
			return perr
		}
		params = uintptr(unsafe.Pointer(p))
	}
	dir, _ := windows.UTF16PtrFromString("")

	r, _, callErr := procShellExecuteW.Call(
		0,
		uintptr(unsafe.Pointer(verb)),
		uintptr(unsafe.Pointer(file)),
		params,
		uintptr(unsafe.Pointer(dir)),
		swShow,
	)
	// ShellExecute returns a value greater than 32 on success.
	if r <= 32 {
		if r == 5 || errors.Is(callErr, syscall.Errno(1223)) {
			return errors.New("the administrator prompt was cancelled")
		}
		return fmt.Errorf("starting the elevated copy failed (code %d)", r)
	}
	return nil
}
