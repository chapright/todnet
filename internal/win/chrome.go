//go:build windows

package win

import (
	"errors"
	"math"
	"unsafe"
)

// The application draws its own title bar.
//
// The system caption was a strip of default Windows chrome in the middle
// of a window that is otherwise a single warm surface, and in the light
// theme it stayed dark, which is the sort of thing that makes an
// otherwise careful interface look like a web page in a frame.
//
// The window keeps its snap behaviour: WS_CAPTION is removed and
// WS_THICKFRAME is put back. The WebView covers the whole client area, so
// the window manager never sees a press on the bar or on an edge: the page
// reports both, and the native side starts the move or size loop on the
// window's own thread. That keeps Aero Snap, the Windows 11 snap layout
// flyout and the system's own move and size feedback.
//
// The frame itself is removed as well, in WM_NCCALCSIZE. A window that
// still has a system frame keeps a seven pixel band of non-client area
// around the client rectangle, and the band above the app's own title bar
// is painted by Windows in the caption colour: a strip of white above a
// dark interface, with nothing of the app in it. The client area is
// therefore made the whole window, and the interface reports presses on the
// resize edges itself.
const (
	// TitleBarHeight is the height of the app's own title bar, in device
	// independent pixels.
	TitleBarHeight = 40
	// TitleBarButtonWidth is the width of one of the three window buttons.
	TitleBarButtonWidth = 32
	// TitleBarButtonCount is how many buttons the title bar carries.
	TitleBarButtonCount = 3
	// TitleBarButtonGap is the space between two buttons.
	TitleBarButtonGap = 2
	// TitleBarButtonPad is the padding inside the button group.
	TitleBarButtonPad = 3
	// TitleBarButtonEdge is the gap between the group and the window edge.
	// It is also the width of the resize frame, so the group never sits
	// on a resize edge.
	TitleBarButtonEdge = 6
	// TitleBarResizeBorder is the width of the resize edge around the
	// window. The frame is drawn by the interface, and the page reports
	// presses on those edges; web/js/app.js keeps the same number.
	TitleBarResizeBorder = 6
)

// TitleBarButtonGroupWidth is the total width of the window-button group,
// its padding and its border included.
//
// The interface draws the group and the native side has to know where it
// is, or a click on a button would drag the window instead of pressing
// it. The two halves of this number have to agree: .titlebar-buttons in
// web/css/base.css is the other one.
const TitleBarButtonGroupWidth = TitleBarButtonCount*TitleBarButtonWidth +
	(TitleBarButtonCount-1)*TitleBarButtonGap + 2*TitleBarButtonPad + 2

const (
	// gwlStyle is GWL_STYLE. It is a negative index, so it cannot be a
	// constant converted to uintptr directly.
	gwlStyle = ^uintptr(15) // -16

	wsCaption    = 0x00C00000
	wsThickFrame = 0x00040000
	wsSysMenu    = 0x00080000
	wsMinBox     = 0x00020000
	wsMaxBox     = 0x00010000

	// swpNoSize tells SetWindowPos to leave the size alone. Without it a
	// zero width and height means "resize to nothing", and the window
	// collapsed to a sliver when the caption was taken off.
	swpNoSize       = 0x0001
	swpNoMove       = 0x0002
	swpFrameChanged = 0x0020
)

// Window-manager hit-test codes.
const (
	htClient      = 1
	htCaption     = 2
	htLeft        = 10
	htRight       = 11
	htTop         = 12
	htTopLeft     = 13
	htTopRight    = 14
	htBottom      = 15
	htBottomLeft  = 16
	htBottomRight = 17
)

