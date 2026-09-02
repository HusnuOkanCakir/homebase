package auth

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// Joining a household.
//
// Separate from recovery, which it used to borrow. Both hand somebody a
// one-time code that gets them past a password they do not have, and there the
// resemblance stops:
//
// A recovery code is permanent on purpose. It is written on paper and kept for
// the day it is needed, which may be a year away, and one that expired would be
// worthless exactly then.
//
// An invitation is used within the hour. It is read out across a kitchen table
// or sent in a message, and a joining code still live in six months is a way
// into the server sitting in somebody's chat history.
//
// Borrowing recovery for this also made the wrong thing happen on screen.
// Somebody joining produced an event reading "The password for father was reset
// using the recovery code", at error severity, saying every session had been
// destroyed. There were no sessions. It was their first sign-in, and the owner
// of the server was told it looked like a break-in.

// InvitationLifetime is how long a joining code works for.
//
// A week, which is long enough for somebody who was handed a code on Sunday and
// got to it the following weekend, and short enough that a code forgotten in a
// message thread stops being a way in. An administrator can always issue
// another; nobody has to be told a code is unrecoverable, because it is not.
const InvitationLifetime = 7 * 24 * time.Hour

// ErrInvalidInvitation covers a wrong code, an unknown username, an account
// with no invitation, and one whose invitation has expired.
//
// Deliberately one error, for the same reason recovery has one: telling them
// apart says which accounts exist and which are worth attacking. The expiry is
// the one case where saying more would help somebody honest, and it is also the
// case an attacker learns most from — that this name is real.
var ErrInvalidInvitation = errors.New("that joining code is not right")

// Invitation is what an administrator can see about somebody who has not
// arrived yet.
type Invitation struct {
	IssuedBy   string     `json:"issued_by,omitempty"`
	IssuedAt   time.Time  `json:"issued_at"`
	ExpiresAt  time.Time  `json:"expires_at"`
	AcceptedAt *time.Time `json:"accepted_at,omitempty"`
}

// Expired reports whether the code has stopped working.
func (i Invitation) Expired() bool {
	return i.AcceptedAt == nil && time.Now().UTC().After(i.ExpiresAt)
}

