package installer

import (
	"strings"
	"testing"
)

// ADR-0016. This file produces the configuration that erases somebody's disk,
// so most of what is checked is what must never end up in it.

func TestRenderProducesTheDecisionsThatMatter(t *testing.T) {
	rendered, err := Render(Values{
		Hostname:       "homebase",
		AuthorizedKeys: []string{"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIExample somebody@somewhere"},
		Version:        "1.2.3",
	})
	if err != nil {
		t.Fatalf("rendering: %v", err)
	}

	// That the result is valid YAML is checked by `make check`, which has a
	// parser, and settled definitively by the installer test, which feeds it to
	// Ubuntu. What is checked here is the handful of decisions that would still
	// parse perfectly while being wrong.
	required := []struct {
		text string
		why  string
	}{
		{"interactive-sections: []",
			"a seed that stops to ask leaves a machine sitting on a question nobody is watching for"},
		{"hostname: homebase", "the name did not reach the seed"},
		{"username: console",
			"the login user must not be called 'homebase': that is the system account the " +
				"packages create, and the collision leaves core running as a user with a shell"},
		{`password: "!"`, "the console account must have no usable password"},
		{"allow-pw: false", "SSH password authentication must stay off"},
		{"layout:\n      name: direct", "the whole disk is what gets installed onto"},
		{"shutdown: reboot", "the machine must come up on its own afterwards"},
	}

	for _, want := range required {
		if !strings.Contains(rendered, want.text) {
			t.Errorf("the seed does not contain %q\n    %s", want.text, want.why)
		}
	}
}

// The seed must never name a device. A seed that says /dev/sda is right on the
// machine it was written for and wrong on the next one, and being wrong here
// destroys a disk rather than failing an install.
func TestSeedNamesNoDevice(t *testing.T) {
	rendered, err := Render(Values{Hostname: "homebase"})
	if err != nil {
		t.Fatal(err)
	}

	// Comments are stripped first. The template explains *why* it does not name
	// a device, and the explanation mentions one — checking the prose would
	// forbid saying so.
	config := withoutComments(rendered)

	for _, device := range []string{"/dev/sda", "/dev/nvme0n1", "/dev/vda", "/dev/hda"} {
		if strings.Contains(config, device) {
			t.Errorf("the seed names %s. Which disk gets erased must be decided on "+
				"the machine, not written into media that gets carried around", device)
		}
	}
}

// withoutComments returns only the lines that configure something.
func withoutComments(rendered string) string {
	var kept []string
	for _, line := range strings.Split(rendered, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		kept = append(kept, line)
	}
	return strings.Join(kept, "\n")
}

// The console account is only useful if it can become root: the whole reason it
// exists is `sudo homebasectl recovery-code`, run by somebody who has forgotten
// everything else. The installer test found this the hard way.
func TestConsoleAccountCanBecomeRoot(t *testing.T) {
	rendered, err := Render(Values{Hostname: "homebase"})
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(rendered, "NOPASSWD") {
		t.Error("the console account cannot run sudo. Its password is locked, so " +
			"without this it cannot run the recovery path it exists for (ADR-0015)")
	}
	if !strings.Contains(rendered, "visudo -cf") {
		t.Error("the sudoers file is not checked before the machine reboots. " +
			"A malformed one locks everybody out of root permanently")
	}
}

// The seed opens the firewall for the dashboard. That the dashboard is
// *listening* is the packaged unit's business, not the seed's — the two used to
// disagree, and a hole for a port nothing listened on is how that showed up.
func TestTheFirewallLetsTheDashboardThrough(t *testing.T) {
	rendered, err := Render(Values{Hostname: "homebase"})
	if err != nil {
		t.Fatal(err)
	}
	config := withoutComments(rendered)

	// The ordinary ports, because the address on the machine's own screen is a
	// name with no number after it.
	if !strings.Contains(config, "ufw allow 443/tcp") {
		t.Error("the firewall does not let the dashboard through")
	}
	if !strings.Contains(config, "ufw allow 80/tcp") {
		t.Error("plain HTTP is blocked, so somebody typing the name without " +
			"https:// reaches nothing rather than being redirected")
	}
	// Without this the server does not answer to its own name, and the address
	// printed on its screen is the only way to reach it.
	if !strings.Contains(config, "ufw allow 5353/udp") {
		t.Error("mDNS is blocked, so <hostname>.local resolves nowhere")
	}
	if !strings.Contains(config, "ufw default deny incoming") {
		t.Error("the firewall does not deny everything else")
	}
}

