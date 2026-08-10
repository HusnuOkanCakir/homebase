import { useCallback, useEffect, useState } from "react";
import { api, type RecoveryStatus } from "../api";
import { describeError } from "../App";
import { Message } from "../components/Message";

/**
 * The recovery code, seen from inside.
 *
 * ADR-0015. This screen exists because of one failure that is otherwise
 * invisible until it is too late: a user who clicked past the code at setup, or
 * who has lost the paper, has no way of knowing they are one forgotten password
 * from losing the server. Here they can find that out on an ordinary Tuesday
 * instead.
 *
 * The code itself is never shown again — it is stored the way a password is.
 * What is offered instead is a new one, which solves the real problem without
 * anything reversible being kept on the machine.
 */
export function Security({ username }: { username: string }) {
  const [status, setStatus] = useState<RecoveryStatus | null>(null);
  const [fresh, setFresh] = useState<string | null>(null);
  const [confirming, setConfirming] = useState(false);
  const [error, setError] = useState<ReturnType<typeof describeError> | null>(null);
  const [busy, setBusy] = useState(false);

  const refresh = useCallback(async () => {
    try {
      setStatus(await api.recoveryStatus());
      setError(null);
    } catch (caught) {
      setError(describeError(caught));
    }
  }, []);

  useEffect(() => {
    // eslint-disable-next-line react-hooks/set-state-in-effect
    void refresh();
  }, [refresh]);

  async function reissue() {
    setBusy(true);
    setError(null);
    try {
      const { recovery_code } = await api.reissueRecoveryCode();
      setFresh(recovery_code);
      setConfirming(false);
      await refresh();
    } catch (caught) {
      setError(describeError(caught));
    }
    setBusy(false);
  }

  return (
    <>
      {error ? (
        <Message tone="error" title={error.title} detail={error.detail} recovery={error.recovery} />
      ) : null}

      <section className="card">
        <h2>Your recovery code</h2>
        <p className="muted">
          If you forget your password, your recovery code is what gets you back into your
          server. Homebase cannot reset a password for you — nothing about your server
          leaves it, so there is nobody to ask.
        </p>

        {status === null ? (
          <p className="muted">Checking…</p>
        ) : status.exists ? (
          <dl className="facts">
            <dt>Code</dt>
            <dd>Created, and kept where it cannot be read back</dd>
            <dt>Written</dt>
            <dd>{formatDate(status.issued_at)}</dd>
            {status.last_used_at ? (
              <>
                <dt>Last used to get back in</dt>
                <dd>{formatDate(status.last_used_at)}</dd>
              </>
            ) : null}
          </dl>
        ) : (
          <Message
            tone="warning"
            title={`${username} has no recovery code.`}
            detail="If you forget your password, there is no way back into this server from a browser."
            recovery="Create one now and write it down."
          />
        )}
      </section>

      {fresh ? (
        <section className="card">
          <h2>Write this down</h2>
          <p>Your new recovery code. The previous one no longer works.</p>
          <p className="recovery-code" data-testid="recovery-code">
            {fresh}
          </p>
          <p className="muted">
            It is shown once and cannot be displayed again. Keep it with your other
            important papers, not on the server.
          </p>
          <button className="quiet" onClick={() => setFresh(null)}>
            I have written it down
          </button>
        </section>
      ) : null}

      {confirming ? (
        <section className="card card-danger">
          <h2>Create a new recovery code?</h2>
          <p>
            The code you have now will stop working. If you have it written down
            somewhere, that piece of paper becomes useless and you will need to replace
            it with the new one.
          </p>
          <p className="muted">
            Your password does not change, and nothing signs out.
          </p>
          <div className="row">
            <button className="primary" disabled={busy} onClick={() => void reissue()}>
              {busy ? "Creating…" : "Create a new code"}
            </button>
            <button className="quiet" disabled={busy} onClick={() => setConfirming(false)}>
              Cancel
            </button>
          </div>
        </section>
      ) : fresh === null ? (
        <section className="card">
          <h3>Lost your code?</h3>
          <p className="muted">
            Create a new one. This is also what to do if you never wrote the first one
            down.
          </p>
          <button className="quiet" onClick={() => setConfirming(true)}>
            Create a new recovery code
          </button>
        </section>
      ) : null}
    </>
  );
}

function formatDate(value: string | undefined): string {
  if (!value) return "Unknown";
  const when = new Date(value);
  if (Number.isNaN(when.getTime())) return "Unknown";
  return when.toLocaleDateString(undefined, {
    day: "numeric",
    month: "long",
    year: "numeric",
  });
}
