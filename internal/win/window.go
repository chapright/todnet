//go:build windows

package win

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"

	"todnet/internal/appdir"
)

var (
	kernel32                     = windows.NewLazySystemDLL("kernel32.dll")
	user32                       = windows.NewLazySystemDLL("user32.dll")
	shell32                      = windows.NewLazySystemDLL("shell32.dll")
	procCreateMutexW             = kernel32.NewProc("CreateMutexW")
	procCloseHandle              = kernel32.NewProc("CloseHandle")
	procCreateIconFromResourceEx = user32.NewProc("CreateIconFromResourceEx")
	procSendMessageW             = user32.NewProc("SendMessageW")
	procShowWindow               = user32.NewProc("ShowWindow")
	procSetForeground            = user32.NewProc("SetForegroundWindow")
	procMessageBoxW              = user32.NewProc("MessageBoxW")
	procShellExecuteW            = shell32.NewProc("ShellExecuteW")
	procSetWindowTextW           = user32.NewProc("SetWindowTextW")
	procIsWindowVisible          = user32.NewProc("IsWindowVisible")
)

const (
	swHide = 0
	swShow = 5

	wmSetIcon = 0x0080
	iconSmall = 0
	iconBig   = 1
)

var (
	singleInstanceHandle windows.Handle
	mutexName            = "Local\\Todnet.SingleInstance.9f2c"
)

// AcquireSingleInstance claims the per-session mutex. It returns false when
// another instance already holds it.
func AcquireSingleInstance() bool { return AcquireSingleInstanceWait(0) }

// AcquireSingleInstanceWait retries for up to wait, which is what an elevated
// restart needs: the old process is still tearing itself down when the new one
// starts.
func AcquireSingleInstanceWait(wait time.Duration) bool {
	name, err := windows.UTF16PtrFromString(mutexName)
	if err != nil {
		return true
	}
	deadline := time.Now().Add(wait)
	for {
		handle, _, callErr := procCreateMutexW.Call(0, 0, uintptr(unsafe.Pointer(name)))
		if handle == 0 {
			return true
		}
		// ERROR_ALREADY_EXISTS means another process created the mutex first.
		if callErr != windows.ERROR_ALREADY_EXISTS {
			singleInstanceHandle = windows.Handle(handle)
			return true
		}
		procCloseHandle.Call(handle)
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(150 * time.Millisecond)
	}
}

// ReleaseSingleInstance frees the mutex.
func ReleaseSingleInstance() {
	if singleInstanceHandle != 0 {
		procCloseHandle.Call(uintptr(singleInstanceHandle))
		singleInstanceHandle = 0
	}
}

// FocusExistingInstance asks an already-running copy to show its window.
func FocusExistingInstance() error {
	addr, err := readPortFile()
	if err != nil {
		return err
	}
	url := "http://" + addr + "/api/show"
	client := &httpClient{timeout: 2 * time.Second}
	return client.post(url)
}

func readPortFile() (string, error) {
	path, err := appdir.Path("port")
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	addr := strings.TrimSpace(string(data))
	if addr == "" {
		return "", errors.New("no running instance")
	}
	return addr, nil
}

// MessageBox shows a modal information dialog.
func MessageBox(text, caption string) {
	t, _ := windows.UTF16PtrFromString(text)
	c, _ := windows.UTF16PtrFromString(caption)
	const mbOK, mbIconInformation = 0x0, 0x40
	procMessageBoxW.Call(0, uintptr(unsafe.Pointer(t)), uintptr(unsafe.Pointer(c)), mbOK|mbIconInformation)
}

// OpenURL opens a URL with the user's default handler.
//
// It reports whether the handler was reached. ShellExecute answers with a
// value above 32 when it worked, and the caller needs to know: the links
// that leave the app have nothing else to tell the user that they did
// nothing.
func OpenURL(url string) error {
	verb, _ := windows.UTF16PtrFromString("open")
	target, _ := windows.UTF16PtrFromString(url)
	code, _, _ := procShellExecuteW.Call(0,
		uintptr(unsafe.Pointer(verb)),
		uintptr(unsafe.Pointer(target)),
		0, 0, 1)
	if code <= 32 {
		return fmt.Errorf("the default handler did not open it (ShellExecute answered %d)", code)
	}
	return nil
}