// The machine's own screen is the only thing between somebody who has just
// installed Homebase and a machine they cannot reach. They cannot guess the
// address, and there is nowhere else to learn it.
func TestTheMachineSaysWhereToGo(t *testing.T) {
	rendered, err := Render(Values{Hostname: "homebase"})
	if err != nil {
		t.Fatal(err)
	}
	config := withoutComments(rendered)

	if !strings.Contains(config, "update-motd.d/99-homebase") {
		t.Fatal("nothing is shown on the machine's screen, so somebody who has " +
			"just installed it has no way of finding the dashboard")
	}
	if !strings.Contains(config, "ip -4 -brief address") {
		t.Error("the address is not worked out when the message is shown. " +
			"A screen confidently showing the wrong address is worse than one " +
			"showing none")
	}
	if !strings.Contains(config, "not on a network yet") {
		t.Error("a machine with no network says nothing, leaving somebody " +
			"staring at a screen with no address and no explanation")
	}
}

func TestSSHIsInstalledOnlyWhenThereIsAKey(t *testing.T) {
	without, err := Render(Values{Hostname: "homebase"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(without, "install-server: true") {
		t.Error("sshd is installed with no key to authenticate against: a " +
			"listening port that exists for nobody's benefit")
	}

	with, err := Render(Values{
		Hostname:       "homebase",
		AuthorizedKeys: []string{"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIExample a@b"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(with, "install-server: true") {
		t.Error("a key was given but sshd is not installed")
	}
	if !strings.Contains(with, "ufw allow 22/tcp") {
		t.Error("sshd is installed but the firewall does not let it through")
	}
}

func TestRenderRefusesWhatItShould(t *testing.T) {
	cases := []struct {
		name   string
		values Values
		want   string
	}{
		{"no hostname", Values{}, "needs a name"},
		{"a hostname with a space", Values{Hostname: "my server"}, "cannot be a machine's name"},
		{"a hostname starting with a hyphen", Values{Hostname: "-server"}, "cannot be a machine's name"},
		{
			"a private key pasted by mistake",
			Values{Hostname: "homebase", AuthorizedKeys: []string{
				"-----BEGIN OPENSSH PRIVATE KEY-----\nabc\n-----END OPENSSH PRIVATE KEY-----"}},
			"private key",
		},
		{
			"something that is not a key at all",
			Values{Hostname: "homebase", AuthorizedKeys: []string{"hunter2"}},
			"does not look like an SSH public key",
		},
		{
			"an empty key",
			Values{Hostname: "homebase", AuthorizedKeys: []string{"   "}},
			"empty SSH key",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Render(tc.values)
			if err == nil {
				t.Fatalf("accepted %+v", tc.values)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error was %q, wanted something about %q", err, tc.want)
			}
		})
	}
}

// A placeholder that survives rendering would reach a user's disk as a literal
// @WORD@ inside the autoinstall configuration, and fail in a way nothing would
// explain. The template's own prose caught this once already.
func TestNoPlaceholderSurvives(t *testing.T) {
	rendered, err := Render(Values{
		Hostname:       "homebase",
		AuthorizedKeys: []string{"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIExample a@b"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if match := remainingPlaceholder.FindString(rendered); match != "" {
		t.Errorf("unfilled placeholder %s survived rendering", match)
	}
}

// Homebase's packages must come off the media rather than the network, or a
// house with no working internet gets a machine with Ubuntu and nothing else.
func TestPackagesComeFromTheMedia(t *testing.T) {
	rendered, err := Render(Values{Hostname: "homebase"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(rendered, "blkid -L "+SeedLabel) {
		t.Error("the seed volume is never located, so the packages on it are never used")
	}
	if !strings.Contains(rendered, "dpkg -i") {
		t.Error("nothing installs Homebase's own packages")
	}
}

func TestTheMachineSaysWhereItCameFrom(t *testing.T) {
	rendered, err := Render(Values{Hostname: "homebase", Version: "9.9.9"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(rendered, "media_version=9.9.9") {
		t.Error("the version is not stamped onto the installed machine")
	}
	if !strings.Contains(rendered, "installed_by=homebase-installer") {
		t.Error("nothing marks the machine as having come from Homebase's installer")
	}
}
