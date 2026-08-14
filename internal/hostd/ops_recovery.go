package hostd

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Putting a broken server back together.
//
// The three things somebody needs when Homebase is not working, in the order
// they need them: *tell me what is wrong* (a diagnostic bundle), *try fixing it*
// (repair), and *start again without losing my photographs* (factory reset).
//
// Two rules shape all of it.
//
// **Repair is a fixed list, not a diagnosis.** Each step checks whether
// something is true and makes it true if not. There is no branching on what a
// previous step found, because a repair tool that reasons about the state of a
// broken machine is a program written against inputs nobody has seen. Every step
// says what it did, and doing nothing is a result.
//
// **A diagnostic bundle is written for the machine's owner to read.** They are
// going to send it to somebody. So it collects what a person debugging would
// ask for, it says at the top what is in it, and it says what was deliberately
// left out — because the failure mode of a support bundle is not being unhelpful,
// it is quietly containing the password database.

// diagnosticsDir is where bundles are written. core reads them from here and
// nowhere else, so the path never comes from a request.
const diagnosticsDir = "/var/lib/homebase/diagnostics"

// RegisterRecoveryOperations adds the recovery domain to a registry.
func RegisterRecoveryOperations(r *Registry, update *UpdateServices) {
	recovery := &RecoveryServices{update: update}

	r.MustRegister(Operation{
		Name:    "system.diagnostics",
		Summary: "Collect what somebody would need to work out why this server is unwell.",
		// A read, but not a harmless one: it writes a file, and that file is
		// meant to leave the machine. Permissioned accordingly.
		Risk:        RiskLow,
		Permissions: []string{"system.manage"},
		Confirm:     ConfirmNone,
		Timeout:     2 * time.Minute,
		Handler:     Typed(recovery.diagnostics),
	})

	r.MustRegister(Operation{
		Name:    "system.repair",
		Summary: "Put back the things a broken or interrupted install leaves wrong.",
		// Medium. It restarts services and can finish a dpkg transaction, so it
		// is not a read — but every step is idempotent and none of them delete
		// anything, which is what makes it safe to offer to somebody who does
		// not know what is wrong.
		Risk:        RiskMedium,
		Permissions: []string{"system.manage"},
		Confirm:     ConfirmNone,
		Timeout:     10 * time.Minute,
		Rollback:    "", // Each step only ever makes something true that should be.
		Handler:     Typed(recovery.repair),
	})

	r.MustRegister(Operation{
		Name:    "system.factory_reset",
		Summary: "Return this server to how it was before anybody set it up.",
		// The most destructive operation in Homebase, and graded above reboot
		// for that reason. It removes every account — so the person running it
		// can lock themselves out of their own machine — and it can be told to
		// delete everything in /srv/homebase as well.
		Risk:        RiskCritical,
		Permissions: []string{"system.manage"},
		Confirm:     ConfirmExplicit,
		Timeout:     5 * time.Minute,
		Rollback:    "backup.restore, from a backup made before the reset",
		Handler:     Typed(recovery.factoryReset),
	})
}

type RecoveryServices struct {
	// update, because the first thing repair checks is whether a package
	// transaction was left unfinished — which is the state the update work
	// already knows how to recognise. Asking it rather than reimplementing the
	// check keeps one answer to "is this machine half-upgraded?".
	update *UpdateServices
}

// --- The diagnostic bundle ------------------------------------------------------

// Diagnostics is where the bundle went, and what is in it.
type Diagnostics struct {
	Path      string   `json:"path"`
	Bytes     int64    `json:"bytes"`
	CreatedAt string   `json:"created_at"`
	Includes  []string `json:"includes"`

	// Excludes is not documentation. It is the list somebody checks before
	// sending this to a stranger, and it is why the bundle is safe to send.
	Excludes []string `json:"excludes"`

	Message string `json:"message"`
}

