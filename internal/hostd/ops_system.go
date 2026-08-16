package hostd

import (
	"bufio"
	"context"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// System information is read from /proc and /sys directly rather than by
// shelling out to lsb_release, free, df and friends.
//
// Not only for speed. Parsing the output of a command means depending on its
// formatting, its locale and its presence — and it means a root process
// spawning shells, which is the habit ADR-0006 exists to prevent. The kernel's
// own interfaces are stable, documented and require no subprocess at all.

// RegisterSystemOperations adds the system domain to a registry.
func RegisterSystemOperations(r *Registry) {
	r.MustRegister(Operation{
		Name:        "system.get_info",
		Summary:     "Report hostname, operating system, kernel and CPU.",
		Risk:        RiskRead,
		Permissions: nil,
		Confirm:     ConfirmNone,
		Timeout:     5 * time.Second,
		Handler:     Typed(systemGetInfo),
	})

	r.MustRegister(Operation{
		Name:        "system.get_resources",
		Summary:     "Report current memory, load, uptime and power state.",
		Risk:        RiskRead,
		Permissions: nil,
		Confirm:     ConfirmNone,
		Timeout:     5 * time.Second,
		Handler:     Typed(systemGetResources),
	})

	r.MustRegister(Operation{
		Name:    "system.rename",
		Summary: "Change what this server calls itself.",
		// Low, because nothing is destroyed and it can be done again. The cost
		// of getting it wrong is a server somebody has to look up the address
		// of, not data they cannot get back — so it does not ask twice.
		Risk:        RiskLow,
		Permissions: []string{"system.manage"},
		Confirm:     ConfirmNone,
		Timeout:     10 * time.Second,
		Rollback:    "system.rename, with the previous name",
		Handler:     Typed(systemRename),
	})

	r.MustRegister(Operation{
		Name:    "system.reboot",
		Summary: "Restart the machine.",
		// High rather than medium: a reboot on a machine holding somebody's
		// only copy of their photographs interrupts whatever was writing them.
		Risk:        RiskHigh,
		Permissions: []string{"system.manage"},
		Confirm:     ConfirmExplicit,
		Timeout:     30 * time.Second,
		Rollback:    "", // Cannot be undone. Stated, not implied.
		Handler:     Typed(systemReboot),
	})
}

// --- system.get_info ---------------------------------------------------------

type NoParams struct{}

type SystemInfo struct {
	Hostname      string `json:"hostname"`
	OS            string `json:"os"`
	Kernel        string `json:"kernel"`
	Architecture  string `json:"architecture"`
	UptimeSeconds int64  `json:"uptime_seconds"`
	CPU           CPU    `json:"cpu"`
	Virtualised   bool   `json:"virtualised"`
}

type CPU struct {
	Model   string `json:"model"`
	Cores   int    `json:"cores"`
	Threads int    `json:"threads"`
}

func systemGetInfo(ctx context.Context, _ NoParams) (any, error) {
	hostname, err := os.Hostname()
	if err != nil {
		return nil, internalError("hostname: " + err.Error())
	}

	var uname syscall.Utsname
	if err := syscall.Uname(&uname); err != nil {
		return nil, internalError("uname: " + err.Error())
	}

	uptime, err := readUptime()
	if err != nil {
		return nil, internalError("uptime: " + err.Error())
	}

	cpu, virtualised := readCPU()

	return SystemInfo{
		Hostname:      hostname,
		OS:            readOSName(),
		Kernel:        charsToString(uname.Release),
		Architecture:  charsToString(uname.Machine),
		UptimeSeconds: uptime,
		CPU:           cpu,
		Virtualised:   virtualised,
	}, nil
}

// --- system.get_resources ----------------------------------------------------

type SystemResources struct {
	Memory        Memory     `json:"memory"`
	LoadAverage   [3]float64 `json:"load_average"`
	UptimeSeconds int64      `json:"uptime_seconds"`
	Power         Power      `json:"power"`

	// Temperature is how hot the machine is, or that it cannot tell. Homebase
	// runs on old laptops in cupboards, and a machine cooking itself looks from
	// the outside exactly like a machine that is broken.
	Temperature Temperature `json:"temperature"`

	// Fan is what the cooling is doing. Reported beside the temperature because
	// the two are only meaningful together: a quiet machine at 50 °C is fine, a
	// loud one at 50 °C has a fan problem, and a loud one at 90 °C has a dust
	// problem. The number alone cannot tell those apart.
	Fan Fan `json:"fan"`

	// Counters are running totals since boot — processor time and network
	// bytes. Totals rather than rates, because a rate is a difference between
	// two moments and only whoever keeps the record knows both. See counters.go.
	Counters Counters `json:"counters"`
}

type Memory struct {
	TotalBytes     uint64 `json:"total_bytes"`
	AvailableBytes uint64 `json:"available_bytes"`
}

type Power struct {
	// OnBattery and BatteryPercent are pointers because "unknown" and "false"
	// are different answers. A desktop has no battery; reporting 0 % would be a
	// lie, and the dashboard needs to show nothing rather than something wrong.
	OnBattery      *bool `json:"on_battery"`
	BatteryPercent *int  `json:"battery_percent"`
}

func systemGetResources(ctx context.Context, _ NoParams) (any, error) {
	mem, err := readMemory()
	if err != nil {
		return nil, internalError("meminfo: " + err.Error())
	}

	load, err := readLoadAverage()
	if err != nil {
		return nil, internalError("loadavg: " + err.Error())
	}

	uptime, err := readUptime()
	if err != nil {
		return nil, internalError("uptime: " + err.Error())
	}

	return SystemResources{
		Memory:        mem,
		LoadAverage:   load,
		UptimeSeconds: uptime,
		Power:         readPower(),
		Temperature:   readTemperature(sysThermal),
		Fan:           readFan(sysHwmon),
		Counters:      readCounters(procStat, sysClassNet),
	}, nil
}

// --- system.reboot -----------------------------------------------------------

type RebootParams struct {
	// Reason is recorded in the audit log. Not required, but a reboot with no
	// stated reason is one nobody can explain afterwards.
	Reason string `json:"reason,omitempty"`

	// Confirm must equal the machine's hostname.
	//
	// This is what ConfirmExplicit means: naming the target so a confirmation
	// cannot be replayed against a different machine. It matters more than it
	// looks — Stage 2 will have an operator proposing reboots, and a
	// confirmation that is just "yes" is one that can be obtained for one
	// machine and spent on another.
	Confirm string `json:"confirm"`
}

type RebootResult struct {
	Scheduled bool   `json:"scheduled"`
	Message   string `json:"message"`
}

func systemReboot(ctx context.Context, params RebootParams) (any, error) {
	hostname, err := os.Hostname()
	if err != nil {
		return nil, internalError("hostname: " + err.Error())
	}

	if params.Confirm != hostname {
		return nil, &Error{
			Code:        "system.confirmation_mismatch",
			Message:     "The confirmation did not match this server's name.",
			Detail:      "confirm must equal the hostname of the machine being restarted",
			Recoverable: true,
			Recovery:    "Confirm the restart again, naming this server.",
			Status:      428,
		}
	}

	// Checked after the confirmation, deliberately. The confirmation is the
	// security control, and running it first means it is exercised in
	// development too — which is where a bug in it would be found.
	//
	// This guard is a safety measure rather than a permissions check: on a
	// desktop with polkit, `systemctl reboot` from a logged-in session succeeds,
	// so without it a developer clicking "Restart this server" in a local
	// dashboard reboots the laptop they are working on, mid-edit, with no
	// warning.
	if os.Geteuid() != 0 {
		return nil, &Error{
			Code:    "system.not_privileged",
			Message: "This server cannot be restarted from here.",
			Detail: "hostd is not running as root, so this is a development " +
				"instance rather than a real server. Refusing, because the " +
				"machine it would restart is the one you are working on.",
			Recoverable: false,
			Status:      501,
		}
	}

	// systemctl rather than reboot(2): systemd stops units in order, flushes
	// filesystems and lets services shut down cleanly. Calling the syscall
	// directly is how a machine holding somebody's photographs loses a write
	// that was in flight.
	//
	// This is a fixed argument vector with no caller-supplied content, which is
	// what keeps it inside ADR-0006. Nothing from params reaches it.
	cmd := exec.CommandContext(ctx, "/usr/bin/systemctl", "reboot")

	// Strip the variables systemd used to talk to *us*. A child that inherits
	// NOTIFY_SOCKET can send readiness messages systemd attributes to this
	// service — it logs "reception only permitted for main PID" and the
	// intent is ambiguous. LISTEN_FDS is worse: a child that believed it had
	// been socket-activated would try to serve on our listener.
	cmd.Env = withoutSystemdVariables(os.Environ())

	if out, err := cmd.CombinedOutput(); err != nil {
		return nil, &Error{
			Code:        "system.reboot_failed",
			Message:     "The server could not be restarted.",
			Detail:      strings.TrimSpace(string(out)) + " (" + err.Error() + ")",
			Recoverable: true,
			Recovery:    "Try again. If it keeps failing, restart the machine by hand.",
			Status:      500,
		}
	}

	return RebootResult{
		Scheduled: true,
		Message:   "The server is restarting. It will be back in a minute or two.",
	}, nil
}

// withoutSystemdVariables removes the service-manager handshake variables from
// an environment, so they are not inherited by anything we execute.
func withoutSystemdVariables(env []string) []string {
	drop := map[string]bool{
		"NOTIFY_SOCKET":  true,
		"LISTEN_FDS":     true,
		"LISTEN_PID":     true,
		"LISTEN_FDNAMES": true,
	}
	out := make([]string, 0, len(env))
	for _, entry := range env {
		key, _, _ := strings.Cut(entry, "=")
		if !drop[key] {
			out = append(out, entry)
		}
	}
	return out
}

// --- /proc and /sys readers --------------------------------------------------

func readUptime() (int64, error) {
	data, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return 0, err
	}
	fields := strings.Fields(string(data))
	if len(fields) == 0 {
		return 0, os.ErrInvalid
	}
	seconds, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return 0, err
	}
	return int64(seconds), nil
}

