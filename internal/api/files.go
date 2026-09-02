package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/HusnuOkanCakir/homebase/internal/auth"
)

// Files, in a browser.
//
// Until now the only way to reach a file on this server was SMB, which means a
// computer that can map a drive — not a phone, not a borrowed laptop, and not
// somebody's father who has been told to "open Explorer and type two
// backslashes". Everything here is the same files over HTTPS, which already
// works from anywhere the dashboard does.
//
// # Where the paths come from
//
// `handleDownloadDiagnostics` states this project's position on caller-supplied
// filenames: *"A filename in a request is a path to be validated, and the
// validation is the part that gets subtly wrong; there is nothing to get wrong
// if there is no filename."* A file browser cannot avoid them, so this is the
// first place that argument has to be met rather than sidestepped.
//
// It is met with `os.Root`, which is the kernel doing the work. Every open,
// stat, create and rename below goes through a root handle for the area, and on
// Linux that is `openat2` with `RESOLVE_BENEATH`: a path that would leave the
// directory fails in the kernel, whatever it is made of. `..`, an absolute
// path, a symlink planted over SMB pointing at `/etc`, a symlink created
// between the check and the open — none of them resolve, because there is no
// check to race.
//
// That is a stronger guarantee than the usual clean-join-and-compare, and it is
// why this file has no path arithmetic in it. The one thing still done by hand
// is rejecting a NUL byte, because a name containing one is not a name.
//
// # What an area is
//
// The caller names an *area* and a path inside it, never a path on the server.
// An area is a shared folder or `me`, and the areas somebody may name are
// computed from their account every time — so a folder they may not open is not
// merely hidden from the list, it is not a name they can use.

// areaPersonal is the area holding the caller's own folder. Not a share name:
// share names are lowercase letters, and this is the same for everybody, so it
// cannot collide with one.
const areaPersonal = "me"

// pluggedAreaPrefix namespaces the disks people plug in.
//
// Without it a disk labelled "documents" would collide with the shared folder
// of that name, and whichever came second would be unreachable — silently,
// because an area is found by matching the first one that answers to the name.
const pluggedAreaPrefix = "disk:"

// maxListing is how many entries one directory listing returns.
//
// A downloads folder with forty thousand files in it should produce a slow
// screen rather than a response that never finishes and a browser that gives up
// having rendered nothing.
const maxListing = 5000

// area is one place a person may browse.
type area struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Kind     string `json:"kind"`
	ReadOnly bool   `json:"read_only"`

	// path is where it is on the server. Never sent to a client: a browser has
	// no use for it, and a path in a response is a path somebody will send back.
	path string
}

func (s *Server) registerFileRoutes(mux *http.ServeMux) {
	mux.Handle("GET /api/v1/files/areas", s.require(auth.PermFilesRead, s.handleFileAreas))
	mux.Handle("GET /api/v1/files", s.require(auth.PermFilesRead, s.handleListFiles))
	mux.Handle("GET /api/v1/files/content", s.require(auth.PermFilesRead, s.handleDownloadFile))
	mux.Handle("POST /api/v1/files/upload", s.require(auth.PermFilesWrite, s.handleUploadFile))
	mux.Handle("POST /api/v1/files/folder", s.require(auth.PermFilesWrite, s.handleCreateFolder))
	mux.Handle("POST /api/v1/files/rename", s.require(auth.PermFilesWrite, s.handleRenameFile))
	mux.Handle("POST /api/v1/files/remove", s.require(auth.PermFilesWrite, s.handleRemoveFile))
}

// --- Areas ------------------------------------------------------------------------

