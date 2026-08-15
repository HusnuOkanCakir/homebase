package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	installerpkg "github.com/HusnuOkanCakir/homebase/internal/installer"
)

// Making installation media.
//
// ADR-0016: the official Ubuntu Server ISO is written unmodified, and the
// autoinstall configuration travels beside it on a second volume labelled
// CIDATA, along with Homebase's own packages so that a machine with no working
// network still ends up with a server on it.
//
// This is the only part of Homebase that writes to a block device on somebody
// *else's* computer — the one they are using to make the stick, which is
// usually the computer with all their work on it. Every refusal here is
// deliberate, and none of them may be softened for convenience.

func installer(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		installerUsage(stderr)
		return errors.New("no installer command given")
	}

	switch args[0] {
	case "create":
		return installerCreate(args[1:], stdout, stderr, os.Stdin)
	case "seed":
		return installerSeed(args[1:], stdout)
	case "devices":
		return installerDevices(args[1:], stdout)
	case "-h", "--help", "help":
		installerUsage(stdout)
		return nil
	default:
		installerUsage(stderr)
		return fmt.Errorf("unknown installer command %q", args[0])
	}
}

func installerUsage(w io.Writer) {
	fmt.Fprint(w, `homebasectl installer — make Homebase installation media

  homebasectl installer create --iso PATH --packages DIR --device /dev/sdX
        Write installation media to a USB drive. Everything on the drive
        is erased, and you are asked to type its name first.
        Use --output FILE instead of --device to write an image.

  homebasectl installer seed --output PATH --packages DIR
        Build the autoinstall volume that travels beside the Ubuntu ISO.
        This is what tells the installer what to do, and it carries
        Homebase's packages so the machine needs no internet.

  homebasectl installer devices
        List the removable drives on this computer, with size and model.
        System disks are shown but marked as refused.

Options for seed:
  --output PATH        Where to write the seed volume
  --packages DIR       Directory holding the Homebase .deb packages
  --hostname NAME      What the server calls itself (default homebase)
  --authorized-key K   An SSH public key to install. Repeatable.
                       Without one, the server has no SSH access at all.
  --locale L           Default en_GB.UTF-8
  --keyboard L         Default gb
`)
}

func installerSeed(args []string, stdout io.Writer) error {
	flags := flag.NewFlagSet("installer seed", flag.ContinueOnError)
	output := flags.String("output", "", "where to write the seed volume")
	packages := flags.String("packages", "", "directory holding the .deb packages")
	hostname := flags.String("hostname", "homebase", "what the server calls itself")
	locale := flags.String("locale", "en_GB.UTF-8", "system locale")
	keyboard := flags.String("keyboard", "gb", "keyboard layout")

	var keys stringList
	flags.Var(&keys, "authorized-key", "an SSH public key to install (repeatable)")

	if err := flags.Parse(args); err != nil {
		return err
	}
	if *output == "" {
		return errors.New("--output is required: say where to write the seed volume")
	}
	if *packages == "" {
		return errors.New("--packages is required: the media carries Homebase's own packages")
	}

	seed, cleanup, err := buildSeedFile(seedRequest{
		packages: *packages,
		hostname: *hostname,
		locale:   *locale,
		keyboard: *keyboard,
		keys:     keys,
	})
	if err != nil {
		return err
	}
	defer cleanup()

	if err := copyFile(seed, *output); err != nil {
		return err
	}

	fmt.Fprintf(stdout, "Seed volume written to %s\n", *output)
	if len(keys) == 0 {
		fmt.Fprintln(stdout, "  no SSH keys: the server will be reachable only from a browser")
	}
	return nil
}

// seedRequest is everything a particular stick is built with.
type seedRequest struct {
	packages string
	hostname string
	locale   string
	keyboard string
	keys     []string
}

// buildSeedFile assembles the CIDATA volume and returns where it is.
//
// Shared by `installer seed` and `installer create` so there is one description
// of what goes on the media. Two would drift, and the way that drift shows up
// is a machine installed from a stick that was built differently from the one
// the tests use.
func buildSeedFile(req seedRequest) (string, func(), error) {
	nothing := func() {}

	debs, err := findPackages(req.packages)
	if err != nil {
		return "", nothing, err
	}

	rendered, err := installerpkg.Render(installerpkg.Values{
		Hostname:       req.hostname,
		Locale:         req.locale,
		Keyboard:       req.keyboard,
		AuthorizedKeys: req.keys,
		Version:        version,
	})
	if err != nil {
		return "", nothing, err
	}

	staging, err := os.MkdirTemp("", "homebase-seed-")
	if err != nil {
		return "", nothing, err
	}
	cleanup := func() { _ = os.RemoveAll(staging) }

	contents := filepath.Join(staging, "contents")
	if err := os.MkdirAll(filepath.Join(contents, "packages"), 0o755); err != nil {
		cleanup()
		return "", nothing, err
	}

	if err := os.WriteFile(filepath.Join(contents, "user-data"), []byte(rendered), 0o644); err != nil {
		cleanup()
		return "", nothing, err
	}
	meta := fmt.Sprintf("instance-id: homebase-installer\nlocal-hostname: %s\n", req.hostname)
	if err := os.WriteFile(filepath.Join(contents, "meta-data"), []byte(meta), 0o644); err != nil {
		cleanup()
		return "", nothing, err
	}

	for _, deb := range debs {
		target := filepath.Join(contents, "packages", filepath.Base(deb))
		if err := copyFile(deb, target); err != nil {
			cleanup()
			return "", nothing, err
		}
	}

	volume := filepath.Join(staging, "seed.img")
	if err := buildVolume(contents, volume); err != nil {
		cleanup()
		return "", nothing, err
	}
	return volume, cleanup, nil
}