func readOSName() string {
	f, err := os.Open("/etc/os-release")
	if err != nil {
		return "unknown"
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		if name, ok := strings.CutPrefix(line, "PRETTY_NAME="); ok {
			return strings.Trim(name, `"`)
		}
	}
	return "unknown"
}

// readCPU parses /proc/cpuinfo for the model, the physical core count and the
// logical thread count, and notices whether we are in a VM.
func readCPU() (CPU, bool) {
	cpu := CPU{Model: "unknown"}

	f, err := os.Open("/proc/cpuinfo")
	if err != nil {
		return cpu, false
	}
	defer f.Close()

	physical := map[string]struct{}{}
	var currentPhysicalID, currentCoreID string
	virtualised := false

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		key, value, found := strings.Cut(scanner.Text(), ":")
		if !found {
			// A blank line ends a processor block.
			if strings.TrimSpace(scanner.Text()) == "" && currentPhysicalID != "" {
				physical[currentPhysicalID+"/"+currentCoreID] = struct{}{}
				currentPhysicalID, currentCoreID = "", ""
			}
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)

		switch key {
		case "model name":
			if cpu.Model == "unknown" {
				cpu.Model = value
			}
		case "processor":
			cpu.Threads++
		case "physical id":
			currentPhysicalID = value
		case "core id":
			currentCoreID = value
		case "flags":
			if strings.Contains(value, "hypervisor") {
				virtualised = true
			}
		}
	}
	if currentPhysicalID != "" {
		physical[currentPhysicalID+"/"+currentCoreID] = struct{}{}
	}

	cpu.Cores = len(physical)
	if cpu.Cores == 0 {
		// Some virtual CPUs report no topology at all. Threads is still true.
		cpu.Cores = cpu.Threads
	}
	return cpu, virtualised
}

