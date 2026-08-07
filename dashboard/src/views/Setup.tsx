import { useState, type FormEvent } from "react";
import { api, type User } from "../api";
import { describeError } from "../App";
import { Message } from "../components/Message";

const MIN_PASSWORD_LENGTH = 12;

interface Props {
  onComplete: (user: User) => void;
}

/**
 * First-run setup: claiming the server.
 *
 * This is the first thing anybody sees, and until it is done the server has no
 * administrator — so it is deliberately the only thing reachable. A server that
 * answers before somebody has claimed it is a server anybody on the network can
 * claim.
 */
export function Setup({ onComplete }: Props) {
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [confirmation, setConfirmation] = useState("");
  const [error, setError] = useState<ReturnType<typeof describeError> | null>(null);
  const [busy, setBusy] = useState(false);

  const passwordTooShort = password.length > 0 && password.length < MIN_PASSWORD_LENGTH;
  const mismatch = confirmation.length > 0 && password !== confirmation;
  const ready =
    username.trim().length > 0 &&
    password.length >= MIN_PASSWORD_LENGTH &&
    password === confirmation;

  async function submit(event: FormEvent) {
    event.preventDefault();
    if (!ready || busy) return;

    setBusy(true);
    setError(null);
    try {
      const { user } = await api.createAdministrator(username.trim(), password);
      onComplete(user);
    } catch (caught) {
      setError(describeError(caught));
      setBusy(false);
    }
  }

  return (
    <main className="centred">
      <form className="card" onSubmit={(e) => void submit(e)}>
        <h1>Set up your server</h1>
        <p className="muted">
          This server is new. Choose a name and password for yourself — you will use them
          every time you manage it.
        </p>

        {error ? (
          <Message tone="error" title={error.title} detail={error.detail} recovery={error.recovery} />
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

        <label htmlFor="password">Password</label>
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
          At least {MIN_PASSWORD_LENGTH} characters. A few words you will remember are
          better than something short and complicated.
        </p>

        <label htmlFor="confirmation">Password again</label>
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
          {busy ? "Setting up…" : "Set up this server"}
        </button>

        <p className="hint">
          There is no way to recover this password for you — nothing about your server
          leaves it, including this. Write it down somewhere safe.
        </p>
      </form>
    </main>
  );
}
