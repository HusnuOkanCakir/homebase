package api

import (
	"bufio"
	"context"
	"encoding/csv"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/HusnuOkanCakir/homebase/internal/hostclient"
)

// A record of how hot this machine has been.
//
// One reading tells you almost nothing. 58 °C is fine, or it is the beginning of
// a summer afternoon that ends in thermal shutdown, and the difference is
// entirely in what the last week looked like. The questions worth asking are all
// about change: is it hotter than it was, does it climb whenever something
// transcodes, did cleaning the fan help.
//
// **A CSV, not a table in the database.** The same reasoning as backups being
// plain files ([ADR-0014](../../docs/decisions/0014-backups-are-readable-without-homebase.md)):
// this is a record about a machine, and a record about a machine that can only
// be read by software running on that machine is worth much less than one
// somebody can open in a spreadsheet, plot with two lines of Python, or grep
// from an ssh session while the dashboard is down. It costs an import to make
// the same graph; it saves needing Homebase to be working to answer "was it
// getting hotter before it died".
//
// Written by core, which is unprivileged. Nothing here needs root: the readings
// come from hostd over the socket and land in a directory core already owns.

const (
	// thermalInterval is how often a sample is taken.
	//
	// Five minutes. Fast enough to see a transcode, slow enough that a year is
	// a small file, and slow enough not to be a reason the disk never idles.
	thermalInterval = 5 * time.Minute

	// thermalMaxBytes is where the file is rotated. About four months at five
	// minutes, and one previous file is kept, so the record is bounded at
	// roughly eight months and a few megabytes without needing logrotate to be
	// configured — a server nobody administers must not fill its own disk with
	// a diagnostic.
	thermalMaxBytes = 4 << 20

	thermalHeader = "time,celsius,state,fan_rpm,fan_percent,fan_control,load"

	// DefaultThermalLogPath is where the record is kept: a directory core
	// already owns, beside the other things somebody diagnosing this machine
	// would go looking at.
	DefaultThermalLogPath = "/var/log/homebase/thermal.csv"
)

// ThermalLog records the temperature and fan speed at a fixed interval.
type ThermalLog struct {
	host *hostclient.Client
	log  *slog.Logger
	path string
}

func NewThermalLog(host *hostclient.Client, logger *slog.Logger, path string) *ThermalLog {
	return &ThermalLog{host: host, log: logger, path: path}
}

// Watch samples until the context is cancelled.
func (t *ThermalLog) Watch(ctx context.Context) {
	ticker := time.NewTicker(thermalInterval)
	defer ticker.Stop()

	// Once at startup. It marks the boot in the record, which is what makes a
	// gap in the timestamps readable as "the machine was off" rather than as
	// "the recording stopped working".
	t.Sample(ctx)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			t.Sample(ctx)
		}
	}
}

// Sample takes one reading and appends it.
func (t *ThermalLog) Sample(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	resources, err := t.host.SystemResources(ctx)
	if err != nil {
		// Not logged at error level and not retried. hostd being briefly
		// unreachable is ordinary — it is socket-activated and restarts on
		// upgrade — and a diagnostic that fills the journal with complaints is
		// worse than one that quietly misses a sample every few months.
		t.log.Debug("could not sample the temperature", "error", err)
		return
	}

	// Absent readings are written as empty fields, never as zero. A machine with
	// no sensors is every VM, and a column of zeroes plots as a wonderfully cool
	// server rather than as no data — the same rule the reading itself follows.
	record := []string{
		time.Now().UTC().Format(time.RFC3339),
		optionalInt(resources.Temperature.Celsius),
		resources.Temperature.State,
		optionalInt(resources.Fan.RPM),
		optionalInt(resources.Fan.Percent),
		resources.Fan.Controlled,
		strconv.FormatFloat(resources.LoadAverage[0], 'f', 2, 64),
	}

	if err := t.append(record); err != nil {
		t.log.Debug("could not record the temperature", "error", err)
	}
}

func optionalInt(value *int) string {
	if value == nil {
		return ""
	}
	return strconv.Itoa(*value)
}

// append adds one row, creating the file with a header and rotating it when it
// has grown enough.
func (t *ThermalLog) append(record []string) error {
	if err := os.MkdirAll(filepath.Dir(t.path), 0o750); err != nil {
		return err
	}

	if info, err := os.Stat(t.path); err == nil && info.Size() >= thermalMaxBytes {
		// One previous file, replaced. A server that nobody administers must
		// not fill its own disk with a record of how warm it was.
		_ = os.Rename(t.path, t.path+".1")
	}

	file, err := os.OpenFile(t.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o640)
	if err != nil {
		return err
	}
	defer file.Close()

	if info, err := file.Stat(); err == nil && info.Size() == 0 {
		if _, err := file.WriteString(thermalHeader + "\n"); err != nil {
			return err
		}
	}

	writer := csv.NewWriter(file)
	if err := writer.Write(record); err != nil {
		return err
	}
	writer.Flush()
	return writer.Error()
}

// --- Reading it back -------------------------------------------------------------

// ThermalSample is one reading, as the API reports it.
type ThermalSample struct {
	Time    string `json:"time"`
	Celsius *int   `json:"celsius"`
	State   string `json:"state,omitempty"`
	FanRPM  *int   `json:"fan_rpm"`
	Percent *int   `json:"fan_percent"`
	Control string `json:"fan_control,omitempty"`
	Load    *float64
}

