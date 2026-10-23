//go:build windows

package win

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"

	"todnet/internal/appdir"
)

// Per-monitor DPI, the minimum window size, the dark title bar and the
// close-to-tray interception all need the same thing: a WndProc subclass on
// the WebView host window. Everything here degrades to a no-op when a call is
// unavailable, so the app still runs on an older build.

const (
	wmClose         = 0x0010
	wmGetMinMaxInfo = 0x0024
	wmDpiChanged    = 0x02E0
	wmNCHitTest     = 0x0084
	wmNCCalcSize    = 0x0083
	wmNCActivate    = 0x0086
	// wmAppDrag and wmAppResize ask the window's own thread to start a move
	// or size loop. The interface cannot start them itself: the loops are
	// window-manager state and have to be entered from the thread that owns
	// the window.
	wmAppDrag   = 0x8000 + 1
	wmAppResize = 0x8000 + 2

	swpNoZOrder   = 0x0004
	swpNoActivate = 0x0010

	dwmwaUseImmersiveDarkMode    = 20
	dwmwaUseImmersiveDarkModeOld = 19
	dwmwaCaptionColor            = 35
	dwmwaBorderColor             = 34
	// dwmwaWindowCornerPreference asks the desktop window manager for the
	// Windows 11 rounded window shape. It is ignored on Windows 10.
	dwmwaWindowCornerPreference = 33
	dwmwcpRound                 = 2
	// dwmwaSystemBackdropType asks the desktop window manager for a
	// material backdrop behind the window.
	dwmwaSystemBackdropType = 38
	// dwmsbtMainWindow is the backdrop used by ordinary application
	// windows on Windows 11.
	dwmsbtMainWindow = 2
)

// Logical minimum size of the window, in device-independent pixels.
const (
	minWindowW = 760
	minWindowH = 560
)

var (
	procSetProcessDpiAwarenessContext = user32.NewProc("SetProcessDpiAwarenessContext")
	procSetProcessDPIAware            = user32.NewProc("SetProcessDPIAware")
	procGetDpiForWindow               = user32.NewProc("GetDpiForWindow")
	procGetDpiForSystem               = user32.NewProc("GetDpiForSystem")
	procSetWindowLongPtrW             = user32.NewProc("SetWindowLongPtrW")
	procGetWindowLongPtrW             = user32.NewProc("GetWindowLongPtrW")
	procCallWindowProcW               = user32.NewProc("CallWindowProcW")
	procSetWindowPos                  = user32.NewProc("SetWindowPos")
	procGetWindowRect                 = user32.NewProc("GetWindowRect")
	procGetWindowPlacement            = user32.NewProc("GetWindowPlacement")
	procSetWindowPlacement            = user32.NewProc("SetWindowPlacement")
	procGetSystemMetrics              = user32.NewProc("GetSystemMetrics")
	procMonitorFromWindow             = user32.NewProc("MonitorFromWindow")
	procGetMonitorInfoW               = user32.NewProc("GetMonitorInfoW")

	dwmapi                    = windows.NewLazySystemDLL("dwmapi.dll")
	procDwmSetWindowAttribute = dwmapi.NewProc("DwmSetWindowAttribute")
)

// wsOverlappedWindow is the style the WebView host is created with.
const wsOverlappedWindow = 0x00CF0000

const (
	smCXVirtualScreen = 78
	smCYVirtualScreen = 79
	smXVirtualScreen  = 76
	smYVirtualScreen  = 77
)

// EnableDPIAwareness opts the process into per-monitor-v2 DPI scaling. Without
// this Windows renders the whole window into a bitmap and stretches it, which
// is why text looked soft at 125% and above (audit W-02).
func EnableDPIAwareness() {
	// DPI_AWARENESS_CONTEXT_PER_MONITOR_AWARE_V2 == (HANDLE)-4.
	const perMonitorV2 = ^uintptr(3)
	if r, _, _ := procSetProcessDpiAwarenessContext.Call(perMonitorV2); r != 0 {
		return
	}
	// Windows 10 1607 and older.
	procSetProcessDPIAware.Call()
}

// WindowDPI reports the effective DPI of a window, for the startup log.
func WindowDPI(hwnd uintptr) int { return dpiForWindow(hwnd) }

func dpiForWindow(hwnd uintptr) int {
	if hwnd != 0 {
		if d, _, _ := procGetDpiForWindow.Call(hwnd); d != 0 {
			return int(d)
		}
	}
	if d, _, _ := procGetDpiForSystem.Call(); d != 0 {
		return int(d)
	}
	return 96
}

