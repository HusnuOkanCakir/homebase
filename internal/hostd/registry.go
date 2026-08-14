// Package hostd implements the privileged host service.
//
// hostd runs as root and accepts only a fixed, compiled-in set of named
// operations. There is no generic execution path, no dynamic dispatch, and no
// way to reach code that is not registered in this file's table. That is the
// boundary described in ADR-0006, and it is not permitted to move.
//
// The test for whether something belongs here: the complete set of things that
// can happen must be enumerable in advance. "Restart the container named
// jellyfin, where jellyfin is a known installed application" is an operation.
// "Restart the container named $X" is not.
package hostd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"
)

// Risk determines how an operation is handled before it runs, and — from Stage 2
// — what an AI operator is permitted to propose without asking.
type Risk string

const (
	// RiskRead observes and changes nothing.
	RiskRead Risk = "read"
	// RiskLow is reversible and does not touch user data.
	RiskLow Risk = "low"
	// RiskMedium affects service availability.
	RiskMedium Risk = "medium"
	// RiskHigh can affect user data.
	RiskHigh Risk = "high"
	// RiskCritical destroys data irreversibly.
	RiskCritical Risk = "critical"
)

// Confirm records whether a caller must have obtained confirmation first.
//
// hostd does not ask the user anything — it has no interface to do so. It
// enforces that the caller *claims* to have confirmed, and audits the claim.
// The actual asking happens in core, which is where the user is.
type Confirm string

const (
	ConfirmNone     Confirm = "none"
	ConfirmOptional Confirm = "optional"
	ConfirmRequired Confirm = "required"
	// ConfirmExplicit additionally requires the caller to name what is being
	// acted on, so that a confirmation cannot be replayed against a different
	// target.
	ConfirmExplicit Confirm = "explicit"
)

// Handler performs an operation. params is the raw request body; use Typed to
// get a decoded, validated struct instead of touching this directly.
type Handler func(ctx context.Context, params json.RawMessage) (any, error)

// Operation is the complete description of one privileged capability.
//
// Every field is part of the security contract. An operation with no declared
// risk, or with a Rollback nobody thought about, is an operation that has not
// been reviewed properly.
type Operation struct {
	// Name is dotted, domain.verb, and stable once shipped.
	Name string `json:"name"`

	// Summary is one sentence, for humans and for the Stage 2 capability list.
	Summary string `json:"summary"`

	Risk        Risk     `json:"risk"`
	Permissions []string `json:"permissions"`
	Confirm     Confirm  `json:"confirmation"`

	// Timeout bounds the handler. Zero is not permitted: an operation that can
	// hang forever is an operation that can wedge the whole service, and the
	// disks this runs on are old enough to hang.
	Timeout time.Duration `json:"-"`

	// Rollback names the operation that undoes this one, or is empty when it
	// cannot be undone. Empty must be a decision, not an oversight — it is
	// reported to callers so they can warn before acting.
	Rollback string `json:"rollback,omitempty"`

	// Secret names request fields that must never reach the audit log.
	//
	// The audit log records the parameters of every privileged call, and that
	// was safe for a long time on the strength of an invariant: hostd deals in
	// references — an application id, a disk id, a location — never in values
	// anybody would mind seeing. `network.wifi_connect` is the first genuine
	// exception, because netplan needs the passphrase itself and there is no
	// reference form of it.
	//
	// So the exception is declared rather than special-cased. It is part of the
	// operation, next to the handler, and it appears in `--describe`, which
	// means the set of operations that handle secrets is reviewable in the same
	// place as the set that require confirmation.
	Secret []string `json:"secret,omitempty"`

	Handler Handler `json:"-"`
}

// TimeoutSeconds renders Timeout for the exported registry.
func (o Operation) TimeoutSeconds() float64 { return o.Timeout.Seconds() }

// MarshalJSON exports an operation as its machine-readable description.
//
// This is what `hostd describe` prints, and it is the single source of truth for
// documentation, contract tests, and — later — the capability list the Stage 2
// policy engine reasons about. Deriving all of those from the same table is what
// stops a hand-maintained copy from drifting into a lie.
func (o Operation) MarshalJSON() ([]byte, error) {
	type exported struct {
		Name           string   `json:"name"`
		Summary        string   `json:"summary"`
		Risk           Risk     `json:"risk"`
		Permissions    []string `json:"permissions"`
		Confirmation   Confirm  `json:"confirmation"`
		TimeoutSeconds float64  `json:"timeout_seconds"`
		Rollback       *string  `json:"rollback"`
		Secret         []string `json:"secret"`
	}
	e := exported{
		Name:           o.Name,
		Summary:        o.Summary,
		Risk:           o.Risk,
		Permissions:    o.Permissions,
		Confirmation:   o.Confirm,
		TimeoutSeconds: o.Timeout.Seconds(),
	}
	if o.Rollback != "" {
		r := o.Rollback
		e.Rollback = &r
	}
	if e.Permissions == nil {
		e.Permissions = []string{}
	}
	e.Secret = o.Secret
	if e.Secret == nil {
		e.Secret = []string{}
	}
	return json.Marshal(e)
}

