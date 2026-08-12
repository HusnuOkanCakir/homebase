package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/HusnuOkanCakir/homebase/internal/auth"
)

// --- The diagnostic bundle ----------------------------------------------------------

// The bundle is meant to leave the machine, so what it does *not* contain has to
// travel with it. A client that had to consult the documentation to find out
// would not, and the question is asked at the moment of sending.
func TestTheDiagnosticFileSaysWhatItDoesNotContain(t *testing.T) {
	h, fake := newAppHarness(t)
	headers := h.signedIn(t)

	fake.responses["system.diagnostics"] = map[string]any{
		"path":  "/var/lib/homebase/diagnostics/homebase-diagnostics-2026-08-12-100000.txt",
		"bytes": 40_000, "created_at": "2026-08-12T10:00:00Z",
		"includes": []any{"which version of each part of Homebase is installed"},
		"excludes": []any{
			"your password, or the scrambled form of it Homebase stores",
			"your recovery code",
			"the contents of Homebase's database",
		},
		"message": "The diagnostic file is ready.",
	}

	rec := h.do("POST", "/api/v1/system/diagnostics", "", headers)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	var result struct {
		Excludes []string `json:"excludes"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Excludes) == 0 {
		t.Fatal("the bundle does not say what it leaves out; nobody can judge whether " +
			"it is safe to send")
	}

	joined := strings.ToLower(strings.Join(result.Excludes, " "))
	for _, secret := range []string{"password", "recovery code", "database"} {
		if !strings.Contains(joined, secret) {
			t.Errorf("the exclusion list does not mention %q: %v", secret, result.Excludes)
		}
	}
}

// There is no filename in the request, and this is why: a path from a caller is
// a path to be validated, and validation is the part that gets subtly wrong.
// core serves the newest file in one fixed directory, so a traversal has nothing
// to traverse.
func TestDownloadingDiagnosticsTakesNoPathFromTheCaller(t *testing.T) {
	h, _ := newAppHarness(t)
	headers := h.signedIn(t)

	// Whatever a caller puts in the query string, the handler ignores it. On a
	// machine with no bundle the answer is 404 — never a file, and never an
	// error mentioning a path the caller chose.
	for _, query := range []string{
		"",
		"?file=../../../../etc/shadow",
		"?path=/var/lib/homebase/homebase.db",
		"?name=homebase-diagnostics-../../etc/passwd",
	} {
		rec := h.do("GET", "/api/v1/system/diagnostics/download"+query, "", headers)
		if rec.Code == http.StatusOK {
			t.Errorf("%q returned a file on a machine with no diagnostics", query)
		}
		if strings.Contains(rec.Body.String(), "root:") {
			t.Fatalf("%q served /etc/passwd", query)
		}
	}
}

// --- Repair -------------------------------------------------------------------------

// Doing nothing is a result, not a success.
//
// "Nothing needed fixing" is the honest answer when whatever is wrong is not one
// of the things repair knows about, and it has to reach the client as such —
// somebody whose server is broken must not be sent away believing it was
// repaired.
func TestRepairReportsHavingChangedNothing(t *testing.T) {
	h, fake := newAppHarness(t)
	headers := h.signedIn(t)

	fake.responses["system.repair"] = map[string]any{
		"steps": []any{
			map[string]any{"what": "Whether a software update was left unfinished"},
			map[string]any{"what": "Whether /srv/homebase exists"},
		},
		"changed": 0, "healthy": true,
		"message": "Everything Homebase knows how to check was already correct.",
	}

	rec := h.do("POST", "/api/v1/system/repair", "", headers)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	var result struct {
		Changed int    `json:"changed"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Changed != 0 {
		t.Errorf("changed = %d, want 0", result.Changed)
	}
	if !strings.Contains(strings.ToLower(result.Message), "already correct") {
		t.Errorf("a repair that fixed nothing does not say so: %q", result.Message)
	}
}

// --- Factory reset ------------------------------------------------------------------

// The server's own name, typed. Not a word like "yes": this removes every
// account, and a `true` a client sends by default is not a confirmation.
func TestFactoryResetRequiresTheServersName(t *testing.T) {
	h, fake := newAppHarness(t)
	headers := h.signedIn(t)

	fake.failures["system.factory_reset"] = hostclientError(
		"system.confirmation_required",
		"Please confirm by typing this server's name.",
		http.StatusPreconditionRequired)

	for _, body := range []string{
		`{}`, `{"confirm":""}`, `{"confirm":"yes"}`, `{"confirm":"reset"}`,
	} {
		rec := h.do("POST", "/api/v1/system/factory-reset", body, headers)
		if rec.Code == http.StatusOK {
			t.Errorf("%q was accepted as confirmation for a factory reset", body)
		}
	}
}

// The safety property in the API: absent means keep. A caller that forgets the
// field keeps the photographs, and a plain bool would have made forgetting it
// mean "delete everything".
func TestForgettingToSayWhetherToKeepDataKeepsIt(t *testing.T) {
	h, fake := newAppHarness(t)
	headers := h.signedIn(t)

	fake.responses["system.factory_reset"] = map[string]any{
		"message": "This server has been reset.",
	}

	rec := h.do("POST", "/api/v1/system/factory-reset", `{"confirm":"the-server"}`, headers)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	calls := fake.callsTo("system.factory_reset")
	if len(calls) != 1 {
		t.Fatalf("%d calls to system.factory_reset, want 1", len(calls))
	}
	if keep, ok := calls[0].Body["keep_data"].(bool); !ok || !keep {
		t.Errorf("a request that did not mention keep_data reached hostd as %v — "+
			"the default has to be the one that keeps somebody's files",
			calls[0].Body["keep_data"])
	}

	// And hostd has to be told the user was asked, so it can check again.
	if !calls[0].Confirmed {
		t.Error("the factory reset reached hostd without the confirmed header")
	}
}

func TestDeletingDataHasToBeAskedForExplicitly(t *testing.T) {
	h, fake := newAppHarness(t)
	headers := h.signedIn(t)

	fake.responses["system.factory_reset"] = map[string]any{"message": "Reset."}

	rec := h.do("POST", "/api/v1/system/factory-reset",
		`{"confirm":"the-server","keep_data":false}`, headers)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	calls := fake.callsTo("system.factory_reset")
	if len(calls) != 1 {
		t.Fatalf("%d calls, want 1", len(calls))
	}
	if keep, _ := calls[0].Body["keep_data"].(bool); keep {
		t.Error("keep_data:false did not reach hostd; asking to erase the disk was ignored")
	}
}

// --- Permissions --------------------------------------------------------------------

func TestRecoveryToolsRequireAuthentication(t *testing.T) {
	h, fake := newAppHarness(t)

	for _, request := range []struct{ method, path string }{
		{"POST", "/api/v1/system/diagnostics"},
		{"GET", "/api/v1/system/diagnostics/download"},
		{"POST", "/api/v1/system/repair"},
		{"POST", "/api/v1/system/factory-reset"},
	} {
		rec := h.do(request.method, request.path, `{"confirm":"the-server"}`, nil)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s %s returned %d without a session; want 401",
				request.method, request.path, rec.Code)
		}
	}

	if len(fake.calls) != 0 {
		t.Errorf("unauthenticated requests reached hostd: %v", fake.calls)
	}
}

