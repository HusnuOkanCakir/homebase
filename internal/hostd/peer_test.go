package hostd

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// These tests run against a real Unix socket rather than httptest, because
// SO_PEERCRED is the thing under test and it does not exist without one.
func serveOnSocket(t *testing.T, allowedUID uint32) (socket string, stop func()) {
	t.Helper()

	registry := NewRegistry()
	if err := registry.Register(readOp()); err != nil {
		t.Fatal(err)
	}

	socket = filepath.Join(t.TempDir(), "hostd.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatalf("listening on %s: %v", socket, err)
	}
	if err := os.Chmod(socket, 0o660); err != nil {
		t.Fatal(err)
	}

	server := NewServer(registry, NewAuditor(io.Discard), slog.New(slog.NewTextHandler(io.Discard, nil)), allowedUID)
	httpServer := server.HTTPServer()

	go func() { _ = httpServer.Serve(listener) }()

	return socket, func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = httpServer.Shutdown(ctx)
	}
}

func socketClient(socket string) *http.Client {
	return &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "unix", socket)
			},
		},
	}
}

// The socket mode (0660 root:homebase) is the primary control. This check is the
// second one, and it exists because the mode lives in packaging — a packaging
// change could widen it to 0666 without touching a line of Go, and this is what
// still refuses the connection when that happens.
func TestPeerWithTheWrongUIDIsRejected(t *testing.T) {
	// Expect a uid this process cannot possibly have.
	wrongUID := uint32(os.Getuid() + 1000)
	if os.Getuid() == 0 {
		t.Skip("running as root, which is allowed by design so recovery tooling works")
	}

	socket, stop := serveOnSocket(t, wrongUID)
	defer stop()

	resp, err := socketClient(socket).Post("http://hostd/v1/op/test.read", "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusForbidden {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, want 403; a peer with the wrong uid was served\nbody: %s", resp.StatusCode, body)
	}

	var envelope struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Error.Code != ErrPeerRejected.Code {
		t.Errorf("error code = %q, want %q", envelope.Error.Code, ErrPeerRejected.Code)
	}
}

func TestPeerWithTheExpectedUIDIsAccepted(t *testing.T) {
	socket, stop := serveOnSocket(t, uint32(os.Getuid()))
	defer stop()

	resp, err := socketClient(socket).Post("http://hostd/v1/op/test.read", "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, want 200\nbody: %s", resp.StatusCode, body)
	}
}

// The describe endpoint maps the privileged surface, so it sits behind the same
// check as everything else even though it changes nothing.
func TestDescribeIsBehindThePeerCheck(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("running as root, which is allowed by design")
	}

	socket, stop := serveOnSocket(t, uint32(os.Getuid()+1000))
	defer stop()

	resp, err := socketClient(socket).Get("http://hostd/v1/operations")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; the operation list was served to an unexpected peer", resp.StatusCode)
	}
}

// The credentials come from the kernel, so a client claiming to be someone else
// in a header changes nothing.
func TestPeerIdentityCannotBeSpoofedByHeaders(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("running as root, which is allowed by design")
	}

	socket, stop := serveOnSocket(t, uint32(os.Getuid()+1000))
	defer stop()

	req, err := http.NewRequest(http.MethodPost, "http://hostd/v1/op/test.read", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	// A determined caller trying every plausible way to assert an identity.
	req.Header.Set("X-Peer-Uid", "0")
	req.Header.Set("X-Forwarded-User", "root")
	req.Header.Set("Authorization", "Bearer root")

	resp, err := socketClient(socket).Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; headers influenced the peer decision", resp.StatusCode)
	}
}
