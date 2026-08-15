package hostd

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// zones builds a fake /sys/class/thermal, so the test does not depend on how
// warm the machine running it happens to be.
func zones(t *testing.T, readings map[string]int) string {
	t.Helper()
	root := t.TempDir()
	i := 0
	for kind, milli := range readings {
		zone := filepath.Join(root, "thermal_zone"+strconv.Itoa(i))
		if err := os.MkdirAll(zone, 0o755); err != nil {
			t.Fatal(err)
		}
		write := func(name, value string) {
			if err := os.WriteFile(filepath.Join(zone, name), []byte(value), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		write("type", kind)
		write("temp", strconv.Itoa(milli)+"\n")
		i++
	}
	return root
}

// A machine with no sensors reports none. It must never report zero.
//
// Every VM is in this state, and so is some real hardware. A machine claiming
// 0 °C would look wonderfully cool, which is the same class of lie as a desktop
// reporting 0 % battery.
func TestAMachineWithNoSensorsSaysSoRatherThanReportingZero(t *testing.T) {
	temperature := readTemperature(t.TempDir())

	if temperature.Celsius != nil {
		t.Errorf("a machine with no thermal zones reported %d °C", *temperature.Celsius)
	}
	if temperature.State != "" {
		t.Errorf("state = %q, want empty — there is nothing to judge", temperature.State)
	}
	if temperature.Message != "" {
		t.Errorf("a machine with no sensors was given advice: %q", temperature.Message)
	}
}

// The hottest zone, not an average. A machine with one component at 95 °C and
// three at 40 °C has a problem, and the average would hide it.
func TestTheHottestZoneIsTheOneReported(t *testing.T) {
	root := zones(t, map[string]int{
		"acpitz":       40_000,
		"x86_pkg_temp": 95_000,
		"iwlwifi_1":    38_000,
	})

	temperature := readTemperature(root)
	if temperature.Celsius == nil {
		t.Fatal("no reading from a machine with three sensors")
	}
	if *temperature.Celsius != 95 {
		t.Errorf("reported %d °C, want 95 — the average would hide the hot one",
			*temperature.Celsius)
	}
	if temperature.Sensor != "x86_pkg_temp" {
		t.Errorf("sensor = %q; the name is what says whether it is the processor "+
			"or the case", temperature.Sensor)
	}
}

// Millidegrees are what the kernel reports, and an unconverted reading looks
// like a house fire.
func TestReadingsAreConvertedFromMillidegrees(t *testing.T) {
	root := zones(t, map[string]int{"acpitz": 52_000})

	temperature := readTemperature(root)
	if temperature.Celsius == nil || *temperature.Celsius != 52 {
		t.Fatalf("52000 millidegrees became %v", temperature.Celsius)
	}
	if temperature.State != "ok" {
		t.Errorf("52 °C reported as %q; that is an ordinary temperature", temperature.State)
	}
	if temperature.Message != "" {
		t.Errorf("an ordinary temperature was given advice: %q", temperature.Message)
	}
}

// A disconnected sensor reports something implausible. Reporting that as the
// machine's temperature would be worse than reporting nothing.
func TestImplausibleReadingsAreIgnored(t *testing.T) {
	root := zones(t, map[string]int{
		"broken":  0,
		"missing": -274_000,
		"silly":   3_000_000,
		"acpitz":  47_000,
	})

	temperature := readTemperature(root)
	if temperature.Celsius == nil {
		t.Fatal("every zone was discarded, including the working one")
	}
	if *temperature.Celsius != 47 {
		t.Errorf("reported %d °C; the working sensor said 47", *temperature.Celsius)
	}
}

func TestOnlyAMachineInTroubleIsToldSo(t *testing.T) {
	cases := []struct {
		celsius int
		state   string
		advises bool
	}{
		{35, "ok", false},
		{72, "ok", false},
		// The thresholds are deliberately high. Processors are designed to run
		// at 80 °C, so warning at 60 would teach people to ignore the warning —
		// which is the failure mode of every temperature indicator ever shipped.
		{79, "ok", false},
		{80, "warm", true},
		{89, "warm", true},
		{90, "hot", true},
		{101, "hot", true},
	}

	for _, c := range cases {
		temperature := readTemperature(zones(t, map[string]int{"acpitz": c.celsius * 1000}))
		if temperature.State != c.state {
			t.Errorf("%d °C reported as %q, want %q", c.celsius, temperature.State, c.state)
		}
		if (temperature.Message != "") != c.advises {
			t.Errorf("%d °C: advice given = %v, want %v (%q)",
				c.celsius, temperature.Message != "", c.advises, temperature.Message)
		}
	}
}

// The advice has to be something somebody can do in their house, not a
// temperature to contemplate.
func TestTheAdviceIsSomethingAPersonCanActOn(t *testing.T) {
	temperature := readTemperature(zones(t, map[string]int{"acpitz": 95_000}))
	for _, expected := range []string{"cupboard", "dust", "air"} {
		if !strings.Contains(temperature.Message, expected) {
			t.Errorf("the advice does not mention %q: %q", expected, temperature.Message)
		}
	}
}

// --- The fan -------------------------------------------------------------------

func writeHwmon(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for name, content := range files {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestTheFanIsReportedWithWhoIsDrivingIt(t *testing.T) {
	root := writeHwmon(t, map[string]string{
		"hwmon0/name":        "acpitz\n",
		"hwmon1/name":        "asus\n",
		"hwmon1/fan1_input":  "3300\n",
		"hwmon1/fan1_label":  "cpu_fan\n",
		"hwmon1/pwm1":        "170\n",
		"hwmon1/pwm1_enable": "2\n",
	})

	fan := readFan(root)
	if fan.RPM == nil || *fan.RPM != 3300 {
		t.Errorf("rpm = %v, want 3300", fan.RPM)
	}
	// 170 of 255 is two thirds, and the point of reporting it is that it says
	// how much cooling is left — which the rpm alone does not.
	if fan.Percent == nil || *fan.Percent != 66 {
		t.Errorf("percent = %v, want 66", fan.Percent)
	}
	if fan.Controlled != "firmware" {
		t.Errorf("controlled = %q, want firmware", fan.Controlled)
	}
	if fan.Message != "" {
		t.Errorf("a normally behaving fan said something: %q", fan.Message)
	}
}

// A fan somebody pinned and a fan working hard on a hot machine sound exactly
// alike from across a room, and the fixes are completely different — one is a
// setting, the other is a heatsink full of dust. So who is driving it is
// reported, and an override is called out.
func TestAnOverriddenFanSaysSo(t *testing.T) {
	root := writeHwmon(t, map[string]string{
		"hwmon0/name":        "asus\n",
		"hwmon0/fan1_input":  "4200\n",
		"hwmon0/pwm1":        "255\n",
		"hwmon0/pwm1_enable": "1\n",
	})

	fan := readFan(root)
	if fan.Controlled != "manual" {
		t.Errorf("controlled = %q, want manual", fan.Controlled)
	}
	if fan.Message == "" {
		t.Error("a fan under manual control said nothing about it")
	}

	// pwm1_enable of 0 is hwmon's "no control at all", which on most hardware
	// means full speed — a different thing from somebody having chosen a value.
	root = writeHwmon(t, map[string]string{
		"hwmon0/name": "asus\n", "hwmon0/fan1_input": "5000\n",
		"hwmon0/pwm1_enable": "0\n",
	})
	if got := readFan(root).Controlled; got != "full" {
		t.Errorf("controlled = %q, want full", got)
	}
}

// A machine with no fan sensor reports nothing rather than zero. Every VM is
// this case, and 0 rpm reads as a seized fan rather than an absent sensor — the
// same rule the temperature follows.
func TestAMachineWithNoFanReportsNothing(t *testing.T) {
	root := writeHwmon(t, map[string]string{
		"hwmon0/name":        "acpitz\n",
		"hwmon0/temp1_input": "45000\n",
	})
	fan := readFan(root)
	if fan.RPM != nil || fan.Percent != nil {
		t.Errorf("a machine with no fan reported rpm=%v percent=%v", fan.RPM, fan.Percent)
	}
	if readFan(filepath.Join(root, "nothing-here")).RPM != nil {
		t.Error("a missing hwmon tree reported a fan")
	}
}

// asus_wmi returns ENXIO for fan1_input while the fan is under manual control,
// so the speed genuinely cannot be read. Reported as unknown rather than as
// zero, and the rest of the answer still comes back.
func TestAFanWhoseSpeedCannotBeReadStillReportsTheRest(t *testing.T) {
	root := writeHwmon(t, map[string]string{
		"hwmon0/name": "asus\n", "hwmon0/fan1_input": "",
		"hwmon0/pwm1": "100\n", "hwmon0/pwm1_enable": "1\n",
	})
	fan := readFan(root)
	if fan.RPM != nil {
		t.Errorf("an unreadable speed was reported as %v", *fan.RPM)
	}
	if fan.Percent == nil || *fan.Percent != 39 {
		t.Errorf("percent = %v, want 39 — the rest of the answer is still known", fan.Percent)
	}
	if fan.Controlled != "manual" {
		t.Errorf("controlled = %q, want manual", fan.Controlled)
	}
}
