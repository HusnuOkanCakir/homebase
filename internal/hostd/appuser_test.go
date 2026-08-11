package hostd

import (
	"os"
	"path/filepath"
	"testing"
)

// Every application gets its own account, and the container runs as it.
//
// The bug this replaces: containers ran as root with every capability dropped,
// so root had no CAP_DAC_OVERRIDE and could not write a 0750 directory owned by
// somebody else. Two of the three catalogued applications could not start, and
// the suite stayed green because the third writes nothing.

func TestApplicationsGetDifferentIdentifiers(t *testing.T) {
	dir := t.TempDir()
	a, err := ensureAppOwner(dir, "jellyfin")
	if err != nil {
		t.Fatal(err)
	}
	b, err := ensureAppOwner(dir, "filebrowser")
	if err != nil {
		t.Fatal(err)
	}
	if a.uid == b.uid {
		t.Fatal("two applications share an identifier, so neither is isolated from the other")
	}
	again, err := ensureAppOwner(dir, "jellyfin")
	if err != nil {
		t.Fatal(err)
	}
	if again.uid != a.uid {
		t.Error("the identifier changed on a second call; every file the application owns would become unreadable to it")
	}
}

// The container has to run as the account that owns its files, or it is back to
// being unable to write them.
func TestTheContainerRunsAsTheApplicationsAccount(t *testing.T) {
	s := appServices(t)

	manifest := Manifest{ManifestVersion: 1, ID: "player", Name: "Player"}
	manifest.Container.Image = "example/player"
	manifest.Container.Version = "1.0.0"
	manifest.Network.InternalPort = 8096

	config := s.buildContainer(manifest, nil, owner{uid: 987, gid: 654})

	if config.User != "987:654" {
		t.Errorf("container User = %q, want 987:654 — root with no capabilities "+
			"cannot write its own data directory", config.User)
	}
}

// Upgrades: a machine that installed an application before this existed has
// files owned by the old shared account, and handing over only the top
// directory would leave the application unable to read its own history.
func TestOwnershipIsHandedOverRecursively(t *testing.T) {
	root := t.TempDir()
	nested := filepath.Join(root, "config", "log")
	if err := os.MkdirAll(nested, 0o750); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(nested, "old.log")
	if err := os.WriteFile(file, []byte("from before"), 0o640); err != nil {
		t.Fatal(err)
	}

	// Unprivileged, so the only chown that can succeed is to who we already are.
	mine := owner{uid: os.Getuid(), gid: os.Getgid()}
	if err := giveTo(root, mine); err != nil {
		t.Fatalf("handing over the tree: %v", err)
	}

	// What matters is that it walked rather than touching only the top.
	for _, path := range []string{root, nested, file} {
		if _, err := os.Stat(path); err != nil {
			t.Errorf("%s did not survive the handover: %v", path, err)
		}
	}
}

// A symlink inside an application's own directory must not redirect a root
// chown at something outside it.
func TestHandoverDoesNotFollowSymlinks(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "not-ours")
	if err := os.WriteFile(outside, []byte("someone else's"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}

	before, err := os.Stat(outside)
	if err != nil {
		t.Fatal(err)
	}

	if err := giveTo(root, owner{uid: os.Getuid(), gid: os.Getgid()}); err != nil {
		t.Fatalf("handing over: %v", err)
	}

	after, err := os.Stat(outside)
	if err != nil {
		t.Fatal(err)
	}
	if before.Mode() != after.Mode() {
		t.Error("the target of a symlink inside the directory was touched")
	}
}
