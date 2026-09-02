package hostclient

import "context"

// RemoteFolder is one folder on another computer this server can open.
//
// Mirrored by hand from hostd's own type, like Share and App. The password is
// deliberately not here in either direction: it goes to hostd once, reaches
// mount.cifs through a root-only file, and no operation returns it.
type RemoteFolder struct {
	Name     string   `json:"name"`
	Host     string   `json:"host"`
	Share    string   `json:"share"`
	Username string   `json:"username"`
	AddedBy  string   `json:"added_by,omitempty"`
	Access   []string `json:"access,omitempty"`
	AddedAt  string   `json:"added_at"`

	Path string `json:"path"`

	// Connected is whether the other computer is answering. A laptop that has
	// gone to sleep leaves a folder that is configured, listed and empty, which
	// looks exactly like one whose files have been deleted.
	Connected bool `json:"connected"`
}

type RemoteStatus struct {
	// Installed is whether this machine can open a folder on another one at
	// all. The client is not part of the base installation.
	Installed bool           `json:"installed"`
	Folders   []RemoteFolder `json:"folders"`
}

func (c *Client) RemoteFolders(ctx context.Context) (*RemoteStatus, error) {
	var status RemoteStatus
	if err := c.Call(ctx, "remote.status", nil, false, &status); err != nil {
		return nil, err
	}
	return &status, nil
}

// ConnectRemoteFolder opens a folder another computer is sharing.
func (c *Client) ConnectRemoteFolder(ctx context.Context,
	name, host, share, username, password, addedBy string, access []string) (map[string]any, error) {
	if access == nil {
		access = []string{}
	}
	params := struct {
		Name     string   `json:"name"`
		Host     string   `json:"host"`
		Share    string   `json:"share"`
		Username string   `json:"username"`
		Password string   `json:"password"`
		AddedBy  string   `json:"added_by"`
		Access   []string `json:"access"`
	}{name, host, share, username, password, addedBy, access}

	var result map[string]any
	if err := c.Call(ctx, "remote.connect", params, true, &result); err != nil {
		return nil, err
	}
	return result, nil
}

func (c *Client) DisconnectRemoteFolder(ctx context.Context, name string) (map[string]any, error) {
	var result map[string]any
	if err := c.Call(ctx, "remote.disconnect",
		struct {
			Name string `json:"name"`
		}{name}, true, &result); err != nil {
		return nil, err
	}
	return result, nil
}

// ReconnectRemoteFolder tries again after the other computer has been woken.
func (c *Client) ReconnectRemoteFolder(ctx context.Context, name string) (map[string]any, error) {
	var result map[string]any
	if err := c.Call(ctx, "remote.reconnect",
		struct {
			Name string `json:"name"`
		}{name}, false, &result); err != nil {
		return nil, err
	}
	return result, nil
}
