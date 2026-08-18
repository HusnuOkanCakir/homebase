package hostd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// A minimal manifest that loads, as the base for mutation.
func validManifest() map[string]any {
	return map[string]any{
		"manifest_version": 1,
		"id":               "test-app",
		"name":             "Test App",
		"container": map[string]any{
			"image":   "example/app",
			"version": "1.0.0",
		},
		"storage": []any{
			map[string]any{"id": "config", "type": "private", "mount_path": "/config"},
		},
		"health": map[string]any{"type": "none"},
	}
}

func writeCatalogue(t *testing.T, files map[string]any) *Catalogue {
	t.Helper()
	dir := t.TempDir()

	for name, content := range files {
		var body []byte
		switch v := content.(type) {
		case string:
			body = []byte(v)
		default:
			encoded, err := json.MarshalIndent(v, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			body = encoded
		}
		if err := os.WriteFile(filepath.Join(dir, name), body, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	catalogue := NewCatalogue(dir)
	if err := catalogue.Load(); err != nil {
		t.Fatalf("loading the catalogue: %v", err)
	}
	return catalogue
}

func TestLoadsAValidManifest(t *testing.T) {
	c := writeCatalogue(t, map[string]any{"test-app.json": validManifest()})

	manifest, ok := c.Lookup("test-app")
	if !ok {
		t.Fatalf("test-app did not load; rejected: %v", c.Rejected())
	}
	if manifest.Name != "Test App" {
		t.Errorf("name = %q", manifest.Name)
	}
	if len(c.Rejected()) != 0 {
		t.Errorf("unexpected rejections: %v", c.Rejected())
	}
}

// The set of installable applications is the set of manifests on disk. Nothing
// at runtime can add to it — that is the whole of ADR-0012, and this is the
// assertion that would notice if it stopped being true.
func TestOnlyManifestsOnDiskAreInstallable(t *testing.T) {
	c := writeCatalogue(t, map[string]any{"test-app.json": validManifest()})

	if ids := c.IDs(); len(ids) != 1 || ids[0] != "test-app" {
		t.Fatalf("ids = %v", ids)
	}
	for _, name := range []string{"jellyfin", "test-app-2", "", "../test-app", "TEST-APP"} {
		if _, ok := c.Lookup(name); ok {
			t.Errorf("%q resolved to a manifest that is not on disk", name)
		}
	}
}

// Each of these is a way a bad manifest could reach a running container. They
// get individual assertions rather than one vague "invalid" expectation, so a
// weakened check is visible.
func TestRejectsManifestsThatShouldNotRun(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(map[string]any)
		wantErr string
	}{
		{
			// A privileged container is a root shell on the host.
			name: "privileged container",
			mutate: func(m map[string]any) {
				m["permissions"] = map[string]any{"privileged": true}
			},
			wantErr: "privileged containers are not permitted",
		},
		{
			// An application that changes version underneath a user is one that
			// breaks with no explanation available to them.
			name: "floating tag",
			mutate: func(m map[string]any) {
				m["container"] = map[string]any{"image": "example/app", "version": "latest"}
			},
			wantErr: `must not be "latest"`,
		},
		{
			name: "unpinned image",
			mutate: func(m map[string]any) {
				m["container"] = map[string]any{"image": "example/app"}
			},
			wantErr: "needs a version or a digest",
		},
		{
			// A mount path that can climb out of where hostd puts it.
			name: "mount path escapes",
			mutate: func(m map[string]any) {
				m["storage"] = []any{map[string]any{
					"id": "config", "type": "private", "mount_path": "/config/../../etc",
				}}
			},
			wantErr: "must not contain ..",
		},
		{
			name: "relative mount path",
			mutate: func(m map[string]any) {
				m["storage"] = []any{map[string]any{
					"id": "config", "type": "private", "mount_path": "config",
				}}
			},
			wantErr: "absolute mount_path",
		},
		{
			// An application that cannot be checked cannot be managed.
			name:    "no health check",
			mutate:  func(m map[string]any) { delete(m, "health") },
			wantErr: "no health check",
		},
		{
			name: "http health check with no path",
			mutate: func(m map[string]any) {
				m["health"] = map[string]any{"type": "http"}
			},
			wantErr: "needs a path",
		},
		{
			name:    "no storage",
			mutate:  func(m map[string]any) { m["storage"] = []any{} },
			wantErr: "no storage declared",
		},
		{
			name: "unknown storage type",
			mutate: func(m map[string]any) {
				m["storage"] = []any{map[string]any{
					"id": "config", "type": "host-path", "mount_path": "/config",
				}}
			},
			wantErr: "unknown type",
		},
		{
			name: "duplicate storage ids",
			mutate: func(m map[string]any) {
				m["storage"] = []any{
					map[string]any{"id": "config", "type": "private", "mount_path": "/a"},
					map[string]any{"id": "config", "type": "private", "mount_path": "/b"},
				}
			},
			wantErr: "duplicate storage id",
		},
		{
			// The id becomes a directory name and a container name.
			name:    "id with a path separator",
			mutate:  func(m map[string]any) { m["id"] = "../escape" },
			wantErr: "not a valid application id",
		},
		{
			name:    "unsupported manifest version",
			mutate:  func(m map[string]any) { m["manifest_version"] = 2 },
			wantErr: "unsupported manifest_version",
		},
		{
			name: "capability with an invented risk level",
			mutate: func(m map[string]any) {
				m["capabilities"] = []any{map[string]any{
					"name": "test.thing", "risk": "harmless",
				}}
			},
			wantErr: "unknown risk",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			manifest := validManifest()
			tc.mutate(manifest)

			// Written under the id's own filename where the id is still valid, so
			// the rejection is for the reason under test rather than a mismatch.
			filename := "test-app.json"
			if id, ok := manifest["id"].(string); ok && id != "test-app" {
				filename = "test-app.json"
			}

			c := writeCatalogue(t, map[string]any{filename: manifest})

			if _, ok := c.Lookup("test-app"); ok {
				t.Fatal("the manifest was accepted and should not have been")
			}

			reasons := c.Rejected()
			reason, recorded := reasons[filename]
			if !recorded {
				t.Fatalf("rejected but not recorded; rejections: %v", reasons)
			}
			if !strings.Contains(reason, tc.wantErr) {
				t.Errorf("rejected for the wrong reason:\n got: %s\nwant substring: %q",
					reason, tc.wantErr)
			}
		})
	}
}

// Strict decoding: a misspelled key means the author intended something we did
// not read, and installing anyway would act on a manifest nobody wrote.
func TestRejectsUnknownFields(t *testing.T) {
	manifest := validManifest()
	manifest["permisions"] = map[string]any{"privileged": true} // sic

	c := writeCatalogue(t, map[string]any{"test-app.json": manifest})

	if _, ok := c.Lookup("test-app"); ok {
		t.Fatal("a manifest with an unknown field was accepted")
	}
	if reason := c.Rejected()["test-app.json"]; !strings.Contains(reason, "unknown field") {
		t.Errorf("reason = %q", reason)
	}
}

// The id names the directory the application's data lives in. A manifest whose
// id disagrees with its filename is ambiguous about which one identifies it.
func TestRejectsIDFilenameMismatch(t *testing.T) {
	manifest := validManifest()
	manifest["id"] = "something-else"

	c := writeCatalogue(t, map[string]any{"test-app.json": manifest})

	if _, ok := c.Lookup("something-else"); ok {
		t.Fatal("a manifest whose id disagrees with its filename was accepted")
	}
	if reason := c.Rejected()["test-app.json"]; !strings.Contains(reason, "does not match the filename") {
		t.Errorf("reason = %q", reason)
	}
}

// One bad manifest must not take the machine's other applications with it — but
// it must be visible, because a missing application is harder to diagnose than a
// rejected one with a reason.
func TestOneBadManifestDoesNotHideTheOthers(t *testing.T) {
	good := validManifest()
	good["id"] = "good-app"
	good["name"] = "Good App"

	c := writeCatalogue(t, map[string]any{
		"good-app.json": good,
		"broken.json":   "{ this is not json",
	})

	if _, ok := c.Lookup("good-app"); !ok {
		t.Error("the valid manifest was lost because another one was broken")
	}
	if reason, recorded := c.Rejected()["broken.json"]; !recorded {
		t.Error("the broken manifest was not recorded")
	} else if reason == "" {
		t.Error("recorded with no reason")
	}
}

func TestIgnoresNonManifestFiles(t *testing.T) {
	c := writeCatalogue(t, map[string]any{
		"test-app.json": validManifest(),
		"README.md":     "not a manifest",
		"notes.txt":     "also not a manifest",
	})

	if len(c.IDs()) != 1 {
		t.Errorf("ids = %v", c.IDs())
	}
	if len(c.Rejected()) != 0 {
		t.Errorf("non-manifest files were rejected rather than ignored: %v", c.Rejected())
	}
}

// A machine with no applications is a legitimate state, not an error.
func TestMissingCatalogueIsNotAnError(t *testing.T) {
	c := NewCatalogue(filepath.Join(t.TempDir(), "does-not-exist"))
	if err := c.Load(); err != nil {
		t.Fatalf("a missing catalogue should not be an error: %v", err)
	}
	if len(c.IDs()) != 0 {
		t.Error("ids returned from a missing catalogue")
	}
}

// A digest names the bytes; a tag names a label somebody can move. Where both
// are present the digest must win.
func TestDigestTakesPrecedenceOverTag(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)

	container := ManifestContainer{Image: "example/app", Version: "1.0.0", Digest: digest}
	if got := container.Reference(); got != "example/app@"+digest {
		t.Errorf("Reference() = %q, want the digest", got)
	}

	container = ManifestContainer{Image: "example/app", Version: "1.0.0"}
	if got := container.Reference(); got != "example/app:1.0.0" {
		t.Errorf("Reference() = %q", got)
	}
}

