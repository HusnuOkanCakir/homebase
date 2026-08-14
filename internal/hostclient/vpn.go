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

	Message string `json:"message,omitempty"`
}

// NewDevice is a device and the configuration for it, returned once.
type NewDevice struct {
	VPNDevice

	Config  string `json:"config"`
	QRCode  string `json:"qr_code,omitempty"`
	Message string `json:"message"`
}

func (c *Client) VPNStatus(ctx context.Context) (*VPNStatus, error) {
	var status VPNStatus
	if err := c.Call(ctx, "vpn.status", struct{}{}, false, &status); err != nil {
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
