package hostd

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// What graphics hardware this machine has, and the name for it that will still
// be right next week.
//
// This exists because of a specific failure. Applications that use a GPU are
// configured with a path like /dev/dri/renderD128, and that number is assigned
// in probe order — it is a fact about one boot, not about the hardware. On the
// machine this was written for, installing a driver for the discrete card made
// the two nodes swap places:
//
//	before   renderD128 = NVIDIA (nouveau)   renderD129 = Intel
//	after    renderD128 = Intel              renderD129 = NVIDIA
//
// Nothing announced it. The media server had been pointed at renderD128 since
// installation, was therefore addressing the wrong chip the whole time, and
// hardware acceleration "not working" was put down to the hardware being old.
// Then it silently became correct.
//
// The same class of mistake as naming a network card enp5s0: correct until the
// day the machine enumerates its devices differently, and then wrong in a way
// nobody connects to what changed.
//
// The kernel already provides the fix. /dev/dri/by-path/ is keyed on the card's
// PCI address, which is a property of where it is plugged in, so the name
// survives a driver change, a reboot and a second card appearing. Homebase
// reports both: the stable path to configure things with, and the number as it
// happens to be today, because that is what every other tool will print.

// Graphics is one GPU, and how to address it.
type Graphics struct {
	// Name is the vendor in words. Deliberately not a model: naming the exact
	// chip needs a PCI id database, which is a dependency and a file that goes
	// stale, to tell somebody something the vendor and driver already imply.
	Name string `json:"name"`

	// Driver is the kernel driver bound to it — i915, amdgpu, nvidia, nouveau.
	// Worth reporting because it is the difference between a card that can do
	// video acceleration and the same card that cannot: nouveau on an NVIDIA
	// chip offers none of what the proprietary driver does.
	Driver string `json:"driver"`

	// RenderNode is the path as it is today: /dev/dri/renderD128.
	RenderNode string `json:"render_node"`

	// StablePath is the one to write down. It names the card by where it is
	// plugged in rather than by the order the kernel found it in.
	StablePath string `json:"stable_path,omitempty"`

	// AcceleratesVideo is whether this is a plausible target for hardware
	// transcoding. A statement about the driver, not about the silicon: it
	// cannot know which codecs are supported, and does not claim to.
	AcceleratesVideo bool `json:"accelerates_video"`
}

// pciVendors are the three that make graphics hardware anybody puts in a home
// server. An id not listed is reported by its number rather than guessed at.
var pciVendors = map[string]string{
	"0x8086": "Intel",
	"0x10de": "NVIDIA",
	"0x1002": "AMD",
}

// videoDrivers are the drivers that expose a video acceleration interface.
// nouveau is deliberately absent: it exposes a render node and almost no
// working encode, which is precisely how a media server ends up pointed at a
// device that will never transcode anything.
var videoDrivers = map[string]bool{
	"i915":   true,
	"xe":     true,
	"amdgpu": true,
	"radeon": true,
	"nvidia": true,
}

// readGraphics lists the render-capable GPUs on this machine.
func readGraphics(classDRM, devDRI string) []Graphics {
	entries, err := os.ReadDir(classDRM)
	if err != nil {
		return nil
	}

	// The stable names, indexed by the node they currently point at, so each
	// card can be given its own without walking the directory again.
	stable := stableRenderPaths(devDRI)

	var found []Graphics
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasPrefix(name, "renderD") {
			continue
		}
		device := filepath.Join(classDRM, name, "device")

		vendor := strings.TrimSpace(readSmallFile(filepath.Join(device, "vendor")))
		if vendor == "" {
			continue
		}
		label, known := pciVendors[strings.ToLower(vendor)]
		if !known {
			label = "Graphics " + vendor
		} else {
			label += " graphics"
		}

		driver := ""
		if target, err := os.Readlink(filepath.Join(device, "driver")); err == nil {
			driver = filepath.Base(target)
		}

		found = append(found, Graphics{
			Name:             label,
			Driver:           driver,
			RenderNode:       filepath.Join(devDRI, name),
			StablePath:       stable[name],
			AcceleratesVideo: videoDrivers[driver],
		})
	}

	// By the stable path rather than by the node number, so the order of this
	// list does not change when the numbering does.
	sort.Slice(found, func(i, j int) bool {
		return found[i].StablePath < found[j].StablePath
	})
	return found
}

// stableRenderPaths maps a render node to the by-path name that always finds it.
func stableRenderPaths(devDRI string) map[string]string {
	byPath := filepath.Join(devDRI, "by-path")
	entries, err := os.ReadDir(byPath)
	if err != nil {
		return nil
	}

	paths := map[string]string{}
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), "-render") {
			continue
		}
		link := filepath.Join(byPath, entry.Name())
		target, err := os.Readlink(link)
		if err != nil {
			continue
		}
		paths[filepath.Base(target)] = link
	}
	return paths
}

// readSmallFile reads a sysfs attribute, or empty if it is not there.
func readSmallFile(path string) string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(raw))
}
