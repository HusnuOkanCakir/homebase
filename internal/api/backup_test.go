package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/HusnuOkanCakir/homebase/internal/auth"
	"github.com/HusnuOkanCakir/homebase/internal/events"
	"github.com/HusnuOkanCakir/homebase/internal/jobs"
)

func backupHarness(t *testing.T) (*harness, *fakeHostd) {
	t.Helper()

	h, fake := newAppHarness(t)
	fake.responses["backup.list"] = map[string]any{
		"backups": []any{map[string]any{
			"id": "2026-08-09-120000-abcdef01", "location": "spare",
			"created_at": "2026-08-09T12:00:00Z", "hostname": "the-server",
			"kind": "full", "files": 120, "total_bytes": 5_000_000, "complete": true,
		}},
	}
	return h, fake
}

const backupID = "2026-08-09-120000-abcdef01"

// --- Restoring ---------------------------------------------------------------------

// The third operation in Homebase that destroys data irreversibly — and the only
// one where what it overwrites is usually what somebody is trying to save.
func TestRestoringRequiresNamingTheBackup(t *testing.T) {
	h, fake := backupHarness(t)
	headers := h.signedIn(t)

	fake.responses["backup.restore"] = map[string]any{"id": backupID, "restored": 10}

	for _, body := range []string{
		``, `{}`, `{"confirm":""}`, `{"confirm":"yes"}`,
		`{"confirm":"restore"}`, `{"confirm":"2026-08-09"}`,
	} {
		rec := h.do("POST", "/api/v1/backups/"+backupID+"/restore?location=spare", body, headers)
		if rec.Code == http.StatusAccepted {
			t.Errorf("%q was accepted as confirmation", body)
		}
	}

	if calls := fake.callsTo("backup.restore"); len(calls) != 0 {
		t.Errorf("%d restores happened without a confirmation", len(calls))
	}
}

func TestAConfirmedRestoreForwardsTheConfirmation(t *testing.T) {
	h, fake := backupHarness(t)
	headers := h.signedIn(t)

	fake.responses["backup.restore"] = map[string]any{
		"id": backupID, "restored": 10, "skipped": 0,
	}

	rec := h.do("POST", "/api/v1/backups/"+backupID+"/restore?location=spare",
		`{"confirm":"`+backupID+`"}`, headers)
	job := h.awaitJob(t, submittedJobID(t, rec), headers)

	if job.State != jobs.StateSucceeded {
		t.Fatalf("job state = %s, error = %+v", job.State, job.Error)
	}

	calls := fake.callsTo("backup.restore")
	if len(calls) != 1 {
		t.Fatalf("backup.restore called %d times", len(calls))
	}
	if calls[0].Body["confirm"] != backupID {
		t.Errorf("the confirmation was not forwarded: %v", calls[0].Body)
	}
	// hostd checks it again, which it cannot do unless core says the user was
	// asked.
	if !calls[0].Confirmed {
		t.Error("backup.restore reached hostd without the confirmed header")
	}

	// The reassurance that makes restoring safe to agree to.
	message := ""
	if job.Message != nil {
		message = strings.ToLower(*job.Message)
	}
	if !strings.Contains(message, "nothing that was already on this server was deleted") {
		t.Errorf("the message does not say a restore is a merge: %q", message)
	}
}

// Previewing is its own endpoint rather than a flag, because a preview a client
// can forget to ask for is a preview nobody sees.
func TestPreviewingChangesNothing(t *testing.T) {
	h, fake := backupHarness(t)
	headers := h.signedIn(t)

	fake.responses["backup.preview"] = map[string]any{
		"id": backupID, "location": "spare", "verified": true,
		"would_overwrite": 4, "files_to_write": 120,
		"message": "Taken on 9 August 2026. 4 files would be replaced.",
	}

	rec := h.do("GET", "/api/v1/backups/"+backupID+"/preview?location=spare", "", headers)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}

	// Read-only: no job, and nothing that writes.
	for _, call := range fake.calls {
		if call.Operation == "backup.restore" || call.Operation == "backup.create" {
			t.Errorf("previewing performed %s", call.Operation)
		}
	}

	var preview struct {
		WouldOverwrite int  `json:"would_overwrite"`
		Verified       bool `json:"verified"`
	}
	json.Unmarshal(rec.Body.Bytes(), &preview)
	if preview.WouldOverwrite != 4 {
		t.Errorf("would_overwrite = %d", preview.WouldOverwrite)
	}
}

// --- Verification ------------------------------------------------------------------

// A damaged backup is a *failed* job, not a successful one with a footnote.
// Somebody skimming the job list must not read it as fine.
func TestADamagedBackupFailsTheJob(t *testing.T) {
	h, fake := backupHarness(t)
	headers := h.signedIn(t)

	fake.responses["backup.verify"] = map[string]any{
		"id": backupID, "valid": false, "files_checked": 118,
		"corrupt": []any{"apps/jellyfin/library.db"},
		"missing": []any{"data/media/film.mkv"},
		"message": "1 file is missing and 1 has been damaged. This backup cannot be relied on.",
	}

	rec := h.do("POST", "/api/v1/backups/"+backupID+"/verify?location=spare", "", headers)
	job := h.awaitJob(t, submittedJobID(t, rec), headers)

	if job.State != jobs.StateFailed {
		t.Fatalf("a damaged backup produced a %s job", job.State)
	}
	if job.Error == nil || job.Error.Code != "backup.damaged" {
		t.Fatalf("error = %+v", job.Error)
	}
	// It has to name what is wrong, and what to do.
	if !strings.Contains(job.Error.Detail, "library.db") {
		t.Errorf("the detail does not name the damaged file: %q", job.Error.Detail)
	}
	if !strings.Contains(job.Error.Recovery, "disk") {
		t.Errorf("the recovery advice does not mention the disk: %q", job.Error.Recovery)
	}
}

