package hostd

import (
	"context"
	"time"
)

// Update operations.
//
// This is the read half. Reporting what a machine is running has to work before
// changing what it runs can be trusted — the interruption tests in ADR-0018 all
// assert against this, and a broken update is diagnosed with it.

// UpdateServices is what the update operations need.
type UpdateServices struct {
	// aptSource is the file that decides where this machine gets packages from.
	aptSource string

	// dpkgUpdates is dpkg's journal directory. Files in it mean dpkg died with
	// work outstanding.
	dpkgUpdates string
}

func NewUpdateServices() *UpdateServices {
	return &UpdateServices{
		aptSource:   defaultAptSource(),
		dpkgUpdates: "/var/lib/dpkg/updates",
	}
}

// RegisterUpdateOperations adds the update domain to a registry.
func RegisterUpdateOperations(r *Registry, services *UpdateServices) {
	r.MustRegister(Operation{
		Name: "update.status",
		Summary: "Report what version this machine is running, whether its " +
			"components agree, and which channel it updates from.",
		Risk:        RiskRead,
		Permissions: nil,
		Confirm:     ConfirmNone,
		Timeout:     15 * time.Second,
		Handler:     Typed(services.status),
	})
}

func (s *UpdateServices) status(ctx context.Context, _ struct{}) (any, error) {
	return ReadUpdateStatus(ctx, s.aptSource, s.dpkgUpdates), nil
}
