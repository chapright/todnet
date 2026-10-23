# Patched copy of github.com/jchv/go-webview2

Version: `v0.0.0-20260205173254-56598839c808`
Upstream: <https://github.com/jchv/go-webview2>
License: see `LICENSE` (unchanged, MIT).

This copy exists for one reason: upstream creates its window with
`CreateWindowExW` and immediately calls `ShowWindow(SW_SHOW)` before the host
program gets the handle back. On a cold start the empty 640x480 frame therefore
appeared for a moment at its default position, before the application had
sized, positioned, themed or loaded anything — a flash of a blank window on
every launch.

The patch keeps the window off the desktop while the host prepares it, without
breaking WebView2:

- `webview.go`: right after the window is created it is covered by DWM's
  cloak (`DWMWA_CLOAK`) and shown with `SW_SHOWNOACTIVATE`. The window is
  visible to the window manager and to WebView2 — so the browser builds its
  composition surface and paints normally — but the desktop does not draw it.
- `webview.go`: new `(*webview).Show()` lifts the cloak, shows and focuses the
  window. This is the one call that puts the interface on screen.
- `common.go`: `Show()` added to the `WebView` interface.

A genuinely hidden window (`WS_VISIBLE` off) was tried first and does not
work: WebView2 creates its controller, scripts run and `/api/ready` arrives,
but the controller never paints a frame, so the window stays white forever
after it is shown. The cloak gives the same "nothing until ready" experience
with a working compositor.

The application creates the window cloaked, sets its title, size, placement,
icon, frame and backdrop, navigates, and calls `Show()` once the interface
reports its first painted frame. If `DwmSetWindowAttribute` is unavailable the
cloak is a no-op and the window simply appears a moment earlier — the old
behaviour, not a failure.

To move back to upstream, remove the `replace` line in `go.mod`, delete this
directory, and make sure the window is shown after navigation in `main.go`.
