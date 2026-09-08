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
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/HusnuOkanCakir/homebase/internal/api"
	"github.com/HusnuOkanCakir/homebase/internal/auth"
	"github.com/HusnuOkanCakir/homebase/internal/events"
	"github.com/HusnuOkanCakir/homebase/internal/hostclient"
	"github.com/HusnuOkanCakir/homebase/internal/jobs"
	"github.com/HusnuOkanCakir/homebase/internal/store"
)

var version = "dev"

// envOr reads a setting from the environment, falling back to a default.
func envOr(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

func main() {
	var (
		// Local by default, because that is the safe answer for a binary
		// somebody has just built and run. The appliance is not that case: the
		// packaged unit sets HOMEBASE_LISTEN, because a Homebase nobody can
		// reach from another room is not a Homebase.
		//
		// Read from the environment rather than written into the unit's
		// ExecStart so that overriding it is a one-line drop-in. A drop-in that
		// has to restate the whole command line silently drops any flag added
		// to it later — which is how the address ended up wrong in two places
		// at once, each for its own good reason.
		addr = flag.String("listen", envOr("HOMEBASE_LISTEN", "127.0.0.1:8080"), "address to listen on")
		// Where the dashboard is actually used from. A password crossing a home
		// network in the clear crosses a network the threat model says contains
		// a smart television with unpatched firmware; the plain port redirects
		// here rather than serving anything of its own.
		//
		// Empty switches TLS off, which is how `make run` and the unit tests
		// work — a developer running this on their own machine reaches it over
		// loopback, which browsers already treat as a secure origin.
		tlsAddr   = flag.String("listen-tls", envOr("HOMEBASE_LISTEN_TLS", ""), "address to serve HTTPS on")
		stateDir  = flag.String("state-dir", envOr("HOMEBASE_STATE_DIR", "/var/lib/homebase"), "where the certificate is kept")
		dbPath    = flag.String("db", "/var/lib/homebase/homebase.db", "SQLite database")
		socket    = flag.String("hostd-socket", hostclient.DefaultSocket, "hostd socket")
		staticDir = flag.String("dashboard", "/usr/share/homebase/dashboard", "built dashboard assets")
		// The local assistant, off unless pointed at something.
		//
		// Empty by default and deliberately so: a language model is not part of
		// a home server the way storage and backups are, and an installation
		// that never asked for one should show no sign of it. Setting this is
		// what turns the tab on.
		assistantURL = flag.String("assistant-url",
			envOr("HOMEBASE_ASSISTANT_URL", ""),
			"OpenAI-compatible URL of a local model, e.g. http://127.0.0.1:8088/v1")
		// A contained model that is already running, if this machine has one.
		//
		// A socket rather than a URL, because the model it points at has no
		// network at all. Homebase can use it and has no way to start it: there
		// is no operation anywhere that unlocks its volume or launches its
		// unit, and adding one would mean a privileged operation whose purpose
		// is to make a model with its refusals removed easier to reach.
		assistantUnrestricted = flag.String("assistant-unrestricted-socket",
			envOr("HOMEBASE_ASSISTANT_UNRESTRICTED_SOCKET", ""),
			"Unix socket of a contained model, offered only to accounts holding assistant.unrestricted")
		assistantKey = flag.String("assistant-key-file",
			envOr("HOMEBASE_ASSISTANT_KEY_FILE", "/etc/qwen-lab/api-key"),
			"file holding the local model's API key")
		// A certificate a browser already trusts, when something can produce one.
		//
		// Empty by default: most machines have no public name, and the
		// self-signed certificate with its printed fingerprint is the honest
		// answer for them.
		tlsCert = flag.String("tls-cert", envOr("HOMEBASE_TLS_CERT", ""),
			"PEM certificate to serve instead of the self-signed one")
		tlsKey = flag.String("tls-key", envOr("HOMEBASE_TLS_KEY", ""),
			"PEM private key for --tls-cert")
		showVersion = flag.Bool("version", false, "print the version and exit")
	)
	flag.Parse()

	if *showVersion {
		fmt.Println(version)
		return
	}

	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	if err := run(log, runOptions{
		addr:      *addr,
		tlsAddr:   *tlsAddr,
		stateDir:  *stateDir,
		dbPath:    *dbPath,
		socket:    *socket,
		staticDir: *staticDir,

		assistantURL:          *assistantURL,
		assistantKeyFile:      *assistantKey,
		assistantUnrestricted: *assistantUnrestricted,
		tlsCert:               *tlsCert,
		tlsKey:                *tlsKey,
	}); err != nil {
		log.Error("core failed", "error", err)
		os.Exit(1)
	}
}

// runOptions is a struct rather than six string arguments, because six strings
// of the same type in a row is a call nobody can read and an argument order
// somebody eventually gets wrong.
type runOptions struct {
	addr      string
	tlsAddr   string
	stateDir  string
	dbPath    string
	socket    string
	staticDir string

	// Empty unless this machine has a local model. See the flag.
	assistantURL     string
	assistantKeyFile string

	// Empty unless this machine has a contained model running. See the flag.
	assistantUnrestricted string

	// Empty unless something can produce a certificate browsers trust.
	tlsCert string
	tlsKey  string
}

func run(log *slog.Logger, opts runOptions) error {
	addr, tlsAddr, stateDir := opts.addr, opts.tlsAddr, opts.stateDir
	dbPath, socket, staticDir := opts.dbPath, opts.socket, opts.staticDir

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

	eventRecorder := events.NewRecorder(db.DB(), log)

	server := api.NewServer(authService, jobManager, host, eventRecorder, log, version)

	// Everybody who has an account gets the private folder an account created
	// today comes with.
	//
	// Folders are made when an account is created and again when somebody signs
	// in, and neither reached the administrator: a session lasts a fortnight, so
	// the person who set the server up and never signed out never signed in
	// either. `people` opened for the rest of the household and refused him,
	// with the reason in a Samba log he had no reason to read.
	//
	// In the background and idempotent, so a server that is already correct
	// does nothing.
	server.EnsureEveryoneHasAFolder(context.Background())

	// A local model, if this machine has one. Nothing is verified here: whether
	// it answers is a question for the moment somebody asks it something, not
	// for startup, and core must not refuse to boot because a model is down.
	if opts.assistantURL != "" {
		server = server.WithAssistant(opts.assistantURL, opts.assistantKeyFile)
		log.Info("local assistant configured", "url", opts.assistantURL)
	}
	if opts.assistantUnrestricted != "" {
		server = server.WithUnrestrictedAssistant(opts.assistantUnrestricted)
		log.Info("a contained model may be used if it is running",
			"socket", opts.assistantUnrestricted)
	}

	// Notice a disk filling up before applications start failing to write.
	// A server that runs out of space does not announce it: applications fail in
	// whatever way each of them fails, and the common cause is visible only to
	// somebody who thinks to look.
	go api.NewSpaceWatcher(host, eventRecorder).Watch(ctx)

	// Record how hot the machine has been, and how hard its fan has worked.
	//
	// One reading tells you almost nothing — 58 °C is fine, or it is the start
	// of an afternoon that ends in thermal shutdown, and the difference is
	// entirely in what the last week looked like.
	go api.NewThermalLog(host, log, api.DefaultThermalLogPath).Watch(ctx)

	// Forget rate-limit buckets nobody has used lately, so a machine being
	// sprayed with requests from many addresses does not accumulate one entry
	// per address for as long as it stays up.
	go server.MaintainRateLimits(ctx)

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

	errCh := make(chan error, 1)

	// HTTPS, when this is an installation rather than somebody's laptop.
	//
	// Set up before the plain listener, because what the plain port serves
	// depends on whether this exists: a redirect when it does, the dashboard
	// itself when it does not.
	var tlsServer *http.Server
	if tlsAddr != "" {
		identity, err := api.EnsureCertificate(stateDir, serverNames(log))
		if err != nil {
			return fmt.Errorf("preparing the certificate: %w", err)
		}

		tlsListener, err := net.Listen("tcp", tlsAddr)
		if err != nil {
			return fmt.Errorf("listening on %s: %w", tlsAddr, err)
		}

		tlsServer = &http.Server{
			Handler:           server.Handler(),
			ReadHeaderTimeout: 10 * time.Second,
			ReadTimeout:       60 * time.Second,
			WriteTimeout:      60 * time.Second,
			IdleTimeout:       2 * time.Minute,
		}

		// A certificate a browser already trusts, if this machine has one.
		//
		// Tailscale is currently the only thing that can produce one here: it
		// gives the machine a real name in a real zone and obtains a Let's
		// Encrypt certificate over DNS-01, without the machine being reachable
		// from the internet — which is the part that used to be impossible.
		// Nothing here is specific to it, though; any pair of PEM files works.
		//
		// The self-signed identity is still prepared above and still stands
		// behind this. If the trusted pair is missing or broken at the moment of
		// a handshake, that one is served and the browser warns — which somebody
		// can click through to reach the screen where they would fix it.
		if opts.tlsCert != "" && opts.tlsKey != "" {
			fallback, err := tls.LoadX509KeyPair(identity.CertPath, identity.KeyPath)
			if err != nil {
				return fmt.Errorf("loading the self-signed certificate: %w", err)
			}
			reloading := api.NewReloadingCertificate(opts.tlsCert, opts.tlsKey, &fallback, log)
			tlsServer.TLSConfig = &tls.Config{
				MinVersion:     tls.VersionTLS12,
				GetCertificate: reloading.GetCertificate,
			}
			log.Info("a trusted certificate will be served when present", "cert", opts.tlsCert)
		}

		// Logged so the fingerprint is in the journal as well as on the screen:
		// somebody helping over the phone needs to be able to read it out.
		log.Info("serving HTTPS",
			"addr", tlsListener.Addr().String(),
			"names", strings.Join(identity.Names, ", "),
			"fingerprint", identity.Fingerprint)

		go func() {
			// Empty paths when TLSConfig already carries GetCertificate:
			// ServeTLS only consults the config when it is given none, and
			// passing both would pin the self-signed one forever.
			certPath, keyPath := identity.CertPath, identity.KeyPath
			if tlsServer.TLSConfig != nil && tlsServer.TLSConfig.GetCertificate != nil {
				certPath, keyPath = "", ""
			}
			if err := tlsServer.ServeTLS(tlsListener, certPath, keyPath); err != nil &&
				!errors.Is(err, http.ErrServerClosed) {
				errCh <- err
			}
		}()
	}

	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listening on %s: %w", addr, err)
	}

	plainHandler := server.Handler()
	if tlsServer != nil {
		plainHandler = api.RedirectToTLS(tlsAddr)
	}

	httpServer := &http.Server{
		Handler:           plainHandler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       60 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}

	log.Info("core listening", "addr", listener.Addr().String(), "version", version)

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

// serverNames is what the certificate should be valid for.
//
// The hostname and its mDNS form. Addresses are added by the certificate code
// itself, because they are a property of the machine rather than a choice —
// and because they change with DHCP while the name does not.
func serverNames(log *slog.Logger) []string {
	hostname, err := os.Hostname()
	if err != nil || strings.TrimSpace(hostname) == "" {
		// A machine with no hostname still needs a certificate, and "homebase"
		// is what the installer would have called it.
		log.Warn("could not read the hostname; the certificate will use a default", "error", err)
		hostname = "homebase"
	}
	hostname = strings.TrimSuffix(strings.ToLower(hostname), ".local")

	return []string{hostname, hostname + ".local"}
}