// ScaleLogical converts device-independent pixels to physical pixels for the
// monitor the window is on.
func ScaleLogical(hwnd uintptr, v int) int {
	return v * dpiForWindow(hwnd) / 96
}

// LogicalSize returns the physical window size that shows w x h logical pixels.
func LogicalSize(hwnd uintptr, w, h int) (int, int) {
	dpi := dpiForWindow(hwnd)
	return w * dpi / 96, h * dpi / 96
}

/* ---------------- title bar ---------------- */

// ApplyTheme tells DWM whether the window is dark, and tints the frame to
// match.
//
// The window draws its own title bar now, so there is no caption to paint.
// The frame is still DWM's, and a dark frame around a light interface is
// the same mistake the caption was: the immersive dark flag is therefore
// turned off in the light theme, and the border colour follows the
// interface's own surface.
func ApplyTheme(hwnd uintptr, dark bool) {
	if hwnd == 0 {
		return
	}
	v := int32(0)
	if dark {
		v = 1
	}
	if r, _, _ := procDwmSetWindowAttribute.Call(hwnd, dwmwaUseImmersiveDarkMode, uintptr(unsafe.Pointer(&v)), 4); r != 0 {
		procDwmSetWindowAttribute.Call(hwnd, dwmwaUseImmersiveDarkModeOld, uintptr(unsafe.Pointer(&v)), 4)
	}
	// COLORREF is 0x00BBGGRR. The border matches the interface's own
	// window hairline, so the native frame and the page edge are one line.
	caption := uintptr(0x000B0C0E) // #0E0C0B, the dark page colour
	border := uintptr(0x003D424A)  // #4A423D, the dark window hairline
	if !dark {
		caption = uintptr(0x00F4F7FB) // #FBF7F4, the light page colour
		border = uintptr(0x00A6B4C9)  // #C9B4A6, the light window hairline
	}
	procDwmSetWindowAttribute.Call(hwnd, dwmwaCaptionColor, uintptr(unsafe.Pointer(&caption)), 4)
	procDwmSetWindowAttribute.Call(hwnd, dwmwaBorderColor, uintptr(unsafe.Pointer(&border)), 4)
}

// SupportsRoundedCorners reports whether DWM can round a window's corners.
// The corner-preference attribute arrived with Windows 11 (build 22000).
func SupportsRoundedCorners() bool {
	return osBuildNumber() >= 22000
}

// ApplyRoundedCorners asks DWM for the Windows 11 rounded window shape.
//
// DWM also draws the window hairline once the border colour is set. The
// interface only draws that hairline itself where this call is unavailable,
// which is Windows 10; there the frame stays square.
func ApplyRoundedCorners(hwnd uintptr) bool {
	if hwnd == 0 || !SupportsRoundedCorners() {
		return false
	}
	preference := int32(dwmwcpRound)
	r, _, _ := procDwmSetWindowAttribute.Call(hwnd, dwmwaWindowCornerPreference,
		uintptr(unsafe.Pointer(&preference)), 4)
	return r == 0
}

/* ---------------- Mica backdrop ---------------- */

// ApplyMica asks for the Windows 11 material backdrop.
//
// The call is harmless on older builds: the attribute is ignored, and the
// opaque surface behind the interface is what the user sees either way. On
// Windows 11 it changes how the window's frame is composited, which is why
// the caption and the border are already tinted to the app's own graphite.
//
// The honest limitation: the WebView2 control paints an opaque background, so
// the material shows through the frame and the caption rather than the whole
// client area. Making the client area transparent needs
// ICoreWebView2Controller::put_DefaultBackgroundColor with an alpha of zero,
// and the WebView2 binding this app uses does not expose it. The call is
// still made because it is the correct thing to ask for, and the opaque
// surface is the fallback everywhere it cannot be honoured.
func ApplyMica(hwnd uintptr) bool {
	if hwnd == 0 {
		return false
	}
	// The attribute arrived in Windows 11 build 22000.
	if osBuildNumber() < 22000 {
		return false
	}
	backdrop := int32(dwmsbtMainWindow)
	r, _, _ := procDwmSetWindowAttribute.Call(hwnd, dwmwaSystemBackdropType, uintptr(unsafe.Pointer(&backdrop)), 4)
	return r != 0
}

// monitorInfo is the Win32 MONITORINFO.
type monitorInfo struct {
	CbSize    uint32
	RcMonitor rect
	RcWork    rect
	Flags     uint32
}

// monitorDefaultToNearest is MONITOR_DEFAULTTONEAREST.
const monitorDefaultToNearest = 2

