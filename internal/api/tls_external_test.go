package api

import (
	"crypto/tls"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func quietLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// writePair generates a self-signed pair on disk and returns its paths.
func writePair(t *testing.T, dir string, names []string) (string, string) {
	t.Helper()
	identity, err := EnsureCertificate(dir, names)
	if err != nil {
		t.Fatal(err)
	}
	return identity.CertPath, identity.KeyPath
}

func loadPair(t *testing.T, certPath, keyPath string) *tls.Certificate {
	t.Helper()
	pair, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		t.Fatal(err)
	}
	return &pair
}

func TestServesTheTrustedCertificateWhenItIsThere(t *testing.T) {
	trustedDir, fallbackDir := t.TempDir(), t.TempDir()
	certPath, keyPath := writePair(t, trustedDir, []string{"homebase.tail1eec88.ts.net"})
	fallbackCert, fallbackKey := writePair(t, fallbackDir, []string{"homebase.local"})

	reloading := NewReloadingCertificate(certPath, keyPath,
		loadPair(t, fallbackCert, fallbackKey), quietLogger())

	got, err := reloading.GetCertificate(nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.Leaf == nil {
		leaf, _ := parseLeaf(got)
		got.Leaf = leaf
	}
	if name := got.Leaf.DNSNames[0]; name != "homebase.tail1eec88.ts.net" {
		t.Fatalf("served %q, want the trusted certificate", name)
	}
}

// The failure that matters: a renewal writes a broken file, or the file is
// removed. The dashboard must still open, because it is where somebody would go
// to put it right.
func TestABrokenCertificateFallsBackRatherThanRefusing(t *testing.T) {
	dir, fallbackDir := t.TempDir(), t.TempDir()
	fallbackCert, fallbackKey := writePair(t, fallbackDir, []string{"homebase.local"})
	fallback := loadPair(t, fallbackCert, fallbackKey)

	for _, c := range []struct {
		name  string
		setup func() (string, string)
	}{
		{"missing entirely", func() (string, string) {
			return filepath.Join(dir, "absent.crt"), filepath.Join(dir, "absent.key")
		}},
		{"present but not a certificate", func() (string, string) {
			cert := filepath.Join(dir, "junk.crt")
			key := filepath.Join(dir, "junk.key")
			os.WriteFile(cert, []byte("this is not PEM"), 0o600)
			os.WriteFile(key, []byte("nor is this"), 0o600)
			return cert, key
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			certPath, keyPath := c.setup()
			reloading := NewReloadingCertificate(certPath, keyPath, fallback, quietLogger())

			got, err := reloading.GetCertificate(nil)
			if err != nil {
				t.Fatalf("refused the handshake (%v); a warning the user can click "+
					"through beats a dashboard that will not open", err)
			}
			if got != fallback {
				t.Fatal("did not serve the self-signed certificate")
			}
		})
	}
}

// These expire in ninety days and are renewed by a timer. Reading once at
// startup means serving an expired certificate for months.
func TestNoticesTheCertificateBeingReplaced(t *testing.T) {
	dir := t.TempDir()
	first := t.TempDir()
	certPath, keyPath := filepath.Join(dir, "tls.crt"), filepath.Join(dir, "tls.key")

	firstCert, firstKey := writePair(t, first, []string{"first.ts.net"})
	copyFile(t, firstCert, certPath)
	copyFile(t, firstKey, keyPath)

	reloading := NewReloadingCertificate(certPath, keyPath, nil, quietLogger())
	got, err := reloading.GetCertificate(nil)
	if err != nil {
		t.Fatal(err)
	}
	leaf, _ := parseLeaf(got)
	if leaf.DNSNames[0] != "first.ts.net" {
		t.Fatalf("served %q first", leaf.DNSNames[0])
	}

	// Renewal: same paths, different content, later modification time.
	second := t.TempDir()
	secondCert, secondKey := writePair(t, second, []string{"second.ts.net"})
	copyFile(t, secondCert, certPath)
	copyFile(t, secondKey, keyPath)
	os.Chtimes(certPath, time.Now().Add(time.Minute), time.Now().Add(time.Minute))

	got, err = reloading.GetCertificate(nil)
	if err != nil {
		t.Fatal(err)
	}
	leaf, _ = parseLeaf(got)
	if leaf.DNSNames[0] != "second.ts.net" {
		t.Fatalf("still serving %q after renewal; it would serve an expired "+
			"certificate until somebody restarted core", leaf.DNSNames[0])
	}
}

func copyFile(t *testing.T, from, to string) {
	t.Helper()
	body, err := os.ReadFile(from)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(to, body, 0o600); err != nil {
		t.Fatal(err)
	}
}
