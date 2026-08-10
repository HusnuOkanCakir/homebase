package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/HusnuOkanCakir/homebase/internal/auth"
	"github.com/HusnuOkanCakir/homebase/internal/hostclient"
	"github.com/HusnuOkanCakir/homebase/internal/jobs"
)

func storageHarness(t *testing.T) (*harness, *fakeHostd) {
	t.Helper()

	h, fake := newAppHarness(t)
	fake.responses["storage.list_locations"] = map[string]any{
		"locations": []any{map[string]any{
			"id": "media", "name": "Films drive", "uuid": "abcd-1234",
			"mount_point": "/srv/homebase/storage/media",
			"connected":   true, "mounted": true, "read_only": false,
			"total_bytes": 2_000_000_000, "available_bytes": 1_500_000_000,
			"added_at": "2026-08-09T00:00:00Z",
		}},
	}
	return h, fake
}

// --- The property that matters -------------------------------------------------

// core must never be able to name a place on the filesystem.
//
// ADR-0013 as a test: a disk is named by filesystem UUID, a location by its id.
// A device path is not a stable name for anything, so an API that accepted one
// would be inviting a client to act on whichever disk holds that name today.
func TestCoreNeverSendsAMountPointToHostd(t *testing.T) {
	h, fake := storageHarness(t)
	headers := h.signedIn(t)

	fake.responses["storage.add_location"] = map[string]any{"id": "media"}
	fake.responses["storage.mount"] = map[string]any{"id": "media"}
	fake.responses["storage.unmount"] = map[string]any{"id": "media"}
	fake.responses["storage.remove_location"] = map[string]any{"id": "media"}

	// A client trying to smuggle a path through.
	h.do("POST", "/api/v1/storage/locations",
		`{"uuid":"abcd-1234","id":"media","mount_point":"/etc"}`, headers)
	h.do("POST", "/api/v1/storage/locations",
		`{"uuid":"abcd-1234","id":"media","name":"Films"}`, headers)
	h.do("POST", "/api/v1/storage/locations/media/mount", "", headers)
	h.do("POST", "/api/v1/storage/locations/media/unmount",
		`{"confirm":"media"}`, headers)

	for _, call := range fake.calls {
		if !strings.HasPrefix(call.Operation, "storage.") {
			continue
		}
		for _, forbidden := range []string{
			"mount_point", "path", "device", "where", "target", "options", "root",
		} {
			if _, present := call.Body[forbidden]; present {
				t.Errorf("%s carried %q to hostd: %v", call.Operation, forbidden, call.Body)
			}
		}
	}
}

// --- Formatting ----------------------------------------------------------------

// The one operation that can destroy data Homebase never created.
func TestFormattingRequiresNamingTheDisk(t *testing.T) {
	h, fake := storageHarness(t)
	headers := h.signedIn(t)

	fake.responses["storage.format"] = map[string]any{"uuid": "new-uuid"}

	for _, body := range []string{
		`{"uuid":"abcd-1234"}`,                       // no confirmation at all
		`{"uuid":"abcd-1234","confirm":""}`,          // an empty one
		`{"uuid":"abcd-1234","confirm":"yes"}`,       // a word anybody would type
		`{"uuid":"abcd-1234","confirm":"ABCD-1234"}`, // nearly right
		`{"uuid":"abcd-1234","confirm":"efgh-5678"}`, // a different disk
	} {
		rec := h.do("POST", "/api/v1/storage/format", body, headers)
		if rec.Code == http.StatusAccepted {
			t.Errorf("%s was accepted as confirmation", body)
		}
	}

	if calls := fake.callsTo("storage.format"); len(calls) != 0 {
		t.Errorf("%d disks were erased without a confirmation", len(calls))
	}
}

func TestFormattingWithoutNamingADiskAtAllIsRejected(t *testing.T) {
	h, fake := storageHarness(t)
	headers := h.signedIn(t)

	rec := h.do("POST", "/api/v1/storage/format", `{"confirm":"anything"}`, headers)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
	if calls := fake.callsTo("storage.format"); len(calls) != 0 {
		t.Error("a format with no target reached hostd")
	}
}

