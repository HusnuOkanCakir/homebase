package api

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeThermalLog(t *testing.T, rows ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "thermal.csv")
	body := thermalHeader + "\n" + strings.Join(rows, "\n") + "\n"
	if err := os.WriteFile(path, []byte(body), 0o640); err != nil {
		t.Fatal(err)
	}
	return path
}

func minutesAgoStamp(minutesAgo int) string {
	return time.Now().UTC().Add(-time.Duration(minutesAgo) * time.Minute).Format(time.RFC3339)
}

func TestTheHistorySummarisesWhatWasRecorded(t *testing.T) {
	path := writeThermalLog(t,
		minutesAgoStamp(30)+",52,ok,2900,33,firmware,0.10",
		minutesAgoStamp(20)+",86,warm,4200,67,firmware,3.90",
		minutesAgoStamp(10)+",61,ok,3300,33,firmware,0.50",
	)

	history := readThermalHistory(path, 24*time.Hour, 0)
	if len(history.Samples) != 3 {
		t.Fatalf("got %d samples, want 3", len(history.Samples))
	}
	if !history.Recording {
		t.Error("a file that exists was not reported as recording")
	}
	for name, got := range map[string]*int{
		"hottest": history.Hottest, "coolest": history.Coolest,
		"average": history.Average, "loudest": history.LoudestF,
	} {
		if got == nil {
			t.Fatalf("%s was not computed", name)
		}
	}
	if *history.Hottest != 86 || *history.Coolest != 52 {
		t.Errorf("range %d–%d, want 52–86", *history.Coolest, *history.Hottest)
	}
	if *history.Average != 66 {
		t.Errorf("average %d, want 66", *history.Average)
	}
	if *history.LoudestF != 4200 || *history.Quietest != 2900 {
		t.Errorf("fan range %d–%d, want 2900–4200", *history.Quietest, *history.LoudestF)
	}
}

// An absent sensor is absent. A machine with no thermal zone — every VM — must
// not appear in the record as a wonderfully cool one, and a run of zeroes plots
// exactly like that.
func TestAMissingReadingIsNotZero(t *testing.T) {
	path := writeThermalLog(t,
		minutesAgoStamp(20)+",,,,,,0.10",
		minutesAgoStamp(10)+",55,ok,,,firmware,0.20",
	)

	history := readThermalHistory(path, 24*time.Hour, 0)
	if len(history.Samples) != 2 {
		t.Fatalf("got %d samples, want 2", len(history.Samples))
	}
	if history.Samples[0].Celsius != nil {
		t.Errorf("an empty reading became %d", *history.Samples[0].Celsius)
	}
	if history.Samples[1].FanRPM != nil {
		t.Errorf("an empty fan speed became %d", *history.Samples[1].FanRPM)
	}
	// And the summary ignores them rather than averaging a zero in.
	if history.Average == nil || *history.Average != 55 {
		t.Errorf("average = %v, want 55 — empty readings must not be counted", history.Average)
	}
	if history.Quietest != nil {
		t.Errorf("a fan speed was reported from readings that had none: %d", *history.Quietest)
	}
}

// Readings older than the window are left out, and a rotated file is still read
// — a rotation in the middle of the period must not silently truncate it.
func TestTheWindowIsRespectedAcrossARotation(t *testing.T) {
	path := writeThermalLog(t, minutesAgoStamp(30)+",60,ok,3000,33,firmware,0.10")
	older := thermalHeader + "\n" +
		minutesAgoStamp(120) + ",70,ok,3500,33,firmware,0.10\n" +
		minutesAgoStamp(60*24*9) + ",99,hot,5000,67,firmware,4.00\n"
	if err := os.WriteFile(path+".1", []byte(older), 0o640); err != nil {
		t.Fatal(err)
	}

	history := readThermalHistory(path, 24*time.Hour, 0)
	if len(history.Samples) != 2 {
		t.Fatalf("got %d samples, want 2 — the rotated file within the window counts",
			len(history.Samples))
	}
	if *history.Hottest != 70 {
		t.Errorf("hottest = %d, want 70 — the nine-day-old reading is outside the window",
			*history.Hottest)
	}
	// Oldest first, or every chart drawn from this is backwards.
	if history.Samples[0].Time > history.Samples[1].Time {
		t.Error("samples came back newest first")
	}
}