// The system commands that start a move or a size loop, and the sizing
// directions they carry.
const (
	wmNCLButtonDown     = 0x00A1
	wmNCLButtonDblClick = 0x00A3
	wmSysCommand        = 0x0112
	scMove              = 0xF010
	scSize              = 0xF000
	scMaximize          = 0xF030
	scRestore           = 0xF120

	// The sizing directions of WM_SYSCOMMAND / SC_SIZE. The corner values
	// are interleaved with the sides in the Win32 numbering, not grouped:
	// TOPLEFT is 4 and BOTTOM is 6, not the other way round.
	wmszLeft        = 1
	wmszRight       = 2
	wmszTop         = 3
	wmszTopLeft     = 4
	wmszTopRight    = 5
	wmszBottom      = 6
	wmszBottomLeft  = 7
	wmszBottomRight = 8
)

// Show-window commands.
const (
	swMinimize = 6
	swMaximize = 3
	swRestore  = 9
)

var (
	procGetClientRect  = user32.NewProc("GetClientRect")
	procIsZoomed       = user32.NewProc("IsZoomed")
	procScreenToClient = user32.NewProc("ScreenToClient")
	procReleaseCapture = user32.NewProc("ReleaseCapture")
)

// ErrUnknownWindowAction is returned for an action the window does not
// know.
var ErrUnknownWindowAction = errors.New("unknown window action")

// rectMirrored is the Win32 RECT.
type rectMirrored struct {
	Left, Top, Right, Bottom int32
}

// TitleBarButtonBox returns the horizontal span of the window-button
// group, in client pixels.
//
// The group is always on the trailing edge of the bar, in every language:
// the bar keeps the order it has in English, so there is one case here
// rather than two.
func TitleBarButtonBox(clientWidth int) (left, right int) {
	left = clientWidth - TitleBarButtonEdge - TitleBarButtonGroupWidth
	return left, left + TitleBarButtonGroupWidth
}

// ApplyCustomTitleBar removes the system caption and keeps everything
// else about the window: the resize border, the snap behaviour, the
// system menu and the minimise and maximise boxes.
//
// The window keeps its size. The client area grows by the height of the
// caption that was removed, and the interface gives that space to its own
// title bar, so the layout does not shift.
func ApplyCustomTitleBar(hwnd uintptr) {
	if hwnd == 0 {
		return
	}

	style, _, _ := procGetWindowLongPtrW.Call(hwnd, gwlStyle)
	newStyle := style &^ uintptr(wsCaption)
	newStyle |= wsThickFrame | wsSysMenu | wsMinBox | wsMaxBox
	if newStyle == style {
		return
	}
	procSetWindowLongPtrW.Call(hwnd, uintptr(gwlStyle), newStyle)

	// The frame changed, so the client area has to be recomputed and the
	// child view resized with it. The size is left alone: a zero width and
	// height without SWP_NOSIZE resizes the window to nothing.
	procSetWindowPos.Call(hwnd, 0, 0, 0, 0, 0,
		uintptr(swpNoZOrder|swpNoActivate|swpNoSize|swpFrameChanged))
}

// TitleBarHitTest returns the window-manager code for a point in the
// client area.
//
// The strip along the top is the title bar: dragging it moves the window,
// and double clicking it maximises. The three buttons are reported as
// ordinary client area, so the interface draws and handles them itself
// and their hover state works. The edges of the window are reported as
// resize zones, because the system frame that used to do that is gone.
//
// The sizes here are device-independent. TitleBarHitTestAt is what the
// window uses, and it scales them to the window's current display.
func TitleBarHitTest(clientWidth, clientHeight, clientX, clientY int) int {
	return titleBarHitTest(clientWidth, clientHeight, clientX, clientY, 1)
}

