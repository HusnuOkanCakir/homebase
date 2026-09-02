package api

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// filesHarness is a server with two real directories behind it: one shared
// folder and one private folder belonging to the account that signs in.
func filesHarness(t *testing.T) (*harness, *fakeHostd, string, map[string]string) {
	t.Helper()

	h, fake := newAppHarness(t)
	root := t.TempDir()

	documents := filepath.Join(root, "shares", "documents")
	people := filepath.Join(root, "people")
	mine := filepath.Join(people, "alex")
	for _, dir := range []string{documents, mine} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(documents, "notes.txt"),
		[]byte("the shared one"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mine, "private.txt"),
		[]byte("mine alone"), 0o600); err != nil {
		t.Fatal(err)
	}

	fake.responses["share.status"] = map[string]any{
		"installed": true, "running": true, "users": []any{"alex"},
		"server_name": "homebase", "people_path": people,
		"shares": []any{map[string]any{
			"name": "documents", "location": "internal", "read_only": false,
			"path": documents, "available": true,
			"address": `\\homebase\documents`, "added_at": "2026-08-16T21:46:03Z",
		}},
	}

	headers := h.signedIn(t)
	return h, fake, root, headers
}

// --- The property that matters ------------------------------------------------------

// The single most dangerous thing in this feature.
//
// Every one of these is a real attempt: two spellings of the parent directory,
// an absolute path, a symlink pointing out of the area — the kind somebody
// creates without meaning anything by it, over a Windows drive mapping — and a
// symlink chain. None of them may resolve, and the reason none of them do is
// that os.Root asks the kernel rather than comparing strings.
func TestNoPathEscapesAnArea(t *testing.T) {
	h, _, root, headers := filesHarness(t)

	secret := filepath.Join(root, "not-shared.txt")
	if err := os.WriteFile(secret, []byte("nobody may read this"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Planted inside the shared folder, the way a person with a mapped drive
	// can plant one.
	documents := filepath.Join(root, "shares", "documents")
	if err := os.Symlink(secret, filepath.Join(documents, "shortcut.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("..", filepath.Join(documents, "up")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/etc/passwd", filepath.Join(documents, "passwd")); err != nil {
		t.Fatal(err)
	}

	attempts := []string{
		"../not-shared.txt",
		"../../not-shared.txt",
		"..%2Fnot-shared.txt",
		"/etc/passwd",
		"shortcut.txt",
		"up/not-shared.txt",
		"passwd",
		"./../not-shared.txt",
		"subdir/../../not-shared.txt",
	}
	for _, attempt := range attempts {
		rec := h.do(http.MethodGet,
			"/api/v1/files/content?area=documents&path="+urlQuery(attempt), "", headers)
		if rec.Code == http.StatusOK {
			t.Errorf("%q was served, and it is outside the folder: %s",
				attempt, first(rec.Body.String(), 80))
			continue
		}
		if strings.Contains(rec.Body.String(), "nobody may read this") ||
			strings.Contains(rec.Body.String(), "root:") {
			t.Errorf("%q leaked the contents of a file outside the folder", attempt)
		}
	}
}

// An area somebody may not open is not a name they can use. Refused, rather
// than merely absent from the list — otherwise guessing a name is a way in.
func TestARestrictedFolderIsNotAnAreaSomebodyCanName(t *testing.T) {
	h, fake, root, headers := filesHarness(t)

	documents := filepath.Join(root, "shares", "documents")
	fake.responses["share.status"] = map[string]any{
		"installed": true, "running": true, "users": []any{"alex"},
		"server_name": "homebase", "people_path": filepath.Join(root, "people"),
		"shares": []any{map[string]any{
			"name": "documents", "location": "internal", "read_only": false,
			"path": documents, "available": true, "access": []any{"someone-else"},
			"address": `\\homebase\documents`, "added_at": "2026-08-16T21:46:03Z",
		}},
	}

	rec := h.do(http.MethodGet, "/api/v1/files/areas", "", headers)
	if strings.Contains(rec.Body.String(), "documents") {
		t.Fatalf("a folder kept for somebody else is offered: %s", rec.Body)
	}

	rec = h.do(http.MethodGet,
		"/api/v1/files/content?area=documents&path=notes.txt", "", headers)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("naming it directly returned %d, not 404: %s", rec.Code, rec.Body)
	}
	if strings.Contains(rec.Body.String(), "the shared one") {
		t.Fatal("the file was served from a folder kept for somebody else")
	}
}

// --- What it is for -------------------------------------------------------------

func TestSomebodyCanListAndDownloadTheirFiles(t *testing.T) {
	h, _, _, headers := filesHarness(t)

	rec := h.do(http.MethodGet, "/api/v1/files/areas", "", headers)
	if rec.Code != http.StatusOK {
		t.Fatalf("areas returned %d: %s", rec.Code, rec.Body)
	}
	var areas struct {
		Areas []struct {
			ID   string `json:"id"`
			Kind string `json:"kind"`
		} `json:"areas"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &areas); err != nil {
		t.Fatal(err)
	}
	found := map[string]string{}
	for _, one := range areas.Areas {
		found[one.ID] = one.Kind
	}
	if found["me"] != "personal" || found["documents"] != "shared" {
		t.Fatalf("the areas are %v, which is not one folder of their own and one shared", found)
	}

	// A path on the server is never sent to a browser: it has no use for one,
	// and a path in a response is a path somebody sends back.
	if strings.Contains(rec.Body.String(), "/tmp") ||
		strings.Contains(rec.Body.String(), "path") {
		t.Fatalf("a place on the server was sent to the client: %s", rec.Body)
	}

	rec = h.do(http.MethodGet, "/api/v1/files?area=me&path=", "", headers)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "private.txt") {
		t.Fatalf("listing their own folder returned %d: %s", rec.Code, rec.Body)
	}

	rec = h.do(http.MethodGet, "/api/v1/files/content?area=me&path=private.txt", "", headers)
	if rec.Code != http.StatusOK {
		t.Fatalf("downloading returned %d: %s", rec.Code, rec.Body)
	}
	if rec.Body.String() != "mine alone" {
		t.Fatalf("the file came back as %q", rec.Body.String())
	}
}

// A file on this server arrived over a Windows drive mapping from a machine
// Homebase knows nothing about. Served as its apparent type, an HTML file would
// be a page running on the server's own origin — the session cookie, the API,
// everything.
func TestAFileIsNeverServedAsSomethingABrowserWillRun(t *testing.T) {
	h, _, root, headers := filesHarness(t)

	page := "<script>alert(document.cookie)</script>"
	if err := os.WriteFile(filepath.Join(root, "people", "alex", "page.html"),
		[]byte(page), 0o600); err != nil {
		t.Fatal(err)
	}

	rec := h.do(http.MethodGet, "/api/v1/files/content?area=me&path=page.html", "", headers)
	if rec.Code != http.StatusOK {
		t.Fatalf("downloading returned %d: %s", rec.Code, rec.Body)
	}
	if kind := rec.Header().Get("Content-Type"); kind != "application/octet-stream" {
		t.Errorf("served as %q, which a browser may render", kind)
	}
	if disposition := rec.Header().Get("Content-Disposition"); !strings.HasPrefix(disposition, "attachment") {
		t.Errorf("Content-Disposition is %q, so a browser may display it", disposition)
	}
	if rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Error("a browser is allowed to guess the type, which undoes the rest of this")
	}
}

// Somebody's holiday photographs are called things like `Örnek Belge.pdf`, and
// a header that mangles the name is a download called `_rnek Belge.pdf`.
func TestADownloadKeepsANameThatIsNotEnglish(t *testing.T) {
	h, _, root, headers := filesHarness(t)

	name := "Örnek Belge.txt"
	if err := os.WriteFile(filepath.Join(root, "people", "alex", name),
		[]byte("belge"), 0o600); err != nil {
		t.Fatal(err)
	}

	rec := h.do(http.MethodGet,
		"/api/v1/files/content?area=me&path="+urlQuery(name), "", headers)
	if rec.Code != http.StatusOK {
		t.Fatalf("downloading returned %d: %s", rec.Code, rec.Body)
	}
	disposition := rec.Header().Get("Content-Disposition")
	if !strings.Contains(disposition, "filename*=UTF-8''") {
		t.Fatalf("no encoded name in %q, so the accents are lost", disposition)
	}
	if !strings.Contains(disposition, `filename="`) {
		t.Fatalf("no plain fallback in %q", disposition)
	}
}

// Homebase's own housekeeping is not what somebody opened this to find — and a
// retired person's folder is one of those names.
func TestHousekeepingIsNotListed(t *testing.T) {
	h, _, root, headers := filesHarness(t)

	if err := os.MkdirAll(filepath.Join(root, "people", ".removed-sam-20260901-181437"),
		0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "people", "alex", ".DS_Store"),
		[]byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	rec := h.do(http.MethodGet, "/api/v1/files?area=me&path=", "", headers)
	if strings.Contains(rec.Body.String(), "DS_Store") {
		t.Fatalf("housekeeping is listed as somebody's file: %s", rec.Body)
	}
}

// --- helpers ---------------------------------------------------------------------

func urlQuery(value string) string {
	return strings.NewReplacer(
		"%", "%25", " ", "%20", "?", "%3F", "#", "%23", "&", "%26", "+", "%2B",
	).Replace(value)
}

func first(value string, n int) string {
	if len(value) <= n {
		return value
	}
	return value[:n]
}

// --- Changing things --------------------------------------------------------------

func TestSomebodyCanUploadRenameAndDelete(t *testing.T) {
	h, _, root, headers := filesHarness(t)

	// Upload.
	body, contentType := multipartFile(t, "me", "", "holiday.txt", "on a beach")
	rec := h.doRaw(http.MethodPost, "/api/v1/files/upload", body, contentType, headers)
	if rec.Code != http.StatusOK {
		t.Fatalf("uploading returned %d: %s", rec.Code, rec.Body)
	}
	saved := filepath.Join(root, "people", "alex", "holiday.txt")
	if data, err := os.ReadFile(saved); err != nil || string(data) != "on a beach" {
		t.Fatalf("the file is not on disk as it was sent: %v", err)
	}

	// Nothing left behind by the streaming write.
	entries, _ := os.ReadDir(filepath.Join(root, "people", "alex"))
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".uploading-") {
			t.Errorf("a half-written file was left behind: %s", entry.Name())
		}
	}

	// New folder.
	rec = h.do(http.MethodPost, "/api/v1/files/folder",
		`{"area":"me","path":"","name":"photos"}`, headers)
	if rec.Code != http.StatusCreated {
		t.Fatalf("creating a folder returned %d: %s", rec.Code, rec.Body)
	}

	// Rename.
	rec = h.do(http.MethodPost, "/api/v1/files/rename",
		`{"area":"me","path":"holiday.txt","name":"beach.txt"}`, headers)
	if rec.Code != http.StatusOK {
		t.Fatalf("renaming returned %d: %s", rec.Code, rec.Body)
	}
	if _, err := os.Stat(filepath.Join(root, "people", "alex", "beach.txt")); err != nil {
		t.Fatalf("the renamed file is not there: %v", err)
	}

	// Delete.
	rec = h.do(http.MethodPost, "/api/v1/files/remove",
		`{"area":"me","path":"beach.txt"}`, headers)
	if rec.Code != http.StatusOK {
		t.Fatalf("removing returned %d: %s", rec.Code, rec.Body)
	}
	if _, err := os.Stat(filepath.Join(root, "people", "alex", "beach.txt")); !os.IsNotExist(err) {
		t.Fatal("the file is still there")
	}
}

