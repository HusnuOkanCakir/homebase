package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/HusnuOkanCakir/homebase/internal/auth"
	"github.com/HusnuOkanCakir/homebase/internal/store"
)

// ADR-0015. This tool is used by somebody who is locked out and standing at the
// machine, so the thing worth testing is that what it prints actually gets them
// back in — not that it printed something.

const password = "a-sufficiently-long-password"

var codePattern = regexp.MustCompile(`[0-9A-HJ-KM-NP-TV-Z]{5}(-[0-9A-HJ-KM-NP-TV-Z]{5}){4}`)

func testDatabase(t *testing.T, accounts ...string) (path string, service *auth.Service) {
	t.Helper()

	path = t.TempDir() + "/homebase.db"
	db, err := store.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	service = auth.NewService(db.DB())
	for i, name := range accounts {
		if i == 0 {
			if _, err := service.CreateAdministrator(context.Background(), name, password); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if _, err := service.CreateUser(context.Background(), name, password, nil); err != nil {
			t.Fatal(err)
		}
	}
	return path, service
}

func TestRecoveryCodeFromTheConsoleActuallyWorks(t *testing.T) {
	path, service := testDatabase(t, "alex")

	var out, errOut bytes.Buffer
	if err := run([]string{"recovery-code", "--database", path}, &out, &errOut); err != nil {
		t.Fatalf("recovery-code: %v\n%s", err, errOut.String())
	}

	code := codePattern.FindString(out.String())
	if code == "" {
		t.Fatalf("no recovery code in the output:\n%s", out.String())
	}

	// The whole point: this code, typed into the browser, opens the server.
	if _, _, err := service.ResetPasswordWithCode(
		context.Background(), "alex", code, "a-brand-new-password"); err != nil {
		t.Fatalf("the code printed at the console did not work: %v", err)
	}

	// And the instructions have to say where to use it, because somebody
	// holding a code and no idea what to do with it is still locked out.
	if !strings.Contains(out.String(), "forgotten my password") {
		t.Error("the output does not say where to use the code")
	}
	if !strings.Contains(out.String(), "shown once") {
		t.Error("the output does not warn that the code is not shown again")
	}
}

// Issuing a new code must invalidate the old one, including when the old one
// came from setup rather than from here.
func TestConsoleCodeReplacesThePreviousOne(t *testing.T) {
	path, service := testDatabase(t, "alex")

	first, err := service.IssueRecoveryCode(context.Background(), mustUser(t, service, "alex"))
	if err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if err := run([]string{"recovery-code", "--database", path}, &out, &out); err != nil {
		t.Fatal(err)
	}

	if _, _, err := service.ResetPasswordWithCode(
		context.Background(), "alex", first, "a-brand-new-password"); err == nil {
		t.Error("the previous code still works after a new one was issued")
	}
}

// With more than one account it must refuse rather than guess: a wasted trip to
// the machine is the cost of picking wrong.
func TestSeveralAccountsRequireANameAndSayWhichExist(t *testing.T) {
	path, _ := testDatabase(t, "alex", "guest")

	var out bytes.Buffer
	err := run([]string{"recovery-code", "--database", path}, &out, &out)
	if err == nil {
		t.Fatal("with two accounts the tool picked one on its own")
	}
	if !strings.Contains(err.Error(), "alex") || !strings.Contains(err.Error(), "guest") {
		t.Errorf("the refusal does not say which accounts there are: %v", err)
	}
	if codePattern.FindString(out.String()) != "" {
		t.Error("a code was printed despite the refusal")
	}

	// Named, it works.
	out.Reset()
	if err := run([]string{"recovery-code", "--database", path, "--user", "guest"}, &out, &out); err != nil {
		t.Fatalf("naming the account: %v", err)
	}
	if codePattern.FindString(out.String()) == "" {
		t.Error("no code printed for the named account")
	}
}

func TestUnknownAccountIsExplained(t *testing.T) {
	path, _ := testDatabase(t, "alex")

	var out bytes.Buffer
	err := run([]string{"recovery-code", "--database", path, "--user", "nobody"}, &out, &out)
	if err == nil {
		t.Fatal("an account that does not exist was accepted")
	}
	if !strings.Contains(err.Error(), "list-accounts") {
		t.Errorf("the error does not say how to find the real names: %v", err)
	}
}

func TestListAccounts(t *testing.T) {
	path, _ := testDatabase(t, "alex", "guest")

	var out bytes.Buffer
	if err := run([]string{"list-accounts", "--database", path}, &out, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "alex") || !strings.Contains(out.String(), "guest") {
		t.Errorf("accounts missing from the listing:\n%s", out.String())
	}
}

// A missing database is the likeliest mistake — somebody running this on the
// wrong machine, or with Homebase installed somewhere else.
func TestAMissingDatabaseSaysWhatToDo(t *testing.T) {
	var out bytes.Buffer
	err := run([]string{"recovery-code", "--database", t.TempDir() + "/absent.db"}, &out, &out)
	if err == nil {
		t.Fatal("a missing database was not reported")
	}
	if !strings.Contains(err.Error(), "--database") {
		t.Errorf("the error does not say how to point at the right one: %v", err)
	}
}

func TestUnknownCommand(t *testing.T) {
	var out, errOut bytes.Buffer
	if err := run([]string{"delete-everything"}, &out, &errOut); err == nil {
		t.Fatal("an unknown command was accepted")
	}
	if !strings.Contains(errOut.String(), "recovery-code") {
		t.Error("an unknown command does not show what the real ones are")
	}
}

func mustUser(t *testing.T, service *auth.Service, name string) string {
	t.Helper()
	user, err := service.UserByName(context.Background(), name)
	if err != nil {
		t.Fatal(err)
	}
	return user.ID
}

// `homebasectl apps --json` means "list the applications, as JSON".
//
// The dispatcher used to read `--json` as a subcommand name and refuse it —
// which is both wrong and the first thing anybody types. Caught in the VM on the
// first run, so it is pinned here where it costs nothing to check.
func TestALeadingFlagIsNotASubcommand(t *testing.T) {
	for _, args := range [][]string{
		{"--json"},
		{"-json"},
		{"--address", "https://127.0.0.1:9"},
	} {
		action, rest := defaultTo(args, "list")
		if action != "list" {
			t.Errorf("%v chose subcommand %q, want the default", args, action)
		}
		if len(rest) != len(args) {
			t.Errorf("%v had an argument eaten: %v", args, rest)
		}
	}
}

func TestASubcommandIsStillASubcommand(t *testing.T) {
	action, rest := defaultTo([]string{"install", "jellyfin"}, "list")
	if action != "install" {
		t.Errorf("action = %q, want install", action)
	}
	if len(rest) != 1 || rest[0] != "jellyfin" {
		t.Errorf("rest = %v, want [jellyfin]", rest)
	}

	if action, _ := defaultTo(nil, "status"); action != "status" {
		t.Errorf("no arguments chose %q, want the default", action)
	}
}

// Exit codes a script can branch on. "Used wrongly" must not look like "failed".
func TestUsageErrorsAreDistinguishable(t *testing.T) {
	err := run([]string{"nonsense"}, io.Discard, io.Discard)
	if err == nil {
		t.Fatal("an unknown command was accepted")
	}
	var usage usageError
	if !errors.As(err, &usage) {
		t.Errorf("an unknown command produced %T, which exits 1 rather than 2", err)
	}
}

// Shared flags work before the subcommand as well as after it.
//
// The help has always listed --address, --database and --json under "Options",
// as though they were global. They were not: a flag before the subcommand was
// read as an unknown command and exited 2. The help was right and the parser was
// wrong, which is the worse way round — a documented flag that is refused is
// worse than one that does not exist.
func TestSharedFlagsWorkBeforeTheSubcommand(t *testing.T) {
	cases := []struct {
		args      []string
		remaining string
		address   string
		asJSON    bool
	}{
		{[]string{"--json", "system"}, "system", defaultAddress, true},
		{[]string{"--address", "https://other", "system"}, "system", "https://other", false},
		{[]string{"--address=https://other", "apps"}, "apps", "https://other", false},
		{[]string{"--json", "--address", "https://other", "apps"}, "apps", "https://other", true},
		{[]string{"system", "--json"}, "system", defaultAddress, false},
	}

	for _, c := range cases {
		globals = options{address: defaultAddress, database: defaultDatabase}
		rest, err := takeGlobalFlags(c.args)
		if err != nil {
			t.Errorf("%v: %v", c.args, err)
			continue
		}
		if len(rest) == 0 || rest[0] != c.remaining {
			t.Errorf("%v left %v, want it to start with %q", c.args, rest, c.remaining)
		}
		if globals.address != c.address {
			t.Errorf("%v gave address %q, want %q", c.args, globals.address, c.address)
		}
		if globals.asJSON != c.asJSON {
			t.Errorf("%v gave json=%v, want %v", c.args, globals.asJSON, c.asJSON)
		}
	}
	globals = options{address: defaultAddress, database: defaultDatabase}
}

// A subcommand is never mistaken for a flag, however it is spelled.
func TestSubcommandsSurviveTheGlobalFlagScan(t *testing.T) {
	globals = options{address: defaultAddress, database: defaultDatabase}
	defer func() { globals = options{address: defaultAddress, database: defaultDatabase} }()

	for _, args := range [][]string{
		{"system"}, {"apps", "install", "jellyfin"}, {"backup", "now", "disk"},
	} {
		rest, err := takeGlobalFlags(args)
		if err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		if len(rest) != len(args) || rest[0] != args[0] {
			t.Errorf("%v was altered: %v", args, rest)
		}
	}
}

// A flag with no value is the command being used wrongly, not a failure.
func TestAFlagWithNoValueIsAUsageError(t *testing.T) {
	globals = options{address: defaultAddress, database: defaultDatabase}
	defer func() { globals = options{address: defaultAddress, database: defaultDatabase} }()

	_, err := takeGlobalFlags([]string{"--address"})
	var usage usageError
	if err == nil || !errors.As(err, &usage) {
		t.Errorf("a flag missing its value gave %v, which does not exit 2", err)
	}
}

// A magic packet is six bytes of 0xFF followed by the target's hardware address
// sixteen times. That is the entire format, which is why it is written here
// rather than pulled in.
func TestTheMagicPacketIsTheRightShape(t *testing.T) {
	hardware, err := net.ParseMAC("AA:BB:CC:DD:EE:FF")
	if err != nil {
		t.Fatal(err)
	}

	packet := make([]byte, 0, 102)
	for range 6 {
		packet = append(packet, 0xFF)
	}
	for range 16 {
		packet = append(packet, hardware...)
	}

	if len(packet) != 102 {
		t.Fatalf("packet is %d bytes, want 102", len(packet))
	}
	for i := range 6 {
		if packet[i] != 0xFF {
			t.Errorf("byte %d is %#x, want 0xFF", i, packet[i])
		}
	}
	// The address, sixteen times, starting right after the header.
	for repeat := range 16 {
		start := 6 + repeat*6
		if !bytes.Equal(packet[start:start+6], hardware) {
			t.Fatalf("repetition %d is not the address", repeat)
		}
	}
}

// A hardware address is checked before anything is sent. Not for safety — a
// broadcast of the wrong bytes wakes nothing — but because "that is not an
// address" is a better answer than silence from a packet nobody acknowledges.
func TestWakeRefusesThingsThatAreNotAddresses(t *testing.T) {
	for _, address := range []string{
		"", "not-an-address", "AA:BB:CC:DD:EE", "AA:BB:CC:DD:EE:FF:00",
		"GG:BB:CC:DD:EE:FF", "192.168.1.1", "AA-BB-CC-DD-EE",
	} {
		if macAddress.MatchString(address) {
			t.Errorf("%q was accepted as a hardware address", address)
		}
	}

	for _, address := range []string{
		"AA:BB:CC:DD:EE:FF", "aa:bb:cc:dd:ee:ff", "AA-BB-CC-DD-EE-FF", "00:1a:2b:3c:4d:5e",
	} {
		if !macAddress.MatchString(address) {
			t.Errorf("%q was refused, and is a hardware address", address)
		}
	}
}

// The address is shown back the way somebody would type it in.
func TestHardwareAddressesAreShownConsistently(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"aa:bb:cc:dd:ee:ff", "AA:BB:CC:DD:EE:FF"},
		{"AA-BB-CC-DD-EE-FF", "AA:BB:CC:DD:EE:FF"},
		{"00:1a:2b:3c:4d:5e", "00:1A:2B:3C:4D:5E"},
	} {
		if got := normaliseMAC(c.in); got != c.want {
			t.Errorf("normaliseMAC(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// `--json` prints what the server said, not a struct this package decoded into.
//
// Those differ whenever core knows a field homebasectl does not — which is every
// time the API gains one — and the difference is silent: the field is simply
// absent from the output, and a script relying on it breaks with nothing to
// read. Found when `vpn status --json` stopped reporting dynamic DNS state that
// the server was returning perfectly well.
func TestJSONOutputIsTheServersAnswerNotOurStruct(t *testing.T) {
	// A body with a field no struct in this package has.
	c := &Client{lastBody: []byte(
		`{"known":"value","something_new":{"the_cli":"has never heard of this"}}`)}

	type onlyKnown struct {
		Known string `json:"known"`
	}

	var out bytes.Buffer
	if err := printResponse(&out, c, onlyKnown{Known: "value"}); err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(out.String(), "something_new") {
		t.Errorf("a field the CLI does not know was dropped:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "has never heard of this") {
		t.Errorf("its contents were dropped too:\n%s", out.String())
	}
}

// With nothing from the server — a command that made no request — the value is
// printed instead, rather than nothing at all.
func TestJSONOutputFallsBackWhenThereWasNoRequest(t *testing.T) {
	var out bytes.Buffer
	if err := printResponse(&out, &Client{}, map[string]string{"local": "value"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "local") {
		t.Errorf("nothing was printed:\n%s", out.String())
	}
}

// The confirmation for something irreversible is the thing's own name.
//
// Never a word like "yes": a confirmation that is the same on every machine is
// one that can be typed without looking, and one that a script can carry from a
// safe context into a dangerous one.
func TestScriptedConfirmationHasToNameTheThing(t *testing.T) {
	var out bytes.Buffer

	for _, given := range []string{"yes", "y", "YES", "true", "confirm", "backup", ""} {
		if given == "" {
			continue // empty means "ask me", tested separately
		}
		agreed, err := confirmDestruction(&out, "2026-08-09-120000-abcdef01",
			"this destroys things", given)
		if agreed {
			t.Errorf("%q was accepted as confirmation", given)
		}
		var usage usageError
		if err == nil || !errors.As(err, &usage) {
			t.Errorf("%q gave %v, which does not exit 2", given, err)
		}
	}

	agreed, err := confirmDestruction(&out, "2026-08-09-120000-abcdef01",
		"this destroys things", "2026-08-09-120000-abcdef01")
	if err != nil {
		t.Fatal(err)
	}
	if !agreed {
		t.Error("the correct value was refused")
	}
}

// Nothing irreversible happens with nobody there to agree to it.
//
// There are two ways for that to be true and both have to be, because which one
// applies depends on how the command was invoked. With stdin on a pipe there is
// no terminal at all and it refuses outright. With stdin on /dev/null — which is
// a character device, and is what `go test` and many scripts provide — it
// prompts, reads nothing, and stops. Neither agrees.
func TestNothingIrreversibleHappensWithNobodyThere(t *testing.T) {
	var out bytes.Buffer

	// Stdin as /dev/null: a character device, so it prompts and gets EOF.
	agreed, err := confirmDestruction(&out, "the-server", "this destroys things", "")
	if agreed {
		t.Error("agreed to something irreversible after reading nothing")
	}
	_ = err

	// Stdin as a pipe: not a terminal, so it says so and says what to pass.
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reader.Close(); _ = writer.Close() }()

	original := os.Stdin
	os.Stdin = reader
	defer func() { os.Stdin = original }()

	agreed, err = confirmDestruction(&out, "the-server", "this destroys things", "")
	if agreed {
		t.Fatal("agreed to something irreversible with no terminal to agree on")
	}

	var usage usageError
	if err == nil || !errors.As(err, &usage) {
		t.Fatalf("gave %v, which does not exit 2", err)
	}
	if !strings.Contains(err.Error(), "--confirm the-server") {
		t.Errorf("it does not say what to pass instead: %v", err)
	}
	// And it says why there is no --yes, because somebody will look for one.
	if !strings.Contains(err.Error(), "no --yes") {
		t.Errorf("it does not explain the absence of --yes: %v", err)
	}
}

// The listing's size floor must not refuse a stick the writer would accept.
//
// It did: the floor was 4 GB, picked by guessing, and an ordinary 4 GB stick
// holds about 3.88 GB — so `devices` said REFUSED about hardware that had 450 MB
// to spare. The writer computes the real requirement from the ISO it is given;
// this floor exists only so a 512 MB stick is not offered as a candidate.
func TestTheListingDoesNotRefuseMediaThatWouldWork(t *testing.T) {
	// Ubuntu 24.04.4 server, plus the seed carrying Homebase's packages.
	const ubuntuISO = 3_405_469_696
	const seed = 23_599_104
	const slack = 4 * 1024 * 1024
	const actuallyNeeded = ubuntuISO + seed + slack

	// The floor must never be above the requirement, or there is a band of
	// media the listing refuses and the writer would have accepted. This caught
	// the second version of this bug as well as the first.
	if smallestUsableMedia > actuallyNeeded {
		t.Errorf("the floor is %d bytes and the media needs %d — the listing "+
			"refuses sticks the writer would accept",
			smallestUsableMedia, actuallyNeeded)
	}

	// A nominal "4 GB" stick, which is what this failed on.
	const nominalFourGB = 3_879_731_200
	if nominalFourGB < smallestUsableMedia {
		t.Errorf("a 4 GB stick (%d bytes) is refused by a floor of %d",
			nominalFourGB, smallestUsableMedia)
	}
	if nominalFourGB < actuallyNeeded {
		t.Errorf("a 4 GB stick does not in fact hold the image (%d < %d)",
			nominalFourGB, actuallyNeeded)
	}

	// And the floor still has to do its job: something far too small is refused.
	const tinyStick = 512_000_000
	if tinyStick >= smallestUsableMedia {
		t.Error("the floor is so low it offers a 512 MB stick as a candidate")
	}
}