// titleBarHitTest is the hit test with a scale factor.
//
// The system sends physical pixels, and the title bar's sizes are
// device-independent, so on a display that is not at 100 % the two have
// to be brought together first. Without this the drag zone was short by
// a third and the window buttons sat in the wrong place, so a click on
// one of them dragged the window instead.
func titleBarHitTest(clientWidth, clientHeight, clientX, clientY int, scale float64) int {
	if clientWidth <= 0 || clientHeight <= 0 {
		return htClient
	}
	// Above the window is not the title bar.
	if clientY < 0 || clientX < 0 || clientX >= clientWidth {
		return htClient
	}

	scaled := func(v int) int { return int(math.Round(float64(v) * scale)) }
	edge := scaled(TitleBarResizeBorder)
	right := clientWidth - edge
	bottom := clientHeight - edge

	switch {
	case clientY >= bottom && clientX < edge:
		return htBottomLeft
	case clientY >= bottom && clientX >= right:
		return htBottomRight
	case clientY >= bottom:
		return htBottom
	case clientX < edge:
		return htLeft
	case clientX >= right:
		return htRight
	}

	if clientY < scaled(TitleBarHeight) {
		// The window buttons are drawn by the interface, so they must stay
		// ordinary client area: a click on one of them would otherwise
		// drag the window instead of pressing the button.
		left, end := titleBarButtonBox(clientWidth, scale)
		if clientX >= left && clientX < end {
			return htClient
		}
		return htCaption
	}
	return htClient
}

// titleBarButtonBox is the span of the window-button group at a scale.
func titleBarButtonBox(clientWidth int, scale float64) (left, right int) {
	edge := int(math.Round(float64(TitleBarButtonEdge) * scale))
	group := int(math.Round(float64(TitleBarButtonGroupWidth) * scale))
	left = clientWidth - edge - group
	return left, left + group
}

// TitleBarButtonHitTest returns the button index under a point in the
// client area, or -1 when the point is not over a button.
//
// The order is the Windows one: minimise, maximise, close, from the
// start of the group.
func TitleBarButtonHitTest(clientWidth, clientX, clientY int) int {
	return titleBarButtonHitTest(clientWidth, clientX, clientY, 1)
}

// titleBarButtonHitTest is the button hit test at a scale.
func titleBarButtonHitTest(clientWidth, clientX, clientY int, scale float64) int {
	scaled := func(v int) int { return int(math.Round(float64(v) * scale)) }
	if clientY < 0 || clientY >= scaled(TitleBarHeight) {
		return -1
	}
	left, end := titleBarButtonBox(clientWidth, scale)
	if clientX < left || clientX >= end {
		return -1
	}
	step := scaled(TitleBarButtonWidth) + scaled(TitleBarButtonGap)
	index := (clientX - left) / step
	if index < 0 || index >= TitleBarButtonCount {
		return -1
	}
	// The last button does not fill its whole slot: the group ends with
	// its own padding and border.
	if index == TitleBarButtonCount-1 && clientX >= left+index*step+scaled(TitleBarButtonWidth) {
		return -1
	}
	return index
}

// TitleBarHitTestAt converts a screen point and reports what it is over.
// It returns the window-manager code and, when the point is over one of
// the window buttons, the button's index.
//
// The sizes are scaled to the window's current display: the system sends
// physical pixels and the title bar is laid out in device-independent
// ones.
func TitleBarHitTestAt(hwnd uintptr, screenX, screenY int) (int, int) {
	var r rectMirrored
	if ok, _, _ := procGetClientRect.Call(hwnd, uintptr(unsafe.Pointer(&r))); ok == 0 {
		return htClient, -1
	}
	pt := point{X: int32(screenX), Y: int32(screenY)}
	procScreenToClient.Call(hwnd, uintptr(unsafe.Pointer(&pt)))
	width := int(r.Right - r.Left)
	height := int(r.Bottom - r.Top)
	scale := float64(dpiForWindow(hwnd)) / 96
	return titleBarHitTest(width, height, int(pt.X), int(pt.Y), scale),
		titleBarButtonHitTest(width, int(pt.X), int(pt.Y), scale)
}