// --- The shipped catalogue ---------------------------------------------------

// The manifests this project actually ships must load in the code that will read
// them on a user's machine. CI validates them against the JSON Schema; this
// validates them against hostd, which is a different implementation of
// overlapping rules and could disagree.
func TestShippedCatalogueLoads(t *testing.T) {
	catalogue := NewCatalogue("../../app-store")
	if err := catalogue.Load(); err != nil {
		t.Fatalf("loading the shipped catalogue: %v", err)
	}

	if rejected := catalogue.Rejected(); len(rejected) > 0 {
		for name, reason := range rejected {
			t.Errorf("shipped manifest %s was rejected: %s", name, reason)
		}
	}

	for _, expected := range []string{"hello-homebase", "filebrowser", "jellyfin"} {
		manifest, ok := catalogue.Lookup(expected)
		if !ok {
			t.Errorf("%s is not in the shipped catalogue", expected)
			continue
		}
		if manifest.Summary == "" {
			t.Errorf("%s has no summary; it is what a user reads when choosing", expected)
		}
		// Every shipped application must be pinned. An unpinned one changes
		// underneath users between installs.
		if manifest.Container.Version == "" && manifest.Container.Digest == "" {
			t.Errorf("%s is not pinned to a version", expected)
		}
	}
}

// --- Publishing to the network ------------------------------------------------

