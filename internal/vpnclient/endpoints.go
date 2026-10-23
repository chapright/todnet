package vpnclient

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"
)

// The addresses of Mozilla's services are variables, not constants, so the
// end-to-end suite can point the real client at a fake on this machine and
// drive the sign-in, the entitlement and the server list without the
// network.
//
// An override is accepted only when it is loopback. The hook is for a test,
// and a build whose environment has been tampered with must not be aimable
// at somebody else's server: anything that is not 127.0.0.1, ::1 or
// localhost is ignored, with a line on stderr saying so.
var (
	fxaAuthServer           = endpointFromEnv("TODNET_FXA_AUTH_URL", "https://api.accounts.firefox.com/v1")
	profileServer           = endpointFromEnv("TODNET_FXA_PROFILE_URL", "https://profile.accounts.firefox.com")
	fxaAPIHost              = endpointFromEnv("TODNET_FXA_API_HOST", "https://api.accounts.firefox.com")
	GuardianEndpointDefault = endpointFromEnv("TODNET_GUARDIAN_URL", "https://vpn.mozilla.org")
	remoteSettingsURL       = endpointFromEnv("TODNET_SERVERLIST_URL", "https://firefox.settings.services.mozilla.com/v1/buckets/main/collections/vpn-serverlist/records")
)

func endpointFromEnv(name, fallback string) string {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback
	}

	parsed, err := url.Parse(raw)
	if err != nil || parsed.Hostname() == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		fmt.Fprintf(os.Stderr, "%s is not an http URL; using %s\n", name, fallback)
		return fallback
	}

	host := parsed.Hostname()
	if host == "localhost" || net.ParseIP(host).IsLoopback() {
		return strings.TrimRight(raw, "/")
	}

	fmt.Fprintf(os.Stderr, "%s is not a loopback address; using %s\n", name, fallback)
	return fallback
}
