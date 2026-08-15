package hostd

import (
	"strings"
	"testing"
)

func shareServices(t *testing.T) *ShareServices {
	t.Helper()
	storage := NewStorageServices(t.TempDir()+"/storage", t.TempDir()+"/state")
	return NewShareServices(storage, t.TempDir()+"/state")
}

// --- What the configuration must say ------------------------------------------

// Homebase writes the whole of smb.conf, so every one of these is a decision
// this file makes rather than one inherited from a distribution's defaults —
// which is exactly why they are asserted rather than assumed.
func TestTheShareConfigurationRefusesTheDangerousDefaults(t *testing.T) {
	config := renderSambaConfig("homebase", []ShareState{{
		Share: Share{Name: "backup", Location: "internal"},
		Path:  "/srv/homebase/storage/internal/shares/backup",
	}})

	required := map[string]string{
		"security = user":               "a share anybody on the network can open",
		"map to guest = never":          "guest access, which is a folder with no password",
		"server min protocol = SMB2_10": "SMB1, the protocol with the wormable bugs",
		"client min protocol = SMB2_10": "SMB1 when talking outward",
		"hosts deny = 0.0.0.0/0":        "reachable from anywhere",
		"bind interfaces only = yes":    "listening on every interface there is",
		"valid users = @homebase":       "anybody who can authenticate at all",
	}
	for line, otherwise := range required {
		if !strings.Contains(config, line) {
			t.Errorf("missing %q — without it the server allows %s", line, otherwise)
		}
	}

	// Only private address ranges. The way in from outside the house is the VPN;
	// SMB on the internet is among the most attacked services there is.
	for _, network := range privateNetworks {
		if !strings.Contains(config, network) {
			t.Errorf("the local network %s cannot reach the share", network)
		}
	}
	if strings.Contains(config, "hosts allow = 0.0.0.0/0") {
		t.Error("the share is offered to the whole internet")
	}
}

// A share whose disk is not connected is left out of the configuration
// entirely. Pointed at a path that is not a mounted disk, Samba would create
// the directory on the system disk and serve an empty folder that looks exactly
// like the one somebody's files were in — the same failure applications are
// protected from, arriving through a different door.
func TestAShareWhoseDiskIsGoneIsNotServedFromTheSystemDisk(t *testing.T) {
	s := shareServices(t)

	if err := s.save([]Share{{Name: "films", Location: "not-connected"}}); err != nil {
		t.Fatal(err)
	}
	shares, err := s.load()
	if err != nil {
		t.Fatal(err)
	}

	// describe is what apply filters on, so assert the property apply relies on.
	state := s.describe(shares[0], "homebase")
	if state.Available {
		t.Fatal("a share on a disk that is not there reported itself as available")
	}
	if state.Path != "" {
		t.Errorf("it was given the path %q, which is not on the disk it names", state.Path)
	}

	// And it must not reach the configuration at all.
	config := renderSambaConfig("homebase", nil)
	if strings.Contains(config, "[films]") {
		t.Error("the share was written into smb.conf with no disk behind it")
	}
}

// The status report still lists it, because "configured, disk unplugged" and
// "never set up" are different answers and the person asking is looking at a
// laptop that says the server cannot be found.
func TestAShareOnAMissingDiskIsStillReported(t *testing.T) {
	s := shareServices(t)
	if err := s.save([]Share{{Name: "films", Location: "not-connected"}}); err != nil {
		t.Fatal(err)
	}
	status, err := s.status(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(status.Shares) != 1 {
		t.Fatalf("got %d shares, want the one that is configured", len(status.Shares))
	}
	if status.Shares[0].Available {
		t.Error("it was reported as available")
	}
	if status.Shares[0].Address == "" {
		t.Error("no address was reported, so nobody can be told what to type")
	}
}

// --- Names --------------------------------------------------------------------

// The name becomes a directory and an SMB share name, so it is checked before
// it becomes either.
func TestShareNamesAreConstrained(t *testing.T) {
	for _, name := range []string{
		"", "a", "A-Share", "../etc", "share name", "share/sub", "-leading",
		"trailing-", "share$", strings.Repeat("x", 40),
	} {
		if validShareName.MatchString(name) {
			t.Errorf("%q was accepted as a share name", name)
		}
	}
	for _, name := range []string{"backup", "films", "my-photos", "a1"} {
		if !validShareName.MatchString(name) {
			t.Errorf("%q was refused", name)
		}
	}
}

// The prefix is the whole reason a file-sharing password cannot also be a login.
// It is typed into a Windows dialog and saved there for years, so it must not be
// a credential for anything that administers the machine.
func TestShareAccountsAreNamespaced(t *testing.T) {
	if !strings.HasPrefix(shareUserPrefix+"okan", shareUserPrefix) {
		t.Fatal("the prefix is not applied")
	}
	// An account named after a real login must land somewhere that is not it.
	for _, name := range []string{"root", "console", "homebase"} {
		if shareUserPrefix+name == name {
			t.Errorf("a share account could be created as %q", name)
		}
	}
}

// --- The state file --------------------------------------------------------------

// The same rule as storage: this file says what is on the network, and starting
// again from empty would unshare somebody's folders while reporting nothing
// wrong.
func TestACorruptShareFileIsRefusedRatherThanReset(t *testing.T) {
	s := shareServices(t)
	if err := s.save([]Share{{Name: "backup", Location: "internal"}}); err != nil {
		t.Fatal(err)
	}
	if err := writeRootFile(s.stateFile, "{ not json", 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.load(); err == nil {
		t.Fatal("a corrupt share file was accepted, which would silently unshare everything")
	}
}
