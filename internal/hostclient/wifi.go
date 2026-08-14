package hostclient

import "context"

// Wireless.
//
// The passphrase travels one way. It is sent to hostd and never returned by
// anything here, because a field that can be read back is a field that ends up
// in a log, a browser's memory, or a diagnostic file.

// WifiNetwork is one network the server can see.
type WifiNetwork struct {
	SSID     string `json:"ssid"`
	Signal   int    `json:"signal"`
	Bars     int    `json:"bars"`
	Security string `json:"security"`
	Current  bool   `json:"current"`
}

// WifiStatus is what the server's wireless is doing.
type WifiStatus struct {
	Available bool   `json:"available"`
	Interface string `json:"interface,omitempty"`

	Connected bool   `json:"connected"`
	SSID      string `json:"ssid,omitempty"`

	Addresses []string `json:"addresses,omitempty"`
	Signal    int      `json:"signal,omitempty"`
	Bars      int      `json:"bars,omitempty"`

	Configured bool `json:"configured"`

	// HasWiredConnection decides how frightening the screen has to be: with a
	// cable in, a failed attempt costs nothing.
	HasWiredConnection bool `json:"has_wired_connection"`
}

type WifiScan struct {
	Networks []WifiNetwork `json:"networks"`
	Message  string        `json:"message,omitempty"`
}

func (c *Client) WifiStatus(ctx context.Context) (*WifiStatus, error) {
	var status WifiStatus
	if err := c.Call(ctx, "network.wifi_status", struct{}{}, false, &status); err != nil {
		return nil, err
	}
	return &status, nil
}

func (c *Client) ScanWifi(ctx context.Context) (*WifiScan, error) {
	var scan WifiScan
	if err := c.Call(ctx, "network.wifi_scan", struct{}{}, false, &scan); err != nil {
		return nil, err
	}
	return &scan, nil
}

// JoinWifi asks the server to join a network.
//
// hostd puts the previous configuration back if the machine does not come up on
// the new one, so a wrong password costs a wait rather than a server.
func (c *Client) JoinWifi(ctx context.Context, ssid, passphrase string) (*WifiStatus, error) {
	params := struct {
		SSID       string `json:"ssid"`
		Passphrase string `json:"passphrase,omitempty"`
	}{SSID: ssid, Passphrase: passphrase}

	var status WifiStatus
	if err := c.Call(ctx, "network.wifi_connect", params, true, &status); err != nil {
		return nil, err
	}
	return &status, nil
}

func (c *Client) ForgetWifi(ctx context.Context) (*WifiStatus, error) {
	var status WifiStatus
	if err := c.Call(ctx, "network.wifi_forget", struct{}{}, true, &status); err != nil {
		return nil, err
	}
	return &status, nil
}
