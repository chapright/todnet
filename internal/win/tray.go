//go:build windows

package win

import (
	"runtime"
	"strings"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	nimAdd    = 0
	nimModify = 1
	nimDelete = 2

	nifMessage = 0x00000001
	nifIcon    = 0x00000002
	nifTip     = 0x00000004
	nifInfo    = 0x00000010

	niifInfo = 0x00000001

	wmApp           = 0x8000
	wmTrayCallback  = wmApp + 1
	wmLButtonUp     = 0x0202
	wmLButtonDblClk = 0x0203
	wmRButtonUp     = 0x0205
	wmContextMenu   = 0x007B
	wmNull          = 0x0000
	wmDestroy       = 0x0002

	mfString       = 0x00000000
	mfSeparator    = 0x00000800
	tpmReturnCmd   = 0x0100
	tpmRightButton = 0x0002

	csVRedraw = 0x0001
	csHRedraw = 0x0002

	idiApplication = 32512
)

var (
	procRegisterClassExW = user32.NewProc("RegisterClassExW")
	procCreateWindowExW  = user32.NewProc("CreateWindowExW")
	procDefWindowProcW   = user32.NewProc("DefWindowProcW")
	procDestroyWindow    = user32.NewProc("DestroyWindow")
	procGetMessageW      = user32.NewProc("GetMessageW")
	procTranslateMessage = user32.NewProc("TranslateMessage")
	procDispatchMessageW = user32.NewProc("DispatchMessageW")
	procPostMessageW     = user32.NewProc("PostMessageW")
	procCreatePopupMenu  = user32.NewProc("CreatePopupMenu")
	procAppendMenuW      = user32.NewProc("AppendMenuW")
	procDestroyMenu      = user32.NewProc("DestroyMenu")
	procTrackPopupMenu   = user32.NewProc("TrackPopupMenu")
	procGetCursorPos     = user32.NewProc("GetCursorPos")
	procLoadIconW        = user32.NewProc("LoadIconW")
	procSetTimer         = user32.NewProc("SetTimer")

	procShellNotifyIconW = shell32.NewProc("Shell_NotifyIconW")

	trayWndProc = syscall.NewCallback(trayWindowProc)
)

type wndClassEx struct {
	CbSize        uint32
	Style         uint32
	LpfnWndProc   uintptr
	CbClsExtra    int32
	CbWndExtra    int32
	HInstance     windows.Handle
	HIcon         windows.Handle
	HCursor       windows.Handle
	HbrBackground windows.Handle
	LpszMenuName  *uint16
	LpszClassName *uint16
	HIconSm       windows.Handle
}

type point struct{ X, Y int32 }

type msgStruct struct {
	HWnd    windows.HWND
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	Pt      point
	Private uint32
}

type notifyIconData struct {
	CbSize           uint32
	HWnd             windows.HWND
	UID              uint32
	UFlags           uint32
	UCallbackMessage uint32
	HIcon            windows.Handle
	SzTip            [128]uint16
	DwState          uint32
	DwStateMask      uint32
	SzInfo           [256]uint16
	UTimeout         uint32
	SzInfoTitle      [64]uint16
	DwInfoFlags      uint32
	GuidItem         [16]byte
	HBalloonIcon     windows.Handle
}

// TrayCallbacks are the actions the tray menu can trigger.
type TrayCallbacks struct {
	OnShow      func()
	OnToggle    func()
	OnQuit      func()
	IsConnected func() bool
	// Detail returns the short description shown next to the state in the
	// tooltip, such as the country and the tunnel mode. It is empty when
	// there is nothing to add.
	Detail func() string
}

// TrayIcons holds the two notification-area marks: the warm one while the
// tunnel is up and the cold one while it is down.
//
// A single static icon meant the user had to hover for the tooltip to
// learn the state, and the notification area is the surface people glance
// at most often.
type TrayIcons struct {
	On  []byte
	Off []byte
}

// Tray is a notification-area icon with a context menu. It runs its own Win32
// message loop on a dedicated OS thread.
type Tray struct {
	cb    TrayCallbacks
	icons TrayIcons
	icon  []byte
	hwnd  uintptr
	ready chan struct{}
	once  sync.Once
	mu    sync.Mutex
	title string
}

// NewTray creates a tray controller. icons.On and icons.Off are complete
// .ico files.
func NewTray(cb TrayCallbacks, icons TrayIcons) *Tray {
	return &Tray{cb: cb, icons: icons, icon: icons.Off, ready: make(chan struct{}), title: "Todnet"}
}

var activeTray *Tray

// Start launches the tray thread and waits until the icon exists.
func (t *Tray) Start() {
	activeTray = t
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		t.run()
	}()
	select {
	case <-t.ready:
	case <-timeAfter(3):
	}
}

