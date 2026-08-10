import { useCallback, useEffect, useState } from "react";
import { api, type Job, type SystemInfo, type User } from "../api";
import { describeError } from "../App";
import { Message } from "../components/Message";
import { Overview } from "./Overview";
import { Applications } from "./Applications";
import { Storage } from "./Storage";
import { Backup } from "./Backup";
import { Security } from "./Security";
import { FirstSteps } from "./FirstSteps";
import { Restarting } from "./Restarting";

/**
 * The signed-in shell.
 *
 * It owns the two things both screens need: the machine's identity, and the
 * restart flow. The restart lives here rather than inside Overview because a
 * machine that is going away takes every screen with it, not just the one the
 * button was on.
 */

const REFRESH_MS = 5000;

type Tab = "overview" | "applications" | "storage" | "backup" | "security";

interface Props {
  user: User;
  onSignOut: () => void;
}

export function Dashboard({ user, onSignOut }: Props) {
  const [tab, setTab] = useState<Tab>("overview");
  const [system, setSystem] = useState<SystemInfo | null>(null);
  const [error, setError] = useState<ReturnType<typeof describeError> | null>(null);
  const [rebooting, setRebooting] = useState<Job | null>(null);

  const refresh = useCallback(async () => {
    try {
      setSystem(await api.system());
      setError(null);
    } catch (caught) {
      setError(describeError(caught));
    }
  }, []);

  useEffect(() => {
    // setState runs after the fetch resolves rather than synchronously in the
    // effect body — the rule cannot see through the async boundary.
    // eslint-disable-next-line react-hooks/set-state-in-effect
    void refresh();
    // Polled rather than pushed. On a LAN five seconds is imperceptible, and it
    // avoids a connection that would have to be re-established every time the
    // server restarts — which, on this screen, it is expected to.
    const timer = setInterval(() => void refresh(), REFRESH_MS);
    return () => clearInterval(timer);
  }, [refresh]);

  if (rebooting) {
    return (
      <Restarting
        job={rebooting}
        onBack={() => void refresh().then(() => setRebooting(null))}
      />
    );
  }

  return (
    <div className="app">
      <header className="app-header">
        <h1>{system?.hostname ?? "Homebase"}</h1>
        <div className="app-header-actions">
          <span className="muted">{user.username}</span>
          <button className="quiet" onClick={onSignOut}>
            Sign out
          </button>
        </div>
      </header>

      <nav className="tabs" aria-label="Sections">
        <button
          className={tab === "overview" ? "tab tab-current" : "tab"}
          aria-current={tab === "overview" ? "page" : undefined}
          onClick={() => setTab("overview")}
        >
          This server
        </button>
        <button
          className={tab === "applications" ? "tab tab-current" : "tab"}
          aria-current={tab === "applications" ? "page" : undefined}
          onClick={() => setTab("applications")}
        >
          Applications
        </button>
        <button
          className={tab === "storage" ? "tab tab-current" : "tab"}
          aria-current={tab === "storage" ? "page" : undefined}
          onClick={() => setTab("storage")}
        >
          Storage
        </button>
        <button
          className={tab === "backup" ? "tab tab-current" : "tab"}
          aria-current={tab === "backup" ? "page" : undefined}
          onClick={() => setTab("backup")}
        >
          Backup
        </button>
        <button
          className={tab === "security" ? "tab tab-current" : "tab"}
          aria-current={tab === "security" ? "page" : undefined}
          onClick={() => setTab("security")}
        >
          Security
        </button>
      </nav>

      <main className="app-main">
        {error ? (
          <Message
            tone="error"
            title={error.title}
            detail={error.detail}
            recovery={error.recovery}
          />
        ) : null}

        {tab === "overview" ? (
          system ? (
            <>
              {/*
                Above the machine's vital statistics, because on a server that
                has just been claimed the useful thing is what to do next, not
                how much memory is free.
              */}
              <FirstSteps system={system} onGo={setTab} />
              <Overview system={system} onRebootStarted={setRebooting} />
            </>
          ) : (
            !error && <p className="muted">Reading your server…</p>
          )
        ) : tab === "applications" ? (
          <Applications canManage={user.permissions.includes("apps.manage")} />
        ) : tab === "storage" ? (
          <Storage canManage={user.permissions.includes("storage.modify")} />
        ) : tab === "backup" ? (
          <Backup canManage={user.permissions.includes("backup.run")} />
        ) : (
          <Security username={user.username} />
        )}
      </main>
    </div>
  );
}