// Registry holds every operation hostd can perform.
type Registry struct {
	ops map[string]Operation
}

func NewRegistry() *Registry {
	return &Registry{ops: make(map[string]Operation)}
}

// Register adds an operation, rejecting anything incompletely specified.
//
// These checks fail at startup rather than at call time, deliberately: a service
// that refuses to boot is a far better outcome than one that runs with an
// unreviewed privileged operation in it.
func (r *Registry) Register(op Operation) error {
	switch {
	case op.Name == "":
		return fmt.Errorf("operation has no name")
	case op.Handler == nil:
		return fmt.Errorf("%s: no handler", op.Name)
	case op.Summary == "":
		return fmt.Errorf("%s: no summary; it appears in audit records and in the capability list", op.Name)
	case op.Timeout <= 0:
		return fmt.Errorf("%s: no timeout; an operation that can hang forever can wedge the service", op.Name)
	}

	switch op.Risk {
	case RiskRead, RiskLow, RiskMedium, RiskHigh, RiskCritical:
	default:
		return fmt.Errorf("%s: unknown risk %q", op.Name, op.Risk)
	}

	switch op.Confirm {
	case ConfirmNone, ConfirmOptional, ConfirmRequired, ConfirmExplicit:
	default:
		return fmt.Errorf("%s: unknown confirmation %q", op.Name, op.Confirm)
	}

	// A mutating operation with no permission requirement is almost certainly a
	// mistake, and it is the kind of mistake that is invisible in review.
	if op.Risk != RiskRead && len(op.Permissions) == 0 {
		return fmt.Errorf("%s: risk %q requires at least one permission", op.Name, op.Risk)
	}

	// Read-only operations must not claim to change anything.
	if op.Risk == RiskRead && op.Rollback != "" {
		return fmt.Errorf("%s: a read-only operation cannot have a rollback", op.Name)
	}

	if _, exists := r.ops[op.Name]; exists {
		return fmt.Errorf("%s: already registered", op.Name)
	}

	r.ops[op.Name] = op
	return nil
}

// MustRegister is Register for use at init time, where a failure should stop the
// process rather than be handled.
func (r *Registry) MustRegister(op Operation) {
	if err := r.Register(op); err != nil {
		panic("hostd: " + err.Error())
	}
}

// Lookup returns an operation by exact name.
//
// There is no prefix matching, no aliasing and no fallback. An unrecognised name
// is rejected; it is never forwarded anywhere.
func (r *Registry) Lookup(name string) (Operation, bool) {
	op, ok := r.ops[name]
	return op, ok
}

// Names returns every registered operation name, sorted.
func (r *Registry) Names() []string {
	names := make([]string, 0, len(r.ops))
	for name := range r.ops {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Describe renders the whole registry as JSON — the machine-readable list of
// everything this build of hostd can do.
func (r *Registry) Describe() ([]byte, error) {
	ops := make([]Operation, 0, len(r.ops))
	for _, name := range r.Names() {
		ops = append(ops, r.ops[name])
	}
	return json.MarshalIndent(struct {
		Operations []Operation `json:"operations"`
	}{ops}, "", "  ")
}

// Typed wraps a handler that wants decoded parameters.
//
// Decoding is strict: unknown fields are an error rather than being ignored. A
// caller that misspells a field name is a caller whose intent we do not actually
// know, and quietly performing the operation without the parameter they thought
// they set is the wrong way to resolve that ambiguity.
func Typed[T any](fn func(ctx context.Context, params T) (any, error)) Handler {
	return func(ctx context.Context, raw json.RawMessage) (any, error) {
		var params T

		if len(bytes.TrimSpace(raw)) > 0 {
			dec := json.NewDecoder(bytes.NewReader(raw))
			dec.DisallowUnknownFields()
			if err := dec.Decode(&params); err != nil {
				return nil, &Error{
					Code:    "request.invalid_parameters",
					Message: "The request parameters were not valid.",
					Detail:  err.Error(),
					// Not recoverable by retrying: the request was built wrong,
					// so sending it again unchanged cannot help. 400 rather than
					// 500 — 500 means a bug in Homebase, and a caller reading
					// that would look in the wrong place.
					Recoverable: false,
					Status:      400,
				}
			}
			// Reject trailing content: two JSON documents in one body means the
			// caller and we disagree about what was sent.
			if dec.More() {
				return nil, &Error{
					Code:        "request.invalid_parameters",
					Message:     "The request parameters were not valid.",
					Detail:      "unexpected content after the JSON body",
					Recoverable: false,
					Status:      400,
				}
			}
		}

		return fn(ctx, params)
	}
}