// whatIsCollected is the whole bundle, as a fixed list.
//
// Each entry is a name, a description for the person reading, and a command with
// fixed arguments. Nothing here takes a parameter from a request: a diagnostic
// bundle that could be told what to run would be the generic execution path
// ADR-0006 exists to prevent, wearing a support tool's clothes.
var whatIsCollected = []struct {
	file, describes string
	command         []string
}{
	{"versions.txt", "which version of each part of Homebase is installed",
		[]string{"dpkg-query", "-W", "-f", "${Package} ${Version} ${Status}\n",
			"homebase-hostd", "homebase-core", "homebase-apps", "homebase-dashboard"}},

	{"services.txt", "whether Homebase's services are running",
		[]string{"systemctl", "status", "--no-pager", "--lines=0",
			"homebase-hostd.service", "homebase-hostd.socket", "homebase-core.service",
			"homebase-backup.timer", "homebase-update-check.timer"}},

	{"failed-units.txt", "anything on the machine that failed to start",
		[]string{"systemctl", "list-units", "--state=failed", "--no-pager", "--no-legend"}},

	{"journal.txt", "the last day of messages from Homebase's services",
		[]string{"journalctl", "--no-pager", "--since", "-24h", "-n", "5000",
			"-u", "homebase-hostd", "-u", "homebase-core", "-u", "homebase-backup",
			"-u", "homebase-update-check", "-u", "homebase-update-apply"}},

	{"boot.txt", "whether the machine came up cleanly",
		[]string{"systemd-analyze", "blame", "--no-pager"}},

	{"disks.txt", "the disks this server can see and how full they are",
		[]string{"lsblk", "-o", "NAME,SIZE,FSTYPE,LABEL,MOUNTPOINT,TRAN", "--json"}},

	{"space.txt", "free space, which is the cause more often than anything else",
		[]string{"df", "-h"}},

	{"memory.txt", "memory and swap",
		[]string{"free", "-h"}},

	{"network.txt", "addresses this server has",
		[]string{"ip", "-brief", "address"}},

	{"update.txt", "where updates come from and whether one was interrupted",
		[]string{"apt-cache", "policy", "homebase-core", "homebase-hostd"}},

	{"dpkg.txt", "whether a package transaction was left unfinished",
		[]string{"dpkg", "--audit"}},

	{"os.txt", "which Ubuntu this is",
		[]string{"cat", "/etc/os-release"}},
}

// whatIsNeverCollected is what somebody is told is not in the bundle.
//
// Reported to the caller rather than only implied by the list above, because
// this is the sentence that decides whether it is reasonable to email the file
// to somebody. It has to be true, so nothing is added to whatIsCollected without
// checking it against this.
var whatIsNeverCollected = []string{
	"your password, or the scrambled form of it Homebase stores",
	"your recovery code",
	"the keys that secure the connection between your browser and this server",
	"anything from your own files — no photographs, documents or media",
	"the contents of Homebase's database",
	"the settings inside your applications, which may hold their own passwords",
}

func (s *RecoveryServices) diagnostics(ctx context.Context, _ struct{}) (any, error) {
	if err := os.MkdirAll(diagnosticsDir, 0o750); err != nil {
		return nil, &Error{
			Code:        "diagnostics.failed",
			Message:     "Homebase could not write the diagnostic file.",
			Detail:      err.Error(),
			Recoverable: true,
			Recovery:    "Check there is free space on the system disk.",
			Status:      500,
		}
	}

	// The directory as well as the file. hostd creates it as root, and the
	// process that serves the download runs as `homebase` — a root:root 0750
	// directory is one it cannot even list, so the file inside being readable
	// makes no difference. Found by the download returning 404 on a machine
	// where the file was sitting there.
	if err := giveToServiceGroup(diagnosticsDir); err != nil {
		return nil, &Error{
			Code:        "diagnostics.unreadable",
			Message:     "Homebase cannot reach the folder it keeps diagnostic files in.",
			Detail:      err.Error(),
			Recoverable: true,
			Recovery:    "Try again. If it keeps happening, the homebase account is missing.",
			Status:      500,
		}
	}

	// One at a time. Keeping every bundle ever made would fill the disk of a
	// machine whose owner is already having trouble with it.
	removeOldDiagnostics()

	now := time.Now().UTC()
	path := filepath.Join(diagnosticsDir,
		fmt.Sprintf("homebase-diagnostics-%s.txt", now.Format("2006-01-02-150405")))

	var out strings.Builder
	out.WriteString("Homebase diagnostics\n")
	out.WriteString("====================\n\n")
	out.WriteString("Collected on " + now.Format(time.RFC1123) + ".\n\n")
	out.WriteString("This file is meant to be sent to somebody helping you. It does not\n")
	out.WriteString("contain:\n\n")
	for _, excluded := range whatIsNeverCollected {
		out.WriteString("  - " + excluded + "\n")
	}
	out.WriteString("\nIt does contain the name of this server, the names of your disks and\n")
	out.WriteString("applications, and error messages, which can include file paths.\n")

	includes := make([]string, 0, len(whatIsCollected))
	for _, section := range whatIsCollected {
		out.WriteString("\n\n=== " + section.file + " — " + section.describes + " ===\n\n")
		out.WriteString(collect(ctx, section.command))
		includes = append(includes, section.describes)
	}

	if err := os.WriteFile(path, []byte(out.String()), 0o640); err != nil {
		return nil, &Error{
			Code:        "diagnostics.failed",
			Message:     "Homebase could not write the diagnostic file.",
			Detail:      err.Error(),
			Recoverable: true,
			Recovery:    "Check there is free space on the system disk.",
			Status:      500,
		}
	}
	if err := chownToService(path); err != nil {
		// Not fatal: the file exists and root can read it. But core downloads it
		// for the browser, so this failing means the download will not work, and
		// that is worth saying rather than discovering later.
		return nil, &Error{
			Code:        "diagnostics.unreadable",
			Message:     "The diagnostic file was written but Homebase cannot read it back.",
			Detail:      err.Error(),
			Recoverable: true,
			Recovery:    "Try again. If it keeps happening, the homebase account is missing.",
			Status:      500,
		}
	}

	info, err := os.Stat(path)
	size := int64(0)
	if err == nil {
		size = info.Size()
	}

	return Diagnostics{
		Path:      path,
		Bytes:     size,
		CreatedAt: now.Format(time.RFC3339),
		Includes:  includes,
		Excludes:  whatIsNeverCollected,
		Message: "The diagnostic file is ready. It is safe to send to somebody " +
			"helping you — read the top of it to see what it does and does not contain.",
	}, nil
}

