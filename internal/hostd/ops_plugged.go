package hostd

import (
	"context"
	"strings"
	"time"
)

// Operations for disks people plug into the server. See plugged.go for what
// they are and why they are not storage locations.

func RegisterPluggedOperations(r *Registry, services *PluggedServices) {
	r.MustRegister(Operation{
		Name:    "plugged.status",
		Summary: "List the disks somebody has plugged into this server.",
		Risk:    RiskRead,
		Confirm: ConfirmNone,
		Timeout: 30 * time.Second,
		Handler: Typed(services.statusOp),
	})

	r.MustRegister(Operation{
		Name: "plugged.eject",
		Summary: "Finish with a plugged-in disk so it can be unplugged without " +
			"losing anything.",
		// Low. It stops offering a disk that is read-only anyway, and the disk
		// itself is untouched — this is the safe half of unplugging, not a
		// change to anybody's data.
		Risk:        RiskLow,
		Permissions: []string{"files.read"},
		Confirm:     ConfirmNone,
		Timeout:     1 * time.Minute,
		Handler:     Typed(services.ejectOp),
	})
}

func (s *PluggedServices) statusOp(_ context.Context, _ NoParams) (any, error) {
	disks := s.Status()
	if disks == nil {
		disks = []PluggedDisk{}
	}
	return map[string]any{"disks": disks}, nil
}

type PluggedDiskRef struct {
	Name string `json:"name"`
}

func (s *PluggedServices) ejectOp(ctx context.Context, params PluggedDiskRef) (any, error) {
	name := strings.ToLower(strings.TrimSpace(params.Name))
	if err := s.Eject(ctx, name); err != nil {
		return nil, err
	}
	return map[string]any{
		"name":     name,
		"ejected":  true,
		"message":  "Finished with " + name + ". It can be unplugged now.",
		"readable": false,
	}, nil
}