// maximizedClientRect returns the client rectangle a maximized window
// should have: the work area of the monitor it is on.
//
// A maximized window's rectangle hangs off the screen by the invisible
// resize border, so a client area that covered the whole window would put
// the content under the screen edges and cut it off. The work area is
// exactly what the user can see.
func maximizedClientRect(hwnd uintptr) (rect, bool) {
	hMon, _, _ := procMonitorFromWindow.Call(hwnd, monitorDefaultToNearest)
	if hMon == 0 {
		return rect{}, false
	}
	var mi monitorInfo
	mi.CbSize = uint32(unsafe.Sizeof(mi))
	if ok, _, _ := procGetMonitorInfoW.Call(hMon, uintptr(unsafe.Pointer(&mi))); ok == 0 {
		return rect{}, false
	}
	if mi.RcWork.Right <= mi.RcWork.Left || mi.RcWork.Bottom <= mi.RcWork.Top {
		return rect{}, false
	}
	return mi.RcWork, true
}

/* ---------------- subclass ---------------- */

type rect struct {
	Left, Top, Right, Bottom int32
}

type minMaxInfo struct {
	Reserved     point
	MaxSize      point
	MaxPosition  point
	MinTrackSize point
	MaxTrackSize point
}

type windowPlacement struct {
	Length        uint32
	Flags         uint32
	ShowCmd       uint32
	PtMinPosition point
	PtMaxPosition point
	RcNormalPos   rect
	DeviceName    [32]uint16
}

var (
	subclassMu   sync.Mutex
	subclassHwnd uintptr
	subclassOld  uintptr
	closeToTray  func() bool
)

// WndProc callback. Created once: syscall.NewCallback has a small global budget.
var subclassProc = syscall.NewCallback(func(hwnd, msg, wParam, lParam uintptr) uintptr {
	switch msg {
	case wmClose:
		subclassMu.Lock()
		handler := closeToTray
		subclassMu.Unlock()
		if handler != nil && handler() {
			return 0 // handled: the window hides, the process keeps running
		}
	case wmGetMinMaxInfo:
		// Let Windows fill the structure first, then clamp the minimum so the
		// layout can never be dragged below the size it was designed for.
		ret, _, _ := procDefWindowProcW.Call(hwnd, msg, wParam, lParam)
		if lParam != 0 {
			mmi := (*minMaxInfo)(unsafe.Pointer(lParam))
			cw := ScaleLogical(hwnd, minWindowW)
			ch := ScaleLogical(hwnd, minWindowH)
			// The client area is the whole window, so the track size is the
			// client minimum: there is no frame to add to it.
			mmi.MinTrackSize.X = int32(cw)
			mmi.MinTrackSize.Y = int32(ch)
		}
		return ret
	case wmNCCalcSize:
		// The window draws its own title bar, so the system caption and the
		// frame around it are not wanted. Leaving the proposed window
		// rectangle alone and returning zero makes the client area the
		// whole window.
		//
		// Without this the client area starts seven pixels below the top of
		// the window, and Windows paints that strip in the caption colour:
		// a band of white above the app's own dark title bar. The resize
		// border goes with the frame, so the interface reports its own
		// edges and asks for the size loop.
		if wParam != 0 && lParam != 0 {
			// The first of the two rectangles is the proposed new window
			// rectangle, in screen coordinates.
			proposed := (*rect)(unsafe.Pointer(lParam))
			// A maximized window's rectangle hangs off the screen by the
			// invisible resize border. A client area that covered the whole
			// window would put the content under the screen edges and cut it
			// off, so the client area is the work area of the monitor
			// instead: exactly what the user can see.
			zoomed, _, _ := procIsZoomed.Call(hwnd)
			if zoomed != 0 {
				if work, ok := maximizedClientRect(hwnd); ok {
					*proposed = work
				}
			}
			return 0
		}
	case wmNCActivate:
		// The non-client area no longer exists, so the system must not
		// repaint it when the activation state changes. Passing -1 as the
		// result parameter is what tells it not to.
		ret, _, _ := procDefWindowProcW.Call(hwnd, msg, wParam, ^uintptr(0))
		return ret
	case wmNCHitTest:
		// Covered by unit tests, but unreachable while the WebView fills
		// the client area: the page receives the mouse, so the interface's
		// own drag and edge detection are what actually run. Kept because
		// it is the correct answer for a window with no system frame, and
		// because the codes are shared with the interface.
		screenX := int(int16(lParam & 0xFFFF))
		screenY := int(int16((lParam >> 16) & 0xFFFF))
		code, _ := TitleBarHitTestAt(hwnd, screenX, screenY)
		return uintptr(code)
	case wmAppDrag:
		// The interface asked for a move. This runs on the window's own
		// thread, and the message that carried the request has just been
		// retrieved, so the thread's message position is the cursor's
		// position at the press rather than whatever the last host-thread
		// mouse message left behind. That position is the anchor of the
		// move loop; the loop's own lParam is ignored for SC_MOVE, but the
		// cursor is the value it would use, so it is passed as well.
		//
		// A maximized window is restored first: the system's restore-on-drag
		// does not know where a window without a caption should land.
		BeginDragFromMaximized(hwnd)
		procSendMessageW.Call(hwnd, wmNCLButtonDown, uintptr(htCaption), cursorAnchor())
		return 0
	case wmAppResize:
		// The interface asked for a resize from the edge in wParam. The
		// message has just been retrieved, so the loop anchors at the
		// cursor's current position, exactly as it would for a caption
		// border.
		if zoomed, _, _ := procIsZoomed.Call(hwnd); zoomed != 0 {
			return 0
		}
		procSendMessageW.Call(hwnd, wmNCLButtonDown, wParam, cursorAnchor())
		return 0
	case wmNCLButtonDown:
		// The window has no system caption, so the system does not start
		// the move and size loops for it. The title bar is the app's own,
		// so the app has to ask for the loop itself: without this the bar
		// could be pressed and nothing moved.
		//
		// The lParam of the click is where the drag begins, and it has to
		// be handed on to the command: a zero there means "use this
		// thread's last mouse message", which is stale because the title
		// bar's mouse events belong to the WebView's process. That is what
		// made the window jump before it started to follow the pointer.
		if command, ok := systemCommandFor(int(wParam)); ok {
			procReleaseCapture.Call()
			procSendMessageW.Call(hwnd, wmSysCommand, command, lParam)
			return 0
		}
	case wmNCLButtonDblClick:
		// A double click on the bar maximises the window, the way a
		// caption does.
		if wParam == uintptr(htCaption) {
			zoomed, _, _ := procIsZoomed.Call(hwnd)
			if zoomed != 0 {
				procSendMessageW.Call(hwnd, wmSysCommand, scRestore, 0)
			} else {
				procSendMessageW.Call(hwnd, wmSysCommand, scMaximize, 0)
			}
			return 0
		}
	case wmDpiChanged:
		// Windows hands us the rectangle the window should occupy at the new
		// scale; honouring it keeps the logical size stable when the window is
		// dragged between monitors with different scaling.
		if lParam != 0 {
			r := (*rect)(unsafe.Pointer(lParam))
			procSetWindowPos.Call(hwnd, 0,
				uintptr(r.Left), uintptr(r.Top),
				uintptr(r.Right-r.Left), uintptr(r.Bottom-r.Top),
				swpNoZOrder|swpNoActivate)
			return 0
		}
	}

	subclassMu.Lock()
	old := subclassOld
	subclassMu.Unlock()
	if old == 0 {
		ret, _, _ := procDefWindowProcW.Call(hwnd, msg, wParam, lParam)
		return ret
	}
	ret, _, _ := procCallWindowProcW.Call(old, hwnd, msg, wParam, lParam)
	return ret
})

