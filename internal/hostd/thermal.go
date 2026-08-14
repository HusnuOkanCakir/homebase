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
// Nothing here acts on the temperature. Homebase does not control fans and
// should not pretend to; what it can do is say so, which is the difference
// between somebody opening a cupboard door and somebody replacing a laptop.
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