// Thinned rather than truncated. Keeping the most recent N would answer "the
// last few hours" to somebody who asked about the last month — a different
// question with a very different answer.
func TestTooManyReadingsAreThinnedNotTruncated(t *testing.T) {
	rows := make([]string, 0, 400)
	for i := 400; i > 0; i-- {
		rows = append(rows, minutesAgoStamp(i)+",50,ok,3000,33,firmware,0.10")
	}
	path := writeThermalLog(t, rows...)

	full := readThermalHistory(path, 24*time.Hour, 0)
	thinned := readThermalHistory(path, 24*time.Hour, 50)

	if len(thinned.Samples) > 50 {
		t.Fatalf("got %d samples, want at most 50", len(thinned.Samples))
	}
	// The period must still be the whole period.
	if thinned.Samples[0].Time != full.Samples[0].Time {
		t.Errorf("thinning moved the start from %s to %s",
			full.Samples[0].Time, thinned.Samples[0].Time)
	}
	// And the last point must be the real last point: it is the only one
	// somebody can check against the machine in front of them.
	if thinned.Samples[len(thinned.Samples)-1].Time !=
		full.Samples[len(full.Samples)-1].Time {
		t.Error("thinning dropped the most recent reading")
	}
}

// A machine that has never recorded anything and one whose recorder is broken
// look identical from an empty list, and only one of them needs anybody to do
// something about it.
func TestNoFileIsDistinguishableFromNoReadings(t *testing.T) {
	missing := readThermalHistory(filepath.Join(t.TempDir(), "nothing.csv"), time.Hour, 0)
	if missing.Recording {
		t.Error("a machine with no record claimed to be recording")
	}
	if missing.Samples == nil {
		t.Error("samples was null rather than an empty list, which clients index into")
	}

	empty := writeThermalLog(t)
	if !readThermalHistory(empty, time.Hour, 0).Recording {
		t.Error("a file with only a header was not reported as recording")
	}
}

// A truncated write — a power cut mid-append — must not stop the rest of the
// record being readable. This is a diagnostic, and the moment it matters most
// is right after the machine did something unexpected.
func TestAPartialRowDoesNotDiscardTheRest(t *testing.T) {
	path := writeThermalLog(t,
		minutesAgoStamp(20)+",52,ok,2900,33,firmware,0.10",
		"this is not a row",
		minutesAgoStamp(10)+",not-a-number,ok,3300,33,firmware,0.50",
		minutesAgoStamp(5)+",61,ok,3300,33,firmware,0.50",
	)

	history := readThermalHistory(path, time.Hour, 0)
	if len(history.Samples) != 3 {
		t.Fatalf("got %d samples, want 3 — the unparseable line is skipped, not fatal",
			len(history.Samples))
	}
	if history.Samples[1].Celsius != nil {
		t.Error("a non-numeric temperature was read as a number")
	}
}

// --- Rates from counters ---------------------------------------------------------