func readMemory() (Memory, error) {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return Memory{}, err
	}
	defer f.Close()

	var mem Memory
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		key, value, found := strings.Cut(scanner.Text(), ":")
		if !found {
			continue
		}
		fields := strings.Fields(value)
		if len(fields) == 0 {
			continue
		}
		kb, err := strconv.ParseUint(fields[0], 10, 64)
		if err != nil {
			continue
		}
		switch key {
		case "MemTotal":
			mem.TotalBytes = kb * 1024
		case "MemAvailable":
			// MemAvailable, not MemFree: free memory excludes reclaimable cache
			// and so understates what is actually usable, sometimes by an order
			// of magnitude. Showing that to a user would look like a machine in
			// trouble when it is fine.
			mem.AvailableBytes = kb * 1024
		}
	}
	return mem, scanner.Err()
}

func readLoadAverage() ([3]float64, error) {
	var load [3]float64
	data, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return load, err
	}
	fields := strings.Fields(string(data))
	if len(fields) < 3 {
		return load, os.ErrInvalid
	}
	for i := 0; i < 3; i++ {
		v, err := strconv.ParseFloat(fields[i], 64)
		if err != nil {
			return load, err
		}
		load[i] = v
	}
	return load, nil
}

// readPower reports battery state, leaving both fields nil on a machine with no
// battery rather than inventing zeroes.
func readPower() Power {
	entries, err := os.ReadDir("/sys/class/power_supply")
	if err != nil {
		return Power{}
	}

	for _, entry := range entries {
		base := "/sys/class/power_supply/" + entry.Name()

		kind, err := os.ReadFile(base + "/type")
		if err != nil || strings.TrimSpace(string(kind)) != "Battery" {
			continue
		}

		var power Power

		if raw, err := os.ReadFile(base + "/capacity"); err == nil {
			if pct, err := strconv.Atoi(strings.TrimSpace(string(raw))); err == nil {
				power.BatteryPercent = &pct
			}
		}

		if raw, err := os.ReadFile(base + "/status"); err == nil {
			discharging := strings.TrimSpace(string(raw)) == "Discharging"
			power.OnBattery = &discharging
		}

		return power
	}

	return Power{}
}