// cursorAnchor packs the cursor position the way a non-client mouse message
// carries it. The move and size loops anchor at the thread's message
// position, which the posted request has just refreshed; passing the same
// point in lParam keeps the two in step.
func cursorAnchor() uintptr {
	var pt point
	if ok, _, _ := procGetCursorPos.Call(uintptr(unsafe.Pointer(&pt))); ok == 0 {
		return 0
	}
	return uintptr(uint16(pt.X)) | uintptr(uint16(pt.Y))<<16
}

// InstallWindowBehaviour subclasses the window so the app controls the minimum
// size, DPI changes, and what the close button does. onClose returns true when
// the close was handled (window hidden) and the process should keep running.
func InstallWindowBehaviour(hwnd uintptr, onClose func() bool) {
	if hwnd == 0 {
		return
	}
	subclassMu.Lock()
	subclassHwnd = hwnd
	closeToTray = onClose
	subclassMu.Unlock()

	// GWLP_WNDPROC is -4; as an unsigned pointer-sized value that is ^3.
	gwlpWndProc := ^uintptr(3)
	old, _, _ := procSetWindowLongPtrW.Call(hwnd, gwlpWndProc, subclassProc)
	if old == 0 {
		return
	}
	subclassMu.Lock()
	subclassOld = old
	subclassMu.Unlock()
}

// RemoveWindowBehaviour restores the original WndProc. Called before the window
// is destroyed so the callback cannot fire into a dead process.
func RemoveWindowBehaviour() {
	subclassMu.Lock()
	hwnd, old := subclassHwnd, subclassOld
	subclassHwnd, subclassOld, closeToTray = 0, 0, nil
	subclassMu.Unlock()
	if hwnd != 0 && old != 0 {
		procSetWindowLongPtrW.Call(hwnd, ^uintptr(3), old)
	}
}