// systemCommandFor maps a hit-test code onto the system command that
// starts the matching move or size loop.
//
// The window has no system caption, so the system does not start those
// loops for it: the title bar is the app's own, and the app has to ask
// for the loop itself. Without this the bar could be pressed and
// nothing moved.
func systemCommandFor(hit int) (uintptr, bool) {
	switch hit {
	case htCaption:
		return scMove | uintptr(htCaption), true
	case htLeft:
		return scSize | wmszLeft, true
	case htRight:
		return scSize | wmszRight, true
	case htTop:
		return scSize | wmszTop, true
	case htBottom:
		return scSize | wmszBottom, true
	case htTopLeft:
		return scSize | wmszTopLeft, true
	case htTopRight:
		return scSize | wmszTopRight, true
	case htBottomLeft:
		return scSize | wmszBottomLeft, true
	case htBottomRight:
		return scSize | wmszBottomRight, true
	}
	return 0, false
}

// WindowAction performs one of the window operations the interface's own
// title bar offers.
func WindowAction(hwnd uintptr, action string) error {
	if hwnd == 0 {
		return errors.New("no window")
	}
	switch action {
	case "minimize":
		procShowWindow.Call(hwnd, swMinimize)
		return nil
	case "maximize":
		procShowWindow.Call(hwnd, swMaximize)
		return nil
	case "restore":
		procShowWindow.Call(hwnd, swRestore)
		return nil
	case "toggle-maximize":
		// The double click on the bar asks for a toggle rather than
		// "maximise" or "restore": the question is what the window is
		// doing now, and the window manager has the exact answer. The
		// interface's own copy of the state is refreshed every second, so
		// a double click right after the maximise button could otherwise
		// maximise an already maximised window.
		zoomed, _, _ := procIsZoomed.Call(hwnd)
		if zoomed != 0 {
			procShowWindow.Call(hwnd, swRestore)
		} else {
			procShowWindow.Call(hwnd, swMaximize)
		}
		return nil
	case "drag":
		// Start the move loop.
		//
		// The mouse lands on the page rather than on the window's caption:
		// the interface's title bar is drawn by the WebView, so the press
		// never becomes the non-client click that would start the loop.
		// The title bar asks for it.
		//
		// The loop has to run on the window's own thread, and that thread
		// has to have seen a recent message: Windows anchors the loop at
		// the thread's last message position, not at the click it is given.
		// The WebView's mouse events never reach this thread, so its idea of
		// the cursor is whatever an earlier click left behind, and the
		// window used to jump by the distance from that stale point before
		// it followed the pointer. Posting the request instead of sending it
		// means the thread retrieves a fresh message, and its position is the
		// cursor's position when the request was posted. The window then
		// stays exactly where it is until the pointer moves.
		procPostMessageW.Call(hwnd, wmAppDrag, 0, 0)
		return nil
	case "close":
		// WM_CLOSE rather than a destroy, so the close-to-tray behaviour
		// still gets its turn.
		procPostMessageW.Call(hwnd, wmClose, 0, 0)
		return nil
	}
	return ErrUnknownWindowAction
}

// resizeHitFor maps an edge name from the interface onto the hit-test code
// the size loop expects.
func resizeHitFor(edge string) (int, bool) {
	switch edge {
	case "left":
		return htLeft, true
	case "right":
		return htRight, true
	case "top":
		return htTop, true
	case "bottom":
		return htBottom, true
	case "top-left":
		return htTopLeft, true
	case "top-right":
		return htTopRight, true
	case "bottom-left":
		return htBottomLeft, true
	case "bottom-right":
		return htBottomRight, true
	}
	return 0, false
}

