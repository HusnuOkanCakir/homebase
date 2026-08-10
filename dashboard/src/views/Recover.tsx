import { useState, type FormEvent } from "react";
import { api, type User } from "../api";
import { describeError } from "../App";
import { Message } from "../components/Message";
import { RecoveryCode } from "../components/RecoveryCode";

const MIN_PASSWORD_LENGTH = 12;

interface Props {
  onRecovered: (user: User) => void;
  onCancel: () => void;
}

/**
 * Getting back into a server whose password has been forgotten.
 *
 * ADR-0015. Everything about this screen assumes somebody having a bad day:
 * they are locked out of their own photographs, they are holding a piece of
 * paper they wrote on months ago, and they are not sure it is the right one.
 *
 * So the code field is forgiving — case, spaces and the hyphens are all
 * optional, and the server folds the glyphs that get confused on paper — and
 * the screen says plainly what will happen before it happens, including the
 * part people do not expect: everything signed in gets signed out.
 */
export function Recover({ onRecovered, onCancel }: Props) {
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
      const result = await api.recover(username.trim(), code.trim(), password);
      setIssued({ user: result.user, code: result.recovery_code });
      setBusy(false);
    } catch (caught) {
      setError(describeError(caught));
      // The code stays in the box. Somebody who mistyped one character of
      // twenty-five should be fixing that character, not typing it all again.
      setBusy(false);
    }
  }

  if (issued) {
    return (
      <main className="centred">
        <RecoveryCode
          code={issued.code}
          acknowledgement="I have written down my new recovery code."
          onAcknowledged={() => onRecovered(issued.user)}
        />
      </main>
    );
  }

  return (
    <main className="centred">
      <form className="card" onSubmit={(e) => void submit(e)}>
        <h1>Use your recovery code</h1>
        <p className="muted">
          The code you wrote down when you set this server up. It lets you choose a new
          password without knowing the old one.
        </p>

        {error ? (
          <Message
            tone="error"
            title={error.title}
            detail={error.detail}
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

        <label htmlFor="recovery-code">Recovery code</label>
        <input
          id="recovery-code"
          name="recovery-code"
          className="code-input"
          autoComplete="off"
          spellCheck={false}
          placeholder="XXXXX-XXXXX-XXXXX-XXXXX-XXXXX"
          value={code}
          onChange={(e) => setCode(e.target.value)}
          disabled={busy}
          aria-describedby="code-hint"
        />
        <p id="code-hint" className="hint">
          Capitals do not matter, and neither do the dashes.
        </p>

        <label htmlFor="password">New password</label>
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

        <label htmlFor="confirmation">New password again</label>
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
          {busy ? "Setting your new password…" : "Set a new password"}
        </button>

        <button type="button" className="quiet" onClick={onCancel} disabled={busy}>
          Back to signing in
        </button>

        <p className="hint">
          This signs out everything that is currently signed in, on every device, and the
          code you use here stops working. You will be given a new one to write down.
        </p>

        <p className="hint">
          If you cannot find your code, somebody with access to the server itself can
          create a new one by running <code>sudo homebasectl recovery-code</code> on it.
        </p>
      </form>
    </main>
  );
}