// areasFor is every place this person may browse, computed from their account
// rather than read from a list somebody maintains.
func (s *Server) areasFor(ctx context.Context, user *auth.User) ([]area, error) {
	status, err := s.host.Shares(ctx)
	if err != nil {
		return nil, err
	}

	var areas []area
	if status.PeoplePath != "" {
		// Offered only if it is actually there. A folder is made when an
		// account is created and again at every sign-in, so a missing one means
		// the disk was unavailable at both moments — and an area that cannot be
		// opened is worse than one that is not listed, because the person
		// clicks it and gets an error about a folder they were told they had.
		mine := path.Join(status.PeoplePath, user.Username)
		if info, err := os.Stat(mine); err == nil && info.IsDir() {
			areas = append(areas, area{
				ID:   areaPersonal,
				Name: "Your folder",
				Kind: "personal",
				path: mine,
			})
		}
	}

	// Disks somebody has plugged into the server.
	//
	// This is the shortest path there is between a disk in a drawer and a person
	// in another country: somebody walks to the server, plugs it in, and it is
	// here. No account on anybody's computer, no sharing dialog, no name to
	// resolve, nothing that has to stay awake.
	//
	// Read-only, and not as a matter of policy — the mount itself is. The disk
	// belongs to whoever carried it in and is standing in another room.
	if disks, err := s.host.PluggedDisks(ctx); err == nil {
		for _, disk := range disks {
			if !disk.Connected || disk.Path == "" {
				continue
			}
			areas = append(areas, area{
				ID:       pluggedAreaPrefix + disk.Name,
				Name:     disk.Name,
				Kind:     "plugged",
				ReadOnly: true,
				path:     disk.Path,
			})
		}
	}

	for _, share := range status.Shares {
		// A share whose disk is unplugged has nothing behind it. Listed as a
		// share elsewhere, because it is still configured; not offered as
		// somewhere to browse, because browsing it would create the folder on
		// the system disk and show an empty one that looks exactly like the
		// place somebody's files used to be.
		if !share.Available || share.Path == "" {
			continue
		}
		if !mayOpenShare(share.Access, user.Username) {
			continue
		}
		areas = append(areas, area{
			ID:       share.Name,
			Name:     share.Name,
			Kind:     "shared",
			ReadOnly: share.ReadOnly,
			path:     share.Path,
		})
	}
	return areas, nil
}

// mayOpenShare answers the same question Samba answers from `valid users`, and
// has to answer it the same way. An empty list is everybody.
func mayOpenShare(access []string, username string) bool {
	if len(access) == 0 {
		return true
	}
	for _, name := range access {
		if strings.EqualFold(name, username) {
			return true
		}
	}
	return false
}

// openArea returns a root handle for an area this person may open.
//
// The handle is the boundary. Everything reached through it is beneath the
// area's directory because the kernel says so, and an area the caller may not
// name does not resolve at all — refused rather than merely absent from a list,
// so that guessing a name gets nowhere.
func (s *Server) openArea(ctx context.Context, user *auth.User, id string) (*os.Root, area, error) {
	areas, err := s.areasFor(ctx, user)
	if err != nil {
		return nil, area{}, err
	}
	for _, candidate := range areas {
		if candidate.ID != id {
			continue
		}
		root, err := os.OpenRoot(candidate.path)
		if err != nil {
			return nil, candidate, err
		}
		return root, candidate, nil
	}
	return nil, area{}, errNoSuchArea
}

var errNoSuchArea = errors.New("no such area")

// --- Listing ----------------------------------------------------------------------

type fileEntry struct {
	Name string `json:"name"`
	Path string `json:"path"`
	// Directory rather than "type", because there are two kinds here and a
	// client asking "is this a folder" should not have to compare strings.
	Directory bool   `json:"directory"`
	Size      int64  `json:"size"`
	Modified  string `json:"modified"`
}

func (s *Server) handleFileAreas(w http.ResponseWriter, r *http.Request, user *auth.User) {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	areas, err := s.areasFor(ctx, user)
	if err != nil {
		s.writeHostError(w, r, err)
		return
	}
	if areas == nil {
		areas = []area{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"areas": areas})
}