// charsToString converts the int8/uint8 arrays syscall.Utsname uses.
func charsToString[T int8 | uint8](chars [65]T) string {
	b := make([]byte, 0, len(chars))
	for _, c := range chars {
		if c == 0 {
			break
		}
		b = append(b, byte(c))
	}
	return string(b)
}

// --- system.rename -----------------------------------------------------------

// RenameParams carries the new name for the machine.
type RenameParams struct {
	Name string `json:"name"`
}

type RenameResult struct {
	Previous string `json:"previous"`
	Name     string `json:"name"`
	Message  string `json:"message"`
}

// systemRename changes what the machine calls itself.
//
// Nothing is destroyed, which is why this is a low-risk operation: the worst
// outcome is a server somebody has to look up the address of again. It is not
// nothing, though — the name is how the machine is found once mDNS lands, and
// it is what `system.reboot` demands as its confirmation, so a rename changes
// the answer to a question the user will be asked later.
//
// The name itself is set by asking systemd rather than by writing
// /etc/hostname. hostd runs under ProtectSystem=strict, so /etc is read-only to
// it, and replacing a file there atomically needs the *directory* writable —
// which would mean handing this service write access to all of /etc to change
// one line. systemd-hostnamed already does this job, is already privileged, and
// updates the running kernel name and the file together.
//
// Found by the browser journey, which renamed a real machine and got
// "read-only file system" from a code path every unit test was happy with.
func systemRename(ctx context.Context, params RenameParams) (any, error) {
	name := strings.TrimSpace(params.Name)

	if err := checkHostname(name); err != nil {
		return nil, err
	}

	previous, err := os.Hostname()
	if err != nil {
		return nil, internalError("hostname: " + err.Error())
	}

	if name == previous {
		return RenameResult{
			Previous: previous,
			Name:     name,
			Message:  "This server is already called " + name + ".",
		}, nil
	}

	if err := setHostname(ctx, name); err != nil {
		return nil, err
	}

	if err := updateHostsFile(previous, name); err != nil {
		// Not fatal. The machine is renamed and working; what is lost is its
		// ability to resolve its own name, which shows up as a slow `sudo`
		// rather than as anything anybody would connect to a rename.
		return RenameResult{
			Previous: previous,
			Name:     name,
			Message: "This server is now called " + name +
				", but its own address book could not be updated.",
		}, nil
	}

	return RenameResult{
		Previous: previous,
		Name:     name,
		Message:  "This server is now called " + name + ".",
	}, nil
}

