package hostd

import (
	"os"
	"path/filepath"
	"testing"
)

// The whole point of this file is that renderD128 is not a name.
//
// Built from the two states one real machine was actually in, an hour apart:
// with nouveau bound to the discrete card, and with the proprietary driver bound
// to it after a reboot. The node numbers swapped. The PCI addresses did not, and
// the media server pointed at renderD128 addressed a different chip on each side
// of that reboot without anything being reconfigured.
func TestTheStablePathSurvivesTheNodesSwapping(t *testing.T) {
	// Before: the discrete card enumerated first.
	before := fakeDRM(t, map[string]fakeCard{
		"renderD128": {pci: "0000:01:00.0", vendor: "0x10de", driver: "nouveau"},
		"renderD129": {pci: "0000:00:02.0", vendor: "0x8086", driver: "i915"},
	})
	// After: a driver change reordered them. Same two cards, same two slots.
	after := fakeDRM(t, map[string]fakeCard{
		"renderD128": {pci: "0000:00:02.0", vendor: "0x8086", driver: "i915"},
		"renderD129": {pci: "0000:01:00.0", vendor: "0x10de", driver: "nvidia"},
	})

	intelBefore := findByName(readGraphics(before.class, before.dev), "Intel graphics")
	intelAfter := findByName(readGraphics(after.class, after.dev), "Intel graphics")
	if intelBefore == nil || intelAfter == nil {
		t.Fatal("the Intel card was not reported in both states")
	}

	// The thing everybody writes into a configuration file, and why they should
	// not: it names a different chip on either side of a reboot.
	if intelBefore.RenderNode == intelAfter.RenderNode {
		t.Fatal("this test is not reproducing the swap it exists to describe")
	}

	// The thing to write down instead. Compared by its own name rather than by
	// the whole path — the two states are built under different temporary
	// directories here, and on a real machine the directory is always /dev/dri.
	beforeName := filepath.Base(intelBefore.StablePath)
	afterName := filepath.Base(intelAfter.StablePath)
	if beforeName != afterName {
		t.Errorf("the stable path moved with the numbering: %q then %q — it is "+
			"supposed to be keyed on where the card is plugged in",
			beforeName, afterName)
	}
	if beforeName != "pci-0000:00:02.0-render" {
		t.Errorf("the stable path is %q; it should name the card's PCI address", beforeName)
	}
	if intelAfter.StablePath == "" {
		t.Error("no stable path was reported, so there is nothing safe to configure")
	}
}

// nouveau exposes a render node and almost no working encode. Reporting it as a
// video accelerator is how a media server gets pointed at a device that will
// never transcode anything — which is exactly what happened.
func TestNouveauIsNotOfferedForVideo(t *testing.T) {
	drm := fakeDRM(t, map[string]fakeCard{
		"renderD128": {pci: "0000:01:00.0", vendor: "0x10de", driver: "nouveau"},
		"renderD129": {pci: "0000:00:02.0", vendor: "0x8086", driver: "i915"},
	})
	cards := readGraphics(drm.class, drm.dev)

	for _, card := range cards {
		switch card.Driver {
		case "nouveau":
			if card.AcceleratesVideo {
				t.Error("nouveau was offered as a target for hardware transcoding")
			}
		case "i915":
			if !card.AcceleratesVideo {
				t.Error("the Intel card was not offered, so nothing usable was")
			}
		}
	}
	// Still listed. "You have an NVIDIA card and it cannot do this" is a useful
	// thing to be told; hiding it invites somebody to go looking for it.
	if len(cards) != 2 {
		t.Errorf("reported %d cards, want both", len(cards))
	}
}

// A machine with no graphics at all, which is most servers, must not be an error.
func TestNoGraphicsIsNotAFailure(t *testing.T) {
	empty := t.TempDir()
	if got := readGraphics(empty, empty); len(got) != 0 {
		t.Errorf("invented %d graphics cards on a machine with none", len(got))
	}
	if got := readGraphics("/nonexistent", "/nonexistent"); got != nil {
		t.Errorf("returned %v where nothing could be read", got)
	}
}

type fakeCard struct{ pci, vendor, driver string }

type fakeDRMTree struct{ class, dev string }

// fakeDRM builds the parts of /sys/class/drm and /dev/dri that are read.
func fakeDRM(t *testing.T, cards map[string]fakeCard) fakeDRMTree {
	t.Helper()
	root := t.TempDir()
	class := filepath.Join(root, "class-drm")
	dev := filepath.Join(root, "dev-dri")
	byPath := filepath.Join(dev, "by-path")
	for _, dir := range []string{class, dev, byPath} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	for node, card := range cards {
		device := filepath.Join(class, node, "device")
		if err := os.MkdirAll(device, 0o755); err != nil {
			t.Fatal(err)
		}
		writeAttribute(t, filepath.Join(device, "vendor"), card.vendor+"\n")
		// The driver is a symlink in sysfs, and its basename is the name.
		driverDir := filepath.Join(root, "bus-pci-drivers", card.driver)
		if err := os.MkdirAll(driverDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(driverDir, filepath.Join(device, "driver")); err != nil {
			t.Fatal(err)
		}
		// And the stable name the kernel maintains beside the numbered one.
		link := filepath.Join(byPath, "pci-"+card.pci+"-render")
		if err := os.Symlink(filepath.Join("..", node), link); err != nil {
			t.Fatal(err)
		}
	}
	return fakeDRMTree{class: class, dev: dev}
}

func writeAttribute(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func findByName(cards []Graphics, name string) *Graphics {
	for i := range cards {
		if cards[i].Name == name {
			return &cards[i]
		}
	}
	return nil
}