func (s *Server) handleListFiles(w http.ResponseWriter, r *http.Request, user *auth.User) {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	where := r.URL.Query().Get("path")
	root, _, err := s.openArea(ctx, user, r.URL.Query().Get("area"))
	if err != nil {
		s.writeFileError(w, r, err)
		return
	}
	defer root.Close()

	dir, err := openWithin(root, where)
	if err != nil {
		s.writeFileError(w, r, err)
		return
	}
	defer dir.Close()

	info, err := dir.Stat()
	if err != nil {
		s.writeFileError(w, r, err)
		return
	}
	if !info.IsDir() {
		s.writeError(w, r, http.StatusBadRequest, apiError{
			Code:        "files.not_a_folder",
			Message:     "That is a file, not a folder.",
			Recoverable: true,
			Recovery:    "Open it instead of listing it.",
		})
		return
	}

	names, err := dir.Readdirnames(0)
	if err != nil {
		s.writeFileError(w, r, err)
		return
	}
	sort.Strings(names)

	entries := make([]fileEntry, 0, len(names))
	truncated := false
	for _, name := range names {
		if len(entries) >= maxListing {
			truncated = true
			break
		}
		// Names beginning with a dot are Homebase's own housekeeping here — a
		// retired person's folder is `.removed-sam-…` — and the operating
		// system's everywhere else. Neither is what somebody opened this to
		// find.
		if strings.HasPrefix(name, ".") {
			continue
		}
		child := path.Join(where, name)
		// Lstat, not Stat: a symlink is described as what it is rather than as
		// what it points at. Following one here would report a size and a date
		// for a target that opening it will refuse to reach.
		info, err := root.Lstat(cleanPath(child))
		if err != nil {
			continue
		}
		entries = append(entries, fileEntry{
			Name:      name,
			Path:      child,
			Directory: info.IsDir(),
			Size:      info.Size(),
			Modified:  info.ModTime().UTC().Format(time.RFC3339),
		})
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"path":      where,
		"entries":   entries,
		"truncated": truncated,
	})
}

// --- Download ----------------------------------------------------------------------

func (s *Server) handleDownloadFile(w http.ResponseWriter, r *http.Request, user *auth.User) {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	where := r.URL.Query().Get("path")
	root, _, err := s.openArea(ctx, user, r.URL.Query().Get("area"))
	if err != nil {
		s.writeFileError(w, r, err)
		return
	}
	defer root.Close()

	file, err := openWithin(root, where)
	if err != nil {
		s.writeFileError(w, r, err)
		return
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		s.writeFileError(w, r, err)
		return
	}
	if info.IsDir() {
		s.writeError(w, r, http.StatusBadRequest, apiError{
			Code:        "files.is_a_folder",
			Message:     "That is a folder, not a file.",
			Recoverable: true,
			Recovery:    "Open it to see what is inside.",
		})
		return
	}

	// Always an attachment, and always this content type.
	//
	// A file on this server is a file somebody put there, over SMB, from a
	// laptop Homebase knows nothing about. Served as its apparent type it would
	// be a way to run a page on the server's own origin — the session cookie,
	// the API, everything — using a file that arrived through a Windows drive
	// mapping. Downloading is what a file browser is for; rendering is not.
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", attachmentHeader(path.Base(where)))
	w.Header().Set("X-Content-Type-Options", "nosniff")

	// ServeContent rather than reading it in: it handles Range requests, so a
	// film that stops downloading at 90% on a train resumes rather than starts
	// again, and nothing here holds a file in memory to send it.
	http.ServeContent(w, r, info.Name(), info.ModTime(), file)
}

// attachmentHeader names the file in a way every browser reads the same.
//
// Two spellings, which is the documented way to do this: a plain ASCII fallback
// for anything old, and RFC 5987 for the real name — because these are somebody
// else's holiday photographs and they are called things like `Örnek Belge.pdf`.
func attachmentHeader(name string) string {
	ascii := strings.Map(func(r rune) rune {
		if r < 32 || r > 126 || r == '"' || r == '\\' {
			return '_'
		}
		return r
	}, name)
	if ascii == "" {
		ascii = "download"
	}
	return `attachment; filename="` + ascii + `"; filename*=UTF-8''` + url.PathEscape(name)
}

// --- Paths ------------------------------------------------------------------------

// cleanPath turns a path from a request into one os.Root will accept.
//
// It does not make the path safe — os.Root does that, in the kernel, and this
// runs before it precisely so that it cannot be mistaken for the thing keeping
// anybody out. It exists because `path.Clean("")` is `"."` and `os.Root` wants
// a relative name, and because a leading slash is what every client sends for
// the top of an area.
func cleanPath(where string) string {
	where = strings.TrimPrefix(path.Clean("/"+where), "/")
	if where == "" {
		return "."
	}
	return where
}

// openWithin opens a name inside an area.
//
// The NUL check is the one piece of validation done by hand. A name containing
// one is not a name any filesystem will accept; catching it here means the
// error says so rather than arriving as "invalid argument" from a syscall.
func openWithin(root *os.Root, where string) (*os.File, error) {
	if strings.ContainsRune(where, 0) {
		return nil, errBadName
	}
	return root.Open(cleanPath(where))
}

var errBadName = errors.New("that is not a name a file can have")

// --- Errors ------------------------------------------------------------------------