// collect runs one fixed command and returns its output, or why it has none.
//
// A command that is missing or fails does not fail the bundle. Half a bundle
// from a badly broken machine is exactly what somebody needs; refusing to
// produce one because `systemd-analyze` is not installed would withhold it in
// precisely the situation it exists for.
func collect(ctx context.Context, command []string) string {
	binary, err := exec.LookPath(command[0])
	if err != nil {
		return "(" + command[0] + " is not on this machine)\n"
	}

	limited, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	cmd := exec.CommandContext(limited, binary, command[1:]...)
	cmd.Env = aptEnv()
	output, err := cmd.CombinedOutput()

	text := strings.TrimRight(string(output), "\n")
	if text == "" && err != nil {
		return "(" + command[0] + " reported: " + err.Error() + ")\n"
	}
	if text == "" {
		return "(nothing to report)\n"
	}
	return text + "\n"
}

func removeOldDiagnostics() {
	entries, err := os.ReadDir(diagnosticsDir)
	if err != nil {
		return
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "homebase-diagnostics-") {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)
	// Keep the two most recent: somebody comparing before and after a repair
	// needs the one from before it.
	for len(names) > 2 {
		os.Remove(filepath.Join(diagnosticsDir, names[0]))
		names = names[1:]
	}
}

// --- Repair -----------------------------------------------------------------------

// RepairResult is what repair looked at and what it changed.
type RepairResult struct {
	Steps []RepairStep `json:"steps"`

	// Changed is how many steps had something to do. Zero is the answer that
	// matters most: it means the thing that is wrong is not one of the things
	// this knows how to fix, and saying so is more use than a list of ticks.
	Changed int    `json:"changed"`
	Healthy bool   `json:"healthy"`
	Message string `json:"message"`
}

type RepairStep struct {
	// What is the check, in words somebody can read.
	What string `json:"what"`

	// Done is what was actually done, or empty if nothing needed doing.
	Done string `json:"done,omitempty"`

	// Problem is set when the step could not be completed.
	Problem string `json:"problem,omitempty"`
}

func (s *RecoveryServices) repair(ctx context.Context, _ struct{}) (any, error) {
	result := RepairResult{}

	add := func(step RepairStep) {
		if step.Done != "" {
			result.Changed++
		}
		result.Steps = append(result.Steps, step)
	}

	// First, because an interrupted update is the state the whole update
	// milestone is built around reporting, and `dpkg --configure -a` is the
	// command its error message names. Doing it here means the remedy is
	// something a person can press rather than something they have to type.
	add(s.finishInterruptedPackages(ctx))

	// Then the directories and their ownership. A package that unpacked but
	// never configured leaves these missing or root-owned, and core running as
	// `homebase` then cannot write its own database.
	for _, place := range []struct {
		path  string
		mode  os.FileMode
		owner string
	}{
		{"/etc/homebase", 0o750, "root"},
		{"/var/lib/homebase", 0o750, serviceAccount},
		{"/srv/homebase", 0o750, serviceAccount},
		{"/var/log/homebase", 0o750, serviceAccount},
	} {
		add(repairDirectory(place.path, place.mode, place.owner))
	}

	// And finally the services, in dependency order.
	for _, unit := range []string{"homebase-hostd.socket", "homebase-core.service"} {
		add(repairUnit(ctx, unit))
	}

	result.Healthy = true
	for _, step := range result.Steps {
		if step.Problem != "" {
			result.Healthy = false
		}
	}

	switch {
	case !result.Healthy:
		result.Message = "Homebase could not fix everything. The diagnostic file " +
			"will say more."
	case result.Changed == 0:
		result.Message = "Everything Homebase knows how to check was already correct. " +
			"Whatever is wrong is something else — make a diagnostic file."
	default:
		result.Message = fmt.Sprintf("Homebase fixed %d thing(s). "+
			"Try what was not working again.", result.Changed)
	}
	return result, nil
}