// HideWindow hides a top-level window.
func HideWindow(hwnd uintptr) { procShowWindow.Call(hwnd, swHide) }

// ShowWindow shows and focuses a top-level window.
func ShowWindow(hwnd uintptr) {
	procShowWindow.Call(hwnd, swShow)
	procSetForeground.Call(hwnd)
}

// IsWindowVisible reports whether hwnd is currently visible.
func IsWindowVisible(hwnd uintptr) bool {
	r, _, _ := procIsWindowVisible.Call(hwnd)
	return r != 0
}

// SetWindowTitle sets the window caption.
func SetWindowTitle(hwnd uintptr, title string) {
	t, _ := windows.UTF16PtrFromString(title)
	procSetWindowTextW.Call(hwnd, uintptr(unsafe.Pointer(t)))
}

// SetWindowIcon installs an .ico image as both the big and small window icon.
// The bytes must be a complete ICO file.
func SetWindowIcon(hwnd uintptr, ico []byte) {
	sizes := []struct {
		kind uintptr
		want int
	}{{iconBig, 32}, {iconSmall, 16}}

	for _, s := range sizes {
		data, w, h, err := pickIconImage(ico, s.want)
		if err != nil || len(data) == 0 {
			continue
		}
		hIcon, _, _ := procCreateIconFromResourceEx.Call(
			uintptr(unsafe.Pointer(&data[0])),
			uintptr(len(data)),
			1, // fIcon
			0x00030000,
			uintptr(int32(w)),
			uintptr(int32(h)),
			0, // LR_DEFAULTCOLOR
		)
		if hIcon != 0 {
			procSendMessageW.Call(hwnd, wmSetIcon, s.kind, hIcon)
		}
	}
}

// pickIconImage extracts the best-matching image payload from an ICO file.
func pickIconImage(ico []byte, want int) ([]byte, int, int, error) {
	if len(ico) < 6 {
		return nil, 0, 0, errors.New("icon too small")
	}
	count := int(binary.LittleEndian.Uint16(ico[4:6]))
	if count == 0 {
		return nil, 0, 0, errors.New("icon has no images")
	}

	best := -1
	bestScore := 1 << 30
	for i := 0; i < count; i++ {
		off := 6 + i*16
		if off+16 > len(ico) {
			break
		}
		w := int(ico[off])
		if w == 0 {
			w = 256
		}
		h := int(ico[off+1])
		if h == 0 {
			h = 256
		}
		score := abs(w - want)
		if score < bestScore {
			bestScore = score
			best = off
		}
	}
	if best < 0 {
		return nil, 0, 0, errors.New("no usable icon entry")
	}

	size := int(binary.LittleEndian.Uint32(ico[best+8 : best+12]))
	offset := int(binary.LittleEndian.Uint32(ico[best+12 : best+16]))
	if offset < 0 || size <= 0 || offset+size > len(ico) {
		return nil, 0, 0, errors.New("icon entry out of range")
	}

	w := int(ico[best])
	if w == 0 {
		w = 256
	}
	h := int(ico[best+1])
	if h == 0 {
		h = 256
	}
	return ico[offset : offset+size], w, h, nil
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// WaitForSignal blocks until the process receives an interrupt.
func WaitForSignal() {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, os.Interrupt, syscall.SIGTERM)
	<-ch
}

// httpClient is a tiny POST helper used for instance hand-off.
type httpClient struct{ timeout time.Duration }

func (c *httpClient) post(url string) error {
	client := &http.Client{Timeout: c.timeout}
	resp, err := client.Post(url, "application/json", strings.NewReader("{}"))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode >= 400 {
		return fmt.Errorf("instance returned HTTP %d", resp.StatusCode)
	}
	return nil
}

// FormatError renders an error for a dialog.
func FormatError(err error) string {
	if err == nil {
		return ""
	}
	return fmt.Sprintf("Todnet error:\n%v", err)
}