// writeFileError turns the failures of a filesystem into answers a person can
// act on, and says nothing about where anything is on the server.
func (s *Server) writeFileError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, errNoSuchArea):
		// The same answer for "there is no such folder" and "that folder is not
		// yours". Telling them apart says which folders exist to somebody who
		// may not open them.
		s.writeError(w, r, http.StatusNotFound, apiError{
			Code:        "files.no_such_area",
			Message:     "There is no folder here by that name.",
			Recoverable: true,
			Recovery:    "Go back and choose one of the folders listed.",
		})
	case errors.Is(err, errBadName):
		s.writeError(w, r, http.StatusBadRequest, apiError{
			Code:        "files.invalid_name",
			Message:     "That is not a name a file can have.",
			Recoverable: true,
			Recovery:    "Check the name and try again.",
		})
	case errors.Is(err, os.ErrNotExist):
		s.writeError(w, r, http.StatusNotFound, apiError{
			Code:        "files.not_found",
			Message:     "That file is not there.",
			Recoverable: true,
			Recovery: "It may have been moved or deleted from another computer. " +
				"Go back and look again.",
		})
	case errors.Is(err, os.ErrPermission):
		// Includes every attempt to leave the area: os.Root refuses those as
		// permission errors, and this is the one place that has to be said out
		// loud, because the answer is deliberately the same as for a file the
		// server genuinely cannot read.
		s.writeError(w, r, http.StatusForbidden, apiError{
			Code:        "files.refused",
			Message:     "Homebase will not open that.",
			Recoverable: false,
		})
	default:
		s.writeInternal(w, r, err)
	}
}

// --- Changing things ----------------------------------------------------------------

// A name Homebase will create.
//
// Stricter than Linux, on purpose. These folders are opened from Windows as
// often as from here, and Windows cannot open a file whose name contains any of
// `\ / : * ? " < > |`, or one that ends in a space or a dot. A file created here
// with such a name would be invisible from the machine most of this household
// uses, with nothing to explain it. Refusing is kinder than creating.
//
// A leading dot is refused for a different reason: listings hide those, so a
// file created with one would vanish the moment it was made.
func validFileName(name string) error {
	switch {
	case name == "", name == ".", name == "..":
		return errBadName
	case len(name) > 255:
		return errBadName
	case strings.HasPrefix(name, "."):
		return errBadName
	case strings.HasSuffix(name, " "), strings.HasSuffix(name, "."):
		return errBadName
	}
	if strings.ContainsAny(name, `\/:*?"<>|`) {
		return errBadName
	}
	for _, r := range name {
		if r < 32 || r == 127 {
			return errBadName
		}
	}
	return nil
}

// openWritableArea is openArea plus the question of whether writing is allowed
// at all. A read-only share is read-only here too: it is the same folder, and a
// rule that holds over SMB and not in the browser is not a rule.
func (s *Server) openWritableArea(ctx context.Context, user *auth.User, id string) (*os.Root, area, error) {
	root, which, err := s.openArea(ctx, user, id)
	if err != nil {
		return nil, which, err
	}
	if which.ReadOnly {
		root.Close()
		return nil, which, errReadOnlyArea
	}
	return root, which, nil
}

var errReadOnlyArea = errors.New("that folder is shared read-only")