func (s *RecoveryServices) finishInterruptedPackages(ctx context.Context) RepairStep {
	step := RepairStep{What: "Whether a software update was left unfinished"}

	if !ReadUpdateStatus(ctx, s.update.aptSource, s.update.dpkgUpdates).Interrupted {
		return step
	}

	// A unit of its own, for the reason ADR-0018 gives: hostd's own unit sets
	// ProtectSystem=strict, and finishing a dpkg transaction writes across the
	// filesystem. The action is the unit — not an argument — so nothing a caller
	// sends becomes part of a command line.
	if out, err := runUpdateUnit(ctx, "homebase-repair.service"); err != nil {
		step.Problem = "the unfinished update could not be completed: " +
			strings.TrimSpace(firstLine(out)+" "+err.Error())
		return step
	}

	if ReadUpdateStatus(ctx, s.update.aptSource, s.update.dpkgUpdates).Interrupted {
		step.Problem = "the update is still unfinished"
		return step
	}
	step.Done = "finished an update that had been interrupted"
	return step
}

func firstLine(text string) string {
	if index := strings.IndexByte(text, '\n'); index >= 0 {
		return text[:index]
	}
	return text
}

func repairDirectory(path string, mode os.FileMode, owner string) RepairStep {
	step := RepairStep{What: "Whether " + path + " exists and belongs to the right account"}

	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		if err := os.MkdirAll(path, mode); err != nil {
			step.Problem = "could not create it: " + err.Error()
			return step
		}
		if owner != "root" {
			if err := chownToService(path); err != nil {
				step.Problem = "created it, but could not give it to " + owner
				return step
			}
		}
		step.Done = "created it"
		return step
	}
	if err != nil {
		step.Problem = err.Error()
		return step
	}
	if !info.IsDir() {
		// Deliberately not deleted. Something is there that Homebase did not put
		// there, and a repair tool that removes files it does not recognise is
		// worse than the problem.
		step.Problem = path + " is not a directory"
		return step
	}

	if owner == "root" {
		return step
	}
	wrong, err := notOwnedByService(path)
	if err != nil {
		step.Problem = err.Error()
		return step
	}
	if wrong {
		if err := chownToService(path); err != nil {
			step.Problem = "could not give it to " + owner + ": " + err.Error()
			return step
		}
		step.Done = "gave it back to the " + owner + " account"
	}
	return step
}

func repairUnit(ctx context.Context, unit string) RepairStep {
	step := RepairStep{What: "Whether " + unit + " is running"}

	if unitIsActive(ctx, unit) {
		return step
	}

	// enable as well as start: a unit that is running but not enabled comes back
	// stopped after a reboot, which is the same fault again next week.
	if err := runSystemctl(ctx, "enable", "--now", unit); err != nil {
		step.Problem = "could not start it: " + err.Error()
		return step
	}
	if !unitIsActive(ctx, unit) {
		step.Problem = "it was started and is not running"
		return step
	}
	step.Done = "started it"
	return step
}

// --- Factory reset -----------------------------------------------------------------

type factoryResetRequest struct {
	// Confirm must be this server's hostname. Not a word like "yes": this
	// removes every account on the machine.
	Confirm string `json:"confirm"`

	// KeepData leaves /srv/homebase alone. Default true, and that default is the
	// safety property — a caller that forgets the field keeps the photographs.
	KeepData *bool `json:"keep_data,omitempty"`
}

// FactoryResetResult is what was removed and what was left.
type FactoryResetResult struct {
	Removed []string `json:"removed"`
	Kept    []string `json:"kept"`
	Message string   `json:"message"`
}