// An application published onto the LAN must not be able to claim a port the
// server is already using. 443 is the one that matters: an application there
// would sit where the dashboard is served, so the thing that could undo it is
// the thing it replaced.
func TestAnApplicationCannotTakeAPortTheServerUses(t *testing.T) {
	for port, what := range map[int]string{443: "the dashboard", 80: "the redirect", 22: "ssh"} {
		manifest := validManifest()
		manifest["network"] = map[string]any{
			"internal_port":  8096,
			"protocol":       "http",
			"reachable_from": "network",
			"host_port":      port,
		}

		// The filename has to match the id, or the manifest is rejected for
		// that instead and this test passes without ever reaching the port.
		catalogue := writeCatalogue(t, map[string]any{"test-app.json": manifest})
		if _, ok := catalogue.Lookup("test-app"); ok {
			t.Errorf("an application was allowed to publish on %d (%s)", port, what)
		}
		reason := catalogue.Rejected()["test-app.json"]
		if !strings.Contains(reason, strconv.Itoa(port)) {
			t.Errorf("port %d rejected with %q, which does not name the port", port, reason)
		}
	}
}

// Publishing needs something to publish, and only a thing a browser can open.
func TestPublishingNeedsAWebPort(t *testing.T) {
	noPort := validManifest()
	noPort["network"] = map[string]any{"reachable_from": "network"}

	notWeb := validManifest()
	notWeb["network"] = map[string]any{
		"internal_port": 1900, "protocol": "udp", "reachable_from": "network",
	}

	for what, manifest := range map[string]any{"no port": noPort, "udp": notWeb} {
		catalogue := writeCatalogue(t, map[string]any{"test-app.json": manifest})
		if _, ok := catalogue.Lookup("test-app"); ok {
			t.Errorf("a manifest with %s was accepted", what)
		}
		// Rejected for the stated reason, not for something incidental.
		if reason := catalogue.Rejected()["test-app.json"]; !strings.Contains(reason, "network") {
			t.Errorf("%s rejected with %q, which is not about publishing", what, reason)
		}
	}
}

