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
