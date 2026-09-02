package api

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/HusnuOkanCakir/homebase/internal/auth"
	"github.com/HusnuOkanCakir/homebase/internal/events"
)

// Joining a household, which used to be the recovery endpoint.
//
// It worked, and it said the wrong things. Somebody's first sign-in produced an
// event at error severity reading "The password for father was reset using the
// recovery code. Everything signed in as father was signed out." Nothing had
// been signed out — there had never been a session. The owner of the server was
// told their brother arriving looked like somebody breaking in, on the one
// screen that exists so that a break-in is noticed.
//
// So this is its own route, with its own event, its own error, and a code that
// expires. See internal/auth/invitations.go for why the two mechanisms want
// different properties rather than one shared one.
func (s *Server) handleClaimAccount(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Username    string `json:"username"`
		Code        string `json:"joining_code"`
		NewPassword string `json:"new_password"`
	}
	if !s.decode(w, r, &body) {
		return
	}

	user, recovery, err := s.auth.ClaimAccount(
		r.Context(), body.Username, body.Code, body.NewPassword)

	switch {
	case errors.Is(err, auth.ErrWeakPassword):
		s.writeError(w, r, http.StatusUnprocessableEntity, apiError{
			Code:        "auth.password_too_short",
			Message:     "Please choose a longer password.",
			Detail:      "at least " + strconv.Itoa(auth.MinPasswordLen) + " characters",
			Recoverable: true,
			Recovery: "Use a password of at least " +
				strconv.Itoa(auth.MinPasswordLen) + " characters.",
		})
		return

	case errors.Is(err, auth.ErrInvalidInvitation):
		// One answer for a wrong code, an unknown name, a code already used and
		// an expired one. An expired code is the case where saying more would
		// help somebody honest, and it is also the case an attacker learns most
		// from — that this name is real. The recovery it offers covers all four
		// without distinguishing them.
		s.events.Warn(r.Context(), "account.claim_failed", body.Username, "invalid_code",
			"Somebody tried to join this server with a code that was not right.")
		s.writeError(w, r, http.StatusUnauthorized, apiError{
			Code:        "auth.invalid_joining_code",
			Message:     "That joining code is not right.",
			Recoverable: true,
			Recovery: "Check the code you were given. They stop working after a " +
				"week, so if it is an old one, ask for another.",
		})
		return

	case err != nil:
		s.writeInternal(w, r, err)
		return
	}

	// Info, not error. Somebody joining is the ordinary thing this feature is
	// for, and an event that cries wolf on the ordinary case is one nobody
	// reads on the day it matters.
	s.events.Info(events.WithActor(r.Context(), user.Username), "account.claimed",
		user.Username, user.Username+" signed in for the first time and chose a password.")
	s.log.Info("account claimed", "username", user.Username, "from", clientKey(r))

	// Their file-sharing password, set from the one they just chose — the same
	// thing that happens when anybody changes a password. Done here rather than
	// in the background because this is the moment the arrangement is for: they
	// type one password and a Windows drive accepts it.
	note := s.syncFileSharingPassword(r.Context(), user.Username, body.NewPassword, false)

	// And a recovery code of their own, shown once. Somebody who joins and is
	// never given one loses the account the first time they forget the
	// password — the failure first-run setup issues a code to avoid.
	s.issueSessionWithExtra(w, r, user, http.StatusOK, map[string]any{
		"recovery_code": recovery,
		"message": "Write this recovery code down. It is the way back in if you " +
			"forget your password, and it is shown once. Your password also opens " +
			"folders from another computer." + note,
	})
}