func timeAfter(seconds int) <-chan struct{} {
	ch := make(chan struct{})
	go func() {
		timer := windows.NewLazyDLL("kernel32.dll").NewProc("Sleep")
		timer.Call(uintptr(seconds * 1000))
		close(ch)
	}()
	return ch
}

// Stop removes the icon and ends the message loop.
func (t *Tray) Stop() {
	if t.hwnd != 0 {
		t.removeIcon()
		procPostMessageW.Call(t.hwnd, wmDestroy, 0, 0)
	}
}

func (t *Tray) run() {
	procGetModuleHandleW := kernel32.NewProc("GetModuleHandleW")
	instance, _, _ := procGetModuleHandleW.Call(0)
	hInstance := windows.Handle(instance)
	className, _ := windows.UTF16PtrFromString("TodnetTrayWindow")

	wc := wndClassEx{
		CbSize:        uint32(unsafe.Sizeof(wndClassEx{})),
		Style:         csHRedraw | csVRedraw,
		LpfnWndProc:   trayWndProc,
		HInstance:     hInstance,
		LpszClassName: className,
	}
	procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc)))

	hwnd, _, _ := procCreateWindowExW.Call(
		0,
		uintptr(unsafe.Pointer(className)),
		uintptr(unsafe.Pointer(className)),
		0, // not visible
		0, 0, 0, 0,
		0, 0, uintptr(hInstance), 0,
	)
	if hwnd == 0 {
		close(t.ready)
		return
	}
	t.mu.Lock()
	t.hwnd = hwnd
	t.mu.Unlock()

	t.addIcon()
	close(t.ready)

	var msg msgStruct
	for {
		ret, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(&msg)), 0, 0, 0)
		if int32(ret) <= 0 {
			return
		}
		procTranslateMessage.Call(uintptr(unsafe.Pointer(&msg)))
		procDispatchMessageW.Call(uintptr(unsafe.Pointer(&msg)))
	}
}

func (t *Tray) addIcon() {
	t.mu.Lock()
	hwnd := t.hwnd
	t.mu.Unlock()
	if hwnd == 0 {
		return
	}

	hIcon := t.makeIcon(t.currentIcon())
	if hIcon == 0 {
		hIcon, _, _ = procLoadIconW.Call(0, idiApplication)
	}

	var nid notifyIconData
	nid.CbSize = uint32(unsafe.Sizeof(nid))
	nid.HWnd = windows.HWND(hwnd)
	nid.UID = 1
	nid.UFlags = nifMessage | nifIcon | nifTip
	nid.UCallbackMessage = wmTrayCallback
	nid.HIcon = windows.Handle(hIcon)
	copyTip(nid.SzTip[:], t.tip())

	procShellNotifyIconW.Call(nimAdd, uintptr(unsafe.Pointer(&nid)))
}

// currentIcon returns the mark that matches the current state.
func (t *Tray) currentIcon() []byte {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.icon == nil {
		return nil
	}
	return t.icon
}

// makeIcon turns an .ico file into a Win32 icon handle.
func (t *Tray) makeIcon(ico []byte) uintptr {
	if len(ico) == 0 {
		return 0
	}
	data, w, h, err := pickIconImage(ico, 16)
	if err != nil || len(data) == 0 {
		return 0
	}
	hIcon, _, _ := procCreateIconFromResourceEx.Call(
		uintptr(unsafe.Pointer(&data[0])),
		uintptr(len(data)),
		1, 0x00030000,
		uintptr(int32(w)), uintptr(int32(h)), 0,
	)
	return hIcon
}

// SetConnected swaps the notification-area icon to match the tunnel state
// and refreshes the tooltip.
func (t *Tray) SetConnected(connected bool) {
	t.mu.Lock()
	hwnd := t.hwnd
	if connected {
		t.icon = t.icons.On
	} else {
		t.icon = t.icons.Off
	}
	icon := t.icon
	t.mu.Unlock()

	if hwnd == 0 {
		return
	}

	var nid notifyIconData
	nid.CbSize = uint32(unsafe.Sizeof(nid))
	nid.HWnd = windows.HWND(hwnd)
	nid.UID = 1
	nid.UFlags = nifIcon | nifTip
	if hIcon := t.makeIcon(icon); hIcon != 0 {
		nid.HIcon = windows.Handle(hIcon)
	}
	copyTip(nid.SzTip[:], t.tip())
	procShellNotifyIconW.Call(nimModify, uintptr(unsafe.Pointer(&nid)))
}

func (t *Tray) tip() string {
	state := "Disconnected"
	if t.cb.IsConnected != nil && t.cb.IsConnected() {
		state = "Connected"
	}
	detail := ""
	if t.cb.Detail != nil {
		detail = strings.TrimSpace(t.cb.Detail())
	}
	if detail == "" {
		return "Todnet — " + state
	}
	return "Todnet — " + state + " (" + detail + ")"
}