// The default is loopback. An application that says nothing about being
// reachable is not reachable — a manifest gaining a network by omission is the
// failure this default exists to prevent.
func TestAnApplicationIsNotPublishedUnlessItSaysSo(t *testing.T) {
	for _, network := range []map[string]any{
		{"internal_port": 8096, "protocol": "http"},
		{"internal_port": 8096, "protocol": "http", "reachable_from": "server"},
	} {
		manifest := validManifest()
		manifest["network"] = network

		catalogue := writeCatalogue(t, map[string]any{"test-app.json": manifest})
		app, ok := catalogue.Lookup("test-app")
		if !ok {
			t.Fatalf("rejected: %v", catalogue.Rejected())
		}
		if app.Network.PublishedToNetwork() {
			t.Errorf("%v was published to the network", network)
		}
	}
}

// The port on the server defaults to the port inside the container, and is
// overridden when the container's own port is one the server already uses —
// File Browser listens on 80, where Homebase is.
func TestThePublishedPortDefaultsToTheContainersOwn(t *testing.T) {
	if got := (ManifestNetwork{InternalPort: 8096}).PublishedPort(); got != 8096 {
		t.Errorf("published port %d, want 8096", got)
	}
	if got := (ManifestNetwork{InternalPort: 80, HostPort: 8080}).PublishedPort(); got != 8080 {
		t.Errorf("published port %d, want 8080", got)
	}
}

// The address has to say where, and a loopback address has to be distinguishable
// from one that works from another machine. Until these existed, an application
// installed, started, passed its health check and was reachable at an address
// nothing anywhere reported.
func TestTheAddressSaysWhereToOpenIt(t *testing.T) {
	published := Manifest{Network: ManifestNetwork{
		InternalPort: 8096, Protocol: "http", Path: "/", ReachableFrom: "network",
	}}
	url := appURL(published, 8096)
	if !strings.HasSuffix(url, ".local:8096/") || !strings.HasPrefix(url, "http://") {
		t.Errorf("published application URL is %q, want this machine's name and port", url)
	}
	if strings.Contains(url, "127.0.0.1") {
		t.Error("a published application was given a loopback address")
	}

	onlyHere := Manifest{Network: ManifestNetwork{
		InternalPort: 80, Protocol: "http", Path: "/",
	}}
	if url := appURL(onlyHere, 32768); url != "http://127.0.0.1:32768/" {
		t.Errorf("loopback application URL is %q", url)
	}

	// Nothing to open, and nothing a browser could open, produce no address at
	// all rather than one that does not work.
	if url := appURL(published, 0); url != "" {
		t.Errorf("an application with no port was given the address %q", url)
	}
	dlna := Manifest{Network: ManifestNetwork{InternalPort: 1900, Protocol: "udp"}}
	if url := appURL(dlna, 1900); url != "" {
		t.Errorf("a UDP service was given the browser address %q", url)
	}
}

