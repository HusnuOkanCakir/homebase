import { useCallback, useEffect, useState } from "react";
import { api, ApiError, NetworkError, type User } from "./api";
import { Setup } from "./views/Setup";
import { Login } from "./views/Login";
import { Dashboard } from "./views/Dashboard";
import { Message } from "./components/Message";

type Screen =
  | { kind: "loading" }
  | { kind: "unreachable"; detail: string }
  | { kind: "setup" }
  | { kind: "login" }
  | { kind: "signed-in"; user: User };

export function App() {
  const [screen, setScreen] = useState<Screen>({ kind: "loading" });

  // Work out what to show: does this server have an administrator yet, and are
  // we signed in? Both questions are asked of the server rather than remembered,
  // because a remembered answer is one that can be wrong.
  const determine = useCallback(async () => {
    try {
      const { needs_setup } = await api.setupStatus();
      if (needs_setup) {
        setScreen({ kind: "setup" });
        return;
      }
    } catch (error) {
      setScreen({
        kind: "unreachable",
        detail: error instanceof Error ? error.message : String(error),
      });
      return;
    }

    try {
      const user = await api.me();
      setScreen({ kind: "signed-in", user });
    } catch (error) {
      if (error instanceof NetworkError) {
        setScreen({ kind: "unreachable", detail: error.message });
        return;
      }
      // Any other failure here means "not signed in", which is the ordinary
      // case rather than an error worth showing.
      setScreen({ kind: "login" });
    }
  }, []);

  useEffect(() => {
    // The state is set after an await, so this is one render when the answer
    // arrives rather than the cascading render the rule guards against. Asking
    // the server what to show is the "subscribe to an external system" case the
    // rule's own message describes; it cannot see through the async boundary.
    // eslint-disable-next-line react-hooks/set-state-in-effect
    void determine();
  }, [determine]);

  const signedIn = useCallback((user: User) => {
    setScreen({ kind: "signed-in", user });
  }, []);

  const signOut = useCallback(async () => {
    try {
      await api.logout();
    } catch {
      // Signing out locally matters more than the server acknowledging it.
    }
    setScreen({ kind: "login" });
  }, []);

  switch (screen.kind) {
    case "loading":
      return (
        <main className="centred">
          <p className="muted">Connecting to your server…</p>
        </main>
      );

    case "unreachable":
      return (
        <main className="centred">
          <div className="card">
            <h1>Homebase is not responding</h1>
            <Message
              tone="error"
              title="Your server did not answer."
              detail={screen.detail}
              recovery="This usually means it is still starting up. Wait a moment and try again."
            />
            <button className="primary" onClick={() => void determine()}>
              Try again
            </button>
          </div>
        </main>
      );

    case "setup":
      return <Setup onComplete={signedIn} />;

    case "login":
      return <Login onSignedIn={signedIn} />;

    case "signed-in":
      return <Dashboard user={screen.user} onSignOut={() => void signOut()} />;
  }
}

/** Turn any thrown value into something worth showing a person. */
export function describeError(error: unknown): {
  title: string;
  detail?: string;
  recovery?: string;
} {
  if (error instanceof ApiError) {
    return {
      title: error.message,
      ...(error.detail === undefined ? {} : { detail: error.detail }),
      ...(error.recovery === undefined ? {} : { recovery: error.recovery }),
    };
  }
  if (error instanceof NetworkError) {
    return {
      title: "Homebase is not responding.",
      recovery: "Check that your server is switched on, then try again.",
    };
  }
  return {
    title: "Something went wrong.",
    recovery: "Try again. If it keeps happening, export a diagnostic bundle.",
  };
}
