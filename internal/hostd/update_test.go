package hostd

import (
	"os"
	"path/filepath"
	"testing"
)

// The states these tests describe are the ones an interrupted update leaves
// behind. They are written from dpkg's real output rather than from a
// simplification of it, because the whole value of this code is telling a
// half-applied update apart from a finished one, and a fixture that only ever
// says "installed" would agree with a version that never checked.

func TestAMachineRunningOneVersionSaysSo(t *testing.T) {
	components := parseComponents(
		"homebase-hostd\t0.4.0\tinstalled\n" +
			"homebase-core\t0.4.0\tinstalled\n" +
			"homebase-apps\t0.4.0\tinstalled\n" +
			"homebase-dashboard\t0.4.0\tinstalled\n")

	if len(components) != 4 {
		t.Fatalf("expected four components, got %d", len(components))
	}
	if !consistent(components) {
		t.Error("four packages at the same version should be consistent")
	}
	if dpkgInterrupted(components, t.TempDir()) {
		t.Error("nothing here was interrupted")
	}
}

func TestAnUpdateThatStoppedHalfwayIsNotReportedAsFine(t *testing.T) {
	// What the machine looks like when dpkg was killed after upgrading two
	// packages. Every package is "installed" — nothing is in a partial state —
	// so the only evidence is that the versions disagree. This is the case a
	// status check that reported a single version would call healthy.
	components := parseComponents(
		"homebase-hostd\t0.5.0\tinstalled\n" +
			"homebase-core\t0.5.0\tinstalled\n" +
			"homebase-apps\t0.4.0\tinstalled\n" +
			"homebase-dashboard\t0.4.0\tinstalled\n")

	if consistent(components) {
		t.Error("two packages at 0.5.0 and two at 0.4.0 is a half-applied update")
	}
}

func TestAPackageDpkgNeverFinishedIsReportedAsInterrupted(t *testing.T) {
	components := parseComponents(
		"homebase-hostd\t0.5.0\tinstalled\n" +
			"homebase-core\t0.5.0\thalf-configured\n" +
			"homebase-apps\t0.5.0\tunpacked\n" +
			"homebase-dashboard\t0.5.0\tinstalled\n")

	if !dpkgInterrupted(components, t.TempDir()) {
		t.Error("half-configured and unpacked packages mean dpkg did not finish")
	}
}

func TestARemovedPackageIsNotAnInterruptedOne(t *testing.T) {
	// `config-files` is a package somebody removed without purging. It is a
	// finished state, and calling it an interrupted transaction would send a
	// user to run `dpkg --configure -a` over a deliberate removal.
	components := parseComponents(
		"homebase-hostd\t0.4.0\tinstalled\n" +
			"homebase-core\t0.4.0\tinstalled\n" +
			"homebase-apps\t0.4.0\tconfig-files\n" +
			"homebase-dashboard\t0.4.0\tinstalled\n")

	if dpkgInterrupted(components, t.TempDir()) {
		t.Error("a removed-but-not-purged package is not an interrupted transaction")
	}
	if !consistent(components) {
		t.Error("a removed package should not make the remaining ones disagree")
	}
}

func TestADpkgJournalLeftBehindMeansInterrupted(t *testing.T) {
	// dpkg died before it could update its own database, so every package still
	// reads as fine and the only evidence is the journal it left behind.
	updates := t.TempDir()
	if err := os.WriteFile(filepath.Join(updates, "0017"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	components := parseComponents("homebase-core\t0.4.0\tinstalled\n")
	if !dpkgInterrupted(components, updates) {
		t.Error("files in dpkg's journal directory mean a transaction was not replayed")
	}
}

func TestAPackageDpkgSaidNothingAboutIsReportedMissing(t *testing.T) {
	// dpkg-query prints nothing for a package it does not know, and exits
	// non-zero. Dropping it from the report would make a machine with no
	// dashboard installed look complete.
	components := parseComponents("homebase-hostd\t0.4.0\tinstalled\n")

	if len(components) != 4 {
		t.Fatalf("all four components should be reported, got %d", len(components))
	}
	for _, component := range components[1:] {
		if component.State != "not-installed" {
			t.Errorf("%s: expected not-installed, got %q", component.Package, component.State)
		}
		if component.installed() {
			t.Errorf("%s should not count as installed", component.Package)
		}
	}
}

func TestTheChannelIsReadFromAptsOwnSource(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "homebase.sources")
	if err := os.WriteFile(source, []byte(
		"Types: deb\n"+
			"URIs: https://apt.homebase.test\n"+
			"Suites: beta\n"+
			"Components: main\n"+
			"Signed-By: /usr/share/keyrings/homebase-archive-keyring.gpg\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	channel, origin := readChannel(source)
	if channel != "beta" {
		t.Errorf("channel: got %q, want beta", channel)
	}
	if origin != "https://apt.homebase.test" {
		t.Errorf("origin: got %q", origin)
	}
}

func TestAMachineWithNoUpdateSourceHasNoChannel(t *testing.T) {
	// Every machine installed from media starts here. Reporting a channel it
	// does not have would promise updates that cannot arrive.
	channel, origin := readChannel(filepath.Join(t.TempDir(), "absent.sources"))
	if channel != "" || origin != "" {
		t.Errorf("got channel %q origin %q, want both empty", channel, origin)
	}
}

func TestOnlyChannelsHomebasePublishesAreAccepted(t *testing.T) {
	for _, name := range knownChannels {
		if !validChannel(name) {
			t.Errorf("%q should be a valid channel", name)
		}
	}

	// The rejected cases are not hypothetical tidiness. This string is written
	// into apt's source file, which decides what code the machine runs as root,
	// so anything that is not one of four known words has no business reaching
	// it.
	for _, name := range []string{
		"", "STABLE", "nightly", "stable\nSuites: evil",
		"../../../etc/apt/sources.list.d/evil", "main stable",
	} {
		if validChannel(name) {
			t.Errorf("%q should not be accepted as a channel", name)
		}
	}
}

func TestAnUpdateSourceHasToBeAnAddress(t *testing.T) {
	for _, origin := range []string{
		"https://apt.homebase.computer",
		"http://10.0.2.2:8000/repo",
	} {
		if err := validOrigin(origin); err != nil {
			t.Errorf("%q should be accepted: %v", origin, err)
		}
	}

	// file: is refused even though apt supports it. An update source on the
	// local disk is not an update source; it is a way to install something
	// that never crossed a signature check anybody watched.
	for _, origin := range []string{
		"", "file:///tmp/evil", "apt.homebase.computer", "https://",
		"https://example.com\nSigned-By: /tmp/attacker.gpg",
	} {
		if err := validOrigin(origin); err == nil {
			t.Errorf("%q should have been refused", origin)
		}
	}
}
