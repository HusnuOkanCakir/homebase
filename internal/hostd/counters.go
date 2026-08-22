package hostd

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// The counters behind "how busy is this machine".
//
// Everything here is **cumulative since boot**, and is reported that way rather
// than as a rate. That is the whole design decision in this file.
//
// A rate is a difference between two moments, and the only place that knows both
// moments is whatever is keeping the record. If hostd computed rates it would
// have to remember its own last reading, which means a number that is wrong for
// the first call after every restart, wrong for any caller that polls at a
// different interval, and impossible to recompute later from what was stored.
// Counters have none of those problems: two rows of a log ten minutes apart give
// the average over those ten minutes, exactly, for ever.
//
// The cost is that a counter wraps and resets — on reboot, and on 32-bit
// interface counters roughly every four gigabytes. Whoever computes the
// difference has to treat a decrease as a reset rather than as negative traffic,
// which is one line where it is computed and no lines here.

// Counters is the state of the machine as running totals.
type Counters struct {
	// CPUBusy and CPUTotal are in whatever units the kernel counts in — the
	// ratio is what matters, so the unit does not. Busy is everything that is
	// not idle or waiting for a disk.
	CPUBusy  uint64 `json:"cpu_busy"`
	CPUTotal uint64 `json:"cpu_total"`

	// NetworkRx and NetworkTx are bytes across every real interface, added up.
	//
	// Loopback is excluded, and so are the container bridges: a machine talking
	// to itself is not network traffic, and counting the Docker bridge would
	// report every byte an application serves twice.
	NetworkRx uint64 `json:"network_rx"`
	NetworkTx uint64 `json:"network_tx"`
}

const procStat = "/proc/stat"

// readCounters gathers the running totals.
func readCounters(statPath, classNet string) Counters {
	counters := Counters{}
	counters.CPUBusy, counters.CPUTotal = readCPUTime(statPath)
	counters.NetworkRx, counters.NetworkTx = readNetworkBytes(classNet)
	return counters
}

// readCPUTime returns busy and total time since boot.
//
// The first line of /proc/stat, which is every processor added together. Idle
// and iowait are the two fields that are not work: a machine waiting for a slow
// disk is not busy, and counting iowait as busy would report a laptop copying a
// film as pinned when it is nearly asleep.
func readCPUTime(path string) (busy, total uint64) {
	content, err := os.ReadFile(path)
	if err != nil {
		return 0, 0
	}
	for _, line := range strings.Split(string(content), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 5 || fields[0] != "cpu" {
			continue
		}
		// user nice system idle iowait irq softirq steal …
		for i, field := range fields[1:] {
			value, err := strconv.ParseUint(field, 10, 64)
			if err != nil {
				continue
			}
			total += value
			if i != 3 && i != 4 {
				busy += value
			}
		}
		return busy, total
	}
	return 0, 0
}

// readNetworkBytes adds up traffic across the interfaces that carry it.
func readNetworkBytes(classNet string) (rx, tx uint64) {
	entries, err := os.ReadDir(classNet)
	if err != nil {
		return 0, 0
	}
	for _, entry := range entries {
		name := entry.Name()
		if !countableInterface(name, classNet) {
			continue
		}
		rx += readCounterFile(filepath.Join(classNet, name, "statistics", "rx_bytes"))
		tx += readCounterFile(filepath.Join(classNet, name, "statistics", "tx_bytes"))
	}
	return rx, tx
}

// countableInterface reports whether an interface's traffic is real traffic.
//
// The same reasoning as the network status screen, and it matters more here:
// every byte an application serves crosses the Docker bridge *and* the real
// card, so counting both would double everything and make a server look twice
// as busy as it is.
func countableInterface(name, classNet string) bool {
	switch {
	case name == "lo":
		return false
	case strings.HasPrefix(name, "docker"), strings.HasPrefix(name, "br-"),
		strings.HasPrefix(name, "veth"), strings.HasPrefix(name, "virbr"):
		return false
	}
	// A wg0 is real traffic but it is the same bytes as the card underneath it,
	// wrapped. Counted once, on the card.
	if strings.HasPrefix(name, "wg") {
		return false
	}
	// Anything without a device behind it is virtual. This is what excludes
	// bridges and tunnels that do not match a name above.
	if _, err := os.Stat(filepath.Join(classNet, name, "device")); err != nil {
		return false
	}
	return true
}

func readCounterFile(path string) uint64 {
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	value, err := strconv.ParseUint(strings.TrimSpace(string(raw)), 10, 64)
	if err != nil {
		return 0
	}
	return value
}
