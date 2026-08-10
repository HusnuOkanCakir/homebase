package auth

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// Recovery codes — the way back into a server whose password is gone.
//
// ADR-0015. The whole mechanism exists because Homebase has no second channel:
// no email, no phone, no other signed-in device, and frequently no internet. A
// forgotten password used to mean a lost server, and restoring a backup did not
// help, because a backup faithfully restores the hash nobody can match.
//
// This is deliberately an authentication bypass. What makes it defensible is
// that the secret is held by the user rather than by the machine: it is written
// on paper, it is never stored in a form Homebase can show again, and using it
// destroys every session so it is worth something in the case where somebody
// suspects they have lost control of the account.

const (
	// No I, L, O or U. The first three are unreadable in handwriting and the
	// last one keeps unfortunate words out of the code.
	recoveryAlphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

	// Twenty-five characters at five bits each: 125 bits, shown as five groups
	// of five. Long enough that the rate limiter is defence in depth rather
	// than the thing standing between an attacker and the server.
	recoveryLength = 25
	recoveryGroup  = 5
)

// ErrInvalidRecoveryCode covers a wrong code, an unknown username and an
// account with no code at all. Deliberately one error: telling them apart says
// which accounts are worth attacking.
var ErrInvalidRecoveryCode = errors.New("that recovery code is not right")

