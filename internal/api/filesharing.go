package api

import (
	"context"
	"regexp"
	"slices"
	"time"
)

// One password.
//
// Homebase asks somebody to choose a password when they join, and the file
// server needs one too. Two passwords for one person on one machine is a
// support burden nobody signed up for: the second one is typed into a Windows
// dialog once, saved there, and forgotten, so the day it has to be typed again
// is a day it cannot be remembered.
//
// So the file-sharing password follows the sign-in password, set at the two
// moments core legitimately holds a plaintext one and never stored anywhere it
// could be read back. There is no third moment: Homebase cannot reverse its own
// hashes, which is why this happens at sign-in rather than on a screen with a
// button marked "sync".
//
// What that costs, said plainly: Samba keeps an unsalted NT hash of whatever it
// is given, which is a weaker record than the one Homebase keeps. Anybody with
// root on this machine could already read both. It is not a reason to make
// people carry two passwords, and it is a reason not to reuse a Homebase
// password anywhere else.

// shareUsername is the shape hostd will accept for a file-sharing account. Kept
// here so a name that cannot have one produces an explanation rather than an
// error from three layers down.
var shareUsername = regexp.MustCompile(`^[a-z][a-z0-9-]{0,30}[a-z0-9]$`)

// syncFileSharingPassword makes somebody's file-sharing password the one they
// just signed in with, and reports what a person needs to know.
//
// Returns an empty string when there is nothing to say — which is the usual
// case, including on a server with no file sharing installed at all. A file
// server that is not there must never be a reason somebody cannot sign in.
func (s *Server) syncFileSharingPassword(ctx context.Context, username, password string,
	onlyIfMissing bool) string {
	status, err := s.host.Shares(ctx)
	if err != nil || !status.Installed {
		return ""
	}
	// Already has an account, and this is a sign-in rather than a password
	// change. Setting it again would be right and would cost a Unix account
	// helper and two smbpasswd runs on every single sign-in.
	if onlyIfMissing && slices.Contains(status.Users, username) {
		return ""
	}
	if !shareUsername.MatchString(username) {
		return " Your name cannot be used for file sharing, which needs lowercase " +
			"letters, numbers and hyphens. Somebody with an administrator account " +
			"can set a file-sharing name for you."
	}

	if _, err := s.host.SetSharePassword(ctx, username, password); err != nil {
		s.log.Warn("could not set the file-sharing password",
			"username", username, "error", err)
		return " Your password for opening folders from another computer could " +
			"not be set. Signing in here still works; try again, or ask an " +
			"administrator."
	}
	s.log.Info("file-sharing password set from a sign-in", "username", username)
	return ""
}

// EnsureEveryoneHasAFolder gives a private folder to every account that has
// none, once, when core starts.
//
// The sign-in catch-up was not enough, and the person it failed for was the
// owner. Folders are made when an account is created and again when somebody
// signs in — but a session lasts a fortnight, so anybody still holding one from
// before this feature existed never signs in and never gets a folder. The
// administrator, who set the server up first and has been signed in ever since,
// is the likeliest person in the house to be in that state.
//
// What that looked like: `people` opened for everybody except him, and Samba
// said `canonicalize_connect_path failed for service people, path
// .../people/okan` in a log he had no reason to read. On the dashboard the
// folder appeared to exist, because the Files screen offers the area from the
// people directory rather than from his own folder in it.
//
// In the background, because it is a few filesystem checks and nothing should
// wait for them, and idempotent: hostd reports whether it actually made
// anything, so a server that is already correct does no work and reconfigures
// nothing.
func (s *Server) EnsureEveryoneHasAFolder(ctx context.Context) {
	go func() {
		accounts, err := s.auth.Accounts(ctx)
		if err != nil {
			s.log.Warn("could not check who needs a private folder", "error", err)
			return
		}
		for _, account := range accounts {
			if _, err := s.host.MakePersonalFolder(ctx, account.Username); err != nil {
				s.log.Warn("could not create a private folder",
					"username", account.Username, "error", err)
			}
		}
	}()
}

// catchUpFileSharing gives somebody what an account created today would have
// come with, without making them wait for it.
//
// Two things: a private folder, and a file-sharing account. Both are ordinary
// for an account created since this feature existed and missing for every
// account created before it — which is every account on every server that has
// been running for more than a few weeks, including the administrator made at
// first-run setup. Without this they would never acquire either, and the Files
// screen would be empty for the person who owns the machine.
//
// Signing in is the moment because it is the only moment: Homebase cannot read
// a password back, so the plaintext exists here and nowhere else.
//
// In the background because none of it should stand between a person and their
// dashboard — it runs a user-account helper and two smbpasswd commands — and in
// its own context, because the request's is cancelled the moment the response
// is written. Everything it does converges, so a failure means the next sign-in
// tries again rather than something being left half-made.
func (s *Server) catchUpFileSharing(username, password string) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()

		if _, err := s.host.MakePersonalFolder(ctx, username); err != nil {
			s.log.Warn("could not create a private folder at sign-in",
				"username", username, "error", err)
		}
		s.syncFileSharingPassword(ctx, username, password, true)
	}()
}
