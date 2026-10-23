package main

import (
	"context"
	"embed"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"sync/atomic"
	"time"

	webview "github.com/jchv/go-webview2"

	"todnet/internal/api"
	"todnet/internal/appdir"
	"todnet/internal/engine"
	"todnet/internal/store"
	"todnet/internal/win"
)

//go:embed all:web
var webFS embed.FS

// version is overridden at build time with -ldflags "-X main.version=...".
var version = "1.0.0"

// forceVerbose mirrors the -verbose flag so headless mode, which has no
// settings object of its own yet, can enable per-connection logging too.
var forceVerbose bool

func main() {
	var (
		headless  = flag.Bool("headless", false, "run the control server without a window (for diagnostics)")
		minimized = flag.Bool("minimized", false, "start hidden in the tray")
		devtools  = flag.Bool("debug", false, "enable the WebView developer tools")
		verbose   = flag.Bool("verbose", false, "force verbose (per-connection) logging")
		waitPrev  = flag.Bool("wait-instance", false, "wait for a previous instance to exit (elevated restart)")
		port      = flag.Int("port", 0, "port for the local control server (0 asks for a free one)")
	)
	flag.Parse()
	forceVerbose = *verbose

	// Per-monitor DPI has to be claimed before the first window exists, or
	// Windows caches the process as DPI-unaware and every later frame is
	// stretched from a 96 DPI bitmap (audit W-02).
	win.EnableDPIAwareness()

	// A windowed app has no console, so without a log file a crash or an
	// unexplained exit leaves no evidence at all.
	if path := appLogPath(); path != "" {
		_ = os.MkdirAll(filepath.Dir(path), 0o700)
		if err := engine.EnableFileLogging(path); err == nil {
			defer engine.CloseFileLogging()
		}
	}
	defer func() {
		if r := recover(); r != nil {
			engine.LogError("panic: %v\n%s", r, debug.Stack())
			win.MessageBox(fmt.Sprintf("Todnet hit an unexpected error and has to close.\n\n%v\n\nDetails were written to:\n%s", r, appLogPath()), "Todnet")
		}
	}()

	engine.LogInfo("Todnet %s starting (pid %d)", version, os.Getpid())
	engine.LogInfo("system: %s", win.SystemSummary())
	engine.LogInfo("executable: %s", win.ExePath())

	if *headless {
		runHeadless(*port)
		return
	}

	// Only one instance may own the tunnel and the system proxy.
	wait := time.Duration(0)
	if *waitPrev {
		wait = 8 * time.Second
	}
	if !win.AcquireSingleInstanceWait(wait) {
		if err := win.FocusExistingInstance(); err != nil {
			win.MessageBox("Todnet is already running.", "Todnet")
		}
		return
	}
	defer win.ReleaseSingleInstance()

	settings, err := store.New()
	if err != nil {
		win.MessageBox("Todnet could not create its data folder:\n"+err.Error(), "Todnet")
		return
	}
	// A settings file that cannot be read falls back to the defaults, but
	// the user has to be told: otherwise every preference of theirs looks
	// like it was ignored.
	if loadErr := settings.LoadError(); loadErr != nil {
		engine.LogWarn("settings could not be read, the defaults are in use: %v", loadErr)
	}

	// The startup entry was written under the old product name. Remove it so
	// Windows does not keep trying to launch an executable that no longer
	// exists, and write the new one when the user had it enabled.
	win.RemoveLegacyAutostart()
	if settings.Get().LaunchAtStartup {
		if err := win.SetLaunchAtStartup(true); err != nil {
			engine.LogWarn("launch at startup could not be refreshed: %v", err)
		}
	}

	eng := engine.NewEngine()
	engine.SetVerbose(settings.Get().VerboseLogs || forceVerbose)

	srv := api.New(eng, settings, webFS)
	srv.LoadSavedSession()
	api.Version = version
	// "Connect on launch" is a real setting now: it used to be written to
	// settings.json and read by nothing.
	srv.MaybeAutoConnect()

	listener, err := net.Listen("tcp", listenAddr(*port))
	if err != nil {
		win.MessageBox("Todnet could not start its local service:\n"+err.Error(), "Todnet")
		return
	}

	httpSrv := &http.Server{
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		if err := httpSrv.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("local server stopped: %v", err)
		}
	}()

	url := "http://" + listener.Addr().String() + "/"
	// The marker tells the interface it is inside the native window. A
	// browser fallback keeps the plain URL and does not draw the Windows 10
	// window hairline around a tab. The rounded flag suppresses that
	// fallback from the first frame on Windows 11.
	webviewURL := url + "?native=1"
	if win.SupportsRoundedCorners() {
		webviewURL += "&rounded=1"
	}
	writePortFile(listener.Addr().String())
	engine.LogInfo("control service listening on %s", listener.Addr().String())

	var w webview.WebView
	windowReady := false
	var quitting atomic.Bool
	var tray *win.Tray
	trayDone := make(chan struct{})

	shutdown := func() {
		engine.LogInfo("shutting down")
		close(trayDone)
		if tray != nil {
			tray.Stop()
		}
		if windowReady && !quitting.Load() {
			win.SaveWindowPlacement(uintptr(w.Window()))
		}
		win.RemoveWindowBehaviour()
		srv.RestoreProxy()
		_ = eng.Disconnect()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_ = httpSrv.Shutdown(ctx)
		cancel()
		removePortFile()
		engine.LogInfo("shutdown complete")
	}

	quit := func() {
		quitting.Store(true)
		if windowReady {
			win.SaveWindowPlacement(uintptr(w.Window()))
			w.Dispatch(func() { w.Destroy() })
			return
		}
		shutdown()
		os.Exit(0)
	}

	api.QuitFunc = quit

	// Build the window. If WebView2 is unavailable, fall back to the default
	// browser so the app is still usable rather than failing outright.
	//
	// The window is created cloaked (see third_party/go-webview2/PATCHED.md):
	// it is visible to the compositor from the start, so WebView2 can paint,
	// but the desktop does not draw it until every step below — size,
	// placement, frame, theme, backdrop, the page itself — has happened.
	// Launch shows one already-drawn window instead of an empty frame that
	// jumps, and never a white one: a hidden window is not an option, because
	// WebView2 never paints into a window that was hidden at creation.
	w = webview.NewWithOptions(webview.WebViewOptions{
		Debug: *devtools,
		WindowOptions: webview.WindowOptions{
			Title:  "Todnet",
			Width:  1000,
			Height: 720,
		},
	})
	if w == nil {
		engine.LogWarn("WebView2 runtime not available; opening the interface in your browser")
		openInBrowser(url)
		<-waitForInterrupt()
		shutdown()
		return
	}
	windowReady = true
	defer w.Destroy()

	hwnd := uintptr(w.Window())

	// Honoured by the interface as well as the flag, so "Start minimized" in
	// Settings actually does something (audit K-05). The window is never
	// shown in that case, which is why the preference can be honoured
	// without the window appearing first.
	startHidden := *minimized || settings.Get().StartMinimized

	// Revealing the window is the last step of startup: the interface asks
	// for it once it has drawn its first frame, so the first thing the user
	// sees is the interface rather than a white rectangle. The timer is the
	// safety net: a page that never loads must not leave an invisible app
	// running, where the only way out would be the notification area icon.
	var revealed atomic.Bool
	reveal := func() {
		if !revealed.CompareAndSwap(false, true) || startHidden || quitting.Load() {
			return
		}
		// Show lifts the DWM cloak the WebView host put on the window while
		// it was being sized and loaded; ShowWindow brings it to the front.
		w.Show()
		win.ShowWindow(hwnd)
	}
	api.ReadyFunc = reveal
	go func() {
		select {
		case <-trayDone:
		case <-time.After(4 * time.Second):
			reveal()
		}
	}()

	// A second launch asks this instance to surface its window. While the
	// interface is still starting there is nothing to surface yet, and
	// showing it early would be the blank-frame flash this whole sequence
	// exists to avoid: the ready handshake or the timer above will reveal it
	// a moment later anyway.
	api.ShowFunc = func() {
		// Starting up with no window yet: the ready handshake or the timer
		// will reveal it. "Start in the tray" is the exception — the user
		// asked for the window explicitly, so it may come up early.
		if !revealed.Load() && !startHidden {
			return
		}
		w.Show()
		win.ShowWindow(hwnd)
	}

	// The window's own behaviour is installed before anything is sized,
	// because the client area is made the whole window in WM_NCCALCSIZE and
	// the first size the window is given has to be the final one.
	closeHintShown := false
	win.InstallWindowBehaviour(hwnd, func() bool {
		if quitting.Load() {
			return false
		}
		win.HideWindow(hwnd)
		if !closeHintShown {
			closeHintShown = true
			// The balloon is drawn by Windows, so the words come from the
			// interface: they are in the language the user picked.
			title, text := srv.TrayNotice()
			if title == "" {
				title = "Todnet is still running"
				text = "Your tunnel stays on. Use the notification area icon to reopen or quit."
			}
			// The tray is created a moment after the window is, so it may
			// not be there yet if the window is closed immediately.
			if tray != nil {
				tray.Notify(title, text)
			}
		}
		return true
	})

	w.SetTitle("Todnet")

	// Size in device-independent pixels so the window is the same physical size
	// relative to the UI at 100%, 125% and 200%.
	logicalW, logicalH := win.LogicalSize(hwnd, 1000, 720)
	w.SetSize(logicalW, logicalH, webview.HintNone)

	if icon, iconErr := loadIcon(); iconErr == nil && len(icon) > 0 {
		win.SetWindowIcon(hwnd, icon)
	}
	// The interface draws its own title bar, so the system caption comes
	// off. The resize border, the snap behaviour and the system menu all
	// stay.
	win.ApplyCustomTitleBar(hwnd)
	// Windows 11 rounds the top-level window and draws its hairline. The
	// interface only draws a fallback border on Windows 10, which stays
	// square.
	win.ApplyRoundedCorners(hwnd)
	// The client area is then set to make room for that title bar, so the
	// application keeps exactly the height it had before.
	win.SetClientSize(hwnd, logicalW, logicalH+win.TitleBarHeight)
	// A rectangle the user chose comes last, so it wins. With no usable
	// rectangle (first run, or the monitor it was on is gone) the window is
	// centred instead of opening in the corner.
	if !win.RestoreWindowPlacement(hwnd) {
		win.CenterWindow(hwnd)
	}
	// The frame follows the interface's theme, so a light window does not
	// carry a dark caption.
	win.ApplyTheme(hwnd, themeIsDark(settings))
	// Windows 11 material backdrop. Ignored on older builds, and the opaque
	// surface stays the fallback everywhere it cannot be honoured.
	win.ApplyMica(hwnd)
	srv.SetWindow(hwnd)

	// The tray exists for the whole session, not just for -minimized: closing
	// the window must not silently drop the tunnel (audit W-05).
	tray = win.NewTray(win.TrayCallbacks{
		OnShow: func() {
			// Show also lifts the cloak on a window that has never been
			// revealed (a launch with "start minimized").
			w.Show()
			win.ShowWindow(hwnd)
			w.Dispatch(func() {})
		},
		OnToggle: func() {
			if eng.Status().State == engine.StateConnected {
				srv.RestoreProxy()
				_ = eng.Disconnect()
				return
			}
			go func() {
				resp, err := http.Post(url+"api/connect", "application/json", strings.NewReader("{}"))
				if err == nil {
					io.Copy(io.Discard, resp.Body)
					resp.Body.Close()
				}
			}()
		},
		OnQuit: quit,
		IsConnected: func() bool {
			return eng.Status().State == engine.StateConnected
		},
		Detail: func() string {
			// The tooltip says where the traffic is going and how, which is
			// the next thing a user wants to know after "is it on".
			st := eng.Status()
			if st.State != engine.StateConnected {
				return ""
			}
			parts := make([]string, 0, 2)
			if st.Location != nil {
				if st.Location.CountryName != "" {
					parts = append(parts, st.Location.CountryName)
				} else if st.Location.CountryCode != "" {
					parts = append(parts, st.Location.CountryCode)
				}
			}
			if st.Mode == "tun" {
				parts = append(parts, "TUN")
			} else if st.Mode == "proxy" {
				parts = append(parts, "System proxy")
			}
			return strings.Join(parts, " · ")
		},
	}, win.TrayIcons{On: embeddedIcon, Off: embeddedTrayOffIcon})
	tray.Start()
	defer tray.Stop()

	// Keep the tray label and icon in step with the engine, follow the
	// interface's theme on the window frame, and checkpoint the window
	// rectangle periodically: the window is hidden rather than destroyed
	// when the user closes it, so there is no single "closing" moment to
	// hook.
	go func() {
		last := ""
		lastDark := themeIsDark(settings)
		tick := 0
		for {
			select {
			case <-trayDone:
				return
			case <-time.After(2 * time.Second):
			}
			cur := string(eng.Status().State)
			if cur != last {
				last = cur
				// The icon changes with the state, so the notification area
				// says whether the tunnel is up without a hover.
				tray.SetConnected(cur == string(engine.StateConnected))
			}
			dark := themeIsDark(settings)
			if dark != lastDark {
				lastDark = dark
				win.ApplyTheme(hwnd, dark)
			}
			tick++
			if tick%3 == 0 && win.IsWindowVisible(hwnd) {
				win.SaveWindowPlacement(hwnd)
			}
		}
	}()

	engine.LogInfo("window created (hwnd %#x, %d dpi), loading %s", w.Window(), win.WindowDPI(hwnd), webviewURL)
	w.Navigate(webviewURL)

	w.Run()
	engine.LogInfo("window loop exited; shutting down")
	shutdown()
}

