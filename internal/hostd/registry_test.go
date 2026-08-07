package hostd

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func noopHandler(context.Context, json.RawMessage) (any, error) { return nil, nil }

func validOp() Operation {
	return Operation{
		Name:    "test.thing",
		Summary: "Do a thing.",
		Risk:    RiskRead,
		Confirm: ConfirmNone,
		Timeout: time.Second,
		Handler: noopHandler,
	}
}

// The registry's job is to refuse incompletely specified privileged operations
// at startup. Each case here is a way an unreviewed operation could otherwise
// reach production, so each gets its own assertion rather than a table with one
// vague "invalid" expectation.
func TestRegisterRejectsIncompleteOperations(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*Operation)
		wantErr string
	}{
		{
			name:    "no name",
			mutate:  func(o *Operation) { o.Name = "" },
			wantErr: "no name",
		},
		{
			name:    "no handler",
			mutate:  func(o *Operation) { o.Handler = nil },
			wantErr: "no handler",
		},
		{
			name:    "no summary",
			mutate:  func(o *Operation) { o.Summary = "" },
			wantErr: "no summary",
		},
		{
			// An operation with no timeout can hang forever and wedge the
			// service. The disks this runs on are old enough for that to be a
			// realistic outcome rather than a theoretical one.
			name:    "no timeout",
			mutate:  func(o *Operation) { o.Timeout = 0 },
			wantErr: "no timeout",
		},
		{
			name:    "unknown risk",
			mutate:  func(o *Operation) { o.Risk = "harmless" },
			wantErr: "unknown risk",
		},
		{
			name:    "unknown confirmation",
			mutate:  func(o *Operation) { o.Confirm = "maybe" },
			wantErr: "unknown confirmation",
		},
		{
			// The one most likely to slip through review: a mutating operation
			// that nobody gated behind a permission.
			name: "mutating operation without a permission",
			mutate: func(o *Operation) {
				o.Risk = RiskHigh
				o.Permissions = nil
			},
			wantErr: "requires at least one permission",
		},
		{
			// A read-only operation claiming a rollback means one of the two
			// fields is wrong, and we cannot tell which.
			name: "read-only operation with a rollback",
			mutate: func(o *Operation) {
				o.Risk = RiskRead
				o.Rollback = "test.undo"
			},
			wantErr: "cannot have a rollback",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			op := validOp()
			tc.mutate(&op)

			err := NewRegistry().Register(op)
			if err == nil {
				t.Fatalf("expected registration to be rejected, but it was accepted")
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("rejected for the wrong reason:\n got: %v\nwant substring: %q", err, tc.wantErr)
			}
		})
	}
}

func TestRegisterAcceptsAValidOperation(t *testing.T) {
	if err := NewRegistry().Register(validOp()); err != nil {
		t.Fatalf("valid operation rejected: %v", err)
	}
}

func TestRegisterRejectsDuplicates(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(validOp()); err != nil {
		t.Fatalf("first registration failed: %v", err)
	}
	if err := r.Register(validOp()); err == nil {
		t.Fatal("a duplicate name was accepted; two operations would share one path")
	}
}

// Lookup must be exact. Prefix matching or aliasing would mean the set of
// reachable operations is larger than the set that was reviewed.
func TestLookupIsExact(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(validOp()); err != nil {
		t.Fatal(err)
	}

	for _, name := range []string{"test", "test.", "test.thing.extra", "TEST.THING", "", "../test.thing"} {
		if _, found := r.Lookup(name); found {
			t.Errorf("%q resolved to an operation; lookup must be exact", name)
		}
	}

	if _, found := r.Lookup("test.thing"); !found {
		t.Error("the exact name did not resolve")
	}
}

// The exported registry is the source of truth for documentation, contract
// tests and Stage 2's capability list, so its shape is part of the contract.
func TestDescribeExportsEveryFieldAPolicyEngineNeeds(t *testing.T) {
	r := NewRegistry()
	RegisterSystemOperations(r)

	body, err := r.Describe()
	if err != nil {
		t.Fatal(err)
	}

	var doc struct {
		Operations []map[string]any `json:"operations"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatalf("the exported registry is not valid JSON: %v", err)
	}

	if len(doc.Operations) == 0 {
		t.Fatal("no operations exported")
	}

	required := []string{"name", "summary", "risk", "permissions", "confirmation", "timeout_seconds", "rollback"}
	for _, op := range doc.Operations {
		for _, field := range required {
			if _, present := op[field]; !present {
				t.Errorf("operation %v is missing %q", op["name"], field)
			}
		}
	}
}

// Strict decoding: a caller who misspells a field is a caller whose intent we do
// not know. Performing the operation without the parameter they thought they set
// is the wrong way to resolve that.
func TestTypedRejectsUnknownFields(t *testing.T) {
	type params struct {
		Reason string `json:"reason"`
	}
	handler := Typed(func(_ context.Context, p params) (any, error) { return p, nil })

	_, err := handler(context.Background(), json.RawMessage(`{"resaon":"typo"}`))
	if err == nil {
		t.Fatal("an unknown field was accepted; the typo would have been silently ignored")
	}

	var e *Error
	if !asError(err, &e) || e.Code != "request.invalid_parameters" {
		t.Fatalf("wrong error for an unknown field: %v", err)
	}
}

func TestTypedAcceptsEmptyBody(t *testing.T) {
	handler := Typed(func(_ context.Context, _ NoParams) (any, error) { return "ok", nil })

	for _, body := range []string{"", "  ", "{}"} {
		if _, err := handler(context.Background(), json.RawMessage(body)); err != nil {
			t.Errorf("empty body %q rejected: %v", body, err)
		}
	}
}

func TestTypedRejectsTrailingContent(t *testing.T) {
	handler := Typed(func(_ context.Context, _ NoParams) (any, error) { return nil, nil })

	if _, err := handler(context.Background(), json.RawMessage(`{} {"second":true}`)); err == nil {
		t.Fatal("two JSON documents in one body were accepted; we and the caller disagree about what was sent")
	}
}

func asError(err error, target **Error) bool {
	e, ok := err.(*Error)
	if ok {
		*target = e
	}
	return ok
}
