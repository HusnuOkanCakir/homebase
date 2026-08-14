package hostd

import (
	"context"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
	"time"
)

// Update operations.
//
// Reporting comes before changing: the interruption tests in ADR-0018 assert
// against `update.status`, and a broken update is diagnosed with it.

// knownChannels are the suites a machine may subscribe to, ordered by how much
// has been tried on them. Ordered rather than a set because that ordering is
// what "promotion" means in ADR-0018.
var knownChannels = []string{"development", "alpha", "beta", "stable"}

// defaultOrigin is where a machine gets updates from unless told otherwise.
const defaultOrigin = "https://apt.homebase.computer"

// UpdateServices is what the update operations need.
type UpdateServices struct {
	// aptSource is the file that decides where this machine gets packages from.
	aptSource string

	// dpkgUpdates is dpkg's journal directory. Files in it mean dpkg died with
	// work outstanding.
	dpkgUpdates string

	// resultDir is where the update units leave what they found.
	resultDir string
}

func NewUpdateServices() *UpdateServices {
	return &UpdateServices{
		aptSource:   defaultAptSource(),
		dpkgUpdates: "/var/lib/dpkg/updates",
		resultDir:   updateResultDir,
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

	r.MustRegister(Operation{
		Name: "update.configure",
		Summary: "Choose which channel this machine updates from, and where " +
			"it fetches updates.",
		// High, and not because it changes anything by itself. It writes the
		// file that decides which code this machine will later execute as root,
		// and moving between channels is how a machine ends up running a build
		// nobody has tried.
		Risk:        RiskHigh,
		Permissions: []string{"update.manage"},
		Confirm:     ConfirmExplicit,
		Timeout:     60 * time.Second,
		Rollback:    "update.configure, with the previous channel",
		Handler:     Typed(services.configure),
	})

	r.MustRegister(Operation{
		Name: "update.apply",
		Summary: "Install the newer version this machine's channel offers, and " +
			"put the machine back if it does not come up healthy.",
		// The highest risk in the system. It replaces every component while
		// somebody's photographs are on the disk, and it restarts the service
		// performing it.
		Risk:        RiskHigh,
		Permissions: []string{"update.manage"},
		Confirm:     ConfirmExplicit,
		Timeout:     30 * time.Second,
		Rollback:    "automatic — a failed health check puts the previous version back",
		Handler:     Typed(services.apply),
	})

	r.MustRegister(Operation{
		Name:        "update.progress",
		Summary:     "Report how far an update has got, and how it ended.",
		Risk:        RiskRead,
		Permissions: []string{"update.read"},
		Confirm:     ConfirmNone,
		Timeout:     10 * time.Second,
		Handler:     Typed(services.progress),
	})

	r.MustRegister(Operation{
		Name:    "update.check",
		Summary: "Ask whether a newer version of Homebase has been published.",
		// A read, but one that reaches the network and refreshes apt's index.
		// It changes nothing this machine runs.
		Risk:        RiskRead,
		Permissions: []string{"update.read"},
		Confirm:     ConfirmNone,
		Timeout:     120 * time.Second,
		Handler:     Typed(services.check),
	})
}

func (s *UpdateServices) status(ctx context.Context, _ struct{}) (any, error) {
	return ReadUpdateStatus(ctx, s.aptSource, s.dpkgUpdates), nil
}

type configureRequest struct {
	Channel string `json:"channel"`

	// Origin is where updates are fetched from. Settable so that a test — and,
	// one day, a mirror — can point a machine somewhere else.
	//
	// This is much less dangerous than it looks, and the reason is worth
	// stating: the source names an explicit keyring with `Signed-By`, so an
	// origin serving anything Homebase did not sign is refused by apt before a
	// byte is unpacked. Pointing a machine at a different address does not let
	// anybody install anything; it lets them serve nothing that verifies.
	Origin string `json:"origin,omitempty"`
}

type configureResult struct {
	Channel string `json:"channel"`
	Origin  string `json:"origin"`

	// Reachable is whether the newly configured source actually answered with
	// an index that verified. Reported rather than fatal: a machine offline
	// when its channel is changed has still had its channel changed, and
	// refusing to record that would leave the file and the report disagreeing.
	Reachable bool   `json:"reachable"`
	Detail    string `json:"detail,omitempty"`
}

func (s *UpdateServices) configure(ctx context.Context, req configureRequest) (any, error) {
	channel := strings.ToLower(strings.TrimSpace(req.Channel))
	if !validChannel(channel) {
		return nil, &Error{
			Code:    "update.unknown_channel",
			Message: "That is not a channel Homebase publishes.",
			Detail: fmt.Sprintf("asked for %q; the channels are %s",
				req.Channel, strings.Join(knownChannels, ", ")),
			Recoverable: true,
			Recovery: "Choose one of: " + strings.Join(knownChannels, ", ") +
				". Most servers should be on stable.",
			Status: 400,
		}
	}

	origin := strings.TrimSpace(req.Origin)
	if origin == "" {
		origin = defaultOrigin
	}
	if err := validOrigin(origin); err != nil {
		return nil, &Error{
			Code:        "update.invalid_origin",
			Message:     "That is not somewhere updates can be fetched from.",
			Detail:      err.Error(),
			Recoverable: true,
			Recovery:    "Leave this blank to use Homebase's own repository.",
			Status:      400,
		}
	}

	if err := writeAptSource(origin, channel); err != nil {
		return nil, err
	}

	// Checked immediately, so that "the channel is set" and "the channel works"
	// are answered at the same moment. A machine that reports a channel it
	// cannot reach is one whose owner finds out at the worst time.
	result := configureResult{Channel: channel, Origin: origin}
	checked := s.refresh(ctx)
	result.Reachable = checked.reachable
	result.Detail = checked.detail
	return result, nil
}

// refreshed is what the check unit reported back.
type refreshed struct {
	reachable bool
	available string
	detail    string
}

// refresh runs the check unit and reads its answer.
func (s *UpdateServices) refresh(ctx context.Context) refreshed {
	out, err := runUpdateUnit(ctx, updateCheckUnit)
	if err != nil {
		// systemd could not run the unit at all — it is missing, or it failed
		// before the script wrote anything. Distinct from the archive being
		// unreachable, and reported as itself rather than as "no update".
		return refreshed{detail: strings.TrimSpace(out) + " (" + err.Error() + ")"}
	}

	values := readResultFile(filepath.Join(s.resultDir, "check"))
	return refreshed{
		reachable: values["reachable"] == "true",
		available: values["available"],
		detail:    values["detail"],
	}
}

type checkResult struct {
	// Current is what is installed; Available is what apt would install.
	Current   string `json:"current"`
	Available string `json:"available"`

	// UpdateAvailable is deliberately computed with dpkg rather than by
	// comparing strings, because Debian version ordering is not string
	// ordering and getting it wrong means either hiding a real update or
	// accepting a downgrade.
	UpdateAvailable bool `json:"update_available"`

	Channel string `json:"channel"`

	// Reachable is whether the repository answered. Separated from "no update
	// available", because they are the same silence and very different facts:
	// one means you are up to date, the other means you cannot find out.
	Reachable bool   `json:"reachable"`
	Detail    string `json:"detail,omitempty"`
}

func (s *UpdateServices) check(ctx context.Context, _ struct{}) (any, error) {
	status := ReadUpdateStatus(ctx, s.aptSource, s.dpkgUpdates)

	result := checkResult{Current: status.Version, Channel: status.Channel}
	if status.Channel == "" {
		result.Detail = "This server has no update source configured yet."
		return result, nil
	}

	checked := s.refresh(ctx)
	result.Reachable = checked.reachable
	result.Detail = checked.detail
	if !checked.reachable {
		return result, nil
	}

	// The candidate is homebase-core's, because that is the component a person
	// is running; the other three move with it by `(= version)` dependency.
	result.Available = checked.available
	result.UpdateAvailable = newerVersion(ctx, checked.available, status.Version)
	return result, nil
}

// validChannel reports whether a name is one Homebase publishes.
//
// Checked before anything writes it into an apt source, because that file
// decides what code this machine will run as root — an unvalidated string there
// is an injection into apt's configuration.
func validChannel(name string) bool {
	for _, known := range knownChannels {
		if name == known {
			return true
		}
	}
	return false
}

// validOrigin rejects anything that is not a plain http or https URL.
//
// http is permitted because apt's security does not come from the transport —
// the index is signed, and a tampered one is refused whether it arrived over
// TLS or not. It is what makes a local mirror, or the test repository, possible
// without a certificate. What is rejected is anything that is not a URL at all,
// because this string is written into apt's configuration.
func validOrigin(origin string) error {
	parsed, err := url.Parse(origin)
	if err != nil {
		return fmt.Errorf("%q is not a valid address", origin)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return fmt.Errorf("an update source must be an http or https address, not %q", origin)
	}
	if parsed.Host == "" {
		return fmt.Errorf("%q has no host in it", origin)
	}
	if strings.ContainsAny(origin, "\n\r") {
		return fmt.Errorf("an update source cannot contain a line break")
	}
	return nil
}

type applyResult struct {
	// Started is whether systemd took the job on. It is not whether the update
	// worked: by the time it has, this process will have been restarted.
	Started bool   `json:"started"`
	Detail  string `json:"detail,omitempty"`
}

// apply asks systemd to run the update, and returns without waiting.
//
// It cannot wait. The update replaces homebase-hostd and restarts it, so the
// process holding the request open is the process being replaced. Callers watch
// `update.progress` instead — which also means a dashboard whose connection
// died during the restart can reconnect and find out how it went.
func (s *UpdateServices) apply(ctx context.Context, _ struct{}) (any, error) {
	status := ReadUpdateStatus(ctx, s.aptSource, s.dpkgUpdates)

	if status.Channel == "" {
		return nil, &Error{
			Code:        "update.no_channel",
			Message:     "This server does not know where to get updates from.",
			Detail:      "no update source is configured",
			Recoverable: true,
			Recovery:    "Choose an update channel first.",
			Status:      409,
		}
	}

	// Refused rather than attempted. Starting a new transaction on a machine
	// with an unfinished one is how a recoverable state becomes an unrecoverable
	// one, and dpkg would refuse anyway — with a message written for somebody
	// who knows what dpkg is.
	if status.Interrupted {
		return nil, &Error{
			Code:        "update.previous_unfinished",
			Message:     "An earlier update on this server did not finish.",
			Detail:      "dpkg has a transaction outstanding",
			Recoverable: true,
			Recovery: "Finish it first by running `sudo dpkg --configure -a` on " +
				"the server, then try again.",
			Status: 409,
		}
	}

	out, err := startUpdateUnit(ctx, updateApplyUnit)
	if err != nil {
		return nil, &Error{
			Code:        "update.could_not_start",
			Message:     "The update could not be started.",
			Detail:      strings.TrimSpace(out) + " (" + err.Error() + ")",
			Recoverable: true,
			Recovery:    "Try again. If it keeps failing, check the system logs.",
			Status:      500,
		}
	}

	return applyResult{Started: true}, nil
}

type progressResult struct {
	// Stage is where the update got to: refreshing, downloading, snapshot,
	// applying, health, rolling-back, rolled-back, done. Empty when no update
	// has ever been started on this machine.
	Stage string `json:"stage"`

	// Result is "ok" or "failed", and is empty while the update is still
	// running. Emptiness is meaningful: it is how a caller tells "in progress"
	// from "finished", without this process having to remember anything across
	// the restart that the update performs on it.
	Result string `json:"result,omitempty"`

	From   string `json:"from,omitempty"`
	To     string `json:"to,omitempty"`
	Detail string `json:"detail,omitempty"`

	// Running is whether systemd still has the unit active. Checked as well as
	// the file, because a machine that lost power mid-update comes back with a
	// stage written and nothing running — which is a finished update in the
	// worst sense and must not read as one in progress for ever.
	Running bool `json:"running"`
}

func (s *UpdateServices) progress(ctx context.Context, _ struct{}) (any, error) {
	values := readResultFile(filepath.Join(s.resultDir, "apply"))

	return progressResult{
		Stage:   values["stage"],
		Result:  values["result"],
		From:    values["from"],
		To:      values["to"],
		Detail:  values["detail"],
		Running: unitIsActive(ctx, updateApplyUnit),
	}, nil
}
