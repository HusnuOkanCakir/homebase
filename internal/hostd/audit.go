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

	// Params is the request body. Operations that take secrets must not exist —
	// hostd deals in credential references, never values — so this is safe to
	// record in full, and being able to see exactly what was asked for is worth
	// a great deal when reconstructing an incident.
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
