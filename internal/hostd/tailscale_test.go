package hostd

import "testing"

// The documents are real: captured from tailscaled's local API rather than
// invented, because a fixture written by the same person as the parser agrees
// with the parser whether or not either matches Tailscale.
func TestParsesALoggedInDaemon(t *testing.T) {
	body := []byte(`{
	  "Version": "1.102.3",
	  "BackendState": "Running",
	  "Self": {
	    "HostName": "homebase",
	    "DNSName": "homebase.tail9c4e2.ts.net.",
	    "TailscaleIPs": ["100.94.12.7", "fd7a:115c:a1e0::1901:c07"],
	    "Online": true
	  }
	}`)

	status := parseTailscaleStatus(body)

	if !status.Installed || !status.Running {
		t.Fatalf("installed=%v running=%v, want both true", status.Installed, status.Running)
	}
	// The trailing dot is correct in DNS and looks like a mistake on a screen.
	if status.Name != "homebase.tail9c4e2.ts.net" {
		t.Fatalf("name = %q, want the trailing dot removed", status.Name)
	}
	if len(status.Addresses) != 2 || status.Addresses[0] != "100.94.12.7" {
		t.Fatalf("addresses = %v", status.Addresses)
	}
}

// The state worth naming: everything is installed and nothing works.
func TestInstalledButNotLoggedInIsNotRunning(t *testing.T) {
	status := parseTailscaleStatus([]byte(`{
	  "BackendState": "NeedsLogin",
	  "Self": {"HostName": "homebase", "DNSName": "", "TailscaleIPs": null}
	}`))

	if !status.Installed {
		t.Fatal("a daemon that answered is installed")
	}
	if status.Running {
		t.Fatal("NeedsLogin reported as running; that is the state that looks fine and is not")
	}
	if status.State != "NeedsLogin" {
		t.Fatalf("state = %q, want the daemon's own word passed through", status.State)
	}
	// No DNSName yet, so the hostname stands in rather than showing nothing.
	if status.Name != "homebase" {
		t.Fatalf("name = %q, want the hostname as a fallback", status.Name)
	}
}

func TestAnUnreadableDocumentStillMeansInstalled(t *testing.T) {
	status := parseTailscaleStatus([]byte("not json at all"))
	if !status.Installed {
		t.Fatal("something answered the socket; that is what installed means")
	}
	if status.Running {
		t.Fatal("nothing was parsed, so nothing may be claimed about running")
	}
}

// Absence is the common case and must not look like a fault.
func TestNoDaemonReportsNothing(t *testing.T) {
	status := readTailscaleStatus(t.Context(), t.TempDir()+"/absent.sock")
	if status.Installed || status.Running {
		t.Fatalf("a machine without Tailscale reported %+v", status)
	}
}