// IssueInvitation creates or replaces somebody's joining code and returns it in
// plaintext exactly once.
//
// Stored the way a password is, so it cannot be shown again — and it does not
// need to be, because issuing another is a button. That is the whole reason
// this is safe to keep out of the database in readable form.
func (s *Service) IssueInvitation(ctx context.Context, userID, issuedBy string) (string, error) {
	code, err := GenerateRecoveryCode()
	if err != nil {
		return "", err
	}
	hash, err := HashPassword(normaliseRecoveryCode(code))
	if err != nil {
		return "", err
	}

	now := time.Now().UTC()
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO invitations (user_id, code_hash, issued_by, issued_at, expires_at, accepted_at)
		VALUES (?, ?, ?, ?, ?, NULL)
		ON CONFLICT(user_id) DO UPDATE SET
			code_hash = excluded.code_hash,
			issued_by = excluded.issued_by,
			issued_at = excluded.issued_at,
			expires_at = excluded.expires_at,
			-- Reissuing un-accepts it. An administrator issuing a second code
			-- for somebody who has already joined is doing so because that
			-- person cannot get in, and the row should describe the invitation
			-- that is now outstanding rather than the one that worked in July.
			accepted_at = NULL`,
		userID, hash, issuedBy,
		now.Format(time.RFC3339Nano),
		now.Add(InvitationLifetime).Format(time.RFC3339Nano))
	if err != nil {
		return "", err
	}
	return code, nil
}

// InvitationFor reports what is outstanding for an account, if anything.
func (s *Service) InvitationFor(ctx context.Context, userID string) (*Invitation, error) {
	var (
		issuedBy sql.NullString
		issued   string
		expires  string
		accepted sql.NullString
	)
	err := s.db.QueryRowContext(ctx, `
		SELECT issued_by, issued_at, expires_at, accepted_at
		FROM invitations WHERE user_id = ?`, userID).
		Scan(&issuedBy, &issued, &expires, &accepted)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	invitation := &Invitation{IssuedBy: issuedBy.String}
	invitation.IssuedAt, _ = time.Parse(time.RFC3339Nano, issued)
	invitation.ExpiresAt, _ = time.Parse(time.RFC3339Nano, expires)
	if accepted.Valid {
		if at, err := time.Parse(time.RFC3339Nano, accepted.String); err == nil {
			invitation.AcceptedAt = &at
		}
	}
	return invitation, nil
}

// ClaimAccount is somebody's first sign-in: the joining code for a password of
// their own.
//
// Returns the account and a recovery code, which is theirs from this moment and
// is the only one they will be shown. Somebody who joins a server and is never
// given a way back in is somebody who loses the account the first time they
// forget the password — the same failure first-run setup issues a code to
// avoid.
func (s *Service) ClaimAccount(ctx context.Context, username, code, password string) (*User, string, error) {
	if len([]rune(password)) < MinPasswordLen {
		return nil, "", ErrWeakPassword
	}

	var (
		userID      string
		codeHash    string
		permissions string
		created     string
		expires     string
		accepted    sql.NullString
	)
	err := s.db.QueryRowContext(ctx, `
		SELECT u.id, i.code_hash, u.permissions, u.created_at, i.expires_at, i.accepted_at
		FROM users u JOIN invitations i ON i.user_id = u.id
		WHERE u.username = ?`, strings.TrimSpace(username)).
		Scan(&userID, &codeHash, &permissions, &created, &expires, &accepted)

	if errors.Is(err, sql.ErrNoRows) {
		// The same deliberate work as a failed sign-in, so that how long this
		// takes does not say whether the name exists or has an invitation.
		_, _ = VerifyPassword(normaliseRecoveryCode(code), dummyHash)
		return nil, "", ErrInvalidInvitation
	}
	if err != nil {
		return nil, "", err
	}

	ok, err := VerifyPassword(normaliseRecoveryCode(code), codeHash)
	if err != nil || !ok {
		return nil, "", ErrInvalidInvitation
	}

	// Checked after the code, not before. An expired invitation and a wrong
	// code have to take the same time and give the same answer, or the pair is
	// a way to find out which names are real.
	if accepted.Valid {
		return nil, "", ErrInvalidInvitation
	}
	if deadline, err := time.Parse(time.RFC3339Nano, expires); err == nil &&
		time.Now().UTC().After(deadline) {
		return nil, "", ErrInvalidInvitation
	}

	newHash, err := HashPassword(password)
	if err != nil {
		return nil, "", err
	}
	recovery, err := GenerateRecoveryCode()
	if err != nil {
		return nil, "", err
	}
	recoveryHash, err := HashPassword(normaliseRecoveryCode(recovery))
	if err != nil {
		return nil, "", err
	}

	// One transaction: a machine that loses power here must not come back with
	// the password set and the invitation still usable by whoever else has the
	// code.
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, "", err
	}
	defer func() { _ = tx.Rollback() }()

	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx,
		`UPDATE users SET password_hash = ? WHERE id = ?`, newHash, userID); err != nil {
		return nil, "", err
	}
	// Marked used rather than deleted. "This account was joined on the fourth,
	// on an invitation from alex" is worth being able to read back, and a
	// deleted row cannot say it.
	if _, err := tx.ExecContext(ctx,
		`UPDATE invitations SET accepted_at = ? WHERE user_id = ?`, now, userID); err != nil {
		return nil, "", err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO recovery_codes (user_id, code_hash, issued_at, last_used_at)
		VALUES (?, ?, ?, NULL)
		ON CONFLICT(user_id) DO UPDATE SET
			code_hash = excluded.code_hash,
			issued_at = excluded.issued_at`,
		userID, recoveryHash, now); err != nil {
		return nil, "", err
	}
	if err := tx.Commit(); err != nil {
		return nil, "", err
	}

	user := &User{ID: userID, Username: strings.TrimSpace(username)}
	_ = json.Unmarshal([]byte(permissions), &user.Permissions)
	if at, err := time.Parse(time.RFC3339Nano, created); err == nil {
		user.CreatedAt = at
	}
	return user, recovery, nil
}
