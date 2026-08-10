// Command hostd is Homebase's privileged host service.
//
// It runs as root and accepts a fixed, compiled-in set of named operations over
// a Unix socket. There is no generic execution path and no dynamic dispatch —
// see docs/decisions/0006-privilege-split.md.
//
// It must start, run and be debuggable on its own. When core will not start,
// hostd is what is left to diagnose the machine with, and that is exactly the
// situation where it is needed most.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"os/user"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/HusnuOkanCakir/homebase/internal/hostd"
)

const (
	defaultSocket   = "/run/homebase/hostd.sock"
	defaultAuditLog = "/var/log/homebase/audit.log"
	defaultPeerUser = "homebase"
)

func main() {
	var (
		socketPath   = flag.String("socket", defaultSocket, "Unix socket to listen on")
		auditPath    = flag.String("audit-log", defaultAuditLog, "append-only audit log")
		peerUser     = flag.String("peer-user", defaultPeerUser, "the unprivileged user permitted to connect")
		catalogue    = flag.String("catalogue", hostd.DefaultCatalogueDir, "directory of application manifests")
		dockerSock   = flag.String("docker-socket", "", "Docker socket (default /var/run/docker.sock)")
		appData      = flag.String("app-data", hostd.DefaultAppDataRoot, "directory holding application data")
		stateDir     = flag.String("state-dir", hostd.DefaultStateDir, "hostd's own state directory")
		storageDir   = flag.String("storage-root", hostd.DefaultStorageRoot, "where managed disks are mounted")
		databasePath = flag.String("database", "/var/lib/homebase/homebase.db",
			"core's database, exported into a backup")
		configDir = flag.String("config-dir", "/etc/homebase", "configuration to back up")
		describe  = flag.Bool("describe", false, "print the operation registry as JSON and exit")
		version   = flag.Bool("version", false, "print the version and exit")
	)
	flag.Parse()

	registry := hostd.NewRegistry()
	hostd.RegisterSystemOperations(registry)

	// The catalogue is read at startup from root-owned files on disk. It is what
	// bounds the set of containers this machine can run, and core cannot add to
	// it — see ADR-0012.
	apps := hostd.NewCatalogue(*catalogue)
	if err := apps.Load(); err != nil {
		fmt.Fprintln(os.Stderr, "hostd: "+err.Error())
		os.Exit(1)
	}
	storage := hostd.NewStorageServices(*storageDir, *stateDir)
	hostd.RegisterStorageOperations(registry, storage)

	appServices := hostd.NewAppServices(apps, *dockerSock, *appData, *stateDir).WithStorage(storage)
	hostd.RegisterAppOperations(registry, appServices)

	hostd.RegisterBackupOperations(registry, hostd.NewBackupServices(
		storage, appServices, *databasePath, *configDir, *stateDir, buildVersion()))

	// --describe needs no socket, no root and no audit log. It exists so that
	// the privileged surface can be inspected — by a reviewer, by the docs
	// build, by contract tests, and later by the Stage 2 policy engine —
	// without running the service.
	if *describe {
		body, err := registry.Describe()
		if err != nil {
			fmt.Fprintln(os.Stderr, "hostd: "+err.Error())
			os.Exit(1)
		}
		fmt.Println(string(body))
		return
	}

	if *version {
		fmt.Println(buildVersion())
		return
	}

	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	if err := run(log, registry, apps, *socketPath, *auditPath, *peerUser); err != nil {
		log.Error("hostd failed", "error", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger, registry *hostd.Registry, apps *hostd.Catalogue,
	socketPath, auditPath, peerUser string) error {
	if os.Geteuid() != 0 {
		// Not fatal: it is useful to run hostd unprivileged while developing,
		// and the operations that need root will fail on their own terms with a
		// comprehensible message. Saying so once at startup is better than
		// letting someone puzzle over the first failure.
		log.Warn("not running as root; operations needing privilege will fail")
	}

	uid, err := resolvePeerUID(peerUser)
	if err != nil {
		return fmt.Errorf("resolving peer user %q: %w", peerUser, err)
	}

	if err := os.MkdirAll(filepath.Dir(auditPath), 0o750); err != nil {
		return fmt.Errorf("creating the audit log directory: %w", err)
	}
	auditFile, err := hostd.OpenAuditLog(auditPath)
	if err != nil {
		return fmt.Errorf("opening the audit log: %w", err)
	}
	defer auditFile.Close()

	// Prefer the socket systemd created: its mode and group come from the unit
	// file, where the privilege boundary is reviewable, rather than from
	// whatever this process sets at runtime.
	listener, activated, err := hostd.ListenerFromSystemd()
	if err != nil {
		return fmt.Errorf("adopting the systemd socket: %w", err)
	}
	if !activated {
		listener, err = listen(socketPath, uid)
		if err != nil {
			return err
		}
	}
	defer listener.Close()

	server := hostd.NewServer(registry, hostd.NewAuditor(auditFile), log, uid)
	httpServer := server.HTTPServer()

	// A rejected manifest is reported rather than merely absent: "Jellyfin is
	// missing from the list" is far harder to diagnose than "Jellyfin's manifest
	// is invalid, and here is why".
	for name, reason := range apps.Rejected() {
		log.Error("rejected an application manifest", "file", name, "reason", reason)
	}

	log.Info("hostd listening",
		"socket", listener.Addr().String(),
		"applications", len(apps.IDs()),
		"socket_activated", activated,
		"operations", len(registry.Names()),
		"peer_user", peerUser,
		"peer_uid", uid,
	)

	// Type=notify: without this systemd waits for a readiness message that never
	// arrives and declares the unit failed while the process runs perfectly well.
	if err := hostd.NotifyReady(); err != nil {
		log.Warn("could not notify systemd", "error", err)
	}

	errCh := make(chan error, 1)
	go func() {
		if err := httpServer.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		log.Info("shutting down")
		_ = hostd.NotifyStopping()
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return httpServer.Shutdown(shutdownCtx)
}

// listen creates the Unix socket with the permissions that make the privilege
// split real.
//
// Mode 0660 with group homebase means core can connect and nothing else on the
// machine can. Widening this to 0666 would remove the boundary without touching
// a line of application code, which is why it is set here rather than left to
// whatever umask the service happens to inherit.
func listen(path string, gid uint32) (net.Listener, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("creating the socket directory: %w", err)
	}

	// A socket left behind by an unclean shutdown would make bind fail. Removing
	// it is safe: if another hostd were live, systemd would not have started us.
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("removing the stale socket: %w", err)
	}

	listener, err := net.Listen("unix", path)
	if err != nil {
		return nil, fmt.Errorf("listening on %s: %w", path, err)
	}

	if err := os.Chmod(path, 0o660); err != nil {
		listener.Close()
		return nil, fmt.Errorf("setting socket permissions: %w", err)
	}

	// Best effort: on a machine where the homebase group does not exist yet,
	// the socket stays root-owned and only root can connect. That fails closed,
	// which is the right direction.
	if err := os.Chown(path, 0, int(gid)); err != nil {
		return listener, nil
	}

	return listener, nil
}

func resolvePeerUID(name string) (uint32, error) {
	u, err := user.Lookup(name)
	if err != nil {
		var unknown user.UnknownUserError
		if errors.As(err, &unknown) {
			return 0, fmt.Errorf(
				"user %q does not exist; the package should create it, "+
					"or pass --peer-user for development", name)
		}
		return 0, err
	}
	uid, err := strconv.ParseUint(u.Uid, 10, 32)
	if err != nil {
		return 0, err
	}
	return uint32(uid), nil
}

// version is set at build time with -ldflags.
var version = "dev"

func buildVersion() string { return version }
