package hostd

import (
	"encoding/json"
	"io"
	"os"
	"sync"
	"time"
)

// AuditEvent records one attempt to invoke a privileged operation.
//
// "Attempt" is the important word. The record is written *before* the operation
// runs, then updated with the outcome. An action that panics the kernel halfway
// through must still leave evidence that it was attempted — otherwise the audit
// log is only trustworthy for actions that worked, which is precisely backwards.
type AuditEvent struct {
	Time      time.Time `json:"time"`
	RequestID string    `json:"request_id"`
	Operation string    `json:"operation"`
	Risk      Risk      `json:"risk"`

	// PeerUID and PeerPID identify the calling process, taken from the kernel
	// via SO_PEERCRED rather than from anything the caller told us.
	PeerUID uint32 `json:"peer_uid"`
	PeerPID int32  `json:"peer_pid"`

	// Phase is "attempt" for the record written before the operation, and
	// "result" for the one written after. Both are kept: the pair is what makes
	// an interrupted operation visible.
	Phase string `json:"phase"`

	Outcome    string  `json:"outcome,omitempty"`
	ErrorCode  string  `json:"error_code,omitempty"`
	DurationMS float64 `json:"duration_ms,omitempty"`

	// Params is the request body, with any field the operation declared as
	// `Secret` replaced by "[redacted]".
	//
	// This used to be recorded in full, on the strength of an invariant: hostd
	// deals in references — an application id, a disk id — never in values
	// anybody would mind seeing. `network.wifi_connect` broke it, because
	// netplan needs the passphrase itself and there is no reference form of it,
	// and the passphrase went straight into an append-only file kept for ever.
	// Caught by a test that looked for it there.
	//
	// Redaction is by declaration rather than by inspection: see
	// Operation.Secret.
	Params json.RawMessage `json:"params,omitempty"`
}

// Auditor writes audit events. Append-only, one JSON object per line.
type Auditor struct {
	mu  sync.Mutex
	out io.Writer
}

func NewAuditor(out io.Writer) *Auditor {
	return &Auditor{out: out}
}

// OpenAuditLog opens the append-only audit log.
//
// Mode 0640: readable by the homebase group so diagnostics can include it,
// writable only by root. An audit log an attacker can rewrite is decoration.
func OpenAuditLog(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o640)
}

// Write records an event. Failures are reported to the caller rather than
// swallowed: if we cannot audit, we do not proceed.
func (a *Auditor) Write(e AuditEvent) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	if e.Time.IsZero() {
		e.Time = time.Now().UTC()
	}

	line, err := json.Marshal(e)
	if err != nil {
		return err
	}
	if _, err := a.out.Write(append(line, '\n')); err != nil {
		return err
	}

	// Flush to disk where we can. The whole point of writing the record before
	// the action is that it survives the action going badly, and a record
	// sitting in a page cache does not survive a power cut.
	if f, ok := a.out.(*os.File); ok {
		_ = f.Sync()
	}
	return nil
}

// redactSecrets removes declared secret fields from a request body before it is
// audited.
//
// The audit log records what was asked for, which is worth a great deal when
// reconstructing an incident, and it is append-only and kept indefinitely. So
// anything written into it is written into it for good — which is exactly the
// wrong place for somebody's Wi-Fi password.
//
// The fields come from the operation's own declaration rather than from a list
// of names that look sensitive. A heuristic here would be a guess, and the thing
// being guessed about is whether a secret is about to be written to a permanent
// file.
//
// A body that is not a JSON object is dropped entirely rather than recorded. If
// the shape is not what it should be, nothing here knows what is in it.
func redactSecrets(body []byte, secret []string) json.RawMessage {
	if len(secret) == 0 {
		return json.RawMessage(body)
	}

	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil {
		return json.RawMessage(`{"redacted":"the request could not be read to remove secrets"}`)
	}

	for _, name := range secret {
		if _, present := fields[name]; present {
			// Present-but-hidden rather than absent. "There was a passphrase and
			// it is not recorded" and "there was no passphrase" are different
			// facts, and somebody reconstructing an incident needs the first.
			fields[name] = json.RawMessage(`"[redacted]"`)
		}
	}

	rewritten, err := json.Marshal(fields)
	if err != nil {
		return json.RawMessage(`{"redacted":"the request could not be rewritten to remove secrets"}`)
	}
	return rewritten
}
