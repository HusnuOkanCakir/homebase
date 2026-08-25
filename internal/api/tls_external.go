package api

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"
)

// A certificate somebody else's authority signed.
//
// The comment above `EnsureCertificate` says a home server has no public name
// and no way to get a certificate anybody already trusts. That was true when it
// was written and Tailscale makes it false: a machine on a tailnet has a real
// name in a real zone, and Tailscale obtains a Let's Encrypt certificate for it
// over DNS-01 — without the machine being reachable from the internet, which is
// the part that used to be impossible.
//
// So when such a certificate exists, Homebase serves it and the browser stops
// warning. When it does not, nothing changes: the self-signed certificate and
// its printed fingerprint remain the default, because most machines have no
// tailnet and trust-on-first-use is still the honest answer for them.
//
// **Reloaded from disk rather than read once.** These certificates expire in
// ninety days and are renewed by a timer. Reading at startup would mean the
// dashboard quietly serving an expired certificate until somebody restarted
// core — months later, and looking exactly like the certificate never worked.
//
// **A broken one falls back rather than failing.** If the file is missing,
// unreadable or malformed at the moment of a handshake, the self-signed
// certificate is served instead. A renewal that goes wrong should produce a
// browser warning somebody can click through, never a dashboard that cannot be
// opened — the dashboard is where they would go to fix it.

// ReloadingCertificate serves a certificate pair from disk, noticing when it is
// replaced, and falls back to a known-good one when it cannot.
type ReloadingCertificate struct {
	certPath, keyPath string
	fallback          *tls.Certificate
	log               *slog.Logger

	mu       sync.Mutex
	cached   *tls.Certificate
	modified time.Time
	// warned stops a failing certificate from writing a line per handshake.
	warned bool
}

// NewReloadingCertificate serves certPath/keyPath, falling back to fallback.
func NewReloadingCertificate(certPath, keyPath string, fallback *tls.Certificate,
	log *slog.Logger) *ReloadingCertificate {
	return &ReloadingCertificate{
		certPath: certPath, keyPath: keyPath, fallback: fallback, log: log,
	}
}

// GetCertificate is the tls.Config hook, called once per handshake.
func (r *ReloadingCertificate) GetCertificate(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	info, err := os.Stat(r.certPath)
	if err != nil {
		return r.fallbackWith("the trusted certificate is not readable", err)
	}

	// Cached until the file changes. A handshake must not read two files from
	// disk, and a renewal is the only thing that should cause a reload.
	if r.cached != nil && info.ModTime().Equal(r.modified) {
		return r.cached, nil
	}

	pair, err := tls.LoadX509KeyPair(r.certPath, r.keyPath)
	if err != nil {
		return r.fallbackWith("the trusted certificate could not be loaded", err)
	}

	r.cached = &pair
	r.modified = info.ModTime()
	if r.warned {
		r.log.Info("the trusted certificate is being served again", "path", r.certPath)
		r.warned = false
	}
	return r.cached, nil
}

// fallbackWith serves the self-signed certificate and says why, once.
func (r *ReloadingCertificate) fallbackWith(reason string, cause error) (*tls.Certificate, error) {
	if !r.warned {
		r.log.Warn(reason+"; serving the self-signed one instead",
			"path", r.certPath, "error", cause)
		r.warned = true
	}
	// Deliberately not an error. Returning one refuses the handshake, and a
	// dashboard that cannot be opened is worse than one that warns.
	r.cached = nil
	if r.fallback == nil {
		return nil, fmt.Errorf("no certificate to serve: %w", cause)
	}
	return r.fallback, nil
}

// parseLeaf returns the certificate's leaf, parsing it if it was not kept.
//
// tls.LoadX509KeyPair leaves Leaf nil, and every caller that wants to know what
// names a certificate carries has to do this.
func parseLeaf(pair *tls.Certificate) (*x509.Certificate, error) {
	if pair.Leaf != nil {
		return pair.Leaf, nil
	}
	if len(pair.Certificate) == 0 {
		return nil, fmt.Errorf("certificate has no contents")
	}
	return x509.ParseCertificate(pair.Certificate[0])
}
