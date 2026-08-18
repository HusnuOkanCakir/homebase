package hostd

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
)

// These run against the real /proc and /sys of whatever machine is running the
// tests, which is the point: a parser that only works against a fixture is a
// parser that has not met a real kernel.

func TestSystemGetInfoReadsTheRealMachine(t *testing.T) {
	result, err := systemGetInfo(context.Background(), NoParams{})
	if err != nil {
		t.Fatalf("system.get_info failed: %v", err)
	}

	info, ok := result.(SystemInfo)
	if !ok {
		t.Fatalf("unexpected result type %T", result)
	}

	hostname, _ := os.Hostname()
	if info.Hostname != hostname {
		t.Errorf("hostname = %q, want %q", info.Hostname, hostname)
	}
	if info.Kernel == "" {
		t.Error("no kernel version")
	}
	if info.Architecture == "" {
		t.Error("no architecture")
	}
	if info.OS == "unknown" {
		t.Error("could not read the OS name from /etc/os-release")
	}
	if info.UptimeSeconds <= 0 {
		t.Errorf("uptime = %d, which cannot be right", info.UptimeSeconds)
	}
	if info.CPU.Threads < 1 {
		t.Errorf("threads = %d", info.CPU.Threads)
	}
	if info.CPU.Cores < 1 {
		t.Errorf("cores = %d", info.CPU.Cores)
	}
	if info.CPU.Cores > info.CPU.Threads {
		t.Errorf("cores (%d) exceeds threads (%d), which is impossible", info.CPU.Cores, info.CPU.Threads)
	}
}

func TestSystemGetResourcesReadsTheRealMachine(t *testing.T) {
	result, err := systemGetResources(context.Background(), NoParams{})
	if err != nil {
		t.Fatalf("system.get_resources failed: %v", err)
	}

	res, ok := result.(SystemResources)
	if !ok {
		t.Fatalf("unexpected result type %T", result)
	}

	if res.Memory.TotalBytes == 0 {
		t.Error("total memory is zero")
	}
	if res.Memory.AvailableBytes == 0 {
		t.Error("available memory is zero")
	}
	if res.Memory.AvailableBytes > res.Memory.TotalBytes {
		t.Errorf("available (%d) exceeds total (%d)", res.Memory.AvailableBytes, res.Memory.TotalBytes)
	}
	if res.LoadAverage[0] < 0 {
		t.Errorf("negative load average: %v", res.LoadAverage)
	}
}

// A machine with no battery must report nothing rather than zero. "No battery"
// and "battery at 0 %" are different facts, and showing the second when the
// first is true would be alarming and wrong.
func TestPowerReportsUnknownRatherThanZero(t *testing.T) {
	power := readPower()

	if power.BatteryPercent == nil {
		if power.OnBattery != nil {
			t.Error("battery percentage is unknown but on_battery is set; report both or neither")
		}
		return
	}

	if *power.BatteryPercent < 0 || *power.BatteryPercent > 100 {
		t.Errorf("battery percentage = %d, outside 0-100", *power.BatteryPercent)
	}
}

// hostd refuses to reboot when it is not root.
//
// This guard exists because of a specific hazard, not for tidiness: a developer
// running the stack locally has hostd as their own user, and on a desktop with
// polkit `systemctl reboot` from a logged-in session succeeds. Without this,
// clicking "Restart this server" in a development dashboard reboots the laptop
// you are working on, mid-edit, with no warning.
func TestRebootRefusesWhenNotRoot(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root, where rebooting is the intended behaviour")
	}

	hostname, err := os.Hostname()
	if err != nil {
		t.Fatal(err)
	}

	// A *correct* confirmation, so the only thing standing between this test and
	// a reboot of the machine running it is the guard.
	_, err = systemReboot(context.Background(), RebootParams{Confirm: hostname})
	if err == nil {
		t.Fatal("reboot was accepted while unprivileged; THIS MACHINE WOULD HAVE REBOOTED")
	}

	var e *Error
	if !asError(err, &e) {
		t.Fatalf("unexpected error type: %T", err)
	}
	if e.Code != "system.not_privileged" {
		t.Errorf("code = %q, want system.not_privileged", e.Code)
	}
	if e.Recoverable {
		t.Error("this is not recoverable by retrying — hostd will still not be root")
	}
}

// The confirmation must name the machine. A confirmation that is just "yes" can
// be obtained for one server and spent on another — which matters once a Stage 2
// operator is the thing proposing reboots.
//
// This never reaches systemctl: the mismatch is rejected first.
func TestRebootRequiresTheHostnameAsConfirmation(t *testing.T) {
	hostname, err := os.Hostname()
	if err != nil {
		t.Fatal(err)
	}

	for _, confirm := range []string{"", "yes", "true", "confirm", strings.ToUpper(hostname), hostname + " "} {
		_, err := systemReboot(context.Background(), RebootParams{Confirm: confirm})
		if err == nil {
			t.Fatalf("confirmation %q was accepted; THIS MACHINE WOULD HAVE REBOOTED", confirm)
		}

		var e *Error
		if !asError(err, &e) {
			t.Fatalf("unexpected error type for %q: %T", confirm, err)
		}
		if e.Code != "system.confirmation_mismatch" {
			t.Errorf("confirmation %q rejected with %q, want system.confirmation_mismatch", confirm, e.Code)
		}
		if e.Recoverable && e.Recovery == "" {
			t.Error("the error is recoverable but says nothing about how")
		}
	}
}

