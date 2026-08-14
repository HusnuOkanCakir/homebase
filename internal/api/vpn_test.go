package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/HusnuOkanCakir/homebase/internal/auth"
	"github.com/HusnuOkanCakir/homebase/internal/events"
)

// Adding a device is the only response in the API besides the recovery code that
// contains a private key. These are about where that key goes, and where it must
// not.

// Deliberately low-entropy and obviously not a key.
//
// A realistic-looking base64 string here is indistinguishable from a real
// WireGuard key to a secret scanner, and gitleaks said so. The alternative was an
// allowlist entry, and the repository's policy is to allowlist individual strings
// rather than exempt paths — so the fewer of those there are, the more the
// remaining ones mean. The test only greps for this value, so it can be anything.
const devicePrivateKey = "NOT-A-REAL-KEY-only-this-test-uses-it"

func vpnHarness(t *testing.T) (*harness, *fakeHostd) {
	t.Helper()
	h, fake := newAppHarness(t)
	fake.responses["vpn.add_device"] = map[string]any{
		"name": "phone", "address": "10.71.0.2", "public_key": "cHVibGlj",
		"config":  "[Interface]\nPrivateKey = " + devicePrivateKey + "\n",
		"message": "This is the only time this configuration can be shown.",
	}
	fake.responses["vpn.status"] = map[string]any{
		"configured": true, "running": true, "hostname": "home.example.org",
		"port": 51820, "devices": []any{}, "ever_connected": false,
	}
	return h, fake
}

// The key reaches the caller — that is the point of the call — and nothing else.
func TestTheDeviceKeyGoesToTheCallerAndNowhereElse(t *testing.T) {
	h, _ := vpnHarness(t)
	headers := h.signedIn(t)

	rec := h.do("POST", "/api/v1/network/vpn/devices", `{"name":"phone"}`, headers)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), devicePrivateKey) {
		t.Fatal("the configuration reached the caller without its key; it could not connect")
	}

	// Not in the event history, which is readable by anybody who can read events
	// and is kept indefinitely.
	list, err := h.events.List(t.Context(), events.Query{})
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range list {
		blob, _ := json.Marshal(event)
		if strings.Contains(string(blob), devicePrivateKey) {
			t.Fatalf("a device's private key is in the event history: %s", blob)
		}
	}

	// And an event was raised, above 'info' — a key to the house's network was
	// handed out, and somebody reading the history later has to find it.
	found := false
	for _, event := range list {
		if event.Type == "vpn.device_added" {
			found = true
			if event.Severity == events.SeverityInfo {
				t.Error("issuing a key to the network was recorded as routine")
			}
		}
	}
	if !found {
		t.Error("a device was given remote access and nothing was recorded")
	}
}

// Asking again must not produce the key a second time. There is no operation
// that returns it, and this is the check that no route quietly acquires one.
func TestNoOtherRouteReturnsADeviceKey(t *testing.T) {
	h, _ := vpnHarness(t)
	headers := h.signedIn(t)

	if rec := h.do("POST", "/api/v1/network/vpn/devices", `{"name":"phone"}`, headers); rec.Code != http.StatusCreated {
		t.Fatalf("creating the device: %d", rec.Code)
	}

	for _, path := range []string{"/api/v1/network/vpn", "/api/v1/network"} {
		rec := h.do("GET", path, "", headers)
		if strings.Contains(rec.Body.String(), devicePrivateKey) {
			t.Errorf("%s returns a device's private key", path)
		}
	}
}

// Reading whether remote access works is a diagnostic; opening a way into the
// house's network is not.
func TestOpeningTheNetworkNeedsMoreThanDiagnosing(t *testing.T) {
	h, fake := vpnHarness(t)
	_ = h.signedIn(t)

	diagnoser, err := h.auth.CreateUser(t.Context(), "helper", goodPassword,
		[]string{auth.PermNetworkDiag})
	if err != nil {
		t.Fatal(err)
	}
	token, _, err := h.auth.CreateSession(t.Context(), diagnoser.ID, "test")
	if err != nil {
		t.Fatal(err)
	}
	headers := map[string]string{"Authorization": "Bearer " + token}

	if rec := h.do("GET", "/api/v1/network/vpn", "", headers); rec.Code != http.StatusOK {
		t.Errorf("network.diagnose could not read the VPN status: %d", rec.Code)
	}

	before := len(fake.calls)
	for _, request := range []struct{ path, body string }{
		{"/api/v1/network/vpn", `{"hostname":"home.example.org"}`},
		{"/api/v1/network/vpn/devices", `{"name":"phone"}`},
		{"/api/v1/network/vpn/devices/remove", `{"name":"phone"}`},
	} {
		rec := h.do("POST", request.path, request.body, headers)
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s returned %d for a diagnostic-only user; want 403",
				request.path, rec.Code)
		}
	}
	if len(fake.calls) != before {
		t.Errorf("a diagnostic-only user caused %d privileged calls", len(fake.calls)-before)
	}
}

func TestVPNEndpointsRequireAuthentication(t *testing.T) {
	h, fake := vpnHarness(t)

	for _, request := range []struct{ method, path string }{
		{"GET", "/api/v1/network/vpn"},
		{"POST", "/api/v1/network/vpn"},
		{"POST", "/api/v1/network/vpn/devices"},
		{"POST", "/api/v1/network/vpn/devices/remove"},
	} {
		rec := h.do(request.method, request.path,
			`{"name":"phone","hostname":"home.example.org"}`, nil)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s %s returned %d without a session; want 401",
				request.method, request.path, rec.Code)
		}
	}
	if len(fake.calls) != 0 {
		t.Errorf("unauthenticated requests reached hostd: %v", fake.calls)
	}
}
