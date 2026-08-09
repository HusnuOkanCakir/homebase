package hostd

import (
	"errors"
	"fmt"
)

// Error is the failure envelope hostd returns.
//
// It mirrors schemas/error.schema.json, so that a failure originating in hostd
// can travel out through core's API without being reshaped on the way. Code is
// the contract and never changes meaning once shipped; Message is not, and may
// be reworded or translated freely.
type Error struct {
	// Code is stable and machine-readable, domain.reason.
	Code string `json:"code"`

	// Message is one sentence for a person who does not know what a mount point
	// is. It must not contain paths, device names or stack traces — those go in
	// Detail.
	Message string `json:"message"`

	// Detail is for whoever is diagnosing the problem. It may name paths and
	// devices. It must never contain a secret.
	Detail string `json:"detail,omitempty"`

	// Recoverable distinguishes "the disk is unplugged" from "this request can
	// never work". An automated caller uses it to decide whether retrying is
	// pointless.
	Recoverable bool `json:"recoverable"`

	// Recovery says what the user can actually do. Required whenever
	// Recoverable is true: reporting that a problem is fixable without saying
	// how is worse than saying nothing.
	Recovery string `json:"recovery,omitempty"`

	// Status is the HTTP status to return. Zero means 500.
	Status int `json:"-"`
}

func (e *Error) Error() string {
	if e.Detail != "" {
		return fmt.Sprintf("%s: %s (%s)", e.Code, e.Message, e.Detail)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

// The errors hostd itself can raise, as opposed to those an operation produces.
var (
	// ErrUnknownOperation is returned for any name not in the registry.
	// Deliberately terse: enumerating what does exist would help an attacker
	// who has already reached the socket map the privileged surface.
	ErrUnknownOperation = &Error{
		Code:        "hostd.unknown_operation",
		Message:     "That operation does not exist.",
		Recoverable: false,
		Status:      404,
	}

	ErrConfirmationRequired = &Error{
		Code:        "hostd.confirmation_required",
		Message:     "This operation needs to be confirmed before it can run.",
		Detail:      "the request did not carry the required confirmation",
		Recoverable: true,
		Recovery:    "Confirm the action and try again.",
		Status:      428,
	}

	ErrPermissionDenied = &Error{
		Code:        "hostd.permission_denied",
		Message:     "This operation is not permitted.",
		Recoverable: false,
		Status:      403,
	}

	// ErrPeerRejected fires when the connecting process is not the expected
	// unprivileged service. Socket permissions should already prevent this;
	// checking the peer's credentials as well is defence in depth, because the
	// socket mode is a packaging decision that could be widened by accident.
	ErrPeerRejected = &Error{
		Code:        "hostd.peer_rejected",
		Message:     "This client is not permitted to use the host service.",
		Recoverable: false,
		Status:      403,
	}

	ErrTimeout = &Error{
		Code:        "hostd.timeout",
		Message:     "The operation took too long and was stopped.",
		Recoverable: true,
		Recovery:    "Try again. If it keeps timing out, check the system logs.",
		Status:      504,
	}
)

// internalError wraps an unexpected failure without leaking its internals to
// the caller. The detail goes to the audit log; the caller gets a request id to
// quote.
func internalError(detail string) *Error {
	return &Error{
		Code:        "hostd.internal_error",
		Message:     "Something went wrong inside the host service.",
		Detail:      detail,
		Recoverable: false,
		Status:      500,
	}
}

// asHostError unwraps an operation failure into the envelope it carries.
//
// errors.As rather than a type assertion, so a wrapped error still resolves —
// an error that lost its code on the way out reaches the user as "something
// went wrong inside Homebase", which is the least useful thing it could say.
func asHostError(err error, target **Error) bool {
	return errors.As(err, target)
}