func TestAConfirmedFormatForwardsTheConfirmation(t *testing.T) {
	h, fake := storageHarness(t)
	headers := h.signedIn(t)

	fake.responses["storage.format"] = map[string]any{
		"uuid": "new-uuid", "filesystem": "ext4",
	}

	rec := h.do("POST", "/api/v1/storage/format",
		`{"uuid":"abcd-1234","confirm":"abcd-1234","label":"Media"}`, headers)
	job := h.awaitJob(t, submittedJobID(t, rec), headers)

	if job.State != jobs.StateSucceeded {
		t.Fatalf("job state = %s, error = %+v", job.State, job.Error)
	}

	calls := fake.callsTo("storage.format")
	if len(calls) != 1 {
		t.Fatalf("storage.format called %d times", len(calls))
	}
	if calls[0].Body["confirm"] != "abcd-1234" {
		t.Errorf("the confirmation was not forwarded: %v", calls[0].Body)
	}
	// hostd checks it again, and cannot unless core says the user was asked.
	if !calls[0].Confirmed {
		t.Error("storage.format reached hostd without the confirmed header")
	}
}

// --- Removing a location --------------------------------------------------------

func TestRemovingALocationRequiresNamingIt(t *testing.T) {
	h, fake := storageHarness(t)
	headers := h.signedIn(t)

	fake.responses["storage.remove_location"] = map[string]any{"id": "media"}

	for _, body := range []string{``, `{}`, `{"confirm":"yes"}`, `{"confirm":"Media"}`} {
		rec := h.do("POST", "/api/v1/storage/locations/media/remove", body, headers)
		if rec.Code == http.StatusAccepted {
			t.Errorf("%q was accepted as confirmation", body)
		}
	}
	if calls := fake.callsTo("storage.remove_location"); len(calls) != 0 {
		t.Error("a location was removed without a confirmation")
	}
}

// Removing a location must say, in the job the user reads, that nothing was
// deleted. It is the whole difference between this and formatting.
func TestRemovingALocationSaysTheDataIsKept(t *testing.T) {
	h, fake := storageHarness(t)
	headers := h.signedIn(t)

	fake.responses["storage.remove_location"] = map[string]any{"id": "media"}

	rec := h.do("POST", "/api/v1/storage/locations/media/remove",
		`{"confirm":"media"}`, headers)
	job := h.awaitJob(t, submittedJobID(t, rec), headers)

	if job.State != jobs.StateSucceeded {
		t.Fatalf("job state = %s, error = %+v", job.State, job.Error)
	}
	if job.Message == nil || !strings.Contains(strings.ToLower(*job.Message), "still there") {
		t.Errorf("the message does not say the data was kept: %v", job.Message)
	}
}

func TestAnUnknownLocationIsRejectedBeforeAJobExists(t *testing.T) {
	h, fake := storageHarness(t)
	headers := h.signedIn(t)

	rec := h.do("POST", "/api/v1/storage/locations/not-a-location/remove",
		`{"confirm":"not-a-location"}`, headers)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404: %s", rec.Code, rec.Body.String())
	}
	if calls := fake.callsTo("storage.remove_location"); len(calls) != 0 {
		t.Error("removal was attempted for a location that does not exist")
	}
}

// --- Assigning storage to an application ----------------------------------------

func TestAssigningStorageSaysNothingIsMoved(t *testing.T) {
	h, fake := storageHarness(t)
	headers := h.signedIn(t)

	fake.responses["app.assign_storage"] = map[string]any{"id": "jellyfin"}

	rec := h.do("POST", "/api/v1/apps/hello-homebase/storage",
		`{"storage_id":"media","location":"media"}`, headers)
	job := h.awaitJob(t, submittedJobID(t, rec), headers)

	if job.State != jobs.StateSucceeded {
		t.Fatalf("job state = %s, error = %+v", job.State, job.Error)
	}

	// A user choosing a different disk needs to know their existing files do not
	// follow. Saying nothing here is how somebody concludes their data is gone.
	message := ""
	if job.Message != nil {
		message = strings.ToLower(*job.Message)
	}
	if !strings.Contains(message, "stays where it is") {
		t.Errorf("the message does not say existing data stays put: %q", message)
	}
	if !strings.Contains(message, "next time it starts") {
		t.Errorf("the message does not say when it takes effect: %q", message)
	}
}

func TestAssigningStorageNeedsBothFields(t *testing.T) {
	h, fake := storageHarness(t)
	headers := h.signedIn(t)

	for _, body := range []string{`{}`, `{"storage_id":"media"}`, `{"location":"media"}`} {
		rec := h.do("POST", "/api/v1/apps/hello-homebase/storage", body, headers)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s returned %d, want 400", body, rec.Code)
		}
	}
	if calls := fake.callsTo("app.assign_storage"); len(calls) != 0 {
		t.Error("an incomplete assignment reached hostd")
	}
}

