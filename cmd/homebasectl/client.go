package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Talking to Homebase from a terminal.
//
// `homebasectl` is an ordinary API client with no privileges of its own — the
// same rule as the dashboard, and for the same reason. It could open the Unix
// socket directly, since it usually runs as root and root can, but that would
// make a second path to every privileged operation with none of core's
// permission checks, its job records or its events on it. ADR-0006 says there is
// one surface. This is a client of it.
//
// **Authentication is by being on the machine.** Running as root, it reads the
// database — which root can read anyway, with sqlite3, less carefully — and
// mints a short-lived session. There is no escalation in that: somebody who is
// root on the box already has everything Homebase could give them. What it buys
// is that `sudo homebasectl apps list` simply works, with no token to create,
// store, rotate or leak.
//
// Not running as root, it needs a token, from `HOMEBASE_TOKEN` or the
// configuration file. That is the path for a script running as an ordinary user,
// and it is deliberately the less convenient one.

const (
	// The server's own address, over HTTPS on the ordinary port.
	//
	// Localhost rather than the machine's name: this runs on the server, and a
	// name that has to resolve is one more thing that can be broken on a machine
	// somebody is trying to fix.
	defaultAddress = "https://127.0.0.1"

	// How long a session minted for one command lives. Minutes rather than the
	// browser's weeks: it exists for the length of a command, and a token that
	// outlives its purpose is one somebody finds later.
	cliSessionLifetime = 10 * time.Minute
)

// Client is a connection to core's API.
type Client struct {
	http    *http.Client
	address string
	token   string

	// releaseSession is called when the client is closed, for a session this
	// process minted. A session left behind after every command would fill the
	// table with credentials nobody is using.
	releaseSession func()
}

// APIError is a failure core described, in the shape of schemas/error.schema.json.
type APIError struct {
	Status      int
	Code        string `json:"code"`
	Message     string `json:"message"`
	Detail      string `json:"detail,omitempty"`
	Recoverable bool   `json:"recoverable"`
	Recovery    string `json:"recovery,omitempty"`
}

func (e *APIError) Error() string {
	out := e.Message
	if e.Detail != "" {
		out += "\n    " + e.Detail
	}
	if e.Recovery != "" {
		out += "\n\n" + e.Recovery
	}
	return out
}

// connect builds a client, working out how to authenticate.
func connect(ctx context.Context, address, database string) (*Client, error) {
	client := &Client{
		address: strings.TrimSuffix(address, "/"),
		http: &http.Client{
			Timeout: 5 * time.Minute,
			Transport: &http.Transport{
				// The server's certificate is the self-signed one it generated
				// for itself (ADR-0017), and this connection is to 127.0.0.1 on
				// the same machine. There is no certificate authority that could
				// vouch for it and no network for anybody to be in the middle
				// of: the packets do not leave the host.
				//
				// This is the one place in Homebase that skips verification, and
				// it is worth being explicit that it is not a shortcut for a
				// remote client. Reaching a *different* machine from here would
				// need the fingerprint checked, and there is no flag for it
				// because there is no such command yet.
				TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec
			},
		},
	}

	if token := strings.TrimSpace(os.Getenv("HOMEBASE_TOKEN")); token != "" {
		client.token = token
		return client, nil
	}

	if token, err := tokenFromFile(); err == nil && token != "" {
		client.token = token
		return client, nil
	}

	if os.Geteuid() != 0 {
		return nil, errors.New(
			"Homebase does not know who you are.\n\n" +
				"Run this with sudo, or put a token in HOMEBASE_TOKEN or " +
				configPath() + ".\n" +
				"Running as root, homebasectl authenticates by reading the " +
				"database, which\nis what root can do anyway.")
	}

	token, release, err := mintSession(ctx, database)
	if err != nil {
		return nil, err
	}
	client.token = token
	client.releaseSession = release
	return client, nil
}

// mintSession creates a session by reading the database directly.
//
// The account is chosen the same way `recovery-code` chooses one: the only
// administrator if there is one, and otherwise an error naming the choice.
func mintSession(ctx context.Context, database string) (string, func(), error) {
	service, _, closeDB, err := open(ctx, database)
	if err != nil {
		return "", nil, err
	}

	name, err := chooseAccount(ctx, service, "")
	if err != nil {
		closeDB()
		return "", nil, err
	}

	user, err := service.UserByName(ctx, name)
	if err != nil {
		closeDB()
		return "", nil, err
	}

	// Named so it is recognisable in the session list. Somebody looking at the
	// sessions on their server should be able to tell a terminal from a browser.
	token, _, err := service.CreateSession(ctx, user.ID, "homebasectl")
	if err != nil {
		closeDB()
		return "", nil, err
	}

	release := func() {
		// Best effort, and deliberately not fatal: the session expires on its
		// own, and a command that has already done its work must not report
		// failure because it could not tidy up after itself.
		revoke, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = service.DeleteSession(revoke, token)
		closeDB()
	}
	return token, release, nil
}