// WindowResize starts a size loop from one of the window's edges.
//
// The WebView covers the client area, so the window manager's own hit test
// never sees a press on an edge: the page asks for the size loop instead,
// the same way it asks for a move. edge is one of "left", "right", "top",
// "bottom", "top-left", "top-right", "bottom-left" or "bottom-right".
func WindowResize(hwnd uintptr, edge string) error {
	if hwnd == 0 {
		return errors.New("no window")
	}
	hit, ok := resizeHitFor(edge)
	if !ok {
		return ErrUnknownWindowAction
	}
	// A maximized window has no edge to drag. The request can arrive just
	// after the window was maximized, before the interface knows.
	if zoomed, _, _ := procIsZoomed.Call(hwnd); zoomed != 0 {
		return nil
	}
	// As with a move, the loop needs the window's own thread and a fresh
	// message position to anchor at.
	procPostMessageW.Call(hwnd, wmAppResize, uintptr(hit), 0)
	return nil
}

// BeginDragFromMaximized restores a maximized window under the cursor
// before its title bar is dragged.
//
// The window manager's own restore-on-drag assumes a system caption and a
// system frame. With both removed it restored the window at the maximized
// rectangle's top-left corner, so dragging the bar of a maximized window
// dropped it against the screen edge with part of it off-screen. Restoring
// here keeps the cursor at the same fraction of the bar it was holding,
// fully on the monitor, and leaves the move loop nothing to do until the
// pointer moves.
func BeginDragFromMaximized(hwnd uintptr) {
	if hwnd == 0 {
		return
	}
	zoomed, _, _ := procIsZoomed.Call(hwnd)
	if zoomed == 0 {
		return
	}

	var wp windowPlacement
	wp.Length = uint32(unsafe.Sizeof(wp))
	if r, _, _ := procGetWindowPlacement.Call(hwnd, uintptr(unsafe.Pointer(&wp))); r == 0 {
		return
	}
	var wr rect
	if r, _, _ := procGetWindowRect.Call(hwnd, uintptr(unsafe.Pointer(&wr))); r == 0 {
		return
	}
	var cp point
	if r, _, _ := procGetCursorPos.Call(uintptr(unsafe.Pointer(&cp))); r == 0 {
		return
	}

	normal := wp.RcNormalPos
	nw, nh := normal.Right-normal.Left, normal.Bottom-normal.Top
	mw, mh := wr.Right-wr.Left, wr.Bottom-wr.Top
	if nw <= 0 || nh <= 0 || mw <= 0 || mh <= 0 {
		return
	}

	// Keep the pointer at the same fraction of the window it was holding:
	// grabbing the middle of the maximized bar puts the middle of the
	// restored bar under the pointer, the way a caption does.
	left := cp.X - int32(float64(cp.X-wr.Left)*float64(nw)/float64(mw))
	top := cp.Y - int32(float64(cp.Y-wr.Top)*float64(nh)/float64(mh))
	if work, ok := maximizedClientRect(hwnd); ok {
		if left < work.Left {
			left = work.Left
		}
		if top < work.Top {
			top = work.Top
		}
		if maxLeft := work.Right - nw; left > maxLeft {
			left = maxLeft
		}
		if maxTop := work.Bottom - nh; top > maxTop {
			top = maxTop
		}
	}

	procShowWindow.Call(hwnd, swRestore)
	procSetWindowPos.Call(hwnd, 0, uintptr(left), uintptr(top),
		uintptr(nw), uintptr(nh), swpNoZOrder|swpNoActivate)
}

// SetClientSize makes the client area exactly the given size, in device
// independent pixels.
//
// The client area is the whole window: the system frame is removed in
// WM_NCCALCSIZE, so the window size is the client size and there is no
// frame to add to it.
func SetClientSize(hwnd uintptr, width, height int) {
	if hwnd == 0 || width <= 0 || height <= 0 {
		return
	}
	procSetWindowPos.Call(hwnd, 0, 0, 0, uintptr(width), uintptr(height),
		uintptr(swpNoZOrder|swpNoActivate|swpNoMove))
}

// WindowMaximized reports whether the window is currently maximised, so
// the interface can draw the right glyph.
func WindowMaximized(hwnd uintptr) bool {
	if hwnd == 0 {
		return false
	}
	ok, _, _ := procIsZoomed.Call(hwnd)
	return ok != 0
}
