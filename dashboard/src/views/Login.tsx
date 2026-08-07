import { useState, type FormEvent } from "react";
import { api, type User } from "../api";
import { describeError } from "../App";
import { Message } from "../components/Message";

interface Props {
  onSignedIn: (user: User) => void;
}

export function Login({ onSignedIn }: Props) {
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [error, setError] = useState<ReturnType<typeof describeError> | null>(null);
  const [busy, setBusy] = useState(false);

  async function submit(event: FormEvent) {
    event.preventDefault();
    if (busy) return;

    setBusy(true);
    setError(null);
    try {
      const { user } = await api.login(username.trim(), password);
      onSignedIn(user);
    } catch (caught) {
      // The server returns the same answer for an unknown name and a wrong
      // password, deliberately — telling them apart would let somebody work out
      // which names are real. The dashboard does not improve on that.
      setError(describeError(caught));
      setPassword("");
      setBusy(false);
    }
  }

  return (
    <main className="centred">
      <form className="card" onSubmit={(e) => void submit(e)}>
        <h1>Homebase</h1>
        <p className="muted">Sign in to manage your server.</p>

        {error ? (
          <Message tone="error" title={error.title} recovery={error.recovery} />
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
          autoComplete="current-password"
          value={password}
          onChange={(e) => setPassword(e.target.value)}
          disabled={busy}
        />

        <button className="primary" type="submit" disabled={busy || !username || !password}>
          {busy ? "Signing in…" : "Sign in"}
        </button>
      </form>
    </main>
  );
}