// Two applications cannot answer on the same port.
//
// Each manifest is valid alone, so the collision exists only across the
// catalogue — which is the only place it can be seen. Without this check the
// second application fails at container creation with a message from Docker
// about an address in use, on a machine where the symptom is that the *first*
// application stopped working.
func TestTwoApplicationsCannotPublishTheSamePort(t *testing.T) {
	first := validManifest()
	first["id"] = "first-app"
	first["name"] = "First"
	first["network"] = map[string]any{
		"internal_port": 8096, "protocol": "http", "reachable_from": "network",
	}

	second := validManifest()
	second["id"] = "second-app"
	second["name"] = "Second"
	second["network"] = map[string]any{
		// A different port inside its container, published on the same one.
		"internal_port": 80, "protocol": "http", "reachable_from": "network",
		"host_port": 8096,
	}

	catalogue := writeCatalogue(t, map[string]any{
		"first-app.json": first, "second-app.json": second,
	})

	loaded := 0
	for _, id := range []string{"first-app", "second-app"} {
		if _, ok := catalogue.Lookup(id); ok {
			loaded++
		}
	}
	if loaded != 1 {
		t.Fatalf("%d of the two applications loaded, want exactly one", loaded)
	}
	reasons := catalogue.Rejected()
	if len(reasons) != 1 {
		t.Fatalf("got %d rejections, want 1: %v", len(reasons), reasons)
	}
	for name, reason := range reasons {
		if !strings.Contains(reason, "8096") {
			t.Errorf("%s was rejected with %q, which does not name the port", name, reason)
		}
	}
}

// An application on loopback claims no port on the machine — Docker picks a
// free one — so two of them are not a collision.
func TestApplicationsOnLoopbackDoNotCollide(t *testing.T) {
	manifests := map[string]any{}
	for _, id := range []string{"one-app", "two-app"} {
		manifest := validManifest()
		manifest["id"] = id
		manifest["name"] = id
		manifest["network"] = map[string]any{"internal_port": 8080, "protocol": "http"}
		manifests[id+".json"] = manifest
	}

	catalogue := writeCatalogue(t, manifests)
	if reasons := catalogue.Rejected(); len(reasons) != 0 {
		t.Errorf("two loopback applications were treated as a collision: %v", reasons)
	}
}

// --- Applications made of more than one container --------------------------------

// A supporting container is never reachable from anywhere but its application.
// There is deliberately no manifest field with which to ask for a port on one:
// a database a manifest *could* publish is a database somebody publishes.
func TestASupportingContainerCannotBePublished(t *testing.T) {
	manifest := validManifest()
	manifest["services"] = []any{
		map[string]any{
			"name": "database", "image": "docker.io/postgres", "version": "16-alpine",
		},
	}
	catalogue := writeCatalogue(t, map[string]any{"test-app.json": manifest})
	app, ok := catalogue.Lookup("test-app")
	if !ok {
		t.Fatalf("rejected: %v", catalogue.Rejected())
	}
	if len(app.Services) != 1 {
		t.Fatalf("got %d services", len(app.Services))
	}

	// The schema is the enforcement, and this is the assertion that it stays
	// that way: nothing on a service describes a port.
	encoded, err := json.Marshal(app.Services[0])
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"port", "host_port", "reachable"} {
		if strings.Contains(strings.ToLower(string(encoded)), forbidden) {
			t.Errorf("a service can describe %q: %s", forbidden, encoded)
		}
	}
}

// The name becomes a hostname on the private network, so two of them cannot be
// the same and neither can be the application's — both would answer, and which
// one answered would be a matter of chance.
func TestServiceNamesMustBeDistinctAndUsableAsHostnames(t *testing.T) {
	for what, services := range map[string][]any{
		"duplicates": {
			map[string]any{"name": "database", "image": "docker.io/postgres", "version": "16"},
			map[string]any{"name": "database", "image": "docker.io/redis", "version": "7"},
		},
		"the application's own name": {
			map[string]any{"name": "test-app", "image": "docker.io/postgres", "version": "16"},
		},
		"a floating tag": {
			map[string]any{"name": "database", "image": "docker.io/postgres", "version": "latest"},
		},
		"no version at all": {
			map[string]any{"name": "database", "image": "docker.io/postgres"},
		},
	} {
		manifest := validManifest()
		manifest["services"] = services
		catalogue := writeCatalogue(t, map[string]any{"test-app.json": manifest})
		if _, ok := catalogue.Lookup("test-app"); ok {
			t.Errorf("a manifest with %s was accepted", what)
		}
	}
}