// setHostname asks systemd to rename the machine.
func setHostname(ctx context.Context, name string) error {
	binary, err := exec.LookPath("hostnamectl")
	if err != nil {
		return &Error{
			Code:        "system.rename_unavailable",
			Message:     "This server cannot be renamed.",
			Detail:      "hostnamectl is not installed",
			Recoverable: false,
			Status:      503,
		}
	}

	// A fixed argument vector, and `--` so a name beginning with a hyphen is
	// never read as an option. checkHostname has already refused those, and
	// this costs nothing.
	out, err := exec.CommandContext(ctx, binary, "set-hostname", "--", name).CombinedOutput()
	if err != nil {
		return internalError("setting the hostname: " + err.Error() + ": " +
			strings.TrimSpace(string(out)))
	}
	return nil
}

// checkHostname refuses names a machine cannot have.
//
// The rules are RFC 1123's, and they are enforced here rather than only in the
// dashboard because hostd re-checks everything core sends it — a name with a
// space in it reaches /etc/hostname and stops the machine resolving itself.
func checkHostname(name string) error {
	refuse := func(detail, recovery string) error {
		return &Error{
			Code:        "system.invalid_name",
			Message:     "That cannot be a server's name.",
			Detail:      detail,
			Recoverable: true,
			Recovery:    recovery,
			Status:      422,
		}
	}

	switch {
	case name == "":
		return refuse("the name is empty", "Give the server a name.")
	case len(name) > 63:
		return refuse("names may be at most 63 characters",
			"Choose something shorter.")
	case strings.HasPrefix(name, "-") || strings.HasSuffix(name, "-"):
		return refuse("names may not start or end with a hyphen",
			"Remove the hyphen from the start or end.")
	}

	for _, r := range name {
		isLetter := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')
		isDigit := r >= '0' && r <= '9'
		if !isLetter && !isDigit && r != '-' {
			return refuse(
				"names may contain only letters, digits and hyphens",
				"Use letters, digits and hyphens — no spaces, dots or accents.")
		}
	}

	return nil
}

// updateHostsFile keeps the machine able to resolve its own name.
//
// Written in place rather than replaced atomically, because replacing a file in
// /etc means creating a sibling and renaming over it, and that needs /etc
// writable — a much wider grant than this one line is worth. The unit allows
// exactly this file. The window where a reader could see a partial hosts file
// is a single write of a few hundred bytes, and the caller already treats a
// failure here as survivable.
func updateHostsFile(previous, name string) error {
	raw, err := os.ReadFile("/etc/hosts")
	if err != nil {
		return err
	}

	rewritten, err := rewriteHosts(string(raw), previous, name)
	if err != nil {
		return err
	}

	return os.WriteFile("/etc/hosts", []byte(rewritten), 0o644)
}

// rewriteHosts replaces the machine's own name in a hosts file.
//
// Kept separate from reading and writing so it can be tested without a machine
// to rename. Only the loopback line naming this host is touched: everything
// else in that file was put there by somebody for a reason, and a rename that
// removed the printer's address would be a mystery to whoever noticed.
func rewriteHosts(contents, previous, name string) (string, error) {
	lines := strings.Split(contents, "\n")
	replaced := false

	for i, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 || fields[0] != "127.0.1.1" {
			continue
		}
		for j := 1; j < len(fields); j++ {
			if fields[j] == previous {
				fields[j] = name
				replaced = true
			}
		}
		lines[i] = strings.Join(fields, "\t")
	}

	if !replaced {
		// Appended rather than reported as an error: a machine with no entry
		// for itself is unusual but not broken, and renaming it is not the
		// moment to refuse.
		trailing := len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == ""
		entry := "127.0.1.1\t" + name
		if trailing {
			lines[len(lines)-1] = entry
			lines = append(lines, "")
		} else {
			lines = append(lines, entry, "")
		}
	}

	return strings.Join(lines, "\n"), nil
}