// A backup that fails must be recorded at error severity. The failure this
// guards against is backups stopping and nobody noticing for eight months.
func TestAFailedBackupIsRecordedAsAnError(t *testing.T) {
	h, fake := backupHarness(t)
	headers := h.signedIn(t)

	fake.failures["backup.create"] = hostclientError(
		"backup.destination_not_connected",
		"Spare drive is not connected, so Homebase cannot back up to it.",
		http.StatusConflict)

	rec := h.do("POST", "/api/v1/backups", `{"location":"spare"}`, headers)
	h.awaitJob(t, submittedJobID(t, rec), headers)

	list, err := h.events.List(t.Context(), events.Query{Severity: events.SeverityError})
	if err != nil {
		t.Fatal(err)
	}

	for _, event := range list {
		if event.Type == "backup_failed" {
			if event.Reason == nil || *event.Reason != "backup.destination_not_connected" {
				t.Errorf("reason = %v", event.Reason)
			}
			return
		}
	}
	t.Errorf("a failed backup raised no error event; got %d events", len(list))
}

// Restoring is recorded above 'info' even when it succeeds: it is a large
// irreversible change, and somebody reading the history months later has to be
// able to find it.
func TestASuccessfulRestoreIsRecordedAboveInfo(t *testing.T) {
	h, fake := backupHarness(t)
	headers := h.signedIn(t)

	fake.responses["backup.restore"] = map[string]any{"id": backupID, "restored": 10}

	rec := h.do("POST", "/api/v1/backups/"+backupID+"/restore?location=spare",
		`{"confirm":"`+backupID+`"}`, headers)
	h.awaitJob(t, submittedJobID(t, rec), headers)

	list, err := h.events.List(t.Context(), events.Query{})
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range list {
		if event.Type == "backup_restored" {
			if event.Severity == events.SeverityInfo {
				t.Error("a restore was recorded as routine information")
			}
			return
		}
	}
	t.Error("a restore raised no event at all")
}

// --- Which disk ------------------------------------------------------------------------

// A backup lives on a disk, and which disk is not something Homebase guesses.
func TestBackupEndpointsRequireADisk(t *testing.T) {
	h, fake := backupHarness(t)
	headers := h.signedIn(t)

	for _, request := range []struct{ method, path, body string }{
		{"GET", "/api/v1/backups", ""},
		{"GET", "/api/v1/backups/" + backupID + "/preview", ""},
		{"POST", "/api/v1/backups/" + backupID + "/verify", ""},
		{"POST", "/api/v1/backups/" + backupID + "/restore", `{"confirm":"` + backupID + `"}`},
		{"POST", "/api/v1/backups", `{}`},
	} {
		rec := h.do(request.method, request.path, request.body, headers)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s %s returned %d without a disk; want 400",
				request.method, request.path, rec.Code)
		}
	}

	if len(fake.callsTo("backup.restore")) != 0 {
		t.Error("a restore with no disk reached hostd")
	}
}

// --- Permissions -------------------------------------------------------------------------

// backup.read must not be enough to restore. Restoring overwrites data.
func TestReadPermissionCannotRestore(t *testing.T) {
	h, fake := backupHarness(t)
	_ = h.signedIn(t)

	reader, err := h.auth.CreateUser(t.Context(), "reader", goodPassword,
		[]string{auth.PermBackupRead})
	if err != nil {
		t.Fatal(err)
	}
	token, _, err := h.auth.CreateSession(t.Context(), reader.ID, "test")
	if err != nil {
		t.Fatal(err)
	}
	headers := map[string]string{"Authorization": "Bearer " + token}

	if rec := h.do("GET", "/api/v1/backups?location=spare", "", headers); rec.Code != http.StatusOK {
		t.Fatalf("backup.read could not list backups: %d", rec.Code)
	}

	before := len(fake.calls)

	for _, request := range []struct{ path, body string }{
		{"/api/v1/backups", `{"location":"spare"}`},
		{"/api/v1/backups/" + backupID + "/restore?location=spare", `{"confirm":"` + backupID + `"}`},
		{"/api/v1/backups/" + backupID + "/delete?location=spare", `{"confirm":"` + backupID + `"}`},
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

func TestBackupEndpointsRequireAuthentication(t *testing.T) {
	h, fake := backupHarness(t)

	for _, request := range []struct{ method, path string }{
		{"GET", "/api/v1/backups?location=spare"},
		{"POST", "/api/v1/backups"},
		{"GET", "/api/v1/backups/" + backupID + "/preview?location=spare"},
		{"POST", "/api/v1/backups/" + backupID + "/restore?location=spare"},
		{"POST", "/api/v1/backups/" + backupID + "/delete?location=spare"},
	} {
		rec := h.do(request.method, request.path, `{"confirm":"`+backupID+`"}`, nil)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s %s returned %d without a session; want 401",
				request.method, request.path, rec.Code)
		}
	}

	if len(fake.calls) != 0 {
		t.Errorf("unauthenticated requests reached hostd: %v", fake.calls)
	}
}
