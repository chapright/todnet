package main

import (
	_ "embed"

	"todnet/internal/win"
)

// embeddedIcon is the multi-resolution .ico used for the window and the
// taskbar, and for the notification area while the tunnel is up.
//
//go:embed web/icon.ico
var embeddedIcon []byte

// embeddedTrayOffIcon is the cold mark used in the notification area while
// the tunnel is down. A single static icon left the user to hover for a
// tooltip to learn the one thing the tray exists to say.
//
//go:embed web/icon-tray-off.ico
var embeddedTrayOffIcon []byte

// waitForSignal blocks until the process is asked to stop.
func waitForSignal() {
	win.WaitForSignal()
}
