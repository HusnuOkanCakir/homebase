package hostd

import (
	"os"
	"slices"
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
	}}, "")

	required := map[string]string{
		"security = user":               "a share anybody on the network can open",
		"map to guest = never":          "guest access, which is a folder with no password",
		"server min protocol = SMB2_10": "SMB1, the protocol with the wormable bugs",
		"client min protocol = SMB2_10": "SMB1 when talking outward",
		"hosts deny = 0.0.0.0/0":        "reachable from anywhere",
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

// `bind interfaces only` used to be what kept the applications off the file
// server, and it had to be turned off: smbd will not bind the Tailscale tunnel,
// so with it on, everybody away from home was refused by every folder.
//
// Turning it off moves the boundary rather than removing it. This test is here
// so that the move stays deliberate — if the firewall rule ever stops being
// scoped to interfaces, nine containers can read every shared folder and
// nothing else in the code will say so.
func TestTheApplicationsAreKeptOffTheFileServerByTheFirewall(t *testing.T) {
	config := renderSambaConfig("homebase", []ShareState{{
		Share: Share{Name: "backup", Location: "internal"},
		Path:  "/srv/homebase/storage/internal/shares/backup",
	}}, "")
	if !strings.Contains(config, "bind interfaces only = no") {
		t.Fatal("smbd is binding selected interfaces again; if that is deliberate, " +
			"check first that it binds the tunnel, because it did not before")
	}

	// 172.16.0.0/12 is where Docker puts its bridges. Trusting it as a source
	// range is the mistake this whole arrangement exists to avoid.
	for _, network := range privateNetworks {
		if network == "172.16.0.0/12" {
			t.Fatal("172.16.0.0/12 is trusted as a source, which admits every container")
		}
	}
	if strings.Contains(config, "hosts allow = 127.0.0.1 192.168.0.0/16 10.0.0.0/8 172.16.0.0/12") {
		t.Fatal("the container bridges are admitted by smbd itself")
	}

	// And the cards the port is opened on are the real ones, never a bridge.
	for _, name := range shareInterfaces() {
		if strings.HasPrefix(name, "docker") || strings.HasPrefix(name, "br-") {
			t.Fatalf("445 would be opened on %s, which is a container bridge", name)
		}
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
	config := renderSambaConfig("homebase", nil, "")
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
	if !strings.HasPrefix(shareUserPrefix+"alex", shareUserPrefix) {
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

// Somebody away from home could open the dashboard and not one shared folder.
// smbd binds the interfaces it is told about, and the tunnel was not among them.
func TestTheConfigurationAdmitsTheTailscaleTunnel(t *testing.T) {
	// The real render, with the tunnel present and absent, is decided by
	// tailnetPresent() reading /sys/class/net — so this asserts the two shapes
	// the renderer produces rather than reaching into the machine.
	allowed := sambaHostsAllow()
	if allowed[0] != "127.0.0.1" {
		t.Fatalf("the machine itself is not first in %v", allowed)
	}
	if tailnetPresent() && !slices.Contains(allowed, tailnetNetwork) {
		t.Fatalf("the tunnel is up and %v does not admit it", allowed)
	}

	// The range alone must never be the control. It is also what a carrier
	// hands a subscriber's router — this project's own server has such an
	// address — so the firewall rule that accompanies it is scoped to the
	// interface, and that is asserted where it is written.
	firewall, err := os.ReadFile("../../packaging/firewall")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(firewall), "allow in on $card") {
		t.Fatal("the firewall rule is not scoped to the interface; " +
			"a source range would admit the carrier's other subscribers")
	}
	if !strings.Contains(string(firewall), "interfaces) sources=\"\"") {
		t.Fatal("the interface scope no longer exists, so 445 is opened by address range")
	}
	if !strings.Contains(string(firewall), "PRIVILEGED_PORTS=\"445\"") {
		t.Fatal("445 is not in the privileged allowlist, so opening it is refused " +
			"and the failure is discarded")
	}
}

// The port the file server needs is below 1024, which the helper refuses by
// default. It was refused, silently, and the screen said sharing was on.
func TestTheFirewallHelperAcceptsTheFileSharingPort(t *testing.T) {
	script, err := os.ReadFile("../../packaging/firewall")
	if err != nil {
		t.Fatal(err)
	}
	body := string(script)
	// Still refuses the rest of the system's ports.
	if !strings.Contains(body, `[ "$port" -lt 1024 ] && [ "$privileged_ok" = no ]`) {
		t.Fatal("the allowlist replaced the restriction instead of narrowing it")
	}
}

// The tunnel is reported as an ordinary interface on a machine where it is up,
// so appending it unconditionally listed it twice — and smbd refuses a
// configuration that names an interface twice.
func TestTheTunnelIsNotListedTwice(t *testing.T) {
	seen := map[string]int{}
	for _, name := range localInterfaces() {
		seen[name]++
	}
	for name, count := range seen {
		if count > 1 {
			t.Fatalf("%s appears %d times in the interface list", name, count)
		}
	}
}

// --- Who may open a folder ------------------------------------------------------

// Every share was open to everybody before this field existed, and a share
// written by an older version has no field at all. It must not become
// restricted-to-nobody on upgrade.
func TestAShareWithNoAccessListIsOpenToEverybody(t *testing.T) {
	config := renderSambaConfig("homebase", []ShareState{{
		Share: Share{Name: "backup", Location: "internal"},
		Path:  "/srv/homebase/storage/internal/shares/backup",
	}}, "")
	if !strings.Contains(config, "valid users = @homebase") {
		t.Fatal("a share with no access list is not open to everybody, so an " +
			"upgrade would take away folders that worked yesterday")
	}
}

// A restricted folder names the file-sharing accounts, not the Homebase ones.
// They differ by a prefix, and getting it wrong is a folder that refuses
// everybody with no explanation.
func TestARestrictedShareNamesTheFileSharingAccounts(t *testing.T) {
	config := renderSambaConfig("homebase", []ShareState{{
		Share: Share{Name: "papers", Location: "internal", Access: []string{"alice", "bob"}},
		Path:  "/srv/homebase/storage/internal/shares/papers",
	}}, "")

	if !strings.Contains(config, "valid users = hbshare-alice hbshare-bob") {
		t.Fatalf("the access list is not written as file-sharing accounts:\n%s", config)
	}
	if strings.Contains(config, "valid users = @homebase") {
		t.Fatal("the restricted folder is also open to everybody")
	}
}

// A folder somebody cannot open should not be listed to them. Without this it
// appears in Explorer for the whole house, refuses on being clicked — which
// reads as a broken server — and tells everybody its name.
func TestAFolderSomebodyCannotOpenIsNotListedToThem(t *testing.T) {
	config := renderSambaConfig("homebase", []ShareState{{
		Share: Share{Name: "papers", Location: "internal", Access: []string{"alice"}},
		Path:  "/srv/homebase/storage/internal/shares/papers",
	}}, "")
	if !strings.Contains(config, "access based share enum = yes") {
		t.Fatal("restricted folders are listed to people who cannot open them")
	}
}