// --- Permissions -----------------------------------------------------------------

// storage.read must not be enough to erase a disk. Read and write are separate
// permissions throughout, and this is the endpoint set where confusing them
// destroys somebody's photographs.
func TestReadPermissionCannotChangeStorage(t *testing.T) {
	h, fake := storageHarness(t)
	_ = h.signedIn(t)

	reader, err := h.auth.CreateUser(t.Context(), "reader", goodPassword,
		[]string{auth.PermStorageRead, auth.PermAppsRead})
	if err != nil {
		t.Fatal(err)
	}
	token, _, err := h.auth.CreateSession(t.Context(), reader.ID, "test")
	if err != nil {
		t.Fatal(err)
	}
	headers := map[string]string{"Authorization": "Bearer " + token}

	fake.responses["storage.list_disks"] = map[string]any{"disks": []any{}}

	if rec := h.do("GET", "/api/v1/storage/disks", "", headers); rec.Code != http.StatusOK {
		t.Fatalf("storage.read could not list disks: %d", rec.Code)
	}

	before := len(fake.calls)

	for _, request := range []struct{ path, body string }{
		{"/api/v1/storage/locations", `{"uuid":"abcd-1234","id":"media"}`},
		{"/api/v1/storage/locations/media/remove", `{"confirm":"media"}`},
		{"/api/v1/storage/locations/media/mount", ``},
		{"/api/v1/storage/locations/media/unmount", `{"confirm":"media"}`},
		{"/api/v1/storage/format", `{"uuid":"abcd-1234","confirm":"abcd-1234"}`},
	} {
		rec := h.do("POST", request.path, request.body, headers)
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s returned %d for a read-only user; want 403", request.path, rec.Code)
		}
	}

	if len(fake.calls) != before {
		t.Errorf("a read-only user caused %d privileged calls", len(fake.calls)-before)
	}
}

func TestStorageEndpointsRequireAuthentication(t *testing.T) {
	h, fake := storageHarness(t)

	for _, request := range []struct{ method, path string }{
		{"GET", "/api/v1/storage/disks"},
		{"GET", "/api/v1/storage/locations"},
		{"POST", "/api/v1/storage/locations"},
		{"POST", "/api/v1/storage/locations/media/remove"},
		{"POST", "/api/v1/storage/format"},
		{"GET", "/api/v1/apps/hello-homebase/storage"},
		{"POST", "/api/v1/apps/hello-homebase/storage"},
	} {
		rec := h.do(request.method, request.path, `{"confirm":"media"}`, nil)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s %s returned %d without a session; want 401",
				request.method, request.path, rec.Code)
		}
	}

	if len(fake.calls) != 0 {
		t.Errorf("unauthenticated requests reached hostd: %v", fake.calls)
	}
}

// --- What the client is told ------------------------------------------------------

// "Homebase cannot see the disks" and "there are no disks" must not look the
// same. One of them means somebody's storage is fine and Homebase is broken.
func TestHostdDownIsNotAnEmptyDiskList(t *testing.T) {
	h := newHarness(t) // points at a socket that does not exist
	headers := h.signedIn(t)

	rec := h.do("GET", "/api/v1/storage/disks", "", headers)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503: %s", rec.Code, rec.Body.String())
	}
}

// unreadable and blank travel out to the client separately, because the
// interface has to be able to refuse to offer erasure for one of them.
func TestUnreadableAndBlankVolumesAreDistinguishable(t *testing.T) {
	h, fake := storageHarness(t)
	headers := h.signedIn(t)

	fake.responses["storage.list_disks"] = map[string]any{
		"disks": []any{map[string]any{
			"device": "sdb", "path": "/dev/sdb", "size_bytes": 1000,
			"removable": true, "system": false,
			"volumes": []any{
				map[string]any{"device": "sdb1", "path": "/dev/sdb1",
					"size_bytes": 500, "unreadable": false},
				map[string]any{"device": "sdb2", "path": "/dev/sdb2",
					"size_bytes": 500, "unreadable": true},
			},
		}},
	}

	rec := h.do("GET", "/api/v1/storage/disks", "", headers)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}

	var body struct {
		Items []hostclient.Disk `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}

	volumes := body.Items[0].Volumes
	if volumes[0].Unreadable {
		t.Error("a readable blank volume was reported as unreadable")
	}
	if !volumes[1].Unreadable {
		t.Error("an unreadable volume arrived at the client as a blank one")
	}
}