// Counters are recorded as running totals and differenced on the way out, so a
// rate can be recomputed years later at whatever resolution somebody asks for.
// This is that arithmetic, which is the only place the record can silently lie.
func TestRatesAreComputedFromTheDifference(t *testing.T) {
	// Five minutes apart. 300 MB received in 300 seconds is 1 MB/s; the
	// processor was busy for half the ticks that elapsed.
	path := writeThermalLog(t,
		minutesAgoStamp(10)+",50,ok,3000,33,firmware,0.10,4000000000,16000000000,1000,2000,0,0",
		minutesAgoStamp(5)+",52,ok,3200,33,firmware,0.20,8000000000,16000000000,1500,3000,314572800,104857600",
	)

	history := readThermalHistory(path, time.Hour, 0)
	if len(history.Samples) != 2 {
		t.Fatalf("got %d samples", len(history.Samples))
	}
	second := history.Samples[1]

	if second.CPUPercent == nil || *second.CPUPercent != 50 {
		t.Errorf("cpu = %v, want 50 — 500 busy ticks out of 1000 elapsed", second.CPUPercent)
	}
	if second.Download == nil || *second.Download != 1048576 {
		t.Errorf("download = %v, want 1048576 — 300 MB over 300 seconds", second.Download)
	}
	if second.Upload == nil || *second.Upload != 349525 {
		t.Errorf("upload = %v, want 349525", second.Upload)
	}
	if second.MemoryPercent == nil || *second.MemoryPercent != 50 {
		t.Errorf("memory = %v, want 50", second.MemoryPercent)
	}

	// The first row has nothing to subtract from. Absent, not zero: a zero
	// would draw as a quiet machine at the left edge of every chart.
	if history.Samples[0].CPUPercent != nil || history.Samples[0].Download != nil {
		t.Error("the first reading was given a rate with nothing to compare it to")
	}
}

// A counter that has gone down is a reboot, or a 32-bit interface counter
// wrapping. Neither is negative traffic, and reporting the difference would
// draw an enormous spike out of a machine that was switched off.
func TestACounterGoingBackwardsIsNotTraffic(t *testing.T) {
	path := writeThermalLog(t,
		minutesAgoStamp(10)+",50,ok,3000,33,firmware,0.10,4000000000,16000000000,900000,1800000,9000000000,8000000000",
		minutesAgoStamp(5)+",50,ok,3000,33,firmware,0.10,4000000000,16000000000,100,200,5000,4000",
	)

	second := readThermalHistory(path, time.Hour, 0).Samples[1]
	if second.Download != nil {
		t.Errorf("a counter reset was reported as %d bytes per second", *second.Download)
	}
	if second.CPUPercent != nil {
		t.Errorf("a counter reset was reported as %d%% busy", *second.CPUPercent)
	}
}

// Rows written before these columns existed are ordinary, not corrupt. A record
// spanning months is always read by software newer than what wrote it, so every
// old row must keep parsing — which is why columns are only ever appended.
func TestRowsWrittenBeforeTheseColumnsExistedStillRead(t *testing.T) {
	path := writeThermalLog(t,
		minutesAgoStamp(10)+",56,ok,5600,66,firmware,0.31",
		minutesAgoStamp(5)+",54,ok,5400,66,firmware,0.25",
	)

	history := readThermalHistory(path, time.Hour, 0)
	if len(history.Samples) != 2 {
		t.Fatalf("got %d samples from short rows, want 2", len(history.Samples))
	}
	if history.Samples[0].Celsius == nil || *history.Samples[0].Celsius != 56 {
		t.Error("a short row lost its temperature")
	}
	if history.Samples[1].CPUPercent != nil || history.Samples[1].MemoryPercent != nil {
		t.Error("a row with no counters was given a rate anyway")
	}
}

// Differencing happens before thinning. The other order would compute each rate
// across whatever gap thinning left, averaging away exactly the bursts a long
// chart is drawn to show.
func TestRatesSurviveThinning(t *testing.T) {
	rows := make([]string, 0, 200)
	for i := 200; i > 0; i-- {
		// One row per minute, sixty seconds of processor time each: a machine
		// pinned at one hundred per cent throughout.
		rows = append(rows, fmt.Sprintf("%s,50,ok,3000,33,firmware,1.0,1,2,%d,%d,%d,0",
			minutesAgoStamp(i), (200-i)*6000, (200-i)*6000, (200-i)*1000))
	}
	path := writeThermalLog(t, rows...)

	thinned := readThermalHistory(path, 24*time.Hour, 20)
	var measured int
	for _, sample := range thinned.Samples {
		if sample.CPUPercent == nil {
			continue
		}
		measured++
		if *sample.CPUPercent != 100 {
			t.Fatalf("cpu = %d%%, want 100 — thinning changed the rate", *sample.CPUPercent)
		}
	}
	if measured == 0 {
		t.Error("thinning dropped every rate")
	}
}