// ThermalHistory is the record, plus what it adds up to.
type ThermalHistory struct {
	Samples []ThermalSample `json:"samples"`

	// The summary. Computed here rather than in every client, because the
	// interesting facts about a week of temperatures are all comparisons and
	// each one done differently in three places is three chances to disagree.
	Hottest  *int `json:"hottest_celsius"`
	Coolest  *int `json:"coolest_celsius"`
	Average  *int `json:"average_celsius"`
	LoudestF *int `json:"loudest_rpm"`
	Quietest *int `json:"quietest_rpm"`

	// Since is the oldest reading returned, so a caller can say what period the
	// summary covers rather than implying it covers everything.
	Since string `json:"since,omitempty"`

	// Recording says whether samples are being taken at all. A history that is
	// empty because the machine was just installed and one that is empty because
	// the recorder is broken look identical, and only one needs doing something
	// about.
	Recording bool `json:"recording"`
}

// readThermalHistory returns the samples within the given window.
//
// Both files are read, oldest first, so a rotation in the middle of the window
// does not silently truncate it.
func readThermalHistory(path string, within time.Duration, limit int) ThermalHistory {
	history := ThermalHistory{Samples: []ThermalSample{}}
	cutoff := time.Now().UTC().Add(-within)

	for _, name := range []string{path + ".1", path} {
		file, err := os.Open(name)
		if err != nil {
			continue
		}
		history.Recording = true
		scanner := bufio.NewScanner(file)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" || strings.HasPrefix(line, "time,") {
				continue
			}
			sample, when, ok := parseThermalRow(line)
			if !ok || when.Before(cutoff) {
				continue
			}
			history.Samples = append(history.Samples, sample)
		}
		file.Close()
	}

	sort.Slice(history.Samples, func(i, j int) bool {
		return history.Samples[i].Time < history.Samples[j].Time
	})

	// Thinned rather than truncated. Keeping the most recent N would answer
	// "the last six hours" to somebody who asked about the last month, which is
	// a different question with a very different answer.
	if limit > 0 && len(history.Samples) > limit {
		history.Samples = thin(history.Samples, limit)
	}

	if len(history.Samples) > 0 {
		history.Since = history.Samples[0].Time
	}
	summariseThermal(&history)
	return history
}

func parseThermalRow(line string) (ThermalSample, time.Time, bool) {
	fields := strings.Split(line, ",")
	if len(fields) < 6 {
		return ThermalSample{}, time.Time{}, false
	}
	when, err := time.Parse(time.RFC3339, fields[0])
	if err != nil {
		return ThermalSample{}, time.Time{}, false
	}
	sample := ThermalSample{
		Time:    fields[0],
		Celsius: parseOptionalInt(fields[1]),
		State:   fields[2],
		FanRPM:  parseOptionalInt(fields[3]),
		Percent: parseOptionalInt(fields[4]),
		Control: fields[5],
	}
	if len(fields) > 6 {
		if load, err := strconv.ParseFloat(fields[6], 64); err == nil {
			sample.Load = &load
		}
	}
	return sample, when, true
}

func parseOptionalInt(field string) *int {
	field = strings.TrimSpace(field)
	if field == "" {
		return nil
	}
	value, err := strconv.Atoi(field)
	if err != nil {
		return nil
	}
	return &value
}

// thin reduces a series to at most n points, evenly spaced.
//
// The last point is always kept: it is the only one somebody can check against
// the machine in front of them, and a graph whose right-hand edge disagrees with
// the current reading is a graph nobody believes.
func thin(samples []ThermalSample, n int) []ThermalSample {
	if n <= 1 || len(samples) <= n {
		return samples
	}
	out := make([]ThermalSample, 0, n)
	step := float64(len(samples)-1) / float64(n-1)
	for i := 0; i < n-1; i++ {
		out = append(out, samples[int(float64(i)*step)])
	}
	return append(out, samples[len(samples)-1])
}

func summariseThermal(history *ThermalHistory) {
	var total, count int
	for _, sample := range history.Samples {
		if sample.Celsius != nil {
			value := *sample.Celsius
			total += value
			count++
			if history.Hottest == nil || value > *history.Hottest {
				history.Hottest = intPtr(value)
			}
			if history.Coolest == nil || value < *history.Coolest {
				history.Coolest = intPtr(value)
			}
		}
		if sample.FanRPM != nil {
			rpm := *sample.FanRPM
			if history.LoudestF == nil || rpm > *history.LoudestF {
				history.LoudestF = intPtr(rpm)
			}
			if history.Quietest == nil || rpm < *history.Quietest {
				history.Quietest = intPtr(rpm)
			}
		}
	}
	if count > 0 {
		history.Average = intPtr(total / count)
	}
}

func intPtr(value int) *int { return &value }

// MarshalJSON keeps `load` out of the wire format when it was not recorded,
// rather than sending a zero that plots as an idle machine.
func (s ThermalSample) MarshalJSON() ([]byte, error) {
	fields := []string{
		fmt.Sprintf("%q:%q", "time", s.Time),
		fmt.Sprintf("%q:%s", "celsius", jsonOptionalInt(s.Celsius)),
		fmt.Sprintf("%q:%s", "fan_rpm", jsonOptionalInt(s.FanRPM)),
		fmt.Sprintf("%q:%s", "fan_percent", jsonOptionalInt(s.Percent)),
	}
	if s.State != "" {
		fields = append(fields, fmt.Sprintf("%q:%q", "state", s.State))
	}
	if s.Control != "" {
		fields = append(fields, fmt.Sprintf("%q:%q", "fan_control", s.Control))
	}
	if s.Load != nil {
		fields = append(fields, fmt.Sprintf("%q:%s", "load",
			strconv.FormatFloat(*s.Load, 'f', 2, 64)))
	}
	return []byte("{" + strings.Join(fields, ",") + "}"), nil
}

func jsonOptionalInt(value *int) string {
	if value == nil {
		return "null"
	}
	return strconv.Itoa(*value)
}
