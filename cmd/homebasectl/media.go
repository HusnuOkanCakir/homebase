package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

// Writing the installation media.
//
// ADR-0016: Canonical's ISO goes on byte for byte, and the seed follows it as
// an extra partition. The ISO is already a GPT image with three partitions in
// it, so this appends a fourth rather than building a partition table of its
// own — the boot path stays exactly the one Ubuntu published and tests.
//
// This is the only code in Homebase that writes to a block device on the user's
// own computer, which is usually the computer with all their work on it. Every
// refusal below is deliberate.

const (
	// Slack after the ISO, so the appended partition starts on a sensible
	// boundary and the backup partition table has somewhere to live.
	mediaSlackBytes = 4 * 1024 * 1024

	// The smallest stick worth trying. The ISO alone is 3.2 GB.
	minimumMediaBytes = 6 * 1000 * 1000 * 1000
)

func installerCreate(args []string, stdout, stderr io.Writer, stdin io.Reader) error {
	flags := flag.NewFlagSet("installer create", flag.ContinueOnError)
	iso := flags.String("iso", "", "the Ubuntu Server ISO to build from")
	packages := flags.String("packages", "", "directory holding the Homebase .deb packages")
	device := flags.String("device", "", "the drive to write to; everything on it is erased")
	output := flags.String("output", "", "write an image file instead of a drive")
	hostname := flags.String("hostname", "homebase", "what the server will call itself")
	locale := flags.String("locale", "en_GB.UTF-8", "system locale")
	keyboard := flags.String("keyboard", "gb", "keyboard layout")
	assumeYes := flags.Bool("yes", false, "do not ask before erasing the drive")

	var keys stringList
	flags.Var(&keys, "authorized-key", "an SSH public key to install (repeatable)")

	if err := flags.Parse(args); err != nil {
		return err
	}
	if *iso == "" {
		return errors.New("--iso is required: the Ubuntu Server image to build from")
	}
	if *packages == "" {
		return errors.New("--packages is required: the media carries Homebase's own packages")
	}
	if (*device == "") == (*output == "") {
		return errors.New("say either --device (a drive) or --output (an image file)")
	}

	isoSize, err := sizeOf(*iso)
	if err != nil {
		return fmt.Errorf("reading %s: %w", *iso, err)
	}

	// The seed first, so nothing is written to the drive until everything that
	// can fail has failed.
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

	seedSize, err := sizeOf(seed)
	if err != nil {
		return err
	}

	required := isoSize + seedSize + mediaSlackBytes

	target := *output
	if *device != "" {
		if err := checkDevice(*device, required); err != nil {
			return err
		}
		if !*assumeYes {
			if err := confirmErasing(*device, stdout, stdin); err != nil {
				return err
			}
		}
		target = *device
	} else {
		if err := createImage(*output, required); err != nil {
			return err
		}
	}

	fmt.Fprintf(stdout, "Writing Ubuntu (%d MB)…\n", isoSize/1_000_000)
	if err := writeAt(target, *iso, 0); err != nil {
		return err
	}

	fmt.Fprintln(stdout, "Adding Homebase…")
	offset, err := appendSeedPartition(target, seedSize)
	if err != nil {
		return err
	}
	if err := writeAt(target, seed, offset); err != nil {
		return err
	}

	if err := syncDisks(); err != nil {
		return err
	}

	fmt.Fprintf(stdout, `
Installation media ready: %s

Boot the old computer from it. It will ask, once:

    Continue with autoinstall? (yes|no)

Answering yes erases that computer's whole disk and installs Homebase on it.
Nothing on it is kept.
`, target)
	return nil
}

// checkDevice refuses to write to anything that should not be written to.
func checkDevice(path string, required int64) error {
	devices, err := listBlockDevices()
	if err != nil {
		return err
	}

	for _, candidate := range devices {
		if candidate.Path != path {
			continue
		}
		if reason := candidate.refusal(); reason != "" {
			return fmt.Errorf(
				"%s cannot be written to: %s.\n"+
					"Run `homebasectl installer devices` to see what can.",
				path, reason)
		}
		if candidate.Size < required {
			return fmt.Errorf(
				"%s holds %s, and the media needs about %d GB.",
				path, candidate.humanSize(), required/1_000_000_000+1)
		}
		return nil
	}

	return fmt.Errorf(
		"there is no drive at %s.\n"+
			"Run `homebasectl installer devices` to see what there is.", path)
}

