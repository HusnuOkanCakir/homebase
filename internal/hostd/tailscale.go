package hostd

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

// Reporting Tailscale, when a machine has it.
//
// Homebase does not install, configure or manage Tailscale. It is a separate
// daemon somebody chose to run, and taking ownership of it would mean owning
// its upgrades, its login state and its failures — for a thing that already
// manages itself perfectly well.
//
// What Homebase does owe the person is an answer to "how do I reach this from
// outside", and on a connection where port forwarding cannot work that answer
// is Tailscale. A remote-access screen that knows about Wireguard and not about
// the thing actually carrying the traffic is a screen that lies by omission.
//
// **Read, never write.** Nothing here starts, stops or reconfigures anything.
// It opens tailscaled's local API socket, asks for status, and reports it.
//
// The socket rather than the `tailscale` command, for two reasons: hostd runs
// fixed operations rather than shelling out, and a command's human-readable
// output is a format that changes between releases while the local API is
// versioned.

const (
	tailscaleSocket = "/var/run/tailscale/tailscaled.sock"

	// The local API insists on this Host header. Any other value is refused,
	// which is a deliberate guard against a browser being tricked into making
	// the request.
	tailscaleAPIHost = "local-tailscaled.sock"

	// Short: this runs inside a status call somebody is waiting on, and a
	// tailscaled that is wedged must not hold up the rest of the screen.
	tailscaleTimeout = 2 * time.Second
)

// TailscaleStatus is what Homebase can say about Tailscale on this machine.
type TailscaleStatus struct {
	// Installed is whether tailscaled is present and answering at all.
	Installed bool `json:"installed"`

	// Running is whether it is logged in and carrying traffic. An installed
	// daemon waiting to be logged in is the state most worth naming: everything
	// looks present and nothing works.
	Running bool `json:"running"`

	// State is the daemon's own word for what it is doing — "Running",
	// "NeedsLogin", "Stopped". Passed through rather than translated, because
	// it is what every piece of Tailscale documentation will call it.
	State string `json:"state,omitempty"`

	// Name is the address this machine answers to inside the tailnet, like
	// homebase.tailnet-name.ts.net. This is what somebody types from away.
	Name string `json:"name,omitempty"`

	// Addresses are its tailnet addresses, the 100.x one first.
	Addresses []string `json:"addresses,omitempty"`
}

// tailscaleReply is the part of the local API's status document Homebase reads.
//
// Deliberately a small subset. The full document is large and changes; naming
// only these fields means a new release adding twenty more cannot break this.
type tailscaleReply struct {
	BackendState string `json:"BackendState"`
	Self         struct {
		DNSName      string   `json:"DNSName"`
		HostName     string   `json:"HostName"`
		TailscaleIPs []string `json:"TailscaleIPs"`
	} `json:"Self"`
}

// parseTailscaleStatus turns the local API's document into what the dashboard
// shows.
func parseTailscaleStatus(body []byte) TailscaleStatus {
	var reply tailscaleReply
	if err := json.Unmarshal(body, &reply); err != nil {
		// Answering at all is the thing being reported. A document this code
		// cannot read still means the daemon is there.
		return TailscaleStatus{Installed: true}
	}

	status := TailscaleStatus{
		Installed: true,
		Running:   reply.BackendState == "Running",
		State:     reply.BackendState,
		Addresses: reply.Self.TailscaleIPs,
	}

	// DNSName arrives with a trailing dot, as a fully qualified name does. Shown
	// to a person, so the dot goes — it is correct and it looks like a typo.
	name := strings.TrimSuffix(reply.Self.DNSName, ".")
	if name == "" {
		name = reply.Self.HostName
	}
	status.Name = name

	return status
}

// readTailscaleStatus asks tailscaled what it is doing.
//
// Absence is not a failure: most machines have no Tailscale, and the zero value
// says so. Nothing here logs or errors on that path.
func readTailscaleStatus(ctx context.Context, socket string) TailscaleStatus {
	client := &http.Client{
		Timeout: tailscaleTimeout,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var dialer net.Dialer
				return dialer.DialContext(ctx, "unix", socket)
			},
		},
	}

	ctx, cancel := context.WithTimeout(ctx, tailscaleTimeout)
	defer cancel()

	request, err := http.NewRequestWithContext(ctx, http.MethodGet,
		"http://"+tailscaleAPIHost+"/localapi/v0/status", nil)
	if err != nil {
		return TailscaleStatus{}
	}
	request.Host = tailscaleAPIHost

	response, err := client.Do(request)
	if err != nil {
		return TailscaleStatus{}
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		// It answered, so it is installed; it will not say more.
		return TailscaleStatus{Installed: true}
	}

	// Bounded: this is a local socket, but a status document is not a reason to
	// read an unbounded amount into memory.
	body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return TailscaleStatus{Installed: true}
	}
	return parseTailscaleStatus(body)
}
