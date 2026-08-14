package hostd

import (
	"encoding/json"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
)

// The account an application runs as.
//
// Every application gets its own system account, and everything it owns — its
// private directory, and its folder on whatever disk the user gave it — belongs
// to that account. The container runs as it too.
//
// This exists because of a bug that made the catalogue mostly non-functional.
// Containers ran as root with every capability dropped, which is the right
// hardening and has a consequence nobody traced: root without CAP_DAC_OVERRIDE
// does not bypass file permissions. The data directories were 0750 and owned by
// the `homebase` service account, so no application could write its own files:
//
//	File Browser:  panic: stat /config/filebrowser.db: permission denied
//	Jellyfin:      Access to the path '/config/log' is denied
//
// Two of the three catalogued applications could not run at all, and the tests
// stayed green because the third writes nothing to disk.
//
// The obvious repair — run everything as the service account, which already owns
// the directories — would have worked and would have made every application
// share one identity. Separate accounts cost more and buy the thing that matters
// when a container is the part that gets compromised: an application can reach
// its own files and nothing else's, enforced by the kernel rather than by the
// container runtime.

// Identifiers are allocated rather than created as accounts.
//
// The first attempt shelled out to `useradd`, which failed:
//
//	useradd: cannot lock /etc/passwd; try again later.
//
// hostd runs under ProtectSystem=strict and cannot write /etc — correctly. The
// repair would have been to grant it write access to /etc/passwd, /etc/shadow
// and /etc/group, which is a far worse trade than the problem: write access to
// /etc/shadow is the whole machine. So Homebase allocates a number instead.
//
// Nothing needs a passwd entry. A uid owns files and a container runs as it;
// neither operation resolves a name. The cost is that `ls -l` shows a number
// rather than a name, which is why the mapping is written down somewhere an
// administrator can read it.

const (
	// appUIDBase is where allocation starts. Well above the system range Debian
	// hands out (100–999) and the ordinary user range, and well below 65534, so
	// nothing here can collide with an account the distribution created.
	appUIDBase = 61000

	// appUIDLimit is how many applications one machine can have. Generous: the
	// catalogue has three.
	appUIDLimit = 1000

	appUIDFile = "app-uids.json"
)

// owner is a uid/gid pair. Applications get the same number for both: the group
// exists so that a directory can be group-readable by exactly one application.
type owner struct {
	uid int
	gid int
}

func (o owner) String() string { return strconv.Itoa(o.uid) + ":" + strconv.Itoa(o.gid) }

// ensureAppOwner returns the identifier an application runs as, allocating one
// the first time and reusing it for ever after.
//
// Stability is the whole point: the number is written into the ownership of
// every file the application has, so a machine that allocated differently after
// a restart would leave applications unable to read their own data.
func ensureAppOwner(stateDir, appID string) (owner, error) {
	path := filepath.Join(stateDir, appUIDFile)

	allocated := map[string]int{}
	if raw, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(raw, &allocated); err != nil {
			return owner{}, fmt.Errorf("reading %s: %w", path, err)
		}
	} else if !os.IsNotExist(err) {
		return owner{}, fmt.Errorf("reading %s: %w", path, err)
	}

	if uid, ok := allocated[appID]; ok {
		return owner{uid: uid, gid: uid}, nil
	}

	taken := map[int]bool{}
	for _, uid := range allocated {
		taken[uid] = true
	}

	uid := 0
	for candidate := appUIDBase; candidate < appUIDBase+appUIDLimit; candidate++ {
		if taken[candidate] {
			continue
		}
		// Skipped if the distribution has since created an account with this
		// number: sharing one would undo the isolation this exists for.
		if _, err := user.LookupId(strconv.Itoa(candidate)); err == nil {
			continue
		}
		uid = candidate
		break
	}
	if uid == 0 {
		return owner{}, fmt.Errorf("no free identifier for %s: all %d are allocated", appID, appUIDLimit)
	}

	allocated[appID] = uid
	encoded, err := json.MarshalIndent(allocated, "", "  ")
	if err != nil {
		return owner{}, err
	}
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return owner{}, err
	}
	// Written before it is used. An identifier handed to a chown and then lost
	// would leave files owned by a number nothing remembers.
	if err := os.WriteFile(path, append(encoded, '\n'), 0o600); err != nil {
		return owner{}, fmt.Errorf("recording the identifier for %s: %w", appID, err)
	}

	return owner{uid: uid, gid: uid}, nil
}

// giveTo hands a directory and everything already in it to an account.
//
// Recursive because of upgrades: a machine that installed an application before
// this existed has files owned by the old service account, and an application
// that cannot read its own history is not meaningfully better off than one that
// could not write at all. Walking is bounded by the application's own directory,
// never by the disk it sits on.
func giveTo(path string, to owner) error {
	return filepath.Walk(path, func(name string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		// Symlinks are changed, not followed: following one would let a link
		// planted inside an application's own directory redirect a root chown
		// at something outside it.
		if info.Mode()&os.ModeSymlink != 0 {
			return os.Lchown(name, to.uid, to.gid)
		}
		return os.Chown(name, to.uid, to.gid)
	})
}
