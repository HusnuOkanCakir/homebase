package api

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/HusnuOkanCakir/homebase/internal/auth"
)

// The rest of the household.
//
// Homebase has had one account since it was written, made at first-run setup,
// with no way to make another. This is the surface that changes that.
//
// **An administrator hands over a code, never a password.** Creating an account
// returns a joining code — the same one-time code the recovery flow issues — and
// the person exchanges it for a password only they know, through the recovery
// screen that already exists. There is no endpoint here that sets somebody
// else's password, and there should never be one.
//
// These return the resource rather than a job, which is a documented exception
// to the 202 convention alongside `handleRename`. Each is one SQLite
// transaction, nothing can be usefully interrupted, and the first thing a
// caller does afterwards is read the list back.

func (s *Server) registerAccountRoutes(mux *http.ServeMux) {
	mux.Handle("GET /api/v1/accounts",
		s.require(auth.PermAccountsManage, s.handleListAccounts))
	mux.Handle("POST /api/v1/accounts",
		s.require(auth.PermAccountsManage, s.handleCreateAccount))
	mux.Handle("POST /api/v1/accounts/{id}/role",
		s.require(auth.PermAccountsManage, s.handleSetAccountRole))
	mux.Handle("POST /api/v1/accounts/{id}/remove",
		s.require(auth.PermAccountsManage, s.handleRemoveAccount))
	mux.Handle("POST /api/v1/accounts/{id}/joining-code",
		s.require(auth.PermAccountsManage, s.handleReissueJoiningCode))
}

func (s *Server) handleListAccounts(w http.ResponseWriter, r *http.Request, _ *auth.User) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	accounts, err := s.auth.Accounts(ctx)
	if err != nil {
		s.writeInternal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"accounts": accounts})
}

