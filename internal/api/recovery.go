package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/HusnuOkanCakir/homebase/internal/auth"
	"github.com/HusnuOkanCakir/homebase/internal/events"
)

// Password recovery — ADR-0015.
//
// Three endpoints, and the shape of each follows from what it is for.
//
// `POST /auth/recover` is the only unauthenticated way to change a credential
// in Homebase. It is rationed, it returns one error for every kind of failure,
// and it announces itself loudly afterwards, because a reset the owner did not
// perform is the most important thing this server can tell them.
//
// The other two are ordinary signed-in endpoints: what the state of the code is,
// and give me a new one.

// handleRecover resets a password using the code from the user's piece of paper.
func (s *Server) handleRecover(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Username    string `json:"username"`
		Code        string `json:"recovery_code"`
		NewPassword string `json:"new_password"`
	}
	if !s.decode(w, r, &body) {
		return
	}

	user, replacement, err := s.auth.ResetPasswordWithCode(
		r.Context(), body.Username, body.Code, body.NewPassword)

	switch {
	case errors.Is(err, auth.ErrWeakPassword):
		s.writeError(w, r, http.StatusUnprocessableEntity, apiError{
			Code:        "auth.password_too_short",
			Message:     "Please choose a longer password.",
			Detail:      "at least " + strconv.Itoa(auth.MinPasswordLen) + " characters",
			Recoverable: true,
			Recovery: "Choose a password of at least " +
				strconv.Itoa(auth.MinPasswordLen) + " characters.",
		})
		return

	case errors.Is(err, auth.ErrInvalidRecoveryCode):
		// One answer for a wrong code, an unknown name, and an account that
		// never had a code. Distinguishing them tells somebody which account is
		// worth attacking and whether it has a second door at all.
		s.events.Warn(r.Context(), "auth.recovery_failed", body.Username, "invalid_code",
			"Somebody tried to reset a password with a recovery code that was not right.")
		s.writeError(w, r, http.StatusUnauthorized, apiError{
			Code:        "auth.invalid_recovery_code",
			Message:     "That recovery code is not right.",
			Recoverable: true,
			Recovery: "Check the code you wrote down when you set the server up. " +
				"If you cannot find it, somebody with access to the server itself " +
				"can create a new one from it.",
		})
		return

	case err != nil:
		s.writeInternal(w, r, err)
		return
	}

	// Deliberately not recoverable: there is nothing for the reader to do
	// except know. If this was not them, the server has been taken.
	// Named explicitly: this route is not behind the middleware that marks a
	// request as somebody's, because whoever is calling it cannot sign in. They
	// have proved who they are with the code, which is what the actor records.
	s.events.Error(events.WithActor(r.Context(), user.Username),
		"auth.password_recovered", user.Username, "recovery_code",
		"The password for "+user.Username+" was reset using the recovery code. "+
			"Everything signed in as "+user.Username+" was signed out.", false)

	s.log.Warn("password reset with a recovery code",
		"user", user.Username, "from", clientKey(r))

	// The same password opens folders from another computer.
	//
	// Done here rather than in the background, unlike a sign-in: this is a
	// password *change*, so the old file-sharing password is now wrong and the
	// person is standing in front of the screen that could tell them if the new
	// one could not be set. It is also the moment somebody joining this server
	// chooses their first password, which is where the whole arrangement earns
	// its keep — they type one password, once, and Windows accepts it.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	note := s.syncFileSharingPassword(ctx, user.Username, body.NewPassword, false)

	// The replacement code travels with the response and is never stored in a
	// form that can produce it again. If the user closes the page without
	// writing it down, a signed-in administrator can issue another.
	s.issueSessionWithExtra(w, r, user, http.StatusOK, map[string]any{
		"recovery_code": replacement,
		"message": "This is also the password for opening folders from another " +
			"computer." + note,
	})
}

// handleRecoveryStatus says whether a code exists, without revealing it.
//
// The point is that somebody can find out they never wrote one down at a moment
// when that is still fixable.
func (s *Server) handleRecoveryStatus(w http.ResponseWriter, r *http.Request, user *auth.User) {
	status, err := s.auth.RecoveryStatusFor(r.Context(), user.ID)
	if err != nil {
		s.writeInternal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, status)
}

// handleReissueRecoveryCode issues a fresh code and invalidates the old one.
//
// This is the answer to lost paper, and the reason no plaintext copy needs to
// exist anywhere: a code that cannot be shown again can always be replaced.
func (s *Server) handleReissueRecoveryCode(w http.ResponseWriter, r *http.Request, user *auth.User) {
	code, err := s.auth.IssueRecoveryCode(r.Context(), user.ID)
	if err != nil {
		s.writeInternal(w, r, err)
		return
	}

	s.events.Warn(r.Context(), "auth.recovery_code_reissued", user.Username, "requested",
		"A new recovery code was created for "+user.Username+
			". The previous one no longer works.")

	writeJSON(w, http.StatusOK, map[string]any{"recovery_code": code})
}