// A read-only account must not be able to reset the server, repair it, or make a
// file describing it. The diagnostic bundle is included deliberately: it is a
// read, but it produces something meant to leave the machine.
func TestRecoveryToolsNeedSystemManage(t *testing.T) {
	h, fake := newAppHarness(t)
	_ = h.signedIn(t)

	reader, err := h.auth.CreateUser(t.Context(), "reader", goodPassword,
		[]string{auth.PermSystemRead})
	if err != nil {
		t.Fatal(err)
	}
	token, _, err := h.auth.CreateSession(t.Context(), reader.ID, "test")
	if err != nil {
		t.Fatal(err)
	}
	headers := map[string]string{"Authorization": "Bearer " + token}

	before := len(fake.calls)

	for _, request := range []struct{ method, path string }{
		{"POST", "/api/v1/system/diagnostics"},
		{"GET", "/api/v1/system/diagnostics/download"},
		{"POST", "/api/v1/system/repair"},
		{"POST", "/api/v1/system/factory-reset"},
	} {
		rec := h.do(request.method, request.path, `{"confirm":"the-server"}`, headers)
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s %s returned %d for a read-only user; want 403",
				request.method, request.path, rec.Code)
		}
	}

	if len(fake.calls) != before {
		t.Errorf("a read-only user caused %d privileged calls", len(fake.calls)-before)
	}
}
