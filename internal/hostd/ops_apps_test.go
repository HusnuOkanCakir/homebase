package hostd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func appServices(t *testing.T) *AppServices {
	t.Helper()
	return NewAppServices(NewCatalogue(t.TempDir()), "", t.TempDir()+"/apps")
}

// Every directory created for an application must be created, not only the leaf.
//
// os.MkdirAll creates intermediates as the calling process, and chowning only the
// leaf left /srv/homebase/apps/<id> as 0750 root:root on a real machine — which
// core, running as the service account, cannot traverse. It could not back the
// data up, and it would have failed silently. The VM test found this; the test is
// here so it is found in a second rather than in twelve minutes.
func TestEveryDirectoryUnderTheDataRootIsCreated(t *testing.T) {
	s := appServices(t)

	target := filepath.Join(s.dataRoot, "some-app", "config")
	if err := s.makeOwnedDir(target); err != nil {
		t.Fatalf("makeOwnedDir: %v", err)
	}

	// Each level, including the data root itself, which does not exist on a
	// fresh machine either.
	for _, path := range []string{
		s.dataRoot,
		filepath.Join(s.dataRoot, "some-app"),
		target,
	} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("%s was not created: %v", path, err)
		}
		if !info.IsDir() {
			t.Errorf("%s is not a directory", path)
		}
		// 0750: the service account and nothing else. World-readable would put
		// an application's configuration in reach of every account on the box.
		if mode := info.Mode().Perm(); mode != 0o750 {
			t.Errorf("%s is %o, want 750", path, mode)
		}
	}
}

func TestMakeOwnedDirIsIdempotent(t *testing.T) {
	s := appServices(t)
	target := filepath.Join(s.dataRoot, "some-app", "config")

	for i := 0; i < 3; i++ {
		if err := s.makeOwnedDir(target); err != nil {
			t.Fatalf("call %d: %v", i+1, err)
		}
	}
}

// A path outside the data root must be refused, whatever route it takes there.
// This function creates directories as root; the set of paths it will accept is
// the set of places a manifest can cause a directory to appear.
func TestMakeOwnedDirRefusesPathsOutsideTheDataRoot(t *testing.T) {
	s := appServices(t)

	for _, path := range []string{
		"/etc/homebase",
		"/tmp/elsewhere",
		filepath.Join(s.dataRoot, "..", "escaped"),
		filepath.Join(s.dataRoot, "app", "..", "..", "escaped"),
		s.dataRoot + "-sibling", // a prefix match that is not a child
	} {
		if err := s.makeOwnedDir(path); err == nil {
			t.Errorf("%s was accepted", path)
		}
	}
}

// The data path reported to a user must be the one that is actually used. It is
// what they are told uninstalling will leave behind, and a wrong answer there is
// a promise about the wrong directory.
func TestDataPathFollowsTheConfiguredRoot(t *testing.T) {
	s := appServices(t)

	got := s.appDataDir("jellyfin")
	want := filepath.Join(s.dataRoot, "jellyfin")
	if got != want {
		t.Errorf("appDataDir = %q, want %q", got, want)
	}
	if !strings.HasPrefix(got, s.dataRoot+string(filepath.Separator)) {
		t.Errorf("%q is not under the data root", got)
	}
}

// --- What gets built ----------------------------------------------------------

// The container configuration is the security boundary in practice: a manifest
// that loaded still must not produce a container that can reach the host.
func TestContainerIsBuiltLockedDown(t *testing.T) {
	s := appServices(t)

	manifest := Manifest{
		ManifestVersion: 1,
		ID:              "test-app",
		Name:            "Test App",
	}
	manifest.Container.Image = "example/app"
	manifest.Container.Version = "1.0.0"
	manifest.Network.InternalPort = 8080

	config := s.buildContainer(manifest, []string{"/data:/config:rw"})

	if config.HostConfig.Privileged {
		t.Error("the container is privileged")
	}
	if len(config.HostConfig.CapDrop) != 1 || config.HostConfig.CapDrop[0] != "ALL" {
		t.Errorf("CapDrop = %v, want [ALL]", config.HostConfig.CapDrop)
	}

	var hasNoNewPrivileges bool
	for _, option := range config.HostConfig.SecurityOpt {
		if option == "no-new-privileges" {
			hasNoNewPrivileges = true
		}
	}
	if !hasNoNewPrivileges {
		t.Errorf("SecurityOpt = %v, want no-new-privileges", config.HostConfig.SecurityOpt)
	}

	// unless-stopped rather than always: an application the user stopped must
	// stay stopped across a reboot, and `always` would start it again behind
	// their back.
	if config.HostConfig.RestartPolicy.Name != "unless-stopped" {
		t.Errorf("RestartPolicy = %q", config.HostConfig.RestartPolicy.Name)
	}

	// A home network has a printer, a television and whatever the neighbours can
	// reach on it. An application is published deliberately or not at all.
	for port, bindings := range config.HostConfig.PortBindings {
		for _, binding := range bindings {
			if binding.HostIP != "127.0.0.1" {
				t.Errorf("%s is bound to %q, want 127.0.0.1", port, binding.HostIP)
			}
		}
	}
}

// A manifest asking for a capability gets exactly that one, and still loses the
// rest. Adding one must not become a way to keep all of them.
func TestRequestedCapabilitiesDoNotUndoTheDrop(t *testing.T) {
	s := appServices(t)

	manifest := Manifest{ManifestVersion: 1, ID: "test-app", Name: "Test App"}
	manifest.Container.Image = "example/app"
	manifest.Container.Version = "1.0.0"
	manifest.Permissions.Capabilities = []string{"NET_ADMIN"}

	config := s.buildContainer(manifest, nil)

	if len(config.HostConfig.CapDrop) != 1 || config.HostConfig.CapDrop[0] != "ALL" {
		t.Errorf("CapDrop = %v; a manifest asking for one capability kept the rest",
			config.HostConfig.CapDrop)
	}
	if len(config.HostConfig.CapAdd) != 1 || config.HostConfig.CapAdd[0] != "NET_ADMIN" {
		t.Errorf("CapAdd = %v, want [NET_ADMIN]", config.HostConfig.CapAdd)
	}
}

// The container name is namespaced, so nothing here can act on a container
// somebody else created on the same machine.
func TestContainerNamesAreNamespaced(t *testing.T) {
	name := containerName("jellyfin")
	if !strings.HasPrefix(name, "homebase-") {
		t.Errorf("containerName = %q, want a homebase- prefix", name)
	}
	if name == "jellyfin" {
		t.Error("the container name is the application id, so Homebase could act on a container it does not own")
	}
}
