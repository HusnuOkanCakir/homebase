package api

import (
	"encoding/json"
	"net"
	"net/http"
	"testing"
	"time"
)

// The internet check, where it can actually run.
//
// It used to live in hostd and could never have worked there:
// homebase-hostd.service sets RestrictAddressFamilies=AF_UNIX AF_NETLINK, so
// that process cannot open an internet socket. It returned false on every
// machine that ever ran it, including one downloading Ubuntu updates while it
// said so.
//
// The tests it had injected a fake dialler. They exercised the logic perfectly
// and never asked whether the process could execute it, which is why this
// survived four milestones. So this one dials — a real socket, to a real
// address, from the process that will really do it.
func TestTheInternetCheckCanActuallyOpenASocket(t *testing.T) {
	// A listener on this machine, so the test does not depend on the internet
	// and does not fail on a train. What is being proven is that *this* process
	// is permitted to make an outbound TCP connection at all — which is the
	// thing hostd is not.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			_ = conn.Close()
		}
	}()

	var dialer net.Dialer
	conn, err := dialer.DialContext(t.Context(), "tcp", listener.Addr().String())
	if err != nil {
		t.Fatalf("core cannot open an outbound socket, so the internet check "+
			"cannot work here either: %v", err)
	}
	_ = conn.Close()
}

// A machine with no gateway is not asked, because the answer is already known
// and asking costs the caller a timeout.
func TestAMachineWithNoRouteIsNotAsked(t *testing.T) {
	h, fake := newAppHarness(t)
	headers := h.signedIn(t)

	fake.responses["network.status"] = map[string]any{
		"hostname": "homebase", "interfaces": []any{}, "reachable": false,
		// No gateway.
	}

	started := time.Now()
	rec := h.do("GET", "/api/v1/network", "", headers)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	// Five seconds is the dial timeout. A machine with no route must not spend
	// it.
	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Errorf("a machine with no gateway spent %v deciding it was offline", elapsed)
	}

	var status struct {
		Online bool `json:"online"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if status.Online {
		t.Error("a machine with no route to anywhere was reported as online")
	}
}
