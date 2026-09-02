import { useState, type FormEvent } from "react";
import { api, type User } from "../api";
import { describeError } from "../App";
import { Message } from "../components/Message";
import { RecoveryCode } from "../components/RecoveryCode";

const MIN_PASSWORD_LENGTH = 12;

interface Props {
  onJoined: (user: User) => void;
  onCancel: () => void;
}

/**
 * The first sign-in of somebody who has been given an account.
 *
 * Its own screen, and it used to be the recovery one. That worked and said the
 * wrong things to somebody arriving for the first time: a page headed "Use your
 * recovery code", warning that everything signed in would be signed out and
 * that the code they were holding would stop working. None of it applied. They
 * had never signed in, there was nothing to sign out, and the alarm was being
 * sounded at the one moment it should not be.
 *
 * The shape is deliberately the recovery screen's — a name, a code, a password
 * typed twice — because the same person may use both, months apart, and the
 * second one should feel like something they have done before.
 */
export function Join({ onJoined, onCancel }: Props) {
  const [username, setUsername] = useState("");
  const [code, setCode] = useState("");
  const [password, setPassword] = useState("");
  const [confirmation, setConfirmation] = useState("");
  const [error, setError] = useState<ReturnType<typeof describeError> | null>(null);
  const [busy, setBusy] = useState(false);
  const [issued, setIssued] = useState<{ user: User; code: string } | null>(null);

  const passwordTooShort = password.length > 0 && password.length < MIN_PASSWORD_LENGTH;
  const mismatch = confirmation.length > 0 && password !== confirmation;
  const ready =
    username.trim().length > 0 &&
    code.trim().length > 0 &&
    password.length >= MIN_PASSWORD_LENGTH &&
    password === confirmation;

  async function submit(event: FormEvent) {
    event.preventDefault();
    if (!ready || busy) return;

    setBusy(true);
    setError(null);
    try {
      const result = await api.claimAccount(username.trim(), code.trim(), password);
      setIssued({ user: result.user, code: result.recovery_code });
    } catch (caught) {
      setError(describeError(caught));
    } finally {
      setBusy(false);
    }
  }

  // Their own recovery code, shown once, before they go any further. Somebody
  // who joins and is never given one loses the account the first time they
  // forget the password.
  if (issued) {
    return (
      <main className="centred">
        <RecoveryCode
          code={issued.code}
          acknowledgement="I have written down my recovery code."
          onAcknowledged={() => onJoined(issued.user)}
        />
      </main>
    );
  }

  return (
    <main className="centred">
      <form className="card" onSubmit={(e) => void submit(e)}>
        <h1>Join this server</h1>
        <p className="muted">
          Use the code you were given. It gets you in once, and then you choose a
          password of your own that nobody else knows — not even whoever set this
          server up.
        </p>

        {error ? (
          <Message
            tone="error"
            title={error.title}
            technical={error.detail}
            recovery={error.recovery}
          />
        ) : null}

        <label htmlFor="username">Your name</label>
        <input
          id="username"
          name="username"
          autoComplete="username"
          autoFocus
          value={username}
          onChange={(e) => setUsername(e.target.value)}
          disabled={busy}
        />

        <label htmlFor="joining-code">Joining code</label>
        <input
          id="joining-code"
          name="joining-code"
          className="code-input"
          autoComplete="off"
          spellCheck={false}
          placeholder="XXXXX-XXXXX-XXXXX-XXXXX-XXXXX"
          value={code}
          onChange={(e) => setCode(e.target.value)}
          disabled={busy}
          aria-describedby="joining-code-hint"
        />
        <p id="joining-code-hint" className="hint">
          Capitals do not matter, and neither do the dashes.
        </p>

        <label htmlFor="password">Choose a password</label>
        <input
          id="password"
          name="password"
          type="password"
          autoComplete="new-password"
          value={password}
          onChange={(e) => setPassword(e.target.value)}
          disabled={busy}
          aria-describedby="password-hint"
        />
        <p id="password-hint" className={passwordTooShort ? "hint hint-warning" : "hint"}>
          At least {MIN_PASSWORD_LENGTH} characters.
        </p>

        <label htmlFor="confirmation">The same password again</label>
        <input
          id="confirmation"
          name="confirmation"
          type="password"
          autoComplete="new-password"
          value={confirmation}
          onChange={(e) => setConfirmation(e.target.value)}
          disabled={busy}
        />
        {mismatch ? <p className="hint hint-warning">These two do not match.</p> : null}

        <button className="primary" type="submit" disabled={!ready || busy}>
          {busy ? "Setting up your account…" : "Join"}
        </button>

        <button type="button" className="quiet" onClick={onCancel} disabled={busy}>
          Back to signing in
        </button>

        {/* Said before it is needed. A joining code that has quietly expired
            looks exactly like one that was mistyped, and the person holding it
            has no way to tell which. */}
        <p className="hint">
          A joining code stops working a week after it was made. If yours no longer
          works, ask whoever gave it to you for another — it costs them nothing.
        </p>

        <p className="hint">
          The password you choose here also opens shared folders from another
          computer. There is only one to remember.
        </p>
      </form>
    </main>
  );
}