// There is no wastebasket. Deleting a folder deletes everything in it, and the
// difference between that and deleting one file is five hundred photographs.
func TestDeletingAFullFolderNeedsItsNameTyped(t *testing.T) {
	h, _, root, headers := filesHarness(t)

	full := filepath.Join(root, "people", "alex", "photos")
	if err := os.MkdirAll(full, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(full, "one.jpg"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	rec := h.do(http.MethodPost, "/api/v1/files/remove",
		`{"area":"me","path":"photos"}`, headers)
	if rec.Code != http.StatusConflict {
		t.Fatalf("a full folder was deleted without confirmation: %d %s", rec.Code, rec.Body)
	}
	if _, err := os.Stat(filepath.Join(full, "one.jpg")); err != nil {
		t.Fatal("the photographs are gone")
	}

	rec = h.do(http.MethodPost, "/api/v1/files/remove",
		`{"area":"me","path":"photos","confirm":"photos"}`, headers)
	if rec.Code != http.StatusOK {
		t.Fatalf("confirming returned %d: %s", rec.Code, rec.Body)
	}
	if _, err := os.Stat(full); !os.IsNotExist(err) {
		t.Fatal("the folder is still there after being confirmed")
	}
}

// The same rule over SMB and in the browser, or it is not a rule.
func TestAReadOnlyFolderIsReadOnlyHereToo(t *testing.T) {
	h, fake, root, headers := filesHarness(t)

	fake.responses["share.status"] = map[string]any{
		"installed": true, "running": true, "users": []any{"alex"},
		"server_name": "homebase", "people_path": filepath.Join(root, "people"),
		"shares": []any{map[string]any{
			"name": "films", "location": "internal", "read_only": true,
			"path": filepath.Join(root, "shares", "documents"), "available": true,
			"address": `\\homebase\films`, "added_at": "2026-08-16T21:46:03Z",
		}},
	}

	rec := h.do(http.MethodPost, "/api/v1/files/remove",
		`{"area":"films","path":"notes.txt"}`, headers)
	if rec.Code == http.StatusOK {
		t.Fatal("a read-only folder was written to from the browser")
	}
	if _, err := os.Stat(filepath.Join(root, "shares", "documents", "notes.txt")); err != nil {
		t.Fatal("the file in a read-only folder was deleted")
	}
}

// A name that Windows cannot open is a file that is invisible from the machine
// most of this household uses, with nothing to explain it.
func TestNamesWindowsCannotOpenAreRefused(t *testing.T) {
	h, _, _, headers := filesHarness(t)

	for _, name := range []string{
		"..", ".", "", ".hidden", "with/slash", `with\backslash`,
		"question?", "star*", "colon:name", "trailing ", "trailing.",
		`quote"name`, "pipe|name",
	} {
		body, _ := json.Marshal(map[string]string{
			"area": "me", "path": "", "name": name,
		})
		rec := h.do(http.MethodPost, "/api/v1/files/folder", string(body), headers)
		if rec.Code == http.StatusCreated {
			t.Errorf("a folder called %q was created", name)
		}
	}
}

// An upload cannot choose where it lands by what it calls itself. A browser is
// entitled to send a path in the filename — folder uploads do — and the
// directories in it are not this endpoint's to create.
func TestAnUploadCannotChooseItsOwnFolder(t *testing.T) {
	h, _, root, headers := filesHarness(t)

	for _, name := range []string{"../escaped.txt", `..\escaped.txt`, "/etc/escaped.txt"} {
		body, contentType := multipartFile(t, "me", "", name, "nowhere")
		h.doRaw(http.MethodPost, "/api/v1/files/upload", body, contentType, headers)
	}

	if _, err := os.Stat(filepath.Join(root, "people", "escaped.txt")); !os.IsNotExist(err) {
		t.Fatal("an upload landed outside the area")
	}
	if _, err := os.Stat(filepath.Join(root, "escaped.txt")); !os.IsNotExist(err) {
		t.Fatal("an upload landed two directories outside the area")
	}
}

// doRaw sends a body that is not JSON — an upload, which is the only one.
func (h *harness) doRaw(method, path string, body *bytes.Buffer, contentType string,
	headers map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, body)
	req.Header.Set("Content-Type", contentType)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.handler.ServeHTTP(rec, req)
	return rec
}

// multipartFile builds an upload the way a browser does: the fields first, then
// the file, because the server has to know where it is going before a byte of
// it is written.
func multipartFile(t *testing.T, area, where, name, content string) (*bytes.Buffer, string) {
	t.Helper()

	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	if err := form.WriteField("area", area); err != nil {
		t.Fatal(err)
	}
	if err := form.WriteField("path", where); err != nil {
		t.Fatal(err)
	}
	part, err := form.CreateFormFile("file", name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write([]byte(content)); err != nil {
		t.Fatal(err)
	}
	if err := form.Close(); err != nil {
		t.Fatal(err)
	}
	return &body, form.FormDataContentType()
}

// Every account made before private folders existed has none — which is every
// account on every server that has been running for more than a few weeks,
// including the administrator made at first-run setup. Without this the person
// who owns the machine opens Files and finds nothing.
func TestSigningInGivesAnOlderAccountThePrivateFolderItNeverHad(t *testing.T) {
	h, fake := newAppHarness(t)
	root := t.TempDir()

	fake.responses["share.status"] = map[string]any{
		"installed": false, "running": false, "users": []any{},
		"server_name": "homebase", "people_path": filepath.Join(root, "people"),
		"shares": []any{},
	}
	fake.responses["share.make_personal_folder"] = map[string]any{
		"username": "alex", "created": true,
	}

	h.signedIn(t)
	h.do(http.MethodPost, "/api/v1/auth/login",
		`{"username":"alex","password":"`+goodPassword+`"}`, nil)

	waitForCall(t, fake, "share.make_personal_folder", 1)
	if calls := fake.callsTo("share.make_personal_folder"); calls[0].Body["username"] != "alex" {
		t.Fatalf("a folder was asked for on behalf of %v", calls[0].Body["username"])
	}
}

// A folder that is not there must not be offered. The person clicks it and gets
// an error about a folder they were just told they had.
func TestAPrivateFolderThatIsNotThereIsNotOffered(t *testing.T) {
	h, fake := newAppHarness(t)
	root := t.TempDir()

	fake.responses["share.status"] = map[string]any{
		"installed": true, "running": true, "users": []any{"alex"},
		"server_name": "homebase", "people_path": filepath.Join(root, "people"),
		"shares": []any{},
	}
	fake.responses["share.make_personal_folder"] = map[string]any{"username": "alex"}

	headers := h.signedIn(t)
	rec := h.do(http.MethodGet, "/api/v1/files/areas", "", headers)
	if strings.Contains(rec.Body.String(), areaPersonal) {
		t.Fatalf("a folder that does not exist is offered: %s", rec.Body)
	}
}

// --- Disks people plug into the server ---------------------------------------------

// The shortest path between a disk in a drawer and a person in another country:
// somebody walks to the server, plugs it in, and it is there. Nobody presses
// anything, which is the sentence this was asked for in.
func TestADiskPluggedIntoTheServerIsBrowsedLikeAnyOther(t *testing.T) {
	h, fake := newAppHarness(t)
	root := t.TempDir()

	// Stands in for the mount point: to core, a plugged-in disk is a directory
	// hostd reported, and everything below is the same code path a shared
	// folder takes.
	disk := filepath.Join(root, "plugged", "kingston")
	if err := os.MkdirAll(disk, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(disk, "tax-2019.pdf"),
		[]byte("the papers"), 0o644); err != nil {
		t.Fatal(err)
	}

	fake.responses["share.status"] = map[string]any{
		"installed": true, "running": true, "users": []any{"alex"},
		"server_name": "homebase", "shares": []any{},
	}
	fake.responses["plugged.status"] = map[string]any{
		"disks": []any{map[string]any{
			"name": "kingston", "uuid": "1234-ABCD", "label": "KINGSTON",
			"filesystem": "ntfs", "size_bytes": 64000000000,
			"path": disk, "connected": true,
		}},
	}

	headers := h.signedIn(t)

	rec := h.do(http.MethodGet, "/api/v1/files/areas", "", headers)
	if !strings.Contains(rec.Body.String(), "disk:kingston") {
		t.Fatalf("the plugged-in disk is not offered: %s", rec.Body)
	}
	if !strings.Contains(rec.Body.String(), `"read_only":true`) {
		t.Fatalf("it is offered as writable: %s", rec.Body)
	}

	rec = h.do(http.MethodGet, "/api/v1/files?area=disk:kingston&path=", "", headers)
	if !strings.Contains(rec.Body.String(), "tax-2019.pdf") {
		t.Fatalf("listing it returned %d: %s", rec.Code, rec.Body)
	}

	rec = h.do(http.MethodGet,
		"/api/v1/files/content?area=disk:kingston&path=tax-2019.pdf", "", headers)
	if rec.Code != http.StatusOK || rec.Body.String() != "the papers" {
		t.Fatalf("downloading returned %d: %s", rec.Code, rec.Body)
	}

	// And nothing on somebody else's disk can be deleted from here.
	rec = h.do(http.MethodPost, "/api/v1/files/remove",
		`{"area":"disk:kingston","path":"tax-2019.pdf"}`, headers)
	if rec.Code == http.StatusOK {
		t.Fatal("a file on somebody's disk was deleted from the dashboard")
	}
	if _, err := os.Stat(filepath.Join(disk, "tax-2019.pdf")); err != nil {
		t.Fatal("the file on the plugged-in disk is gone")
	}
}

// A disk pulled out without warning leaves a folder that lists as empty, which
// looks exactly like a disk whose files have been deleted.
func TestADiskThatHasBeenPulledOutIsNotOffered(t *testing.T) {
	h, fake := newAppHarness(t)
	fake.responses["share.status"] = map[string]any{
		"installed": true, "running": true, "users": []any{"alex"},
		"server_name": "homebase", "shares": []any{},
	}
	fake.responses["plugged.status"] = map[string]any{
		"disks": []any{map[string]any{
			"name": "kingston", "path": "/srv/homebase/storage/plugged/kingston",
			"connected": false,
		}},
	}

	headers := h.signedIn(t)
	rec := h.do(http.MethodGet, "/api/v1/files/areas", "", headers)
	if strings.Contains(rec.Body.String(), "kingston") {
		t.Fatalf("a disk nobody can read is offered: %s", rec.Body)
	}
}

// Everybody with an account can read a disk somebody plugged in — and a Member
// can finish with it, because the person who wants to walk away with the disk
// is the one standing next to the server, not the administrator abroad.
func TestAMemberCanReadAndEjectAPluggedDisk(t *testing.T) {
	h, fake := newAppHarness(t)
	fake.responses["share.status"] = map[string]any{
		"installed": true, "running": true, "users": []any{"alex"},
		"server_name": "homebase", "shares": []any{},
	}
	fake.responses["plugged.status"] = map[string]any{
		"disks": []any{map[string]any{"name": "kingston", "path": "/tmp", "connected": true}},
	}
	fake.responses["plugged.eject"] = map[string]any{"name": "kingston", "ejected": true}
	fake.responses["share.make_personal_folder"] = map[string]any{"username": "father"}

	token := h.signIn(t)
	code := inviteAccount(t, h, token, "father")
	h.do(http.MethodPost, "/api/v1/auth/claim",
		`{"username":"father","joining_code":"`+code+`","new_password":"a-password-of-their-own"}`, nil)
	rec := h.do(http.MethodPost, "/api/v1/auth/login",
		`{"username":"father","password":"a-password-of-their-own"}`, nil)
	member := map[string]string{}
	for _, cookie := range rec.Result().Cookies() {
		if cookie.Name == SessionCookie {
			member["Authorization"] = "Bearer " + cookie.Value
		}
	}

	if rec := h.do(http.MethodGet, "/api/v1/plugged-disks", "", member); rec.Code != http.StatusOK {
		t.Fatalf("a member cannot see what is plugged in: %d %s", rec.Code, rec.Body)
	}
	rec = h.do(http.MethodPost, "/api/v1/plugged-disks/eject", `{"name":"kingston"}`, member)
	if rec.Code != http.StatusOK {
		t.Fatalf("a member could not finish with a disk: %d %s", rec.Code, rec.Body)
	}
}

// Windows hides its own plumbing with a file attribute rather than a leading
// dot, and Linux knows nothing about that attribute. On the first real disk
// plugged into this server, `System Volume Information` was two of the three
// things on the screen and the file the household wanted was underneath.
func TestWindowsHousekeepingIsNotListedAsSomebodysFiles(t *testing.T) {
	h, fake := newAppHarness(t)
	root := t.TempDir()

	disk := filepath.Join(root, "plugged", "kingston")
	for _, name := range []string{"System Volume Information", "$RECYCLE.BIN"} {
		if err := os.MkdirAll(filepath.Join(disk, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(disk, "holiday.jpg"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A folder somebody made that merely looks systemish is still theirs.
	if err := os.MkdirAll(filepath.Join(disk, "System backups"), 0o755); err != nil {
		t.Fatal(err)
	}

	fake.responses["share.status"] = map[string]any{
		"installed": true, "running": true, "users": []any{"alex"},
		"server_name": "homebase", "shares": []any{},
	}
	fake.responses["plugged.status"] = map[string]any{
		"disks": []any{map[string]any{
			"name": "kingston", "path": disk, "connected": true,
		}},
	}

	headers := h.signedIn(t)
	rec := h.do(http.MethodGet, "/api/v1/files?area=disk:kingston&path=", "", headers)
	body := rec.Body.String()

	for _, hidden := range []string{"System Volume Information", "RECYCLE"} {
		if strings.Contains(body, hidden) {
			t.Errorf("%q is listed as one of somebody's files", hidden)
		}
	}
	if !strings.Contains(body, "holiday.jpg") {
		t.Fatalf("the file they wanted is missing: %s", body)
	}
	if !strings.Contains(body, "System backups") {
		t.Fatal("a folder somebody made was hidden for looking systemish")
	}
}