// The reboot handler must reject a mistyped parameter rather than treat it as
// absent — silently rebooting because "confrim" was ignored would be the worst
// possible reading of an ambiguous request.
func TestRebootRejectsMisspelledParameters(t *testing.T) {
	handler := Typed(systemReboot)

	hostname, _ := os.Hostname()
	body, _ := json.Marshal(map[string]string{"confrim": hostname})

	_, err := handler(context.Background(), body)
	if err == nil {
		t.Fatal("a misspelled parameter was accepted")
	}

	var e *Error
	if !asError(err, &e) || e.Code != "request.invalid_parameters" {
		t.Fatalf("wrong error: %v", err)
	}
}

// Restarting and switching off are the only operations here that change
// anything, so their declared metadata is worth asserting directly. Both, and
// identically: they share an implementation, and a registration that graded one
// of them lower would be a way past every guard in it.
func TestPowerOperationsAreDeclaredDangerous(t *testing.T) {
	r := NewRegistry()
	RegisterSystemOperations(r)

	for _, name := range []string{"system.reboot", "system.shutdown"} {
		op, found := r.Lookup(name)
		if !found {
			t.Fatalf("%s is not registered", name)
		}

		if op.Risk != RiskHigh && op.Risk != RiskCritical {
			t.Errorf("%s: risk = %q; taking away a machine holding somebody's "+
				"data is not low risk", name, op.Risk)
		}
		if op.Confirm != ConfirmExplicit {
			t.Errorf("%s: confirmation = %q, want explicit", name, op.Confirm)
		}
		if len(op.Permissions) == 0 {
			t.Errorf("%s: no permission required", name)
		}
		if op.Rollback != "" {
			t.Errorf("%s: rollback = %q; neither can be undone", name, op.Rollback)
		}
	}
}

// Read-only operations must stay read-only. If one ever acquires a permission
// requirement or a confirmation, that is a sign it started changing something.
func TestReadOperationsAreDeclaredHarmless(t *testing.T) {
	r := NewRegistry()
	RegisterSystemOperations(r)

	for _, name := range []string{"system.get_info", "system.get_resources"} {
		op, found := r.Lookup(name)
		if !found {
			t.Fatalf("%s is not registered", name)
		}
		if op.Risk != RiskRead {
			t.Errorf("%s: risk = %q, want read", name, op.Risk)
		}
		if op.Confirm != ConfirmNone {
			t.Errorf("%s: confirmation = %q; a read should not need confirming", name, op.Confirm)
		}
	}
}

// --- system.rename -----------------------------------------------------------

// The name reaches /etc/hostname, which is read at boot and by anything
// resolving this machine's name. hostd re-checks what core sends rather than
// trusting it, so these are the rules that actually hold.
func TestRenameRefusesNamesAMachineCannotHave(t *testing.T) {
	cases := []struct {
		name string
		want string
	}{
		{"", "empty"},
		{"my server", "letters, digits and hyphens"},
		{"kitchen.local", "letters, digits and hyphens"},
		{"café", "letters, digits and hyphens"},
		{"-leading", "start or end with a hyphen"},
		{"trailing-", "start or end with a hyphen"},
		{strings.Repeat("a", 64), "at most 63"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := checkHostname(tc.name)
			if err == nil {
				t.Fatalf("accepted %q", tc.name)
			}
			var e *Error
			if !errors.As(err, &e) {
				t.Fatalf("got %T, want *Error", err)
			}
			if e.Code != "system.invalid_name" {
				t.Errorf("code = %q", e.Code)
			}
			if !strings.Contains(e.Detail, tc.want) {
				t.Errorf("detail = %q, wanted something about %q", e.Detail, tc.want)
			}
			if e.Recovery == "" {
				t.Error("a refusal with no way out is a dead end")
			}
		})
	}
}

func TestRenameAcceptsOrdinaryNames(t *testing.T) {
	for _, name := range []string{"homebase", "living-room", "Server2", "a", strings.Repeat("a", 63)} {
		if err := checkHostname(name); err != nil {
			t.Errorf("refused %q: %v", name, err)
		}
	}
}