func (s *Server) handleCreateAccount(w http.ResponseWriter, r *http.Request, user *auth.User) {
	var body struct {
		Username string `json:"username"`
		Role     string `json:"role"`
	}
	if !s.decode(w, r, &body) {
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	created, code, err := s.auth.CreateInvitedAccount(ctx, body.Username, body.Role)
	if err != nil {
		s.writeAccountError(w, r, err)
		return
	}

	// Recorded with the administrator who did it, because an account is a way
	// into this server that outlives whoever granted it.
	s.events.Warn(r.Context(), "account.created", created.Username, body.Role,
		"An account was added for "+created.Username+".")
	s.log.Info("account created",
		"username", created.Username, "role", body.Role, "by", user.Username)

	// And somewhere of their own to put things.
	//
	// After the account, and not allowed to undo it. A folder that could not be
	// made is a folder that can be made on the next attempt — the disk was
	// busy, hostd was restarting — whereas an account rolled back at this point
	// leaves an administrator holding a joining code for somebody who does not
	// exist. So the failure is reported beside the code rather than instead of
	// it.
	folderNote := ""
	if _, err := s.host.MakePersonalFolder(ctx, created.Username); err != nil {
		s.log.Warn("could not create a private folder",
			"username", created.Username, "error", err)
		folderNote = " Their private folder could not be created yet; it will be " +
			"made the next time they are given one."
	}

	// The code is here once and nowhere else. It is stored the way a password
	// is, so nothing — including this server — can produce it again.
	writeJSON(w, http.StatusCreated, map[string]any{
		"id":           created.ID,
		"username":     created.Username,
		"role":         body.Role,
		"joining_code": code,
		"message": "Give this code to " + created.Username +
			". They use it to sign in for the first time and choose their own " +
			"password. It is shown once and cannot be shown again." + folderNote,
	})
}

func (s *Server) handleSetAccountRole(w http.ResponseWriter, r *http.Request, user *auth.User) {
	var body struct {
		Role string `json:"role"`
	}
	if !s.decode(w, r, &body) {
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	target, err := s.auth.UserByID(ctx, r.PathValue("id"))
	if err != nil {
		s.writeAccountError(w, r, err)
		return
	}
	if err := s.auth.SetRole(ctx, target.ID, body.Role); err != nil {
		s.writeAccountError(w, r, err)
		return
	}

	s.events.Warn(r.Context(), "account.role_changed", target.Username, body.Role,
		target.Username+" is now "+roleInWords(body.Role)+".")
	s.log.Info("account role changed",
		"username", target.Username, "role", body.Role, "by", user.Username)

	writeJSON(w, http.StatusOK, map[string]any{
		"id": target.ID, "username": target.Username, "role": body.Role,
	})
}

func (s *Server) handleRemoveAccount(w http.ResponseWriter, r *http.Request, user *auth.User) {
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	target, err := s.auth.UserByID(ctx, r.PathValue("id"))
	if err != nil {
		s.writeAccountError(w, r, err)
		return
	}
	// Named, not a boolean. A confirmation a client can send by default is not
	// a confirmation, and one that names its target cannot be replayed against
	// a different person.
	//
	// The name rather than the id, unlike applications and backups: a person
	// removing their father from the server knows his name and has never seen
	// `usr_b5075ca5…`. Typing a name you can read is a confirmation; copying an
	// identifier out of an error message is a formality.
	if !s.confirmedByName(w, r, target.Username, target.Username) {
		return
	}
	if err := s.auth.DeleteAccount(ctx, target.ID); err != nil {
		s.writeAccountError(w, r, err)
		return
	}

	// And their file-sharing account, which is the half that would otherwise
	// have been missed: an account removed from Homebase whose SMB login still
	// works is somebody who cannot sign in to the dashboard and can still map
	// the drive. Before the folder is moved, so that a failure here leaves
	// somebody with access to a folder that is still in the ordinary place
	// rather than one that has just been renamed under them.
	if _, err := s.host.RemoveShareUser(ctx, target.Username); err != nil {
		// Ordinary when they never had one: everybody gets a file-sharing
		// account at their first sign-in, and somebody who never signed in has
		// none to remove.
		s.log.Info("no file-sharing account to remove",
			"username", target.Username, "reason", err)
	}

	// Their private folder is moved aside, not deleted. Doing it here rather
	// than leaving it means the next person to have this name gets an empty
	// folder instead of the last one's files — which is the kind of thing
	// nobody discovers until it has already happened.
	//
	// The account is already gone. A folder that could not be moved is worth
	// saying out loud and is not worth putting the account back for.
	kept := ""
	result, err := s.host.RetirePersonalFolder(ctx, target.Username)
	switch {
	case err != nil:
		s.log.Warn("could not move a private folder aside",
			"username", target.Username, "error", err)
		kept = " Their private folder could not be moved aside; a new account " +
			"with the same name would be able to open it."
	case result["retired"] == true:
		if path, ok := result["path"].(string); ok {
			kept = " Their private folder was kept, at " + path + "."
		}
	}

	s.events.Warn(r.Context(), "account.removed", target.Username, "",
		target.Username+" was removed from this server. Their files were kept.")
	s.log.Info("account removed", "username", target.Username, "by", user.Username)

	writeJSON(w, http.StatusOK, map[string]any{
		"removed": target.Username,
		"message": "Their files were kept. Removing an account and deleting " +
			"somebody's files are different things." + kept,
	})
}

func (s *Server) handleReissueJoiningCode(w http.ResponseWriter, r *http.Request, user *auth.User) {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	target, err := s.auth.UserByID(ctx, r.PathValue("id"))
	if err != nil {
		s.writeAccountError(w, r, err)
		return
	}
	code, err := s.auth.IssueRecoveryCode(ctx, target.ID)
	if err != nil {
		s.writeInternal(w, r, err)
		return
	}

	// The same event the dashboard's own recovery screen writes, because it is
	// the same act: somebody with access to this machine issued a way in.
	s.events.Warn(r.Context(), "auth.recovery_code_reissued", target.Username, "administrator",
		"A new sign-in code was issued for "+target.Username+".")
	s.log.Info("joining code reissued", "username", target.Username, "by", user.Username)

	writeJSON(w, http.StatusOK, map[string]any{
		"username":     target.Username,
		"joining_code": code,
		"message": "This replaces any earlier code for " + target.Username +
			". It is shown once.",
	})
}

// writeAccountError turns the account errors into the envelope, each with the
// sentence that says what to do instead.
func (s *Server) writeAccountError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, auth.ErrNoSuchUser):
		s.writeError(w, r, http.StatusNotFound, apiError{
			Code:        "accounts.not_found",
			Message:     "There is no account with that name on this server.",
			Recoverable: false,
		})
	case errors.Is(err, auth.ErrUserExists):
		s.writeError(w, r, http.StatusConflict, apiError{
			Code:        "accounts.name_taken",
			Message:     "Somebody on this server already has that name.",
			Recoverable: true,
			Recovery:    "Choose a different name.",
		})
	case errors.Is(err, auth.ErrInvalidUsername):
		s.writeError(w, r, http.StatusBadRequest, apiError{
			Code:    "accounts.invalid_name",
			Message: "That name cannot be used on this server.",
			// Said in full, because the rule is not guessable and the reason is
			// not obvious: this name becomes their file-sharing login too.
			Detail: "Lower-case letters, numbers and hyphens; two to thirty-two " +
				"characters; starting with a letter. It becomes their file-sharing " +
				"name as well, which is why it cannot contain spaces or capitals.",
			Recoverable: true,
			Recovery:    "Try something like their first name in lower case.",
		})
	case errors.Is(err, auth.ErrUnknownRole):
		s.writeError(w, r, http.StatusBadRequest, apiError{
			Code:        "accounts.unknown_role",
			Message:     "That is not a role on this server.",
			Detail:      "Roles are administrator, member and limited.",
			Recoverable: true,
			Recovery:    "Choose one of the three.",
		})
	case errors.Is(err, auth.ErrLastAdministrator):
		s.writeError(w, r, http.StatusConflict, apiError{
			Code:    "accounts.last_administrator",
			Message: "This is the only administrator on the server.",
			Detail: "Removing them or reducing what they can do would leave " +
				"nobody able to administer this machine.",
			Recoverable: true,
			Recovery:    "Make somebody else an administrator first, then try again.",
		})
	default:
		s.writeInternal(w, r, err)
	}
}

// roleInWords is for a sentence somebody reads, not for a machine.
func roleInWords(role string) string {
	switch role {
	case auth.RoleAdministrator:
		return "an administrator"
	case auth.RoleMember:
		return "a member"
	case auth.RoleLimited:
		return "limited to their files"
	}
	return role
}