func (c *Client) Close() {
	if c.releaseSession != nil {
		c.releaseSession()
	}
}

func configPath() string {
	if dir, err := os.UserConfigDir(); err == nil {
		return filepath.Join(dir, "homebase", "token")
	}
	return "~/.config/homebase/token"
}

func tokenFromFile() (string, error) {
	raw, err := os.ReadFile(configPath())
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(raw)), nil
}

// --- Requests ------------------------------------------------------------------

func (c *Client) Get(ctx context.Context, path string, into any) error {
	return c.do(ctx, http.MethodGet, path, nil, into)
}

func (c *Client) Post(ctx context.Context, path string, body, into any) error {
	return c.do(ctx, http.MethodPost, path, body, into)
}

func (c *Client) do(ctx context.Context, method, path string, body, into any) error {
	var payload io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return err
		}
		payload = bytes.NewReader(encoded)
	}

	request, err := http.NewRequestWithContext(ctx, method, c.address+"/api/v1"+path, payload)
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+c.token)

	response, err := c.http.Do(request)
	if err != nil {
		return notRunning(err)
	}
	defer func() { _ = response.Body.Close() }()

	raw, err := io.ReadAll(io.LimitReader(response.Body, 8<<20))
	if err != nil {
		return err
	}

	if response.StatusCode >= 400 {
		return decodeError(response.StatusCode, raw)
	}
	if into == nil || len(raw) == 0 {
		return nil
	}
	return json.Unmarshal(raw, into)
}

// notRunning turns a connection failure into something that says what to do.
//
// "connection refused" is accurate and useless. The machine this runs on is the
// machine the server is on, so a refused connection means one specific thing.
func notRunning(err error) error {
	return fmt.Errorf("Homebase is not answering on this machine.\n    %v\n\n"+
		"Check it is running:  systemctl status homebase-core\n"+
		"Then try:             sudo homebasectl repair", err)
}

func decodeError(status int, raw []byte) error {
	var envelope struct {
		Error APIError `json:"error"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil || envelope.Error.Message == "" {
		body := strings.TrimSpace(string(raw))
		if len(body) > 400 {
			body = body[:400]
		}
		return fmt.Errorf("the server answered %d:\n    %s", status, body)
	}
	envelope.Error.Status = status
	return &envelope.Error
}

// askForSecret reads a secret from the terminal without echoing it.
//
// Not a flag, and not an argument. A password on the command line is in the
// shell's history file, and it is in `ps` output for every user on the machine
// for as long as the command runs — which for joining a Wi-Fi network is up to a
// minute.
//
// Echo is turned off through `stty`, because doing it properly needs
// golang.org/x/term and `homebasectl` ships in the same package as `hostd`,
// which has no third-party dependencies at all (ADR-0002). If `stty` is missing
// or this is not a terminal, the read still happens — visibly — with a warning,
// because refusing to work is worse than a password on the screen of a machine
// somebody is sitting at.
func askForSecret(prompt string) (string, error) {
	fmt.Fprint(os.Stderr, prompt)

	restore, hidden := hideInput()
	if !hidden {
		fmt.Fprint(os.Stderr, "\n(this will be visible) ")
	}
	defer restore()

	reader := bufio.NewReader(os.Stdin)
	line, err := reader.ReadString('\n')
	fmt.Fprintln(os.Stderr)
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}

func hideInput() (restore func(), hidden bool) {
	stty, err := exec.LookPath("stty")
	if err != nil {
		return func() {}, false
	}
	// The current settings, so they can be put back exactly rather than guessed
	// at — a terminal left with echo off after a failed command is a terminal
	// somebody has to close.
	saved, err := exec.Command(stty, "-F", "/dev/tty", "-g").Output()
	if err != nil {
		return func() {}, false
	}
	if err := exec.Command(stty, "-F", "/dev/tty", "-echo").Run(); err != nil {
		return func() {}, false
	}
	return func() {
		_ = exec.Command(stty, "-F", "/dev/tty", strings.TrimSpace(string(saved))).Run()
	}, true
}
