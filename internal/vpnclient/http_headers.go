package vpnclient

import "net/http"

// The control plane fingerprints clients by User-Agent. Keep the same product
// token the official Mozilla VPN client sends; only the platform segment
// differs, so this looks like a desktop Windows build.
const mozillaVPNUserAgent = "MozillaVPN/2.35.0 (sys:windows; iap:true)"

func applyMozillaVPNHeaders(req *http.Request) {
	req.Header.Set("User-Agent", mozillaVPNUserAgent)
	req.Header.Set("Accept", "application/json")
}
