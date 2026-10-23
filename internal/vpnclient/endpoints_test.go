package vpnclient

import (
	"encoding/hex"
	"testing"
)

// The fake identity server in tools/fake_mozilla.mjs derives authPW in
// JavaScript. If the two derivations drift apart, every sign-in test would
// fail with "Unknown account" and look like a client bug. This pins the
// bytes: the hex is what the fake produced for its test account.
func TestAuthPWMatchesTheFakeIdentityServer(t *testing.T) {
	got, err := deriveAuthPW("signin@example.test", "foxy-test-password")
	if err != nil {
		t.Fatalf("deriving: %v", err)
	}
	const want = "a64ac0d55061de80e61e420c70ac9f416ed16311dbb75bde7a27f20d1121d361"
	if encoded := hex.EncodeToString(got); encoded != want {
		t.Fatalf("authPW = %s, want %s", encoded, want)
	}
}

func TestEndpointOverridesAreLoopbackOnly(t *testing.T) {
	t.Setenv("TODNET_TEST_ENDPOINT", "http://127.0.0.1:8080/v1")
	if got := endpointFromEnv("TODNET_TEST_ENDPOINT", "https://real.example"); got != "http://127.0.0.1:8080/v1" {
		t.Fatalf("loopback override was refused: %q", got)
	}

	t.Setenv("TODNET_TEST_ENDPOINT", "http://localhost:8080")
	if got := endpointFromEnv("TODNET_TEST_ENDPOINT", "https://real.example"); got != "http://localhost:8080" {
		t.Fatalf("localhost override was refused: %q", got)
	}

	// A host that is not this machine is ignored: the hook exists for the
	// suite, not for aiming a build at another server.
	for _, raw := range []string{
		"https://accounts.firefox.com",
		"http://10.1.2.3:8080",
		"http://[2001:db8::1]:8080",
	} {
		t.Setenv("TODNET_TEST_ENDPOINT", raw)
		if got := endpointFromEnv("TODNET_TEST_ENDPOINT", "https://real.example"); got != "https://real.example" {
			t.Fatalf("%s was accepted: %q", raw, got)
		}
	}

	// A malformed value is not a URL, and is not accepted either.
	t.Setenv("TODNET_TEST_ENDPOINT", "not a url")
	if got := endpointFromEnv("TODNET_TEST_ENDPOINT", "https://real.example"); got != "https://real.example" {
		t.Fatalf("a malformed override was accepted: %q", got)
	}

	t.Setenv("TODNET_TEST_ENDPOINT", "")
	if got := endpointFromEnv("TODNET_TEST_ENDPOINT", "https://real.example"); got != "https://real.example" {
		t.Fatalf("an empty override changed the endpoint: %q", got)
	}
}
