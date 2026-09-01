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

// catchUpFileSharing gives somebody a file-sharing account if they do not have
// one, without making them wait for it.
//
// In the background because it is a convenience rather than part of signing in:
// it runs a user-account helper and two smbpasswd commands, and none of that
// should stand between a person and their dashboard. It matters for the
// ordinary case where sharing was switched on after somebody had already
// joined — at that moment their password exists only as a hash, and their next
// sign-in is the one chance to give the file server the same one.
//
// Its own context, because the request's is cancelled the moment the response
// is written.
func (s *Server) catchUpFileSharing(username, password string) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		s.syncFileSharingPassword(ctx, username, password, true)
	}()
}
