package api

import (
	"crypto/x509"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The certificate a user is asked to trust once.
//
// What matters here is not that TLS works — Go's library does that — but that
// the thing the user checks stays stable. A fingerprint that changes on its own
// turns "compare these letters" into "click through the warning", which is the
// habit this whole design exists to avoid teaching.

func TestCertificateIsCreatedAndReused(t *testing.T) {
	dir := t.TempDir()

	first, err := EnsureCertificate(dir, []string{"homebase", "homebase.local"})
	if err != nil {
		t.Fatal(err)
	}
	if first.Fingerprint == "" {
		t.Fatal("no fingerprint, so there is nothing for a user to check")
	}

	// Shown to a person reading it off one screen and comparing it to another,
	// so it has to be in the form browsers use.
	if got := strings.Count(first.Fingerprint, ":"); got != 31 {
		t.Errorf("fingerprint has %d separators, want 31: %q", got, first.Fingerprint)
	}

	second, err := EnsureCertificate(dir, []string{"homebase", "homebase.local"})
	if err != nil {
		t.Fatal(err)
	}
	if second.Fingerprint != first.Fingerprint {
		t.Error("restarting produced a different certificate, so every restart " +
			"would ask the user to trust the machine again")
	}
}

func TestCertificateCoversTheNamesTheServerAnswersTo(t *testing.T) {
	dir := t.TempDir()

	identity, err := EnsureCertificate(dir, []string{"attic", "attic.local"})
	if err != nil {
		t.Fatal(err)
	}

	cert := parse(t, identity.CertPath)

	for _, name := range []string{"attic", "attic.local", "localhost"} {
		if err := cert.VerifyHostname(name); err != nil {
			t.Errorf("the certificate is not valid for %q: %v", name, err)
		}
	}

	// Loopback is in there because that is how the machine reaches itself, and
	// how anybody diagnosing it from its own console does.
	if len(cert.IPAddresses) == 0 {
		t.Error("no addresses in the certificate; reaching the server by address " +
			"would produce a second, different-looking warning")
	}
}

// A rename changes the name people reach the machine by, so the certificate has
// to follow — and that is the one time the fingerprint legitimately changes.
func TestRenamingReplacesTheCertificate(t *testing.T) {
	dir := t.TempDir()

	before, err := EnsureCertificate(dir, []string{"homebase", "homebase.local"})
	if err != nil {
		t.Fatal(err)
	}

	after, err := EnsureCertificate(dir, []string{"attic", "attic.local"})
	if err != nil {
		t.Fatal(err)
	}

	if after.Fingerprint == before.Fingerprint {
		t.Fatal("the certificate did not change, so it no longer matches the name " +
			"the machine answers to")
	}
	if err := parse(t, after.CertPath).VerifyHostname("attic.local"); err != nil {
		t.Errorf("the new certificate is not valid for the new name: %v", err)
	}
}

// The private key is the one file here that must not be readable by anything
// else on the machine — including the containers Homebase runs.
func TestThePrivateKeyIsNotReadableByAnybodyElse(t *testing.T) {
	dir := t.TempDir()

	identity, err := EnsureCertificate(dir, []string{"homebase", "homebase.local"})
	if err != nil {
		t.Fatal(err)
	}

	info, err := os.Stat(identity.KeyPath)
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Errorf("the private key is %o, want 600", mode)
	}

	directory, err := os.Stat(filepath.Dir(identity.KeyPath))
	if err != nil {
		t.Fatal(err)
	}
	if mode := directory.Mode().Perm(); mode&0o077 != 0 {
		t.Errorf("the certificate directory is %o, which lets others in", mode)
	}
}

// A half-written certificate must never be served: core would refuse to start,
// on a machine whose owner has no way to see why.
func TestADamagedCertificateIsReplacedRatherThanServed(t *testing.T) {
	dir := t.TempDir()

	first, err := EnsureCertificate(dir, []string{"homebase", "homebase.local"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(first.CertPath, []byte("this is not a certificate"), 0o644); err != nil {
		t.Fatal(err)
	}

	second, err := EnsureCertificate(dir, []string{"homebase", "homebase.local"})
	if err != nil {
		t.Fatalf("a damaged certificate stopped the server starting: %v", err)
	}
	if second.Fingerprint == first.Fingerprint {
		t.Error("the damaged certificate was served")
	}
}

func TestRedirectKeepsTheNameTheUserTyped(t *testing.T) {
	handler := RedirectToTLS("0.0.0.0:8443")

	cases := []struct {
		host string
		path string
		want string
	}{
		{"homebase.local:8080", "/", "https://homebase.local:8443/"},
		{"192.168.1.50:8080", "/applications", "https://192.168.1.50:8443/applications"},
		{"attic:8080", "/api/v1/health", "https://attic:8443/api/v1/health"},
		// No port on the way in is still a name on the way out.
		{"homebase.local", "/", "https://homebase.local:8443/"},
	}

	for _, tc := range cases {
		t.Run(tc.host, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, tc.path, nil)
			request.Host = tc.host

			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, request)

			if recorder.Code != http.StatusTemporaryRedirect {
				t.Errorf("status = %d, want 307", recorder.Code)
			}
			if got := recorder.Header().Get("Location"); got != tc.want {
				t.Errorf("Location = %q, want %q", got, tc.want)
			}
		})
	}
}

// 307 rather than 301: a permanent redirect is cached indefinitely, and a
// machine that later serves plain HTTP again would be unreachable from every
// browser that had ever seen the old answer.
func TestRedirectIsNotPermanent(t *testing.T) {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.Host = "homebase.local:8080"

	RedirectToTLS("0.0.0.0:8443").ServeHTTP(recorder, request)

	if recorder.Code == http.StatusMovedPermanently {
		t.Error("a permanent redirect would outlive the reason for it")
	}
}

func parse(t *testing.T, path string) *x509.Certificate {
	t.Helper()

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(raw)
	if block == nil {
		t.Fatal("the certificate is not PEM")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	return cert
}