// An application and its supporting containers share a network of their own, and
// the names are derived rather than taken from anywhere a caller could reach.
func TestTheNetworkAndContainersAreNamedAfterTheApplication(t *testing.T) {
	if got := networkName("immich"); got != "homebase-immich" {
		t.Errorf("network name %q", got)
	}
	if got := serviceContainerName("immich", "database"); got != "homebase-immich-database" {
		t.Errorf("service container name %q", got)
	}
	// Everything belonging to one application sorts together, which is what
	// somebody scanning `docker ps` is relying on.
	if !strings.HasPrefix(serviceContainerName("immich", "database"), containerName("immich")) {
		t.Error("a service container does not sort with its application")
	}
}

// --- Images that cannot run as an arbitrary user -----------------------------------

// The reason must be substantial enough for a reviewer to check against the
// image's own entrypoint. A blank one is how a relaxation becomes the default:
// the field spreads by copying and nobody can tell which applications genuinely
// need it.
func TestStartingAsRootNeedsAWrittenReason(t *testing.T) {
	for what, reason := range map[string]string{
		"empty":      "",
		"too short":  "needs root",
		"whitespace": "                                                    ",
	} {
		manifest := validManifest()
		manifest["permissions"] = map[string]any{
			"starts_as_root": map[string]any{"reason": reason},
		}
		catalogue := writeCatalogue(t, map[string]any{"test-app.json": manifest})
		if _, ok := catalogue.Lookup("test-app"); ok {
			t.Errorf("a %s reason was accepted", what)
		}
	}

	manifest := validManifest()
	manifest["permissions"] = map[string]any{
		"starts_as_root": map[string]any{
			"reason": "The entrypoint runs as root to chown the configuration " +
				"directory, then drops to its own account with s6. It has no " +
				"option to run as an arbitrary user.",
		},
	}
	catalogue := writeCatalogue(t, map[string]any{"test-app.json": manifest})
	app, ok := catalogue.Lookup("test-app")
	if !ok {
		t.Fatalf("a proper reason was rejected: %v", catalogue.Rejected())
	}
	if app.Permissions.StartsAsRoot == nil {
		t.Error("the declaration did not survive loading")
	}
}

// The grant is exactly five capabilities and they are named. Whatever else this
// mechanism becomes, it must not become a way to reach NET_ADMIN or SYS_ADMIN —
// which are what a container would need to start interfering with the machine
// rather than with its own files.
func TestStartingAsRootGrantsOnlyTheFiveItNeeds(t *testing.T) {
	want := map[string]bool{
		"CHOWN": true, "DAC_OVERRIDE": true, "FOWNER": true,
		"SETUID": true, "SETGID": true,
	}
	if len(rootCapabilities) != len(want) {
		t.Fatalf("granted %v, want exactly the five", rootCapabilities)
	}
	for _, capability := range rootCapabilities {
		if !want[capability] {
			t.Errorf("%q is granted and should not be", capability)
		}
	}
	for _, forbidden := range []string{"SYS_ADMIN", "NET_ADMIN", "NET_RAW", "SYS_PTRACE"} {
		if slices.Contains(rootCapabilities, forbidden) {
			t.Errorf("%q is reachable through starts_as_root", forbidden)
		}
	}
}

// An application that does not declare it keeps the default: its own uid, no
// capabilities at all.
func TestAnApplicationThatDoesNotAskStaysUnprivileged(t *testing.T) {
	manifest := validManifest()
	catalogue := writeCatalogue(t, map[string]any{"test-app.json": manifest})
	app, ok := catalogue.Lookup("test-app")
	if !ok {
		t.Fatal(catalogue.Rejected())
	}
	if app.Permissions.StartsAsRoot != nil {
		t.Error("an application that asked for nothing was given the elevation")
	}
}

