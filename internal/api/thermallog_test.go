package api

import (
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

func at(minutesAgo int) string {
	return time.Now().UTC().Add(-time.Duration(minutesAgo) * time.Minute).Format(time.RFC3339)
}

func TestTheHistorySummarisesWhatWasRecorded(t *testing.T) {
	path := writeThermalLog(t,
		at(30)+",52,ok,2900,33,firmware,0.10",
		at(20)+",86,warm,4200,67,firmware,3.90",
		at(10)+",61,ok,3300,33,firmware,0.50",
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
		at(20)+",,,,,,0.10",
		at(10)+",55,ok,,,firmware,0.20",
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
	path := writeThermalLog(t, at(30)+",60,ok,3000,33,firmware,0.10")
	older := thermalHeader + "\n" +
		at(120) + ",70,ok,3500,33,firmware,0.10\n" +
		at(60*24*9) + ",99,hot,5000,67,firmware,4.00\n"
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
		rows = append(rows, at(i)+",50,ok,3000,33,firmware,0.10")
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
		at(20)+",52,ok,2900,33,firmware,0.10",
		"this is not a row",
		at(10)+",not-a-number,ok,3300,33,firmware,0.50",
		at(5)+",61,ok,3300,33,firmware,0.50",
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