// RefreshTooltip updates the hover text after a state change.
func (t *Tray) RefreshTooltip() {
	t.mu.Lock()
	hwnd := t.hwnd
	t.mu.Unlock()
	if hwnd == 0 {
		return
	}
	var nid notifyIconData
	nid.CbSize = uint32(unsafe.Sizeof(nid))
	nid.HWnd = windows.HWND(hwnd)
	nid.UID = 1
	nid.UFlags = nifTip
	copyTip(nid.SzTip[:], t.tip())
	procShellNotifyIconW.Call(nimModify, uintptr(unsafe.Pointer(&nid)))
}

// Notify shows a balloon tip. Used once, the first time the window is closed
// to the tray, so the tunnel does not appear to have vanished.
func (t *Tray) Notify(title, text string) {
	t.mu.Lock()
	hwnd := t.hwnd
	t.mu.Unlock()
	if hwnd == 0 {
		return
	}
	var nid notifyIconData
	nid.CbSize = uint32(unsafe.Sizeof(nid))
	nid.HWnd = windows.HWND(hwnd)
	nid.UID = 1
	nid.UFlags = nifInfo
	copyTip(nid.SzInfoTitle[:], title)
	copyTip(nid.SzInfo[:], text)
	nid.DwInfoFlags = niifInfo
	procShellNotifyIconW.Call(nimModify, uintptr(unsafe.Pointer(&nid)))
}

func (t *Tray) removeIcon() {
	t.mu.Lock()
	hwnd := t.hwnd
	t.mu.Unlock()
	if hwnd == 0 {
		return
	}
	var nid notifyIconData
	nid.CbSize = uint32(unsafe.Sizeof(nid))
	nid.HWnd = windows.HWND(hwnd)
	nid.UID = 1
	procShellNotifyIconW.Call(nimDelete, uintptr(unsafe.Pointer(&nid)))
}

func copyTip(dst []uint16, s string) {
	utf, err := windows.UTF16FromString(s)
	if err != nil {
		return
	}
	n := len(utf)
	if n > len(dst) {
		n = len(dst)
	}
	copy(dst[:n], utf[:n])
}

func trayWindowProc(hwnd, msg, wparam, lparam uintptr) uintptr {
	switch msg {
	case wmTrayCallback:
		switch uint32(lparam) {
		case wmLButtonUp, wmLButtonDblClk:
			if activeTray != nil && activeTray.cb.OnShow != nil {
				go activeTray.cb.OnShow()
			}
		case wmRButtonUp, wmContextMenu:
			showTrayMenu(hwnd)
		}
		return 0
	case wmDestroy:
		if activeTray != nil {
			activeTray.removeIcon()
		}
		procPostQuitMessage := user32.NewProc("PostQuitMessage")
		procPostQuitMessage.Call(0)
		return 0
	}
	ret, _, _ := procDefWindowProcW.Call(hwnd, msg, wparam, lparam)
	return ret
}

func showTrayMenu(hwnd uintptr) {
	menu, _, _ := procCreatePopupMenu.Call()
	if menu == 0 {
		return
	}
	defer procDestroyMenu.Call(menu)

	connected := activeTray != nil && activeTray.cb.IsConnected != nil && activeTray.cb.IsConnected()

	appendItem(menu, 1, "Open Todnet")
	appendItem(menu, 2, "Disconnect")
	if !connected {
		replaceItem(menu, 2, "Connect")
	}
	procAppendMenuW.Call(menu, mfSeparator, 0, 0)
	appendItem(menu, 3, "Quit")

	var pt point
	procGetCursorPos.Call(uintptr(unsafe.Pointer(&pt)))

	// Required so the menu dismisses correctly when clicking elsewhere.
	procSetForeground.Call(hwnd)
	cmd, _, _ := procTrackPopupMenu.Call(
		menu,
		tpmReturnCmd|tpmRightButton,
		uintptr(pt.X), uintptr(pt.Y),
		0, hwnd, 0,
	)
	procPostMessageW.Call(hwnd, wmNull, 0, 0)

	switch cmd {
	case 1:
		if activeTray != nil && activeTray.cb.OnShow != nil {
			go activeTray.cb.OnShow()
		}
	case 2:
		if activeTray != nil && activeTray.cb.OnToggle != nil {
			go activeTray.cb.OnToggle()
		}
	case 3:
		if activeTray != nil && activeTray.cb.OnQuit != nil {
			go activeTray.cb.OnQuit()
		}
	}
}

func appendItem(menu, id uintptr, text string) {
	t, _ := windows.UTF16PtrFromString(text)
	procAppendMenuW.Call(menu, mfString, id, uintptr(unsafe.Pointer(t)))
}

func replaceItem(menu, id uintptr, text string) {
	procRemoveMenu := user32.NewProc("RemoveMenu")
	procRemoveMenu.Call(menu, id, 0x400) // MF_BYCOMMAND
	appendItem(menu, id, text)
}