// findPackages collects the .debs that will travel on the media.
//
// All four are required. A stick that installs three of them produces a machine
// that is broken in a way nobody would guess from looking at it — core without
// hostd is a server that cannot do anything, and hostd without the dashboard is
// a server with no interface.
func findPackages(directory string) ([]string, error) {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", directory, err)
	}

	found := map[string][]string{}
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".deb") {
			continue
		}
		for _, want := range requiredPackages {
			if strings.HasPrefix(name, want+"_") {
				found[want] = append(found[want], filepath.Join(directory, name))
			}
		}
	}

	var missing []string
	for _, want := range requiredPackages {
		if len(found[want]) == 0 {
			missing = append(missing, want)
		}
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf(
			"%s has no package for: %s\n"+
				"Media that installs some of Homebase produces a server that is broken in a way\n"+
				"nobody would guess from looking at it. Run `make packages` first.",
			directory, strings.Join(missing, ", "))
	}

	// A build directory accumulates versions. Guessing which one was meant is
	// how a stick ends up carrying last month's core and this month's hostd —
	// so this refuses and says exactly what to remove. Getting it wrong is
	// discovered on the machine being installed, hours later.
	var ambiguous []string
	var debs []string
	for _, want := range requiredPackages {
		matches := found[want]
		if len(matches) > 1 {
			sort.Strings(matches)
			ambiguous = append(ambiguous,
				fmt.Sprintf("  %s:\n    %s", want,
					strings.Join(baseNames(matches), "\n    ")))
			continue
		}
		debs = append(debs, matches[0])
	}
	if len(ambiguous) > 0 {
		return nil, fmt.Errorf(
			"%s holds more than one version of some packages:\n%s\n\n"+
				"Which one belongs on the media is not something to guess at. Clear the\n"+
				"directory and build once: rm -rf %s && make packages",
			directory, strings.Join(ambiguous, "\n"), directory)
	}

	sort.Strings(debs)
	return debs, nil
}

func baseNames(paths []string) []string {
	out := make([]string, len(paths))
	for i, path := range paths {
		out[i] = filepath.Base(path)
	}
	return out
}

var requiredPackages = []string{
	"homebase-hostd",
	"homebase-core",
	"homebase-apps",
	"homebase-dashboard",
}

// buildVolume writes a directory out as a volume labelled CIDATA.
//
// Shells out rather than implementing a filesystem: on Linux these tools are
// present or one `apt install` away, and the cross-platform path belongs with
// the graphical controller, which is where Windows and macOS start mattering.
func buildVolume(directory, output string) error {
	type builder struct {
		binary string
		args   []string
	}

	candidates := []builder{
		{"genisoimage", []string{
			"-output", output, "-volid", installerpkg.SeedLabel,
			"-joliet", "-rock", "-quiet", directory,
		}},
		{"xorriso", []string{
			"-as", "genisoimage", "-output", output, "-volid", installerpkg.SeedLabel,
			"-joliet", "-rock", "-quiet", directory,
		}},
		{"mkisofs", []string{
			"-output", output, "-volid", installerpkg.SeedLabel,
			"-joliet", "-rock", "-quiet", directory,
		}},
	}

	for _, candidate := range candidates {
		binary, err := exec.LookPath(candidate.binary)
		if err != nil {
			continue
		}
		command := exec.CommandContext(context.Background(), binary, candidate.args...)
		out, err := command.CombinedOutput()
		if err != nil {
			return fmt.Errorf("%s: %w\n%s", candidate.binary, err, strings.TrimSpace(string(out)))
		}
		return nil
	}

	return errors.New(
		"no tool available to build the seed volume.\n" +
			"    sudo apt install genisoimage")
}

func copyFile(source, destination string) error {
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()

	out, err := os.Create(destination)
	if err != nil {
		return err
	}
	defer func() { _ = out.Close() }()

	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Sync()
}

// --- Devices -----------------------------------------------------------------