func (s *Server) handleUploadFile(w http.ResponseWriter, r *http.Request, user *auth.User) {
	ctx, cancel := context.WithTimeout(r.Context(), 6*time.Hour)
	defer cancel()

	// Streamed, not parsed. http.Request.ParseMultipartForm writes anything
	// over its memory limit to a temporary file in /tmp — which on this machine
	// is the system disk, so uploading a film to a four-terabyte drive would
	// fill the root filesystem and stop the server rather than the upload.
	parts, err := r.MultipartReader()
	if err != nil {
		s.writeError(w, r, http.StatusBadRequest, apiError{
			Code:        "files.not_an_upload",
			Message:     "That request did not carry a file.",
			Recoverable: true,
			Recovery:    "Choose a file and try again.",
		})
		return
	}

	var (
		root  *os.Root
		where string
		saved []string
	)
	defer func() {
		if root != nil {
			root.Close()
		}
	}()

	for {
		part, err := parts.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			s.writeFileError(w, r, err)
			return
		}

		// The fields come before the file, because the area has to be known
		// before a byte of it is written anywhere. A client that sends them the
		// other way round is told so rather than having its file put somewhere
		// chosen by a default.
		switch part.FormName() {
		case "area":
			value, err := readFormValue(part)
			if err != nil {
				s.writeFileError(w, r, err)
				return
			}
			root, _, err = s.openWritableArea(ctx, user, value)
			if err != nil {
				s.writeFileError(w, r, err)
				return
			}
			continue
		case "path":
			value, err := readFormValue(part)
			if err != nil {
				s.writeFileError(w, r, err)
				return
			}
			where = value
			continue
		}

		if root == nil {
			s.writeError(w, r, http.StatusBadRequest, apiError{
				Code:        "files.no_area",
				Message:     "Homebase was not told where to put that.",
				Detail:      "the area field must come before the file",
				Recoverable: true,
				Recovery:    "Try the upload again.",
			})
			return
		}

		// The name the browser sent, reduced to its last element. A browser is
		// entitled to send `photos/holiday/one.jpg` from a folder upload, and
		// the directories in it are not this endpoint's to create.
		name := path.Base(strings.ReplaceAll(part.FileName(), `\`, "/"))
		if err := validFileName(name); err != nil {
			s.writeFileError(w, r, err)
			return
		}

		written, err := s.saveUpload(root, where, name, part)
		if err != nil {
			s.writeFileError(w, r, err)
			return
		}
		saved = append(saved, written)
	}

	if len(saved) == 0 {
		s.writeError(w, r, http.StatusBadRequest, apiError{
			Code:        "files.not_an_upload",
			Message:     "That request did not carry a file.",
			Recoverable: true,
			Recovery:    "Choose a file and try again.",
		})
		return
	}

	s.log.Info("files uploaded", "count", len(saved), "by", user.Username)
	writeJSON(w, http.StatusOK, map[string]any{"saved": saved})
}

// saveUpload streams one file into place.
//
// Written under a temporary name in the destination directory and renamed when
// it is complete, so an upload that fails halfway — a phone leaving a train
// station — leaves nothing rather than a file that looks finished and is not.
// The temporary name begins with a dot, so it is not listed while it is being
// written.
func (s *Server) saveUpload(root *os.Root, where, name string, body io.Reader) (string, error) {
	if err := ensureDirectory(root, where); err != nil {
		return "", err
	}

	final := cleanPath(path.Join(where, name))
	temporary := cleanPath(path.Join(where, ".uploading-"+randomToken()))

	file, err := root.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o664)
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(file, body); err != nil {
		file.Close()
		_ = root.Remove(temporary)
		return "", err
	}
	if err := file.Close(); err != nil {
		_ = root.Remove(temporary)
		return "", err
	}
	if err := root.Rename(temporary, final); err != nil {
		_ = root.Remove(temporary)
		return "", err
	}
	return path.Join(where, name), nil
}

// randomToken names a file that is being written, so that two uploads of the
// same name at the same moment cannot land on each other's temporary file.
func randomToken() string {
	var raw [8]byte
	if _, err := rand.Read(raw[:]); err != nil {
		// Never on Linux, and if it ever were, a predictable temporary name is
		// still contained by the area and is replaced by the rename that
		// follows. Falling back rather than failing an upload for it.
		return strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return hex.EncodeToString(raw[:])
}

// ensureDirectory refuses to write into something that is not a folder, before
// anything is created.
func ensureDirectory(root *os.Root, where string) error {
	info, err := root.Stat(cleanPath(where))
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return errNotAFolder
	}
	return nil
}

var errNotAFolder = errors.New("that is a file, not a folder")

func (s *Server) handleCreateFolder(w http.ResponseWriter, r *http.Request, user *auth.User) {
	var body struct {
		Area string `json:"area"`
		Path string `json:"path"`
		Name string `json:"name"`
	}
	if !s.decode(w, r, &body) {
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	root, _, err := s.openWritableArea(ctx, user, body.Area)
	if err != nil {
		s.writeFileError(w, r, err)
		return
	}
	defer root.Close()

	if err := validFileName(body.Name); err != nil {
		s.writeFileError(w, r, err)
		return
	}
	if err := ensureDirectory(root, body.Path); err != nil {
		s.writeFileError(w, r, err)
		return
	}
	// Mkdir, not MkdirAll: this creates one folder somebody asked for, and a
	// name that turns out to be a path is a mistake rather than an instruction.
	if err := root.Mkdir(cleanPath(path.Join(body.Path, body.Name)), 0o775); err != nil {
		s.writeFileError(w, r, err)
		return
	}

	writeJSON(w, http.StatusCreated, map[string]any{
		"path": path.Join(body.Path, body.Name),
	})
}

func (s *Server) handleRenameFile(w http.ResponseWriter, r *http.Request, user *auth.User) {
	var body struct {
		Area string `json:"area"`
		Path string `json:"path"`
		Name string `json:"name"`
	}
	if !s.decode(w, r, &body) {
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	root, _, err := s.openWritableArea(ctx, user, body.Area)
	if err != nil {
		s.writeFileError(w, r, err)
		return
	}
	defer root.Close()

	if err := validFileName(body.Name); err != nil {
		s.writeFileError(w, r, err)
		return
	}
	// A new *name*, not a new place. Moving between folders is a different
	// operation with a different way to go wrong, and renaming is the one
	// people actually want; offering both through one endpoint means the
	// careless call is the destructive one.
	renamed := cleanPath(path.Join(path.Dir(cleanPath(body.Path)), body.Name))

	if _, err := root.Lstat(renamed); err == nil {
		s.writeError(w, r, http.StatusConflict, apiError{
			Code:        "files.name_in_use",
			Message:     "Something here is already called that.",
			Recoverable: true,
			Recovery:    "Choose a different name.",
		})
		return
	}
	if err := root.Rename(cleanPath(body.Path), renamed); err != nil {
		s.writeFileError(w, r, err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"path": renamed})
}

func (s *Server) handleRemoveFile(w http.ResponseWriter, r *http.Request, user *auth.User) {
	var body struct {
		Area string `json:"area"`
		Path string `json:"path"`
		// Confirm is the folder's own name, typed, and is required only for a
		// folder with something in it. Asking for it to delete one file would
		// make an ordinary action tedious enough that people stop reading it;
		// asking for it before deleting five hundred is the point.
		Confirm string `json:"confirm"`
	}
	if !s.decode(w, r, &body) {
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()

	root, _, err := s.openWritableArea(ctx, user, body.Area)
	if err != nil {
		s.writeFileError(w, r, err)
		return
	}
	defer root.Close()

	target := cleanPath(body.Path)
	if target == "." {
		s.writeError(w, r, http.StatusBadRequest, apiError{
			Code:        "files.cannot_remove_area",
			Message:     "Homebase will not delete a shared folder from here.",
			Recoverable: true,
			Recovery: "Stop sharing it on the Files page instead, which leaves " +
				"the files where they are.",
		})
		return
	}

	info, err := root.Lstat(target)
	if err != nil {
		s.writeFileError(w, r, err)
		return
	}

	occupied := false
	if info.IsDir() {
		if entries, err := readSomeNames(root, target); err == nil && len(entries) > 0 {
			occupied = true
		}
	}
	if occupied && body.Confirm != path.Base(target) {
		s.writeError(w, r, http.StatusConflict, apiError{
			Code:        "files.confirmation_required",
			Message:     "That folder is not empty.",
			Detail:      "type " + path.Base(target) + " to confirm",
			Recoverable: true,
			Recovery: "Deleting it deletes everything in it, and Homebase has no " +
				"wastebasket to take it out of. Type the folder's name to go ahead.",
		})
		return
	}

	// RemoveAll for a folder the caller has confirmed, Remove for anything
	// else — so an unconfirmed mistake can never take a subtree with it.
	if occupied {
		err = root.RemoveAll(target)
	} else {
		err = root.Remove(target)
	}
	if err != nil {
		s.writeFileError(w, r, err)
		return
	}

	s.log.Info("file removed", "area", body.Area, "by", user.Username)
	writeJSON(w, http.StatusOK, map[string]any{"removed": body.Path})
}

// readSomeNames answers "is there anything in here" without reading a folder of
// forty thousand files to find out.
func readSomeNames(root *os.Root, where string) ([]string, error) {
	dir, err := root.Open(where)
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	return dir.Readdirnames(1)
}

// readFormValue reads a small multipart field.
//
// Bounded, because it arrives before anything is known about the sender's
// intentions and an unbounded read of a field called "area" is a way to use all
// the memory on the machine.
func readFormValue(part io.Reader) (string, error) {
	value, err := io.ReadAll(io.LimitReader(part, 4096))
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(value)), nil
}
