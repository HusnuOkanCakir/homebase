package hostclient

import "context"

// PluggedDisk is one filesystem on a disk somebody plugged into the server.
//
// Mirrored by hand from hostd's own type, like Share and App.
type PluggedDisk struct {
	Name       string `json:"name"`
	UUID       string `json:"uuid"`
	Label      string `json:"label,omitempty"`
	Filesystem string `json:"filesystem,omitempty"`
	SizeBytes  uint64 `json:"size_bytes"`

	// Path is where it is mounted on the server, and Connected whether it is
	// still there. A disk pulled out without warning leaves a folder that lists
	// as empty, which looks exactly like a disk whose files have been deleted.
	Path      string `json:"path"`
	Connected bool   `json:"connected"`
}

func (c *Client) PluggedDisks(ctx context.Context) ([]PluggedDisk, error) {
	var reply struct {
		Disks []PluggedDisk `json:"disks"`
	}
	if err := c.Call(ctx, "plugged.status", nil, false, &reply); err != nil {
		return nil, err
	}
	return reply.Disks, nil
}

// EjectPluggedDisk finishes with a disk so it can be unplugged safely.
func (c *Client) EjectPluggedDisk(ctx context.Context, name string) (map[string]any, error) {
	var result map[string]any
	if err := c.Call(ctx, "plugged.eject",
		struct {
			Name string `json:"name"`
		}{name}, false, &result); err != nil {
		return nil, err
	}
	return result, nil
}