// RecoveryStatus is what the dashboard shows about a code without revealing it.
type RecoveryStatus struct {
	Exists     bool       `json:"exists"`
	IssuedAt   *time.Time `json:"issued_at,omitempty"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
}

// GenerateRecoveryCode returns a new code in the form the user sees it.
//
// Exported because the installer and the console tool show codes too, and every
// one of them must have the same shape — somebody comparing the paper to the
// screen should never have to wonder whether they are looking at the same kind
// of thing.
func GenerateRecoveryCode() (string, error) {
	raw := make([]byte, recoveryLength)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}

	var out strings.Builder
	for i, b := range raw {
		if i > 0 && i%recoveryGroup == 0 {
			out.WriteByte('-')
		}
		// 32 divides 256 exactly, so the modulo is unbiased. With an alphabet
		// whose length was not a power of two this would quietly favour the
		// first few characters.
		out.WriteByte(recoveryAlphabet[int(b)%len(recoveryAlphabet)])
	}
	return out.String(), nil
}

// normaliseRecoveryCode puts a typed code into the form that was hashed.
//
// Someone is copying twenty-five characters off a piece of paper, possibly one
// they wrote by hand months ago. Case, spacing and the separators are theirs to
// get wrong. The three genuinely ambiguous glyphs are folded onto the character
// they cannot be, which is safe precisely because the alphabet excludes them.
func normaliseRecoveryCode(code string) string {
	var out strings.Builder
	for _, r := range strings.ToUpper(strings.TrimSpace(code)) {
		switch r {
		case '-', ' ', '\t', '_', '.':
			continue
		case 'O':
			out.WriteRune('0')
		case 'I', 'L':
			out.WriteRune('1')
		default:
			if strings.ContainsRune(recoveryAlphabet, r) {
				out.WriteRune(r)
			} else {
				// Kept rather than dropped: a character that is not in the
				// alphabet means the code is wrong, and silently discarding it
				// could turn a wrong code into a right one.
				out.WriteRune(r)
			}
		}
	}
	return out.String()
}

// IssueRecoveryCode creates or replaces the code for an account and returns it
// in plaintext exactly once. It is never recoverable from the database again.
func (s *Service) IssueRecoveryCode(ctx context.Context, userID string) (string, error) {
	code, err := GenerateRecoveryCode()
	if err != nil {
		return "", err
	}
	if err := s.storeRecoveryCode(ctx, s.db, userID, code, nil); err != nil {
		return "", err
	}
	return code, nil
}

// storeRecoveryCode writes the hash, preserving when the account was last
// recovered. lastUsed overrides that when a reset is what is being recorded.
func (s *Service) storeRecoveryCode(ctx context.Context, q execer, userID, code string, lastUsed *time.Time) error {
	hash, err := HashPassword(normaliseRecoveryCode(code))
	if err != nil {
		return err
	}

	var used any
	if lastUsed != nil {
		used = lastUsed.UTC().Format(time.RFC3339Nano)
	}

	// COALESCE keeps the previous recovery date when this is only a reissue:
	// replacing the code must not erase the record that somebody once used one.
	_, err = q.ExecContext(ctx, `
		INSERT INTO recovery_codes (user_id, code_hash, issued_at, last_used_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(user_id) DO UPDATE SET
			code_hash    = excluded.code_hash,
			issued_at    = excluded.issued_at,
			last_used_at = COALESCE(excluded.last_used_at, recovery_codes.last_used_at)`,
		userID, hash, time.Now().UTC().Format(time.RFC3339Nano), used)
	return err
}

// RecoveryStatusFor reports whether a code exists, without revealing it.
func (s *Service) RecoveryStatusFor(ctx context.Context, userID string) (RecoveryStatus, error) {
	var issued string
	var used sql.NullString

	err := s.db.QueryRowContext(ctx,
		`SELECT issued_at, last_used_at FROM recovery_codes WHERE user_id = ?`, userID).
		Scan(&issued, &used)

	if errors.Is(err, sql.ErrNoRows) {
		return RecoveryStatus{Exists: false}, nil
	}
	if err != nil {
		return RecoveryStatus{}, err
	}

	status := RecoveryStatus{Exists: true}
	if at, err := time.Parse(time.RFC3339Nano, issued); err == nil {
		status.IssuedAt = &at
	}
	if used.Valid {
		if at, err := time.Parse(time.RFC3339Nano, used.String); err == nil {
			status.LastUsedAt = &at
		}
	}
	return status, nil
}

// ResetPasswordWithCode is the recovery path itself: a username, the code from
// the paper, and a new password.
//
// On success it returns the account and a replacement code, shown once. A user
// who recovers and is then left without a code is a user who is one forgotten
// password from where they started, minus the piece of paper.
func (s *Service) ResetPasswordWithCode(ctx context.Context, username, code, newPassword string) (*User, string, error) {
	if len([]rune(newPassword)) < MinPasswordLen {
		return nil, "", ErrWeakPassword
	}

	var (
		userID      string
		codeHash    string
		permissions string
		created     string
	)

	err := s.db.QueryRowContext(ctx, `
		SELECT u.id, r.code_hash, u.permissions, u.created_at
		FROM users u JOIN recovery_codes r ON r.user_id = u.id
		WHERE u.username = ?`, strings.TrimSpace(username)).
		Scan(&userID, &codeHash, &permissions, &created)

	if errors.Is(err, sql.ErrNoRows) {
		// The same deliberate work as a failed sign-in. Whether the username
		// exists, and whether it has a code, must not be readable from how long
		// this takes — an attacker learning "that account has no recovery code"
		// learns which door to stop knocking on.
		_, _ = VerifyPassword(normaliseRecoveryCode(code), dummyHash)
		return nil, "", ErrInvalidRecoveryCode
	}
	if err != nil {
		return nil, "", err
	}

	ok, err := VerifyPassword(normaliseRecoveryCode(code), codeHash)
	if err != nil || !ok {
		return nil, "", ErrInvalidRecoveryCode
	}

	newHash, err := HashPassword(newPassword)
	if err != nil {
		return nil, "", err
	}
	replacement, err := GenerateRecoveryCode()
	if err != nil {
		return nil, "", err
	}

	// One transaction: a machine that loses power here must not come back with
	// the password changed and the old code still working, nor with every
	// session destroyed and nothing else done.
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, "", err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx,
		`UPDATE users SET password_hash = ? WHERE id = ?`, newHash, userID); err != nil {
		return nil, "", err
	}

	// Everything signed in is signed out. Recovery is what somebody reaches for
	// when they think they have lost control of the account, and leaving live
	// sessions alone makes it useless in exactly that case.
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM sessions WHERE user_id = ?`, userID); err != nil {
		return nil, "", err
	}

	now := time.Now().UTC()
	if err := s.storeRecoveryCode(ctx, tx, userID, replacement, &now); err != nil {
		return nil, "", err
	}

	if err := tx.Commit(); err != nil {
		return nil, "", err
	}

	user := &User{ID: userID, Username: strings.TrimSpace(username)}
	_ = json.Unmarshal([]byte(permissions), &user.Permissions)
	user.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	return user, replacement, nil
}

// Usernames lists the accounts on this server, for the console tool to offer
// when somebody has forgotten which name they chose as well as the password.
func (s *Service) Usernames(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT username FROM users ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		names = append(names, name)
	}
	return names, rows.Err()
}

// execer is what both *sql.DB and *sql.Tx satisfy, so storing a code works the
// same inside a transaction and outside one.
type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}
