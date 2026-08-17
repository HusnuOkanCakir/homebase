package hostd

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/user"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

func appServices(t *testing.T) *AppServices {
	t.Helper()
	return NewAppServices(NewCatalogue(t.TempDir()), "", t.TempDir()+"/apps", t.TempDir()+"/state")
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
	if err := s.makeOwnedDir(target, testOwner()); err != nil {
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
		if err := s.makeOwnedDir(target, testOwner()); err != nil {
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
		if err := s.makeOwnedDir(path, testOwner()); err == nil {
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

	config := s.buildContainer(manifest, []string{"/data:/config:rw"}, owner{uid: 900, gid: 900})

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

	config := s.buildContainer(manifest, nil, owner{uid: 900, gid: 900})

	if len(config.HostConfig.CapDrop) != 1 || config.HostConfig.CapDrop[0] != "ALL" {
		t.Errorf("CapDrop = %v; a manifest asking for one capability kept the rest",
			config.HostConfig.CapDrop)
	}
	if len(config.HostConfig.CapAdd) != 1 || config.HostConfig.CapAdd[0] != "NET_ADMIN" {
		t.Errorf("CapAdd = %v, want [NET_ADMIN]", config.HostConfig.CapAdd)
	}
}

// --- Remembering a deliberate stop -------------------------------------------

// An application somebody stopped must not be reported as having crashed.
//
// Docker keeps no record of who stopped a container: a deliberately stopped one
// and a crashed one are identical afterwards, because a program terminated by
// SIGTERM chooses its own exit code. traefik/whoami chooses 2, which made every
// deliberate stop read as "stopped unexpectedly" — found by the browser test.
func TestADeliberateStopIsRememberedAsOne(t *testing.T) {
	s := appServices(t)

	if s.stoppedDeliberately("jellyfin") {
		t.Fatal("an application nobody has stopped is recorded as stopped")
	}

	s.rememberStopped("jellyfin")
	if !s.stoppedDeliberately("jellyfin") {
		t.Error("the stop was not remembered")
	}

	// One application's state must not answer for another's.
	if s.stoppedDeliberately("filebrowser") {
		t.Error("stopping one application marked another as stopped")
	}

	s.forgetStopped("jellyfin")
	if s.stoppedDeliberately("jellyfin") {
		t.Error("starting it again did not clear the record")
	}
}

// It has to survive a restart of the machine: an application the user stopped
// stays stopped across a reboot, and must still read as stopped rather than as
// having crashed while nobody was looking.
func TestTheStopRecordSurvivesARestart(t *testing.T) {
	catalogue := NewCatalogue(t.TempDir())
	dataRoot := t.TempDir() + "/apps"
	stateDir := t.TempDir() + "/state"

	before := NewAppServices(catalogue, "", dataRoot, stateDir)
	before.rememberStopped("jellyfin")

	// A second instance, as though hostd had been restarted with the machine.
	after := NewAppServices(catalogue, "", dataRoot, stateDir)
	if !after.stoppedDeliberately("jellyfin") {
		t.Error("the record did not survive hostd restarting")
	}
}

// Clearing a record that was never written is the ordinary case — every start of
// an application that was already running goes through it — and must not be
// noisy or fail.
func TestForgettingAnUnrecordedStopIsFine(t *testing.T) {
	s := appServices(t)
	s.forgetStopped("never-stopped")
	if s.stoppedDeliberately("never-stopped") {
		t.Error("forgetting created a record")
	}
}

// The marker is hostd's, not the user's, and not core's. core running as the
// service account must not be able to rewrite hostd's account of what it did.
func TestTheStopRecordIsNotReadableByOtherAccounts(t *testing.T) {
	s := appServices(t)
	s.rememberStopped("jellyfin")

	info, err := os.Stat(filepath.Dir(s.stopMarker("jellyfin")))
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != 0o700 {
		t.Errorf("the state directory is %o, want 700", mode)
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

// --- Talking to Docker --------------------------------------------------------

// A failed negotiation must not be remembered.
//
// sync.Once was the first shape and it was wrong: it caches the failure as
// readily as the success, so a hostd asked for something before Docker had
// finished starting would refuse every operation for the rest of its life. On a
// machine that has just booted, hostd being ready first is the normal case, not
// an edge one.
func TestADockerThatStartsLateIsPickedUp(t *testing.T) {
	dir, err := os.MkdirTemp("", "hb")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	socket := filepath.Join(dir, "docker.sock")

	client := newDocker(socket)

	// Nothing is listening yet.
	if _, err := client.apiVersion(t.Context()); err == nil {
		t.Fatal("negotiating against a socket that does not exist succeeded")
	}

	// Docker arrives.
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: http.HandlerFunc(
		func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"Version":"27.0.0","ApiVersion":"1.46","MinAPIVersion":"1.24"}`)
		})}
	go server.Serve(listener)
	t.Cleanup(func() { server.Close() })

	version, err := client.apiVersion(t.Context())
	if err != nil {
		t.Fatalf("hostd never recovered once Docker was running: %v", err)
	}
	if version != "v1.46" {
		t.Errorf("version = %q, want v1.46", version)
	}
}

// The daemon's floor wins over our ceiling. Speaking a version newer than hostd
// was written against is a risk; refusing to run at all on a Docker newer than
// this release is a certainty, and on an appliance the certainty is worse.
func TestVersionNegotiationRespectsBothEnds(t *testing.T) {
	cases := []struct {
		name       string
		daemonAPI  string
		daemonMin  string
		want       string
		wantErrror bool
	}{
		{
			name:      "a daemon newer than us is capped at our ceiling",
			daemonAPI: "1.99", daemonMin: "1.24", want: "v" + dockerMaxAPIVersion,
		},
		{
			name:      "a daemon older than us is met where it is",
			daemonAPI: "1.44", daemonMin: "1.24", want: "v1.44",
		},
		{
			// The case that broke a pinned client: Docker 29 refuses below 1.44.
			name:      "a floor above our ceiling wins",
			daemonAPI: "1.99", daemonMin: "1.99", want: "v1.99",
		},
		{
			name:      "a daemon too old to speak to is refused",
			daemonAPI: "1.20", daemonMin: "1.12", wantErrror: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir, err := os.MkdirTemp("", "hb")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { os.RemoveAll(dir) })
			socket := filepath.Join(dir, "docker.sock")

			listener, err := net.Listen("unix", socket)
			if err != nil {
				t.Fatal(err)
			}
			server := &http.Server{Handler: http.HandlerFunc(
				func(w http.ResponseWriter, _ *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					fmt.Fprintf(w, `{"Version":"x","ApiVersion":%q,"MinAPIVersion":%q}`,
						tc.daemonAPI, tc.daemonMin)
				})}
			go server.Serve(listener)
			t.Cleanup(func() { server.Close() })

			version, err := newDocker(socket).apiVersion(t.Context())
			if tc.wantErrror {
				if err == nil {
					t.Fatalf("a daemon speaking %s was accepted", tc.daemonAPI)
				}
				return
			}
			if err != nil {
				t.Fatalf("negotiation failed: %v", err)
			}
			if version != tc.want {
				t.Errorf("version = %q, want %q", version, tc.want)
			}
		})
	}
}

// Dotted versions are numbers, not strings: "1.9" sorts after "1.10"
// alphabetically and before it numerically, and getting that backwards picks a
// version the daemon rejects.
func TestAPIVersionsCompareNumerically(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"1.43", "1.44", -1},
		{"1.44", "1.43", 1},
		{"1.44", "1.44", 0},
		{"1.9", "1.10", -1}, // the one a string comparison gets wrong
		{"1.10", "1.9", 1},
		{"1.100", "1.99", 1},
		{"2.0", "1.99", 1},
	}

	for _, tc := range cases {
		if got := compareAPIVersions(tc.a, tc.b); got != tc.want {
			t.Errorf("compareAPIVersions(%q, %q) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
	}
}

// --- User-selected storage ----------------------------------------------------

// An application whose disk is missing must refuse to start, rather than running
// against an empty directory on the system disk.
//
// This is ADR-0013's application-facing half. A media server started without its
// media presents an empty library, rebuilds its database from nothing, and fills
// the root filesystem — and none of that reports an error anywhere.
func TestAnApplicationWithNoDiskChosenRefusesToStart(t *testing.T) {
	s := appServices(t).WithStorage(storageServices(t))

	manifest := Manifest{ManifestVersion: 1, ID: "jellyfin", Name: "Jellyfin"}
	manifest.Container.Image = "example/app"
	manifest.Container.Version = "1.0.0"
	manifest.Storage = []ManifestStorage{
		{ID: "config", Type: "private", MountPath: "/config"},
		{ID: "media", Type: "user-selected", MountPath: "/media",
			Description: "The folder holding your films"},
	}

	_, err := s.prepareStorage(manifest, testOwner())
	if err == nil {
		t.Fatal("an application with unassigned storage was prepared anyway")
	}

	var hostErr *Error
	if !asHostError(err, &hostErr) {
		t.Fatalf("got %T: %v", err, err)
	}
	if hostErr.Code != "app.storage_not_assigned" {
		t.Errorf("code = %q", hostErr.Code)
	}
	if !hostErr.Recoverable || hostErr.Recovery == "" {
		t.Error("a user who has not chosen a disk yet was given no way forward")
	}
	if !strings.Contains(hostErr.Message, "Jellyfin") {
		t.Errorf("the message does not name the application: %q", hostErr.Message)
	}
}

// Assigned, but the disk is not plugged in. A different situation with a
// different remedy, and it has to say so.
func TestAnApplicationWhoseDiskIsUnpluggedRefusesToStart(t *testing.T) {
	storage := storageServices(t)
	if err := storage.save([]Location{
		{ID: "media", Name: "Films drive", UUID: "not-connected-0000", Filesystem: "ext4"},
	}); err != nil {
		t.Fatal(err)
	}
	// Written directly: Assign refuses an unmounted disk, which is the point of
	// that check, so the state it protects against is built by hand here.
	if err := storage.saveAssignments([]Assignment{
		{App: "jellyfin", StorageID: "media", Location: "media", Subdirectory: "jellyfin"},
	}); err != nil {
		t.Fatal(err)
	}

	s := appServices(t).WithStorage(storage)

	manifest := Manifest{ManifestVersion: 1, ID: "jellyfin", Name: "Jellyfin"}
	manifest.Container.Image = "example/app"
	manifest.Container.Version = "1.0.0"
	manifest.Storage = []ManifestStorage{
		{ID: "media", Type: "user-selected", MountPath: "/media"},
	}

	_, err := s.prepareStorage(manifest, testOwner())
	if err == nil {
		t.Fatal("an application started with its disk unplugged")
	}

	var hostErr *Error
	if !asHostError(err, &hostErr) || hostErr.Code != "app.storage_unavailable" {
		t.Fatalf("got %v", err)
	}
	if !strings.Contains(hostErr.Message, "Films drive") {
		t.Errorf("the message does not name the disk to plug back in: %q", hostErr.Message)
	}
	if !hostErr.Recoverable {
		t.Error("an unplugged disk was reported as unrecoverable")
	}
}

// Private storage is Homebase's to place. Letting a user move it onto a
// removable disk would mean the application does not start without that disk —
// including the configuration that says which disk it wants.
func TestPrivateStorageCannotBeMovedToAChosenDisk(t *testing.T) {
	storage := storageServices(t)
	catalogue := NewCatalogue(t.TempDir())
	s := NewAppServices(catalogue, "", t.TempDir()+"/apps", t.TempDir()+"/state").
		WithStorage(storage)

	// A catalogue with one application, so manifest lookup succeeds.
	manifest := map[string]any{
		"manifest_version": 1, "id": "jellyfin", "name": "Jellyfin",
		"container": map[string]any{"image": "example/app", "version": "1.0.0"},
		"storage": []any{
			map[string]any{"id": "config", "type": "private", "mount_path": "/config"},
		},
		"health": map[string]any{"type": "none"},
	}
	writeCatalogueFile(t, catalogue, "jellyfin.json", manifest)

	_, err := s.assignStorage(t.Context(), AssignStorageParams{
		ID: "jellyfin", StorageID: "config", Location: "media",
	})
	if err == nil {
		t.Fatal("private storage was moved to a chosen disk")
	}
	var hostErr *Error
	if !asHostError(err, &hostErr) || hostErr.Code != "app.storage_not_choosable" {
		t.Errorf("got %v", err)
	}
}

// Private storage is always ready; user-selected storage is not ready until a
// disk is both chosen and connected. `ready` is what decides whether the
// application can start at all.
func TestStorageStatusReportsWhatIsMissing(t *testing.T) {
	catalogue := NewCatalogue(t.TempDir())
	s := NewAppServices(catalogue, "", t.TempDir()+"/apps", t.TempDir()+"/state").
		WithStorage(storageServices(t))

	writeCatalogueFile(t, catalogue, "jellyfin.json", map[string]any{
		"manifest_version": 1, "id": "jellyfin", "name": "Jellyfin",
		"container": map[string]any{"image": "example/app", "version": "1.0.0"},
		"storage": []any{
			map[string]any{"id": "config", "type": "private", "mount_path": "/config"},
			map[string]any{"id": "media", "type": "user-selected", "mount_path": "/media"},
		},
		"health": map[string]any{"type": "none"},
	})

	result, err := s.storageStatus(t.Context(), AppRef{ID: "jellyfin"})
	if err != nil {
		t.Fatal(err)
	}

	report := result.(map[string]any)
	if report["ready"] != false {
		t.Error("an application with no disk chosen reported itself ready")
	}

	slots := report["storage"].([]AppStorageSlot)
	byID := map[string]AppStorageSlot{}
	for _, slot := range slots {
		byID[slot.ID] = slot
	}

	if !byID["config"].Ready {
		t.Error("private storage was reported as not ready")
	}
	if byID["media"].Ready {
		t.Error("user-selected storage with no disk chosen was reported as ready")
	}
	if byID["media"].Location != "" {
		t.Errorf("an unassigned slot named a location: %q", byID["media"].Location)
	}
}

// Assigning a disk that is not connected is refused at the point of choosing,
// while the user is looking at it — rather than letting them set up something
// that cannot work and find out later.
func TestAssigningADisconnectedDiskIsRefused(t *testing.T) {
	storage := storageServices(t)
	if err := storage.save([]Location{
		{ID: "media", Name: "Films drive", UUID: "not-connected-0000"},
	}); err != nil {
		t.Fatal(err)
	}

	err := storage.Assign("jellyfin", "media", "media", "jellyfin")
	if err == nil {
		t.Fatal("a disconnected disk was accepted")
	}
	var hostErr *Error
	if !asHostError(err, &hostErr) || hostErr.Code != "storage.disk_not_connected" {
		t.Errorf("got %v", err)
	}
}

func TestAssignmentsSurviveARestart(t *testing.T) {
	root := t.TempDir() + "/storage"
	state := t.TempDir() + "/state"

	before := NewStorageServices(root, state)
	if err := before.saveAssignments([]Assignment{
		{App: "jellyfin", StorageID: "media", Location: "usb", Subdirectory: "jellyfin"},
	}); err != nil {
		t.Fatal(err)
	}

	after := NewStorageServices(root, state)
	assignments := after.Assignments("jellyfin")
	if len(assignments) != 1 || assignments["media"].Location != "usb" {
		t.Fatalf("got %+v", assignments)
	}

	// And one application's assignments do not answer for another's.
	if len(after.Assignments("filebrowser")) != 0 {
		t.Error("another application inherited an assignment")
	}
}

// writeCatalogueFile puts a manifest where a catalogue will find it.
func writeCatalogueFile(t *testing.T, catalogue *Catalogue, name string, manifest map[string]any) {
	t.Helper()

	body, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(catalogue.dir, name), body, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := catalogue.Load(); err != nil {
		t.Fatal(err)
	}
}

// Hardware somebody does not have must not stop an application running.
//
// Jellyfin declares the "dri" device for hardware video acceleration. Docker
// refuses to create a container whose device is missing, so on any machine
// without a graphics card — every virtual machine, and plenty of old laptops —
// Jellyfin was created and then would not start:
//
//	error gathering device information while adding custom device "/dev/dri":
//	no such file or directory
//
// The dashboard reported that as "Stopped unexpectedly", which is true and
// useless: it never started, and no amount of pressing start would change that.
func TestADeviceThatIsNotOnThisMachineIsLeftOut(t *testing.T) {
	s := appServices(t)

	manifest := Manifest{ManifestVersion: 1, ID: "player", Name: "Player"}
	manifest.Container.Image = "example/player"
	manifest.Container.Version = "1.0.0"
	manifest.Network.InternalPort = 8096
	manifest.Permissions.Devices = []string{"dri", "dvb"}

	config := s.buildContainer(manifest, nil, owner{uid: 900, gid: 900})

	for _, device := range config.HostConfig.Devices {
		if _, err := os.Stat(device.PathOnHost); err != nil {
			t.Errorf("passed through %s, which is not on this machine: the "+
				"container will not start", device.PathOnHost)
		}
	}
}

// …and hardware that is present is still passed through, or asking for it would
// be decoration.
func TestADeviceThatExistsIsPassedThrough(t *testing.T) {
	s := appServices(t)

	// /dev/null is on every machine, so it stands in for a device that is.
	original := deviceePaths["dri"]
	deviceePaths["dri"] = "/dev/null"
	t.Cleanup(func() { deviceePaths["dri"] = original })

	manifest := Manifest{ManifestVersion: 1, ID: "player", Name: "Player"}
	manifest.Container.Image = "example/player"
	manifest.Container.Version = "1.0.0"
	manifest.Network.InternalPort = 8096
	manifest.Permissions.Devices = []string{"dri"}

	config := s.buildContainer(manifest, nil, owner{uid: 900, gid: 900})

	if len(config.HostConfig.Devices) != 1 {
		t.Fatalf("got %d devices, want the one that exists", len(config.HostConfig.Devices))
	}
	if got := config.HostConfig.Devices[0].PathOnHost; got != "/dev/null" {
		t.Errorf("passed through %q", got)
	}
}

// testOwner is whoever is running the tests. These run unprivileged, so the only
// ownership a chown can succeed in setting is the one the files already have.
func testOwner() owner { return owner{uid: os.Getuid(), gid: os.Getgid()} }

// --- Supplementary groups -------------------------------------------------------

// Passing a device through is not the same as being able to open it. The render
// nodes are root:render and the cards are root:video; a container that is a
// member of neither is refused by all of them.
//
// Jellyfin declared /dev/dri, was given /dev/dri, and could not open it —
// hardware transcoding was impossible on every installation while the manifest
// said it was available. Nothing failed: ffmpeg fell back to the processor and
// the only symptom was a hot laptop.
func TestAnApplicationGivenADeviceCanOpenIt(t *testing.T) {
	// The groups exist on any Linux with a modern udev, which is every machine
	// Homebase installs on. Skipped rather than failed where they do not, so
	// this does not break on a container image without them.
	if _, err := user.LookupGroup("render"); err != nil {
		t.Skip("no render group on this machine")
	}
	render, _ := user.LookupGroup("render")

	groups := supplementaryGroups(Manifest{
		Permissions: ManifestPermissions{Devices: []string{"dri"}},
	})
	if !slices.Contains(groups, render.Gid) {
		t.Errorf("an application given /dev/dri joins %v, which does not include "+
			"the render group (%s) that owns the node", groups, render.Gid)
	}
}

// Nothing is granted that a manifest did not ask for. This decides what access
// a declared permission actually confers; it is not a place where permissions
// are invented.
func TestAnApplicationThatAsksForNothingGetsNoGroups(t *testing.T) {
	groups := supplementaryGroups(Manifest{
		Storage: []ManifestStorage{{ID: "config", Type: "private"}},
	})
	if len(groups) != 0 {
		t.Errorf("an application with private storage and no devices joined %v", groups)
	}
}

// User-selected storage is shared with whoever else writes into it — the file
// server, the backup — so it belongs to the service group rather than to the
// application.
func TestAnApplicationWithASharedFolderJoinsTheServiceGroup(t *testing.T) {
	if _, err := user.LookupGroup(serviceAccount); err != nil {
		t.Skip("no " + serviceAccount + " group on this machine")
	}
	service, _ := user.LookupGroup(serviceAccount)

	groups := supplementaryGroups(Manifest{
		Storage: []ManifestStorage{{ID: "media", Type: "user-selected"}},
	})
	if !slices.Contains(groups, service.Gid) {
		t.Errorf("got %v, which does not include the %s group (%s)",
			groups, serviceAccount, service.Gid)
	}
}

// An application given a folder somebody else also writes into runs with the
// service group as its *primary* group, not merely as a supplementary one.
//
// The difference is what a new file gets. A supplementary group lets a process
// read what the group owns; it does not change what the process creates, because
// a new file takes the writer's primary group. So qBittorrent writing into the
// shared downloads folder produced files owned by qBittorrent's own group —
// which Jellyfin could read and not delete, and the file server could read and
// not replace. The error was "Access to the path '/media/downloads/shows' is
// denied", from an application that had been given exactly that path.
func TestAnApplicationSharingAFolderCreatesFilesInTheServiceGroup(t *testing.T) {
	group, err := user.LookupGroup(serviceAccount)
	if err != nil {
		t.Skip("no " + serviceAccount + " group on this machine")
	}
	gid, err := strconv.Atoi(group.Gid)
	if err != nil {
		t.Fatal(err)
	}

	services := &AppServices{stateDir: t.TempDir()}

	shared, err := services.effectiveOwner(Manifest{
		ID:      "sharing-app",
		Storage: []ManifestStorage{{ID: "media", Type: "user-selected"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if shared.gid != gid {
		t.Errorf("primary group %d, want the service group %d — anything else and "+
			"files it creates are unreachable by everything else that shares the folder",
			shared.gid, gid)
	}

	// And an application with nothing shared keeps a group of its own, because
	// nothing else has any business in its files.
	private, err := services.effectiveOwner(Manifest{
		ID:      "private-app",
		Storage: []ManifestStorage{{ID: "config", Type: "private"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if private.gid == gid {
		t.Error("an application with only private storage was put in the shared group")
	}
	if private.gid != private.uid {
		t.Errorf("private application group %d, want its own %d", private.gid, private.uid)
	}

	// Two applications still have distinct accounts — the shared group is a
	// group, not a shared identity.
	other, _ := services.effectiveOwner(Manifest{
		ID:      "another-app",
		Storage: []ManifestStorage{{ID: "media", Type: "user-selected"}},
	})
	if other.uid == shared.uid {
		t.Error("two applications were given the same account")
	}
}
