// Command core is Homebase's unprivileged service.
//
// It owns the HTTP API, authentication, jobs and state, and runs as the
// `homebase` user with no capabilities. Every privileged action is a named,
// typed, audited operation performed by hostd — see
// docs/decisions/0006-privilege-split.md.
//
// If a feature here appears to need more privilege, it needs a new hostd
// operation instead.
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
	"path/filepath"
	"syscall"
	"time"

	"github.com/HusnuOkanCakir/homebase/internal/api"
	"github.com/HusnuOkanCakir/homebase/internal/auth"
	"github.com/HusnuOkanCakir/homebase/internal/hostclient"
	"github.com/HusnuOkanCakir/homebase/internal/jobs"
	"github.com/HusnuOkanCakir/homebase/internal/store"
)

var version = "dev"

func main() {
	var (
		addr        = flag.String("listen", "127.0.0.1:8080", "address to listen on")
		dbPath      = flag.String("db", "/var/lib/homebase/homebase.db", "SQLite database")
		socket      = flag.String("hostd-socket", hostclient.DefaultSocket, "hostd socket")
		staticDir   = flag.String("dashboard", "/usr/share/homebase/dashboard", "built dashboard assets")
		showVersion = flag.Bool("version", false, "print the version and exit")
	)
	flag.Parse()

	if *showVersion {
		fmt.Println(version)
		return
	}

	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	if err := run(log, *addr, *dbPath, *socket, *staticDir); err != nil {
		log.Error("core failed", "error", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger, addr, dbPath, socket, staticDir string) error {
	if os.Geteuid() == 0 {
		// Not fatal — it is convenient during development — but worth saying
		// loudly, because the whole design rests on this process not being root
		// and nobody notices a privilege they did not ask for.
		log.Warn("core is running as root; it is designed to run unprivileged and does not need it")
	}

	if err := os.MkdirAll(filepath.Dir(dbPath), 0o750); err != nil {
		return fmt.Errorf("creating the state directory: %w", err)
	}

	ctx := context.Background()

	db, err := store.Open(ctx, dbPath)
	if err != nil {
		return err
	}
	defer db.Close()

	schema, _ := db.SchemaVersion(ctx)
	log.Info("state opened", "path", dbPath, "schema", schema)

	authService := auth.NewService(db.DB())
	jobManager := jobs.NewManager(db.DB(), log)
	host := hostclient.New(socket)

	// Settle anything that was running when this process last stopped.
	//
	// This is what turns a reboot into an observable outcome: a job marked as
	// interrupting the host, whose recorded boot id differs from the current
	// one, did what it set out to do. Everything else that was running is over
	// and did not finish, and is marked failed with a message that says so —
	// because a job showing "running, 65 %" with no process behind it is
	// indistinguishable, to a user, from one that is still working.
	resolved, failed, err := jobManager.ResolveInterrupted(ctx)
	if err != nil {
		log.Error("could not resolve interrupted jobs", "error", err)
	} else if resolved+failed > 0 {
		log.Info("resolved jobs interrupted by a restart",
			"succeeded", resolved, "failed", failed)
	}

	if purged, err := authService.PurgeExpiredSessions(ctx); err == nil && purged > 0 {
		log.Info("purged expired sessions", "count", purged)
	}

	needsSetup, err := authService.NeedsSetup(ctx)
	if err != nil {
		return fmt.Errorf("checking setup state: %w", err)
	}
	if needsSetup {
		log.Info("no administrator yet; the dashboard will show first-run setup")
	}

	if !host.Healthy(ctx) {
		// Not fatal. hostd is socket-activated, so it may simply not have been
		// started yet — and core must be able to start and report the problem
		// rather than refusing to run, since a server that will not start is a
		// server nobody can diagnose.
		log.Warn("hostd is not reachable yet", "socket", socket)
	}

	server := api.NewServer(authService, jobManager, host, log, version)

	// The dashboard is optional. During development it is served by Vite on
	// another port, and a missing build directory is not a reason for the API
	// to refuse to start — a server nobody can reach is harder to diagnose
	// than one with no interface.
	if static, err := api.StaticHandler(staticDir); err != nil {
		log.Warn("no dashboard to serve; the API is still available",
			"path", staticDir, "reason", err)
	} else {
		server.SetStatic(static)
		log.Info("serving the dashboard", "path", staticDir)
	}

	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listening on %s: %w", addr, err)
	}

	httpServer := &http.Server{
		Handler:           server.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       60 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}

	log.Info("core listening", "addr", listener.Addr().String(), "version", version)

	errCh := make(chan error, 1)
	go func() {
		if err := httpServer.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	signalCtx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	select {
	case err := <-errCh:
		return err
	case <-signalCtx.Done():
		log.Info("shutting down")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return httpServer.Shutdown(shutdownCtx)
}