// The constraint that makes permanent root tolerable must be enforced, not
// merely written down.
//
// runs_as_root grants uid 0 with DAC_OVERRIDE for the life of the container.
// Over a private directory Homebase created, that is bounded by the mount. Over
// a folder the user chose — their films, their photographs, the folder that is
// also an SMB share — it is root over somebody's data, permanently, from a file
// in the catalogue. So the combination is refused at load, which is before any
// of it can be installed.
func TestPermanentRootMayNotReachTheUsersOwnFolders(t *testing.T) {
	base := func() Manifest {
		return Manifest{
			ManifestVersion: 1,
			ID:              "example",
			Name:            "Example",
			Container:       ManifestContainer{Image: "example/app", Version: "1.0.0"},
			Health:          ManifestHealth{Type: "none"},
			Permissions: ManifestPermissions{
				RunsAsRoot: &ManifestElevation{
					Reason: "Its entrypoint writes into the image and never drops privileges, " +
						"and it ignores PUID and PGID entirely.",
				},
			},
		}
	}

	t.Run("private storage is allowed", func(t *testing.T) {
		m := base()
		m.Storage = []ManifestStorage{{ID: "config", Type: "private", MountPath: "/config"}}
		if err := m.Validate(); err != nil {
			t.Fatalf("a private-only application was refused: %v", err)
		}
	})

	t.Run("user-selected storage is refused", func(t *testing.T) {
		m := base()
		m.Storage = []ManifestStorage{
			{ID: "config", Type: "private", MountPath: "/config"},
			{ID: "media", Type: "user-selected", MountPath: "/data"},
		}
		err := m.Validate()
		if err == nil {
			t.Fatal("an application that is root for ever was given a folder the user chose")
		}
		if !strings.Contains(err.Error(), "media") {
			t.Errorf("the error does not name the offending slot: %v", err)
		}
	})

	t.Run("a reason is required", func(t *testing.T) {
		m := base()
		m.Storage = []ManifestStorage{{ID: "config", Type: "private", MountPath: "/config"}}
		m.Permissions.RunsAsRoot = &ManifestElevation{Reason: "because"}
		if err := m.Validate(); err == nil {
			t.Fatal("permanent root was granted with no reason worth reading")
		}
	})

	t.Run("the two elevations are alternatives", func(t *testing.T) {
		m := base()
		m.Storage = []ManifestStorage{{ID: "config", Type: "private", MountPath: "/config"}}
		m.Permissions.StartsAsRoot = &ManifestElevation{
			Reason: "The linuxserver.io entrypoint corrects ownership and then drops to PUID and PGID.",
		}
		if err := m.Validate(); err == nil {
			t.Fatal("a manifest claimed its entrypoint both does and does not drop privileges")
		}
	})
}

// Whichever way it is declared, the container is built the same. A second
// elevation that quietly built a *different* container would mean the reasoning
// attached to one of them describes something that is not running.
func TestBothElevationsBuildTheSameContainer(t *testing.T) {
	reason := "Its entrypoint needs to write to root-owned paths before anything starts, " +
		"which an unprivileged uid cannot do."

	starts := ManifestPermissions{StartsAsRoot: &ManifestElevation{Reason: reason}}
	runs := ManifestPermissions{RunsAsRoot: &ManifestElevation{Reason: reason}}

	if !starts.Elevated() || !runs.Elevated() {
		t.Fatal("an elevation that does not report itself as one is invisible to the code that acts on it")
	}
	if (ManifestPermissions{}).Elevated() {
		t.Error("an application declaring nothing was reported as elevated")
	}
}

