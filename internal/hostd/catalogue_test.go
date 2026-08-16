package hostd

import (
	"encoding/json"
	"os"
	"path/filepath"
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
