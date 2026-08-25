package hostclient

import "context"

// Remote access.
//
// A device's private key travels one way, exactly once — out of hostd, through
// core, to the person who asked for it, and nowhere else. It is not stored on
// the server and there is no operation that returns it a second time.

// VPNDevice is one thing that can connect from outside.
type VPNDevice struct {
	Name          string `json:"name"`
	Address       string `json:"address"`
	PublicKey     string `json:"public_key"`
	LastHandshake string `json:"last_handshake,omitempty"`
	TransferRx    uint64 `json:"transfer_rx,omitempty"`
	TransferTx    uint64 `json:"transfer_tx,omitempty"`
}

// VPNStatus is what remote access is doing.
type VPNStatus struct {
	Configured bool   `json:"configured"`
	Running    bool   `json:"running"`
	Hostname   string `json:"hostname,omitempty"`
	Port       int    `json:"port"`

	Devices []VPNDevice `json:"devices"`

	// EverConnected is the reachability check: a completed handshake proves the
	// name resolved, the router forwarded and the key was accepted. Nothing here
	// probes anything, because probing from inside the house means asking
	// somebody else's service.
	EverConnected bool `json:"ever_connected"`

	// DNS is the name that has to keep pointing here.
	DNS DDNSStatus `json:"dns"`

	// Tailscale is what is carrying remote access when Wireguard cannot.
	Tailscale TailscaleStatus `json:"tailscale"`

	Message string `json:"message,omitempty"`
}

// TailscaleStatus is what Homebase can say about Tailscale, which it does not
// manage — it only reports it, because on some connections it is the only thing
// that reaches this machine from outside.
type TailscaleStatus struct {
	Installed bool   `json:"installed"`
	Running   bool   `json:"running"`
	State     string `json:"state,omitempty"`
	// Name is what somebody types from away.
	Name      string   `json:"name,omitempty"`
	Addresses []string `json:"addresses,omitempty"`
}

// DDNSStatus is what the dynamic DNS name is doing.
type DDNSStatus struct {
	Configured  bool   `json:"configured"`
	Provider    string `json:"provider,omitempty"`
	Name        string `json:"name,omitempty"`
	Enabled     bool   `json:"enabled"`
	Working     bool   `json:"working"`
	LastChecked string `json:"last_checked,omitempty"`
	Detail      string `json:"detail,omitempty"`
}

// NewDevice is a device and the configuration for it, returned once.
type NewDevice struct {
	VPNDevice

	Config string `json:"config"`
	QRCode string `json:"qr_code,omitempty"`

	// QRImage is the same code as a PNG data URI, for a browser. Separate from
	// the terminal drawing because neither can be shown where the other belongs,
	// and a page that renders block characters as text is not a QR code.
	QRImage string `json:"qr_image,omitempty"`
	Message string `json:"message"`
}

func (c *Client) VPNStatus(ctx context.Context) (*VPNStatus, error) {
	var status VPNStatus
	if err := c.Call(ctx, "vpn.status", struct{}{}, false, &status); err != nil {
		return nil, err
	}
	return &status, nil
}

// DisableVPN switches remote access off, keeping the keys.
//
// "Switch this off" and "forget every device I have set up" are different
// intentions. Collapsing them would mean turning the VPN off for an afternoon
// costs re-issuing a configuration to every phone in the house.
func (c *Client) DisableVPN(ctx context.Context) (*VPNStatus, error) {
	var status VPNStatus
	if err := c.Call(ctx, "vpn.disable", struct{}{}, true, &status); err != nil {
		return nil, err
	}
	return &status, nil
}

func (c *Client) SetUpVPN(ctx context.Context, hostname string) (*VPNStatus, error) {
	params := struct {
		Hostname string `json:"hostname"`
	}{Hostname: hostname}

	var status VPNStatus
	if err := c.Call(ctx, "vpn.setup", params, true, &status); err != nil {
		return nil, err
	}
	return &status, nil
}

// AddVPNDevice issues a key. What comes back is shown once and stored nowhere.
func (c *Client) AddVPNDevice(ctx context.Context, name string) (*NewDevice, error) {
	params := struct {
		Name string `json:"name"`
	}{Name: name}

	var device NewDevice
	if err := c.Call(ctx, "vpn.add_device", params, true, &device); err != nil {
		return nil, err
	}
	return &device, nil
}

func (c *Client) RemoveVPNDevice(ctx context.Context, name string) (*VPNStatus, error) {
	params := struct {
		Name string `json:"name"`
	}{Name: name}

	var status VPNStatus
	if err := c.Call(ctx, "vpn.remove_device", params, true, &status); err != nil {
		return nil, err
	}
	return &status, nil
}

// SetDNS keeps a name pointing at this house. The token travels one way.
func (c *Client) SetDNS(ctx context.Context, provider, name, token string) (*DDNSStatus, error) {
	params := struct {
		Provider string `json:"provider"`
		Name     string `json:"name"`
		Token    string `json:"token,omitempty"`
	}{Provider: provider, Name: name, Token: token}

	var status DDNSStatus
	if err := c.Call(ctx, "vpn.set_dns", params, false, &status); err != nil {
		return nil, err
	}
	return &status, nil
}

func (c *Client) ClearDNS(ctx context.Context) (*DDNSStatus, error) {
	var status DDNSStatus
	if err := c.Call(ctx, "vpn.clear_dns", struct{}{}, true, &status); err != nil {
		return nil, err
	}
	return &status, nil
}
