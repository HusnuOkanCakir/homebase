package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/HusnuOkanCakir/homebase/internal/auth"
	"github.com/HusnuOkanCakir/homebase/internal/events"
)

// The one route in the API that can change how the server is reached.
//
// Everything here is about the failure, not the success: a wrong Wi-Fi password
// is the ordinary mistake, and on this surface the ordinary mistake must not
// cost somebody their server.

func TestAFailedJoinSaysNothingChanged(t *testing.T) {
	h, fake := newAppHarness(t)
	headers := h.signedIn(t)

	fake.failures["network.wifi_connect"] = hostclientError(
		"wifi.did_not_join",
		"This server could not join Home.",
		http.StatusConflict)

	rec := h.do("POST", "/api/v1/network/wifi",
		`{"ssid":"Home","passphrase":"probably-wrong"}`, headers)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	// The code is what a client branches on, and it has to survive the trip out
	// of hostd unchanged — a failure reshaped on the way is one the dashboard
	// cannot recognise.
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Error.Code != "wifi.did_not_join" {
		t.Errorf("code = %q, want wifi.did_not_join", body.Error.Code)
	}
}

// A server that fell off the network is diagnosed afterwards, and "somebody
// tried to join a Wi-Fi network at 21:04" is the line that explains it. So the
// attempt is recorded even when it fails.
func TestAFailedJoinIsStillRecorded(t *testing.T) {
	h, fake := newAppHarness(t)
	headers := h.signedIn(t)

	fake.failures["network.wifi_connect"] = hostclientError(
		"wifi.did_not_join", "Could not join.", http.StatusConflict)

	h.do("POST", "/api/v1/network/wifi", `{"ssid":"Home","passphrase":"wrong-one"}`, headers)

	list, err := h.events.List(t.Context(), events.Query{})
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range list {
		if event.Type == "network.wifi_failed" {
			if event.Severity == events.SeverityInfo {
				t.Error("a failed attempt to change the network was recorded as routine")
			}
			return
		}
	}
	t.Error("an attempt to join a wireless network left no trace in the history")
}

// The passphrase goes one way. A field that can be read back is a field that
// ends up in a log, a browser's memory, or a diagnostic file.
func TestThePassphraseIsNeverReturned(t *testing.T) {
	h, fake := newAppHarness(t)
	headers := h.signedIn(t)

	const secret = "correct-horse-battery"

	fake.responses["network.wifi_connect"] = map[string]any{
		"available": true, "connected": true, "ssid": "Home",
		"addresses": []any{"192.168.1.40"}, "configured": true,
		"has_wired_connection": true,
	}
	fake.responses["network.wifi_status"] = map[string]any{
		"available": true, "connected": true, "ssid": "Home", "configured": true,
		"has_wired_connection": true,
	}

	joined := h.do("POST", "/api/v1/network/wifi",
		`{"ssid":"Home","passphrase":"`+secret+`"}`, headers)
	if joined.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", joined.Code, joined.Body.String())
	}
	if strings.Contains(joined.Body.String(), secret) {
		t.Error("the response to joining contains the passphrase")
	}

	status := h.do("GET", "/api/v1/network/wifi", "", headers)
	if strings.Contains(status.Body.String(), secret) {
		t.Error("the status endpoint returns the passphrase")
	}

	// And it must have reached hostd, which is the only place it belongs.
	calls := fake.callsTo("network.wifi_connect")
	if len(calls) != 1 {
		t.Fatalf("%d calls to network.wifi_connect, want 1", len(calls))
	}
	if calls[0].Body["passphrase"] != secret {
		t.Error("the passphrase did not reach hostd")
	}
	// hostd checks the confirmation again; it cannot unless core says the user
	// was asked.
	if !calls[0].Confirmed {
		t.Error("joining reached hostd without the confirmed header")
	}
}

func TestJoiningNeedsToKnowWhichNetwork(t *testing.T) {
	h, fake := newAppHarness(t)
	headers := h.signedIn(t)

	fake.responses["network.wifi_connect"] = map[string]any{"available": true}

	for _, body := range []string{`{}`, `{"ssid":""}`, `{"ssid":"   "}`} {
		rec := h.do("POST", "/api/v1/network/wifi", body, headers)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s returned %d; want 400", body, rec.Code)
		}
	}
	if calls := fake.callsTo("network.wifi_connect"); len(calls) != 0 {
		t.Errorf("%d joins happened without a network name", len(calls))
	}
}

// Scanning must stay a read. A scan that quietly joined something would be the
// worst possible surprise on this surface.
func TestScanningJoinsNothing(t *testing.T) {
	h, fake := newAppHarness(t)
	headers := h.signedIn(t)

	fake.responses["network.wifi_scan"] = map[string]any{
		"networks": []any{
			map[string]any{"ssid": "Home", "signal": -50, "bars": 4,
				"security": "wpa", "current": false},
		},
	}

	rec := h.do("POST", "/api/v1/network/wifi/scan", "", headers)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if calls := fake.callsTo("network.wifi_connect"); len(calls) != 0 {
		t.Error("scanning joined a network")
	}
}

// Reading the network is a diagnostic; changing it is not. Somebody who can be
// told why the server is unreachable must not be able to make it unreachable.
func TestChangingTheNetworkNeedsMoreThanDiagnosing(t *testing.T) {
	h, fake := newAppHarness(t)
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

	fake.responses["network.wifi_status"] = map[string]any{"available": true}
	fake.responses["network.wifi_scan"] = map[string]any{"networks": []any{}}
	fake.responses["network.wifi_connect"] = map[string]any{"available": true}
	fake.responses["network.wifi_forget"] = map[string]any{"available": true}

	if rec := h.do("GET", "/api/v1/network/wifi", "", headers); rec.Code != http.StatusOK {
		t.Errorf("network.diagnose could not read the wireless status: %d", rec.Code)
	}
	if rec := h.do("POST", "/api/v1/network/wifi/scan", "", headers); rec.Code != http.StatusOK {
		t.Errorf("network.diagnose could not scan: %d", rec.Code)
	}

	before := len(fake.calls)
	for _, request := range []struct{ path, body string }{
		{"/api/v1/network/wifi", `{"ssid":"Home","passphrase":"correct-horse"}`},
		{"/api/v1/network/wifi/forget", ``},
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

func TestWifiEndpointsRequireAuthentication(t *testing.T) {
	h, fake := newAppHarness(t)

	for _, request := range []struct{ method, path string }{
		{"GET", "/api/v1/network/wifi"},
		{"POST", "/api/v1/network/wifi"},
		{"POST", "/api/v1/network/wifi/scan"},
		{"POST", "/api/v1/network/wifi/forget"},
	} {
		rec := h.do(request.method, request.path,
			`{"ssid":"Home","passphrase":"correct-horse"}`, nil)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s %s returned %d without a session; want 401",
				request.method, request.path, rec.Code)
		}
	}

	if len(fake.calls) != 0 {
		t.Errorf("unauthenticated requests reached hostd: %v", fake.calls)
	}
}
