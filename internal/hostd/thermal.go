package hostd

import (
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// How hot the machine is.
//
// The failure this exists for is specific and common: an old laptop, lid shut,
// in a cupboard, with eight years of dust in its fan. It throttles, gets slower,
// and eventually shuts itself off — and from the outside that looks like
// "Homebase is broken" rather than "this machine is cooking".
//
// Nothing here acts on the temperature, and the fan is reported rather than
// driven. That is a decision rather than an omission, and the first real laptop
// is the argument for it: at full load it reached 89 °C with its fan already
// climbing, having passed the 84 °C the processor throttles at. On a machine
// like that, a manual fan setting is not a comfort feature — it is a way to
// cook a computer that is already struggling, and the driver will not even
// report the resulting speed to say what was done.
//
// What a report does instead is answer the question somebody actually has, which
// is whether the noise is the fan being stuck or the machine being hot. Those
// have completely different fixes and look identical from across a room.

const sysHwmon = "/sys/class/hwmon"

//
// **Absent sensors are reported as absent, never as zero.** A VM has no thermal
// zones at all, and a machine reporting 0 °C would look wonderfully cool. This
// is the same rule as the battery: "unknown" and "fine" are different answers.

const sysThermal = "/sys/class/thermal"

// Temperature is how hot this machine is, or that it cannot tell.
type Temperature struct {
	// Celsius is the hottest zone the machine reports. Nil when there are no
	// sensors — which is every VM, and some real hardware.
	Celsius *int `json:"celsius"`

	// Sensor is which zone that reading came from, in the kernel's words:
	// "x86_pkg_temp", "acpitz". Reported because on a machine with several, the
	// name is what tells somebody whether it is the processor or the case.
	Sensor string `json:"sensor,omitempty"`

	// State is "ok", "warm", "hot", or empty when there is no reading.
	//
	// Words rather than a number, because the number needs context nobody has:
	// 82 °C is alarming on a chassis sensor and ordinary on a processor under
	// load.
	State string `json:"state,omitempty"`

	// Message is what to tell somebody, and is only set when something is worth
	// telling them. A machine at a normal temperature says nothing.
	Message string `json:"message,omitempty"`
}

// Fan is what the cooling is doing.
type Fan struct {
	// RPM is the measured speed. Nil where there is no sensor — every VM, most
	// desktops with the fan on the motherboard header, and this hardware while
	// it is under manual control, which is its own answer below.
	RPM *int `json:"rpm"`

	// Percent is how hard it is being driven, 0 to 100. Reported alongside RPM
	// because they answer different questions: RPM says how loud it is, percent
	// says how much is left.
	Percent *int `json:"percent"`

	// Label is the kernel's name for it — "cpu_fan".
	Label string `json:"label,omitempty"`

	// Controlled is who decides the speed: "firmware" when the machine's own
	// controller is running its curve, "manual" when something has overridden
	// it, "" when this cannot be told.
	//
	// Worth reporting on its own. A fan somebody pinned to full years ago and a
	// fan responding correctly to a hot machine sound exactly alike, and the
	// first is fixed in seconds while the second needs the heatsink cleaning.
	Controlled string `json:"controlled,omitempty"`

	// Message is set only when there is something to say.
	Message string `json:"message,omitempty"`
}

// The thresholds.
//
// Deliberately high. Processors are designed to run at 80 °C and throttle in the
// nineties, so warning at 60 would teach people to ignore the warning — which is
// the failure mode of every temperature indicator ever shipped. `hot` is the
// point at which a machine is genuinely losing performance to heat.
const (
	warmCelsius = 80
	hotCelsius  = 90
)

// readTemperature reports the hottest zone the kernel knows about.
//
// The hottest rather than an average: a machine with one component at 95 °C and
// three at 40 °C has a problem, and the average would hide it.
func readTemperature(root string) Temperature {
	zones, err := filepath.Glob(filepath.Join(root, "thermal_zone*"))
	if err != nil || len(zones) == 0 {
		return Temperature{}
	}
	sort.Strings(zones)

	hottest := -1
	sensor := ""
	for _, zone := range zones {
		raw, err := os.ReadFile(filepath.Join(zone, "temp"))
		if err != nil {
			continue
		}
		// Millidegrees, which is what the kernel reports and what makes an
		// unconverted reading look like a house fire.
		milli, err := strconv.Atoi(strings.TrimSpace(string(raw)))
		if err != nil {
			continue
		}
		celsius := milli / 1000

		// A zone can report an implausible value when its sensor is not
		// connected — commonly 0 or a large negative. Reporting those as the
		// machine's temperature would be worse than reporting nothing.
		if celsius < 1 || celsius > 150 {
			continue
		}
		if celsius > hottest {
			hottest = celsius
			sensor = readZoneType(zone)
		}
	}

	if hottest < 0 {
		return Temperature{}
	}

	temperature := Temperature{Celsius: &hottest, Sensor: sensor, State: "ok"}
	switch {
	case hottest >= hotCelsius:
		temperature.State = "hot"
		temperature.Message = "This server is running very hot and is probably slowing " +
			"itself down to cope. Give it more air: take it out of any cupboard or " +
			"drawer, stand it on something hard rather than carpet, and have the " +
			"dust blown out of its fan."
	case hottest >= warmCelsius:
		temperature.State = "warm"
		temperature.Message = "This server is running warm. That is not a fault, but if " +
			"it is in a cupboard or on carpet it will do better somewhere with more air."
	}
	return temperature
}

func readZoneType(zone string) string {
	raw, err := os.ReadFile(filepath.Join(zone, "type"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(raw))
}

// --- The fan ------------------------------------------------------------------

// readFan reports what the cooling is doing, from the first hwmon device that
// has a fan on it.
//
// The first rather than all of them: a laptop has one fan that matters, and a
// list of four readings from a desktop is a worse answer than one. Machines with
// no fan sensor — every VM — report nothing rather than zero, which is the same
// rule the temperature follows and for the same reason: 0 RPM reads as a seized
// fan rather than as an absent sensor.
func readFan(root string) Fan {
	devices, err := filepath.Glob(filepath.Join(root, "hwmon*"))
	if err != nil {
		return Fan{}
	}
	sort.Strings(devices)

	for _, device := range devices {
		if _, err := os.Stat(filepath.Join(device, "fan1_input")); err != nil {
			continue
		}
		fan := Fan{
			Label:      strings.TrimSpace(readFile(filepath.Join(device, "fan1_label"))),
			Controlled: fanControl(readFile(filepath.Join(device, "pwm1_enable"))),
		}

		// Read, and allowed to fail. asus_wmi returns ENXIO for fan1_input
		// while the fan is under manual control, so a machine somebody has
		// overridden reports a speed of "unknown" — which is true, and is the
		// most pointed argument available against overriding it.
		if rpm, ok := readNumber(filepath.Join(device, "fan1_input")); ok && rpm >= 0 && rpm < 30000 {
			fan.RPM = &rpm
		}
		if raw, ok := readNumber(filepath.Join(device, "pwm1")); ok && raw >= 0 && raw <= 255 {
			percent := raw * 100 / 255
			fan.Percent = &percent
		}

		if fan.Controlled == "manual" {
			fan.Message = "Something has taken manual control of this fan. Unless " +
				"that was deliberate, the machine's own controller does a better " +
				"job — it can see temperatures that nothing else can."
		}
		return fan
	}
	return Fan{}
}

// fanControl turns pwm1_enable into words. The numbers are the kernel's hwmon
// convention: 0 is no control at all (full speed), 1 is manual, anything above
// is one of the automatic modes.
func fanControl(raw string) string {
	value, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		return ""
	}
	switch {
	case value == 0:
		return "full"
	case value == 1:
		return "manual"
	default:
		return "firmware"
	}
}

func readFile(path string) string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(raw)
}

func readNumber(path string) (int, bool) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	value, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		return 0, false
	}
	return value, true
}
