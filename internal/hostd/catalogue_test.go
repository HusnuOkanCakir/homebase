package hostd

import (
	"encoding/json"
	"os"
	"path/filepath"
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
