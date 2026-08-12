package hostd

import (
	"testing"
	"time"
)

// The schedule is a fixed table, and that is the security property.
//
// `OnCalendar` goes into a unit file, and a unit file is a way to run commands
// as root. Nothing a caller sends may reach one — so a caller picks a word and
// the calendar expression is ours. This test is here to fail if somebody ever
// makes the table take a string.
func TestOnlyKnownWordsHaveACalendar(t *testing.T) {
	for _, word := range []string{
		"", "hourly", "sometimes", "DAILY", "daily ", "off",
		"daily\nOnCalendar=*-*-* *:*:00",
		"*-*-* 03:00:00",
	} {
		if _, known := schedules[word]; known {
			t.Errorf("%q is accepted as a schedule", word)
		}
	}

	for _, word := range []string{"daily", "weekly"} {
		calendar, known := schedules[word]
		if !known {
			t.Fatalf("%q has no calendar", word)
		}
		if _, described := scheduleInWords[word]; !described {
			t.Errorf("%q has a calendar but nothing to show a person", word)
		}
		// One line, one expression. A newline here would be a second directive
		// in the drop-in.
		if len(calendar) == 0 || calendar[len(calendar)-1] == '\n' {
			t.Errorf("%q has a suspicious calendar: %q", word, calendar)
		}
	}

	if _, described := scheduleInWords["off"]; !described {
		t.Error("there is nothing to show somebody whose backups are turned off")
	}
}

// The bug this test exists for: `NextElapseUSecRealtime` is named after
// microseconds, and current systemd prints a formatted timestamp. Reading only
// the numeric form left the field empty on every real machine — and an empty
// field looks exactly like "no next run", so nothing appeared to be wrong.
func TestNextRunIsReadInEitherFormSystemdUses(t *testing.T) {
	t.Setenv("TZ", "UTC")
	time.Local = time.UTC

	cases := []struct {
		name  string
		value string
		want  string
	}{
		{"current systemd", "Thu 2026-08-13 03:00:00 UTC", "2026-08-13T03:00:00Z"},
		{"with microseconds", "Thu 2026-08-13 03:00:00.000000 UTC", "2026-08-13T03:00:00Z"},
		{"older systemd, microseconds since the epoch", "1786590000000000", "2026-08-13T03:00:00Z"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			when, ok := parseSystemdTime(c.value)
			if !ok {
				t.Fatalf("%q was not understood; the next run would show as unknown", c.value)
			}
			if got := when.UTC().Format(time.RFC3339); got != c.want {
				t.Errorf("%q → %s, want %s", c.value, got, c.want)
			}
		})
	}
}

func TestATimerThatIsNotRunningHasNoNextRun(t *testing.T) {
	for _, value := range []string{"", "  ", "0", "n/a", "infinity", "never"} {
		if when, ok := parseSystemdTime(value); ok {
			t.Errorf("%q was read as a time (%s)", value, when)
		}
	}
}