// listenAddr is where the control server binds. The loopback address is
// never exposed to the network: the interface talks to the server, and
// nothing else may.
//
// The port is a flag so a test harness can reach the server it started. Zero
// asks for a free one, which is what a normal launch wants.
func listenAddr(port int) string {
	return fmt.Sprintf("127.0.0.1:%d", port)
}

// themeIsDark resolves the interface's theme to a single answer, so the
// window frame can be told which one it is sitting in.
func themeIsDark(settings *store.Store) bool {
	switch settings.Get().Theme {
	case "dark":
		return true
	case "light":
		return false
	default:
		return win.SystemUsesDarkTheme()
	}
}

func waitForInterrupt() <-chan struct{} {
	ch := make(chan struct{})
	go func() {
		waitForSignal()
		close(ch)
	}()
	return ch
}

// runHeadless starts the control server and prints the URL, which is useful
// for verifying the app without opening a window.
func runHeadless(port int) {
	settings, err := store.New()
	if err != nil {
		fmt.Fprintln(os.Stderr, "settings:", err)
		os.Exit(1)
	}
	eng := engine.NewEngine()
	// Headless is the mode used to capture a debugging log, so per-connection
	// detail has to be on here too — otherwise the log is missing exactly the
	// lines you would run it to collect.
	engine.SetVerbose(settings.Get().VerboseLogs || forceVerbose)
	srv := api.New(eng, settings, webFS)
	srv.LoadSavedSession()
	api.Version = version

	listener, err := net.Listen("tcp", listenAddr(port))
	if err != nil {
		fmt.Fprintln(os.Stderr, "listen:", err)
		os.Exit(1)
	}
	addr := listener.Addr().String()
	writePortFile(addr)
	fmt.Println("Todnet control server listening on http://" + addr + "/")

	// Additive: the control API's log view keeps working alongside stdout.
	engine.AddLogSink(func(e engine.LogEntry) {
		fmt.Printf("%s %-5s %s\n", e.Time.Format("15:04:05"), e.Level, e.Message)
	})

	api.QuitFunc = func() {
		srv.RestoreProxy()
		os.Exit(0)
	}

	httpSrv := &http.Server{Handler: srv.Handler()}
	if err := httpSrv.Serve(listener); err != nil {
		fmt.Fprintln(os.Stderr, "serve:", err)
		os.Exit(1)
	}
}

func appLogPath() string {
	path, err := appdir.Path("app.log")
	if err != nil {
		return ""
	}
	return path
}

func portFilePath() string {
	path, err := appdir.Path("port")
	if err != nil {
		return ""
	}
	return path
}

func writePortFile(addr string) {
	path := portFilePath()
	if path == "" {
		return
	}
	_ = os.MkdirAll(filepath.Dir(path), 0o700)
	_ = os.WriteFile(path, []byte(addr), 0o600)
}

func removePortFile() {
	if path := portFilePath(); path != "" {
		_ = os.Remove(path)
	}
}

// openInBrowser is the fallback for a machine without the WebView runtime:
// the interface opens in the user's browser instead.
func openInBrowser(url string) {
	if err := win.OpenURL(url); err != nil {
		engine.LogWarn("opening %s in the browser failed: %v", url, err)
		win.MessageBox("Todnet could not open your browser.\n\nOpen this address by hand:\n\n"+url, "Todnet")
	}
}

func loadIcon() ([]byte, error) {
	return embeddedIcon, nil
}