// installerDevices lists what could be written to, and what must not be.
//
// The refusals are the point. Somebody making a Homebase stick is doing it on
// the computer that has all their work on it, and `--output /dev/sda` is one
// slip away from `--output /dev/sdb`.
func installerDevices(args []string, stdout io.Writer) error {
	flags := flag.NewFlagSet("installer devices", flag.ContinueOnError)
	if err := flags.Parse(args); err != nil {
		return err
	}

	devices, err := listBlockDevices()
	if err != nil {
		return err
	}
	if len(devices) == 0 {
		fmt.Fprintln(stdout, "No drives found.")
		return nil
	}

	for _, device := range devices {
		status := "can be written to"
		if reason := device.refusal(); reason != "" {
			status = "REFUSED — " + reason
		}
		fmt.Fprintf(stdout, "%-12s %8s  %-28s %s\n",
			device.Path, device.humanSize(), device.describe(), status)
	}
	return nil
}

type blockDevice struct {
	Name       string        `json:"name"`
	Path       string        `json:"path"`
	Size       int64         `json:"size"`
	Model      string        `json:"model"`
	Vendor     string        `json:"vendor"`
	Removable  bool          `json:"rm"`
	ReadOnly   bool          `json:"ro"`
	Type       string        `json:"type"`
	Transport  string        `json:"tran"`
	MountPoint string        `json:"mountpoint"`
	Children   []blockDevice `json:"children"`
}

// refusal says why this device may not be written to, or "" if it may.
func (d blockDevice) refusal() string {
	if d.Type != "disk" {
		return "not a whole disk"
	}
	if d.ReadOnly {
		return "read-only"
	}
	if d.holdsRunningSystem() {
		return "this computer is running from it"
	}
	if !d.Removable && d.Transport != "usb" {
		return "not a removable drive"
	}
	if d.Size < smallestUsableMedia {
		return fmt.Sprintf("too small — the image alone is about %d GB",
			smallestUsableMedia/1_000_000_000)
	}
	return ""
}

// smallestUsableMedia is a sanity floor for the listing. It is not the
// requirement, and it is deliberately far below it.
//
// The requirement is the size of the ISO being written plus the seed partition,
// and `create` computes exactly that from the file it was given. This listing
// cannot: it does not know which image is coming. So it filters out media that
// could not work with *any* Ubuntu image, and leaves the real judgement to the
// writer, which can state the exact numbers.
//
// The value has been wrong twice, both times by guessing high. It was 4 GB,
// which refused an ordinary 4 GB stick — those hold about 3.88 GB, and Ubuntu
// 24.04.4 with the seed needs 3.43 GB, so there were 450 MB spare. Then it was
// 3.5 GB, which is still above the requirement and would refuse anything between
// the two.
//
// A listing that refuses hardware the writer would accept is worse than one that
// is optimistic, because the writer explains itself precisely and the listing
// cannot. So this is now low enough that it cannot over-refuse, and its only job
// is to stop offering a 512 MB stick as a candidate.
const smallestUsableMedia = 2_000_000_000

// holdsRunningSystem reports whether anything on this disk is mounted.
//
// The check that matters most, and the one that is checked first: a disk
// carrying the running system is the disk somebody's work is on.
func (d blockDevice) holdsRunningSystem() bool {
	if d.MountPoint != "" {
		return true
	}
	for _, child := range d.Children {
		if child.MountPoint != "" || child.holdsRunningSystem() {
			return true
		}
	}
	return false
}

func (d blockDevice) describe() string {
	name := strings.TrimSpace(d.Vendor + " " + d.Model)
	if name == "" {
		name = "unknown drive"
	}
	return name
}

func (d blockDevice) humanSize() string {
	const unit = 1000
	if d.Size < unit {
		return fmt.Sprintf("%d B", d.Size)
	}
	value, exponent := float64(d.Size), 0
	for value >= unit && exponent < 4 {
		value /= unit
		exponent++
	}
	return fmt.Sprintf("%.0f %cB", value, "kMGT"[exponent-1])
}

func listBlockDevices() ([]blockDevice, error) {
	binary, err := exec.LookPath("lsblk")
	if err != nil {
		return nil, errors.New("lsblk is not available, so drives cannot be listed safely")
	}

	out, err := exec.CommandContext(context.Background(), binary,
		"--json", "--bytes", "--paths",
		"--output", "NAME,PATH,SIZE,MODEL,VENDOR,RM,RO,TYPE,TRAN,MOUNTPOINT",
	).Output()
	if err != nil {
		return nil, fmt.Errorf("listing drives: %w", err)
	}

	var parsed struct {
		BlockDevices []blockDevice `json:"blockdevices"`
	}
	if err := json.Unmarshal(out, &parsed); err != nil {
		return nil, fmt.Errorf("reading the drive list: %w", err)
	}

	var disks []blockDevice
	for _, device := range parsed.BlockDevices {
		if device.Type == "disk" {
			disks = append(disks, device)
		}
	}
	return disks, nil
}

// stringList collects a repeatable flag.
type stringList []string

func (l *stringList) String() string { return strings.Join(*l, ", ") }

func (l *stringList) Set(value string) error {
	*l = append(*l, value)
	return nil
}