/* ---------------- placement persistence ---------------- */

type savedPlacement struct {
	Left, Top, Right, Bottom int32
	Maximized                bool
}

func placementPath() string {
	path, err := appdir.Path("window.json")
	if err != nil {
		return ""
	}
	return path
}

// SaveWindowPlacement stores the restored (non-maximized) rectangle so the next
// launch opens where the user left it.
func SaveWindowPlacement(hwnd uintptr) {
	path := placementPath()
	if path == "" || hwnd == 0 {
		return
	}
	var wp windowPlacement
	wp.Length = uint32(unsafe.Sizeof(wp))
	if r, _, _ := procGetWindowPlacement.Call(hwnd, uintptr(unsafe.Pointer(&wp))); r == 0 {
		return
	}
	p := savedPlacement{
		Left:      wp.RcNormalPos.Left,
		Top:       wp.RcNormalPos.Top,
		Right:     wp.RcNormalPos.Right,
		Bottom:    wp.RcNormalPos.Bottom,
		Maximized: wp.ShowCmd == 3, // SW_SHOWMAXIMIZED
	}
	data, err := json.Marshal(p)
	if err != nil {
		return
	}
	_ = os.MkdirAll(filepath.Dir(path), 0o700)
	_ = os.WriteFile(path, data, 0o600)
}

// RestoreWindowPlacement applies a saved rectangle if it is still on a monitor.
// It returns false when there is nothing usable to restore.
func RestoreWindowPlacement(hwnd uintptr) bool {
	path := placementPath()
	if path == "" || hwnd == 0 {
		return false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	var p savedPlacement
	if err := json.Unmarshal(data, &p); err != nil {
		return false
	}
	w, h := p.Right-p.Left, p.Bottom-p.Top
	if w < 400 || h < 300 {
		return false
	}
	// Reject rectangles that are entirely off-screen after a monitor change.
	if !rectVisibleOnAnyMonitor(p) {
		return false
	}
	var wp windowPlacement
	wp.Length = uint32(unsafe.Sizeof(wp))
	if r, _, _ := procGetWindowPlacement.Call(hwnd, uintptr(unsafe.Pointer(&wp))); r == 0 {
		return false
	}
	wp.RcNormalPos = rect{Left: p.Left, Top: p.Top, Right: p.Right, Bottom: p.Bottom}
	if p.Maximized {
		wp.ShowCmd = 3
	} else {
		wp.ShowCmd = 1 // SW_SHOWNORMAL
	}
	r, _, _ := procSetWindowPlacement.Call(hwnd, uintptr(unsafe.Pointer(&wp)))
	return r != 0
}

// CenterWindow puts the window in the middle of the work area of the monitor
// it is on.
//
// It is the fallback for a first run or a placement that no longer fits: the
// window has no system frame to inherit a default position from, and a window
// that has never been placed would otherwise open in the screen's corner.
func CenterWindow(hwnd uintptr) {
	if hwnd == 0 {
		return
	}
	var wr rect
	if ok, _, _ := procGetWindowRect.Call(hwnd, uintptr(unsafe.Pointer(&wr))); ok == 0 {
		return
	}
	work, ok := maximizedClientRect(hwnd)
	if !ok {
		return
	}
	w, h := wr.Right-wr.Left, wr.Bottom-wr.Top
	if w <= 0 || h <= 0 {
		return
	}
	left := work.Left + (work.Right-work.Left-w)/2
	top := work.Top + (work.Bottom-work.Top-h)/2
	procSetWindowPos.Call(hwnd, 0, uintptr(left), uintptr(top), 0, 0,
		swpNoZOrder|swpNoActivate|swpNoSize)
}

func rectVisibleOnAnyMonitor(p savedPlacement) bool {
	// Cheap overlap test against the virtual screen. A window dragged to a
	// disconnected monitor would otherwise open off-screen.
	x, _, _ := procGetSystemMetrics.Call(smXVirtualScreen)
	y, _, _ := procGetSystemMetrics.Call(smYVirtualScreen)
	cx, _, _ := procGetSystemMetrics.Call(smCXVirtualScreen)
	cy, _, _ := procGetSystemMetrics.Call(smCYVirtualScreen)
	vx, vy := int32(x), int32(y)
	vw, vh := int32(cx), int32(cy)
	return p.Right > vx && p.Bottom > vy && p.Left < vx+vw && p.Top < vy+vh
}