// Port 53 is reserved because the machine resolves names on it. Exactly one
// application may take it, and only by saying that is what it is for.
func TestOnlyANameServerMayTakePort53(t *testing.T) {
	base := func() Manifest {
		return Manifest{
			ManifestVersion: 1,
			ID:              "example",
			Name:            "Example",
			Container:       ManifestContainer{Image: "example/app", Version: "1.0.0"},
			Health:          ManifestHealth{Type: "none"},
			Storage:         []ManifestStorage{{ID: "config", Type: "private", MountPath: "/config"}},
			Network: ManifestNetwork{
				InternalPort:  3000,
				HostPort:      3080,
				Protocol:      "http",
				ReachableFrom: "network",
			},
		}
	}

	t.Run("a name server may", func(t *testing.T) {
		m := base()
		m.Provides = "dns"
		m.Network.ExtraPorts = []ManifestPort{
			{InternalPort: 53, Protocol: "udp", Purpose: "the resolver itself"},
		}
		if err := m.Validate(); err != nil {
			t.Fatalf("a declared name server was refused port 53: %v", err)
		}
	})

	t.Run("anything else may not", func(t *testing.T) {
		m := base()
		m.Network.ExtraPorts = []ManifestPort{
			{InternalPort: 53, Protocol: "udp", Purpose: "no reason given at all"},
		}
		if err := m.Validate(); err == nil {
			t.Fatal("an ordinary application took the port the machine resolves names on")
		}
	})

	t.Run("the exception unlocks one port and not the list", func(t *testing.T) {
		for _, port := range []int{22, 80, 443} {
			m := base()
			m.Provides = "dns"
			m.Network.ExtraPorts = []ManifestPort{
				{InternalPort: 53, Protocol: "udp", Purpose: "the resolver itself"},
				{InternalPort: port, Purpose: "something it should not have"},
			}
			if err := m.Validate(); err == nil {
				t.Errorf("a name server was also given port %d", port)
			}
		}
	})

	t.Run("claiming it without using it is refused", func(t *testing.T) {
		m := base()
		m.Provides = "dns"
		if err := m.Validate(); err == nil {
			t.Fatal("an application claimed to be the resolver while publishing nothing on 53")
		}
	})

	t.Run("an extra port on a loopback application is not published", func(t *testing.T) {
		m := base()
		m.Network.ReachableFrom = ""
		m.Network.ExtraPorts = []ManifestPort{
			{InternalPort: 53, Protocol: "udp", Purpose: "the resolver itself"},
		}
		// Not an error — it is simply not published, and publishedPorts is what
		// every check reads. An unpublished port cannot collide or be reserved.
		if got := m.publishedPorts(); len(got) != 0 {
			t.Errorf("a loopback application published %v", got)
		}
	})

	t.Run("the same port on both protocols is two bindings, not a duplicate", func(t *testing.T) {
		m := base()
		m.Provides = "dns"
		m.Network.ExtraPorts = []ManifestPort{
			{InternalPort: 53, Protocol: "udp", Purpose: "the resolver itself"},
			{InternalPort: 53, Protocol: "tcp", Purpose: "answers too large for a datagram"},
		}
		if err := m.Validate(); err != nil {
			t.Fatalf("53/udp and 53/tcp were read as the same port: %v", err)
		}
	})

	t.Run("but the same port and protocol twice is", func(t *testing.T) {
		m := base()
		m.Provides = "dns"
		m.Network.ExtraPorts = []ManifestPort{
			{InternalPort: 53, Protocol: "udp", Purpose: "the resolver itself"},
			{InternalPort: 53, Protocol: "udp", Purpose: "the resolver, again"},
		}
		if err := m.Validate(); err == nil {
			t.Fatal("the same binding was accepted twice")
		}
	})
}

// A manifest may not name a capability that is not on the list, and hostd must
// be the one refusing it — the schema is checked in CI, and CI is not what runs
// on somebody's server.
func TestCapabilitiesAreCheckedByHostdAndNotOnlyBySchema(t *testing.T) {
	m := Manifest{
		ManifestVersion: 1,
		ID:              "example",
		Name:            "Example",
		Container:       ManifestContainer{Image: "example/app", Version: "1.0.0"},
		Health:          ManifestHealth{Type: "none"},
		Storage:         []ManifestStorage{{ID: "config", Type: "private", MountPath: "/config"}},
	}

	m.Permissions.Capabilities = []string{"NET_BIND_SERVICE"}
	if err := m.Validate(); err != nil {
		t.Fatalf("the one capability an application is expected to need was refused: %v", err)
	}

	for _, capability := range []string{"ALL", "SYS_PTRACE", "SYS_MODULE", "", "cap_sys_admin"} {
		m.Permissions.Capabilities = []string{capability}
		if err := m.Validate(); err == nil {
			t.Errorf("a manifest was granted %q", capability)
		}
	}
}