func (s *RecoveryServices) factoryReset(ctx context.Context, req factoryResetRequest) (any, error) {
	hostname, _ := os.Hostname()

	// The machine's own name, typed. It is the only string that is specific to
	// this server, which is what stops a reset meant for one machine landing on
	// another.
	if strings.TrimSpace(req.Confirm) != hostname {
		return nil, &Error{
			Code:    "system.confirmation_required",
			Message: "Please confirm by typing this server's name.",
			Detail:  fmt.Sprintf("confirm must be %q", hostname),
			Recovery: "A factory reset removes every account and every setting on this " +
				"server. Type its name exactly to confirm.",
			Recoverable: true,
			Status:      428,
		}
	}

	keepData := true
	if req.KeepData != nil {
		keepData = *req.KeepData
	}

	result := FactoryResetResult{}

	// Stopped first. core holds the database open, and removing a SQLite file
	// underneath a running process leaves it writing to an unlinked inode —
	// which looks like it worked until the next restart.
	_ = runSystemctl(ctx, "stop", "homebase-core.service")

	for _, path := range []string{
		"/var/lib/homebase/homebase.db",
		"/var/lib/homebase/homebase.db-wal",
		"/var/lib/homebase/homebase.db-shm",
	} {
		if err := os.Remove(path); err == nil {
			result.Removed = append(result.Removed,
				"every account, and this server's record of what was set up")
			break
		}
	}

	// Configuration, but not the directories: the packages own those, and
	// removing them would leave a machine that cannot start rather than one that
	// asks to be set up.
	//
	// The certificate goes with them, and that is a decision rather than
	// tidiness. It is this server's identity, and the reason to reset a machine
	// is usually that it is being passed on — so the previous owner keeping a
	// key that still authenticates as it is exactly wrong. The cost is that
	// every browser has to be shown the new fingerprint once, which the reset
	// screen says.
	for _, path := range []string{
		"/etc/homebase/homebase.yaml",
		"/etc/homebase/backup-schedule.conf",
		"/var/lib/homebase/tls",
		diagnosticsDir,
	} {
		if err := os.RemoveAll(path); err == nil {
			result.Removed = append(result.Removed, path)
		}
	}

	// The update channel is left alone on purpose. A machine that forgets where
	// its security updates come from is a worse machine than one that remembers
	// it after a reset, and it is not somebody's personal data.
	result.Kept = append(result.Kept, "where this server gets its updates from")

	if keepData {
		result.Kept = append(result.Kept,
			"everything in /srv/homebase — your files and your applications' data")
	} else {
		if err := emptyDirectory("/srv/homebase"); err != nil {
			return nil, &Error{
				Code:        "system.reset_failed",
				Message:     "Homebase could not remove your files.",
				Detail:      err.Error(),
				Recoverable: false,
				Recovery: "Some of your data may have been deleted and some may not. " +
					"Restore from a backup.",
				Status: 500,
			}
		}
		result.Removed = append(result.Removed, "everything in /srv/homebase")
	}

	if err := runSystemctl(ctx, "start", "homebase-core.service"); err != nil {
		return nil, &Error{
			Code:        "system.reset_incomplete",
			Message:     "This server was reset, but did not start again.",
			Detail:      err.Error(),
			Recoverable: true,
			Recovery: "Restart the machine. It should come back asking to be set up " +
				"like a new server.",
			Status: 500,
		}
	}

	result.Message = "This server has been reset. Open it in a browser and it will " +
		"ask you to set it up, like a new one."
	if keepData {
		result.Message += " Your files are still there and will be waiting once you " +
			"have made an account."
	}
	return result, nil
}

// notOwnedByService reports whether a path belongs to somebody other than the
// service account.
//
// Resolved by name at call time, like chownToService, so a machine where the
// account does not exist fails with something legible rather than comparing
// against a uid that means nothing.
func notOwnedByService(path string) (bool, error) {
	account, err := user.Lookup(serviceAccount)
	if err != nil {
		return false, fmt.Errorf("looking up the %s account: %w", serviceAccount, err)
	}
	uid, err := strconv.Atoi(account.Uid)
	if err != nil {
		return false, err
	}

	info, err := os.Stat(path)
	if err != nil {
		return false, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return false, fmt.Errorf("cannot read the owner of %s", path)
	}
	return int(stat.Uid) != uid, nil
}

// emptyDirectory removes everything inside a directory, keeping the directory.
//
// The directory itself survives because its ownership and mode are what the
// package set up, and recreating it is one more thing to get subtly wrong on the
// machine least able to tolerate it.
func emptyDirectory(root string) error {
	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if err := os.RemoveAll(filepath.Join(root, entry.Name())); err != nil {
			return err
		}
	}
	return nil
}
