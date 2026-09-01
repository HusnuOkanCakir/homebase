package hostclient

import "context"

// File sharing, as core sees it.
//
// The half of a home server a browser cannot be: a drive on somebody's laptop.
// core passes these through and adds nothing — the decisions about who may open
// what are all in hostd, where the configuration is written.

// Share is one folder published onto the local network.
type Share struct {
	Name     string `json:"name"`
	Location string `json:"location"`
	ReadOnly bool   `json:"read_only"`
	AddedAt  string `json:"added_at"`

	// Access is who may open it, by Homebase username. Empty means everybody
	// with an account.
	Access []string `json:"access,omitempty"`

	Path string `json:"path"`

	// Available is whether the disk holding it is connected. A share whose disk
	// is unplugged is still configured and has nothing behind it, which is a
	// different thing from not being shared.
	Available bool `json:"available"`

	// Address is what somebody types on another machine.
	Address string `json:"address"`
}

// ShareStatus is everything about file sharing on this server.
type ShareStatus struct {
	Installed  bool     `json:"installed"`
	Running    bool     `json:"running"`
	Shares     []Share  `json:"shares"`
	Users      []string `json:"users"`
	ServerName string   `json:"server_name"`

	// PeoplePath is where private folders live on this server, as hostd reports
	// it. Never composed here: a path core builds for itself keeps working
	// after the layout changes and lands somewhere else entirely.
	PeoplePath string `json:"people_path,omitempty"`
}

func (c *Client) Shares(ctx context.Context) (*ShareStatus, error) {
	var status ShareStatus
	if err := c.Call(ctx, "share.status", nil, false, &status); err != nil {
		return nil, err
	}
	return &status, nil
}

// AddShare publishes a folder onto the local network.
func (c *Client) AddShare(ctx context.Context, name, location string, readOnly bool) (map[string]any, error) {
	params := struct {
		Name     string `json:"name"`
		Location string `json:"location"`
		ReadOnly bool   `json:"read_only"`
	}{name, location, readOnly}

	var result map[string]any
	if err := c.Call(ctx, "share.add", params, true, &result); err != nil {
		return nil, err
	}
	return result, nil
}

// RemoveShare takes a folder off the network. The files stay.
func (c *Client) RemoveShare(ctx context.Context, name string) (map[string]any, error) {
	var result map[string]any
	if err := c.Call(ctx, "share.remove",
		struct {
			Name string `json:"name"`
		}{name}, true, &result); err != nil {
		return nil, err
	}
	return result, nil
}

// SetSharePassword creates or changes a file-sharing account's password.
//
// The password travels to hostd and no further: the operation declares it as a
// secret, so the audit log records that the password was set and never what it
// was set to.
func (c *Client) SetSharePassword(ctx context.Context, username, password string) (map[string]any, error) {
	params := struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}{username, password}

	var result map[string]any
	if err := c.Call(ctx, "share.set_password", params, true, &result); err != nil {
		return nil, err
	}
	return result, nil
}

func (c *Client) RemoveShareUser(ctx context.Context, username string) (map[string]any, error) {
	var result map[string]any
	if err := c.Call(ctx, "share.remove_user",
		struct {
			Username string `json:"username"`
		}{username}, true, &result); err != nil {
		return nil, err
	}
	return result, nil
}

// MakePersonalFolder creates the private folder that belongs to one person.
//
// Called when an account is created. The failure is not fatal to creating the
// account, and the caller is expected to say so rather than roll back: an
// account that exists without a folder acquires one on the next attempt, while
// an account that failed to be created because a disk was busy leaves an
// administrator with a joining code they have already handed over.
func (c *Client) MakePersonalFolder(ctx context.Context, username string) (map[string]any, error) {
	var result map[string]any
	if err := c.Call(ctx, "share.make_personal_folder",
		struct {
			Username string `json:"username"`
		}{username}, false, &result); err != nil {
		return nil, err
	}
	return result, nil
}

// RetirePersonalFolder moves somebody's private folder aside when their account
// is removed. Nothing is deleted; the answer says where the files went.
func (c *Client) RetirePersonalFolder(ctx context.Context, username string) (map[string]any, error) {
	var result map[string]any
	if err := c.Call(ctx, "share.retire_personal_folder",
		struct {
			Username string `json:"username"`
		}{username}, true, &result); err != nil {
		return nil, err
	}
	return result, nil
}

// SetShareAccess chooses who may open a folder. An empty list means everybody
// with an account.
func (c *Client) SetShareAccess(ctx context.Context, name string, access []string) (map[string]any, error) {
	if access == nil {
		access = []string{}
	}
	var result map[string]any
	if err := c.Call(ctx, "share.set_access",
		struct {
			Name   string   `json:"name"`
			Access []string `json:"access"`
		}{name, access}, true, &result); err != nil {
		return nil, err
	}
	return result, nil
}