// confirmErasing makes somebody type the name of the drive being destroyed.
//
// Not a yes/no: the mistake this guards against is not failing to think about
// the question, it is answering it about the wrong drive. Typing the name is
// the only confirmation that cannot be given by reflex.
func confirmErasing(path string, stdout io.Writer, stdin io.Reader) error {
	fmt.Fprintf(stdout, `
Everything on %s will be erased.

Type the drive's name to confirm: `, path)

	reader := bufio.NewReader(stdin)
	typed, err := reader.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return err
	}

	if strings.TrimSpace(typed) != path {
		return fmt.Errorf("that is not %s. Nothing was written.", path)
	}
	return nil
}

func createImage(path string, size int64) error {
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()

	// Sparse: the file claims its full size without occupying it until written.
	if err := file.Truncate(size); err != nil {
		return err
	}
	return nil
}

// writeAt copies a file onto the media at a byte offset.
func writeAt(target, source string, offset int64) error {
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()

	out, err := os.OpenFile(target, os.O_WRONLY, 0)
	if err != nil {
		return fmt.Errorf("opening %s for writing: %w", target, err)
	}
	defer func() { _ = out.Close() }()

	if _, err := out.Seek(offset, io.SeekStart); err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		return fmt.Errorf("writing to %s: %w", target, err)
	}
	return out.Sync()
}

// appendSeedPartition adds a partition after Ubuntu's, and says where it starts.
//
// The ISO arrives as a GPT image with its partitions already in it. Rather than
// building a table of our own — which is how media boots on the machine it was
// tested on and nowhere else — this relocates the backup table to the end of the
// larger medium and adds one entry after the last.
func appendSeedPartition(target string, seedSize int64) (int64, error) {
	sgdisk, err := exec.LookPath("sgdisk")
	if err != nil {
		return 0, errors.New(
			"sgdisk is needed to add Homebase's part of the media.\n" +
				"    sudo apt install gdisk")
	}

	// Move the backup header to the end of the *medium*, which is bigger than
	// the ISO it was written for. Without this every later operation is working
	// from a table that says the disk ends where the ISO did.
	if out, err := exec.CommandContext(context.Background(), sgdisk,
		"--move-second-header", target).CombinedOutput(); err != nil {
		return 0, fmt.Errorf("relocating the partition table: %w\n%s",
			err, strings.TrimSpace(string(out)))
	}

	sectors := (seedSize + 511) / 512

	// `0:0:+N` means: the next free partition number, starting at the first
	// aligned free sector, N sectors long. Nothing here names a number or an
	// offset, because both depend on what the ISO happened to contain.
	if out, err := exec.CommandContext(context.Background(), sgdisk,
		"--new", fmt.Sprintf("0:0:+%d", sectors),
		"--typecode", "0:0700",
		"--change-name", "0:HOMEBASE",
		target).CombinedOutput(); err != nil {
		return 0, fmt.Errorf("adding Homebase's partition: %w\n%s",
			err, strings.TrimSpace(string(out)))
	}

	return lastPartitionOffset(target, sgdisk)
}

// lastPartitionOffset reads back where the partition just added begins.
//
// Read back rather than calculated: the offset depends on alignment decisions
// sgdisk made, and a calculated answer that disagrees with the partition table
// writes the seed somewhere nothing will look for it.
func lastPartitionOffset(target, sgdisk string) (int64, error) {
	out, err := exec.CommandContext(context.Background(), sgdisk,
		"--print", target).Output()
	if err != nil {
		return 0, fmt.Errorf("reading the partition table back: %w", err)
	}

	var start int64
	var found bool
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 6 {
			continue
		}
		if _, err := strconv.Atoi(fields[0]); err != nil {
			continue // not a partition row
		}
		if fields[len(fields)-1] != "HOMEBASE" {
			continue
		}
		value, err := strconv.ParseInt(fields[1], 10, 64)
		if err != nil {
			return 0, fmt.Errorf("could not read where Homebase's partition starts: %q", line)
		}
		start, found = value, true
	}

	if !found {
		return 0, errors.New(
			"the partition table has no HOMEBASE partition after adding one.\n" +
				"    Nothing was written to it, so the media is Ubuntu without Homebase.")
	}
	return start * 512, nil
}

func syncDisks() error {
	if sync, err := exec.LookPath("sync"); err == nil {
		return exec.CommandContext(context.Background(), sync).Run()
	}
	return nil
}

func sizeOf(path string) (int64, error) {
	info, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	return info.Size(), nil
}
