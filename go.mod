module todnet

go 1.27.2

// The WebView host is vendored as a patched copy: upstream always shows its
// window before the host can size or paint it, which flashed an empty frame
// on every launch. third_party/go-webview2/webview.go creates it hidden and
// exposes Show() instead. See third_party/go-webview2/PATCHED.md.
replace github.com/jchv/go-webview2 => ./third_party/go-webview2

require (
	github.com/jchv/go-webview2 v0.0.0-20260205173254-56598839c808
	github.com/quic-go/quic-go v0.59.0
	golang.org/x/crypto v0.57.0
	golang.org/x/net v0.59.0
	golang.org/x/sys v0.48.0
	golang.zx2c4.com/wintun v0.0.0-20230126152724-0fa3db229ce2
	gvisor.dev/gvisor v0.0.0-20240722211153-64c016c92987
)

require (
	github.com/google/btree v1.1.2 // indirect
	github.com/jchv/go-winloader v0.0.0-20250406163304-c1995be93bd1 // indirect
	github.com/quic-go/qpack v0.6.0 // indirect
	golang.org/x/text v0.42.0 // indirect
	golang.org/x/time v0.15.0 // indirect
)