// The hosts file keeps the machine able to resolve its own name. Getting this
// wrong shows up as sudo taking ten seconds, which nobody connects to a rename.
func TestRenameUpdatesTheHostsFileWithoutDisturbingIt(t *testing.T) {
	original := strings.Join([]string{
		"127.0.0.1\tlocalhost",
		"127.0.1.1\told-name",
		"",
		"# The following lines are desirable for IPv6 capable hosts",
		"::1     ip6-localhost ip6-loopback",
		"192.168.1.50\tthe-printer",
		"",
	}, "\n")

	rewritten, err := rewriteHosts(original, "old-name", "new-name")
	if err != nil {
		t.Fatal(err)
	}

	if strings.Contains(rewritten, "old-name") {
		t.Error("the old name is still in the hosts file")
	}
	if !strings.Contains(rewritten, "127.0.1.1\tnew-name") {
		t.Errorf("the new name is not mapped to 127.0.1.1:\n%s", rewritten)
	}

	// Everything somebody else put there stays.
	for _, keep := range []string{"127.0.0.1", "localhost", "ip6-localhost",
		"192.168.1.50", "the-printer", "# The following lines"} {
		if !strings.Contains(rewritten, keep) {
			t.Errorf("rewriting the hosts file lost %q:\n%s", keep, rewritten)
		}
	}
}

// A machine whose hosts file has no entry for itself must gain one, rather than
// being renamed into a state where it cannot resolve its own name at all.
func TestRenameAddsAHostsEntryWhenThereIsNone(t *testing.T) {
	rewritten, err := rewriteHosts("127.0.0.1\tlocalhost\n", "old-name", "new-name")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(rewritten, "127.0.1.1\tnew-name") {
		t.Errorf("no entry was added:\n%s", rewritten)
	}
	if !strings.Contains(rewritten, "localhost") {
		t.Error("the existing entry was lost")
	}
}

// Everything asserted about rebooting must hold for switching off.
//
// The two are one function with one word changed, which is exactly the shape
// that acquires a difference nobody meant. So the guards are checked through
// both entry points rather than through the shared one: a handler wired to the
// wrong powerAction, or a shutdown that skipped the root check, is a bug this
// catches and a test of power() alone would not.
func TestShutdownIsGuardedLikeReboot(t *testing.T) {
	hostname, err := os.Hostname()
	if err != nil {
		t.Fatal(err)
	}

	t.Run("refuses when not root", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("running as root, where switching off is the intended behaviour")
		}

		// A *correct* confirmation, so the only thing between this test and the
		// machine running it going dark is the guard.
		_, err := systemShutdown(context.Background(), ShutdownParams{Confirm: hostname})
		if err == nil {
			t.Fatal("shutdown was accepted while unprivileged; THIS MACHINE WOULD HAVE SWITCHED OFF")
		}
		var e *Error
		if !asError(err, &e) {
			t.Fatalf("unexpected error type: %T", err)
		}
		if e.Code != "system.not_privileged" {
			t.Errorf("code = %q, want system.not_privileged", e.Code)
		}
		if e.Recoverable {
			t.Error("this is not recoverable by retrying — hostd will still not be root")
		}
	})

	t.Run("requires the hostname", func(t *testing.T) {
		for _, confirm := range []string{"", "yes", "true", "confirm",
			strings.ToUpper(hostname), hostname + " "} {
			_, err := systemShutdown(context.Background(), ShutdownParams{Confirm: confirm})
			if err == nil {
				t.Fatalf("confirmation %q was accepted; THIS MACHINE WOULD HAVE SWITCHED OFF", confirm)
			}
			var e *Error
			if !asError(err, &e) {
				t.Fatalf("unexpected error type for %q: %T", confirm, err)
			}
			if e.Code != "system.confirmation_mismatch" {
				t.Errorf("confirmation %q rejected with %q, want system.confirmation_mismatch",
					confirm, e.Code)
			}
		}
	})

	t.Run("rejects a misspelled parameter", func(t *testing.T) {
		body, _ := json.Marshal(map[string]string{"confrim": hostname})
		if _, err := Typed(systemShutdown)(context.Background(), body); err == nil {
			t.Fatal("a misspelled confirmation was ignored rather than rejected")
		}
	})
}

// The two must not be interchangeable by accident.
//
// They share PowerParams, so one confirmation is valid for either — deliberate
// and safe, because it names the *machine* and both act on the same one. What
// must never be shared is the verb. If the shutdown path ever ends up asking
// systemd to reboot, somebody who chose "switch off" gets a machine that comes
// back, and nothing in the confirmation would have hinted at it.
//
// Asserted against the literal strings rather than only against each other: two
// identical wrong verbs and two different wrong verbs are both possible, and
// only one of those a difference test would catch.
func TestTheTwoPowerActionsDoNotShareAVerb(t *testing.T) {
	if rebootAction.verb == shutdownAction.verb {
		t.Fatalf("both power actions run `systemctl %s`; one of them is doing "+
			"the wrong thing to the machine", rebootAction.verb)
	}
	if rebootAction.verb != "reboot" {
		t.Errorf("reboot runs `systemctl %s`, want reboot", rebootAction.verb)
	}
	if shutdownAction.verb != "poweroff" {
		t.Errorf("shutdown runs `systemctl %s`, want poweroff", shutdownAction.verb)
	}

	// Said to a person and therefore worth being different too — a shutdown
	// that reports "it will be back in a minute or two" is a lie somebody would
	// wait out.
	if rebootAction.succeeded == shutdownAction.succeeded {
		t.Error("both say the same thing afterwards, so neither says which happened")
	}
}
