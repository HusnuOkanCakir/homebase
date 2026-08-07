package hostd

import (
	"context"
	"encoding/json"
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

// system.reboot is the only operation here that changes anything, so its
// declared metadata is worth asserting directly.
func TestRebootIsDeclaredDangerous(t *testing.T) {
	r := NewRegistry()
	RegisterSystemOperations(r)

	op, found := r.Lookup("system.reboot")
	if !found {
		t.Fatal("system.reboot is not registered")
	}

	if op.Risk != RiskHigh && op.Risk != RiskCritical {
		t.Errorf("risk = %q; restarting a machine holding somebody's data is not low risk", op.Risk)
	}
	if op.Confirm != ConfirmExplicit {
		t.Errorf("confirmation = %q, want explicit", op.Confirm)
	}
	if len(op.Permissions) == 0 {
		t.Error("no permission required to reboot the machine")
	}
	if op.Rollback != "" {
		t.Errorf("rollback = %q; a reboot cannot be undone", op.Rollback)
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
