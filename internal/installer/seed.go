// Package installer builds the autoinstall seed that travels on Homebase's
// installation media.
//
// ADR-0016: the official Ubuntu Server ISO is written unmodified, and this is
// what goes beside it — a volume labelled CIDATA holding the autoinstall
// configuration and Homebase's own packages, so a machine with no working
// network still ends up with a server on it.
//
// The template is embedded rather than read from disk. A `homebasectl` that
// needs a checkout next to it to make media is one that only works on the
// machine it was built on.
package installer

import (
	_ "embed"
	"fmt"
	"regexp"
	"strings"
)

//go:embed user-data.yaml.in
var template string

// SeedLabel is the volume label Ubuntu's NoCloud datasource matches on. Not
// configurable: the installer looks for exactly this.
const SeedLabel = "CIDATA"

// UbuntuRelease is the Ubuntu Server LTS the media is built against. Pinned,
// because a point release can change the installer's behaviour underneath us.
const UbuntuRelease = "24.04"

// Values are the decisions a particular stick is built with.
type Values struct {
	// Hostname is what the server calls itself, and what somebody types into a
	// browser to reach it once mDNS lands in Milestone 7.
	Hostname string

	Locale   string
	Keyboard string

	// AuthorizedKeys install SSH access. Empty means none at all — which is the
	// right default for an appliance managed entirely from a browser, and the
	// wrong one for a test that has to look inside the machine afterwards.
	AuthorizedKeys []string

	// Version is stamped into /etc/homebase-installed, so a machine can say
	// which media produced it.
	Version string
}

// Render fills the template in, or says which value was missing.
func Render(values Values) (string, error) {
	if strings.TrimSpace(values.Hostname) == "" {
		return "", fmt.Errorf("the server needs a name")
	}
	if !validHostname.MatchString(values.Hostname) {
		return "", fmt.Errorf(
			"%q cannot be a machine's name: letters, digits and hyphens only, "+
				"and it may not start or end with a hyphen", values.Hostname)
	}
	if strings.TrimSpace(values.Locale) == "" {
		values.Locale = "en_GB.UTF-8"
	}
	if strings.TrimSpace(values.Keyboard) == "" {
		values.Keyboard = "gb"
	}
	if strings.TrimSpace(values.Version) == "" {
		values.Version = "unknown"
	}

	for _, key := range values.AuthorizedKeys {
		if err := checkAuthorizedKey(key); err != nil {
			return "", err
		}
	}

	// SSH is installed only when there is a key to install. A server with sshd
	// running and no way to authenticate to it is a listening port that exists
	// for nobody's benefit.
	installSSH := "false"
	sshFirewall := "true"
	if len(values.AuthorizedKeys) > 0 {
		installSSH = "true"
		sshFirewall = "ufw allow 22/tcp comment 'SSH'"
	}

	replacements := map[string]string{
		"@HOSTNAME@":        values.Hostname,
		"@LOCALE@":          values.Locale,
		"@KEYBOARD@":        values.Keyboard,
		"@INSTALL_SSH@":     installSSH,
		"@AUTHORIZED_KEYS@": yamlList(values.AuthorizedKeys),
		"@SSH_FIREWALL@":    sshFirewall,
		"@SEED_LABEL@":      SeedLabel,
		"@VERSION@":         values.Version,
		"@UBUNTU_RELEASE@":  UbuntuRelease,
	}

	rendered := template
	for placeholder, value := range replacements {
		rendered = strings.ReplaceAll(rendered, placeholder, value)
	}

	// A placeholder that survives means the template gained a field nobody
	// filled in — which would reach a user's disk as a literal @WORD@ in the
	// autoinstall configuration and fail in a way nothing here would explain.
	if leftover := remainingPlaceholder.FindString(rendered); leftover != "" {
		return "", fmt.Errorf("the seed template has an unfilled placeholder: %s", leftover)
	}

	return rendered, nil
}

var (
	validHostname = regexp.MustCompile(`^[a-zA-Z0-9]([a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?$`)
	// Matches @WORD@ but not an email address, which the template's prose has.
	remainingPlaceholder = regexp.MustCompile(`@[A-Z][A-Z0-9_]*@`)
)

// checkAuthorizedKey refuses anything that is not plausibly an SSH public key.
//
// A private key pasted here by mistake would be written to a volume that gets
// carried around and handed to other people, so the check is worth its weight.
func checkAuthorizedKey(key string) error {
	trimmed := strings.TrimSpace(key)
	if trimmed == "" {
		return fmt.Errorf("an empty SSH key was given")
	}
	if strings.Contains(trimmed, "PRIVATE KEY") {
		return fmt.Errorf(
			"that looks like a private key, not a public one.\n" +
				"The media is written to a stick that gets carried around — " +
				"give it the .pub file instead")
	}
	if strings.ContainsAny(trimmed, "\n\r") {
		return fmt.Errorf("an SSH key must be a single line")
	}
	for _, prefix := range []string{"ssh-ed25519 ", "ssh-rsa ", "ecdsa-sha2-", "sk-ssh-", "sk-ecdsa-"} {
		if strings.HasPrefix(trimmed, prefix) {
			return nil
		}
	}
	return fmt.Errorf("%q does not look like an SSH public key", firstWord(trimmed))
}

func firstWord(s string) string {
	if index := strings.IndexByte(s, ' '); index > 0 {
		return s[:index]
	}
	return s
}

// yamlList renders a YAML sequence inline, or an empty one.
func yamlList(items []string) string {
	if len(items) == 0 {
		return "[]"
	}
	var out strings.Builder
	for _, item := range items {
		out.WriteString("\n      - ")
		out.WriteString(strings.TrimSpace(item))
	}
	return out.String()
}
