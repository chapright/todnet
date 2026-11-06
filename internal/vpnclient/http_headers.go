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

// applyFxaHeaders sets the headers for Firefox Accounts API calls.
//
// The FxA API is fronted by Fastly's anti-bot challenge: the solver earns a
// _fs_ch_cp cookie by solving the challenge with a Firefox browser
// User-Agent (fastlySolverUserAgent), and the edge validates that the
// User-Agent on the API request matches the one used during solving. If the
// API request uses a different User-Agent (for example MozillaVPN/...),
// the edge returns HTTP 406 even with a valid cookie, so sign-in loops
// forever: "Fastly anti-bot challenge still failing after 5 attempts
// (HTTP 406)". FxA itself accepts a Firefox User-Agent (it reaches the
// API and returns a normal FxA error like errno 102 for an unknown
// account), so FxA calls must use the same UA as the solver. This is the
// upstream firefox-vpn-client arrangement: FxA uses a Firefox identity,
// Guardian/Remote Settings use MozillaVPN.
//
// Guardian (vpn.mozilla.org) and Remote Settings are not behind the same
// challenge and keep using MozillaVPN UA via applyMozillaVPNHeaders.
func applyFxaHeaders(req *http.Request) {
	req.Header.Set("User-Agent", fastlySolverUserAgent)
	req.Header.Set("Accept", "application/json")
}
