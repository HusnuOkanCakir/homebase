import { useCallback, useEffect, useState } from "react";
import { api, type Job, type SystemInfo, type User } from "../api";
import { describeError } from "../App";
import { Message } from "../components/Message";
import { Home } from "./Home";
import { Settings } from "./Settings";
import { type PowerChoice } from "../components/PowerCard";
import { Applications } from "./Applications";
import { Storage } from "./Storage";
import { Backup } from "./Backup";
import { Network } from "./Network";
import { Repair } from "./Repair";
import { FirstSteps } from "./FirstSteps";
import { Leaving } from "./Leaving";
import { Shares } from "./Shares";
import { Assistant } from "./Assistant";

/**
 * The signed-in shell.
 *
 * It owns the two things both screens need: the machine's identity, and the
 * restart flow. The restart lives here rather than inside Overview because a
 * machine that is going away takes every screen with it, not just the one the
 * button was on.
 */

const REFRESH_MS = 5000;

/**
 * The sections, and there are deliberately fewer than there were.
 *
 * Nine tabs wrapped onto two rows and spent three of themselves — updates,
 * security, and the machine's own settings — on things somebody does once or
 * twice a year, while the applications the server exists to run had one tab
 * shared with the catalogue of ones it does not.
 *
 * Seven now, each a word rather than a phrase, on one line at any width worth
 * designing for. "Home" is where somebody lands and where they press the thing
 * they came to press; everything rarer is behind a word they would think of.
 */
type Tab =
  | "home"
  | "apps"
  | "assistant"
  | "files"
  | "storage"
  | "network"
  | "settings"
  | "help";

const TABS: { id: Tab; label: string }[] = [
  { id: "home", label: "Home" },
  { id: "apps", label: "Apps" },
  // After the applications rather than before them: this is a thing the server
  // can do, not the thing it is for. Shown only on a machine that actually has
  // a model — see assistantReady below — so most installations still see seven.
  { id: "assistant", label: "Assistant" },
  { id: "files", label: "Files" },
  { id: "storage", label: "Storage" },
  { id: "network", label: "Network" },
  { id: "settings", label: "Settings" },
  // Last, and named for what somebody is thinking rather than for what it does.
  // Nobody looks for "recovery"; they look for the tab that sounds like "this
  // is not working".
  { id: "help", label: "Help" },
];

interface Props {
  user: User;
  onSignOut: () => void;
}

export function Dashboard({ user, onSignOut }: Props) {
  const [tab, setTab] = useState<Tab>("home");
  // Set when Home sends somebody to a particular application, so the Apps
  // screen opens on it rather than on the list.
  const [openApp, setOpenApp] = useState<string | null>(null);
  const [system, setSystem] = useState<SystemInfo | null>(null);
  const [error, setError] = useState<ReturnType<typeof describeError> | null>(null);
  // What the machine is doing instead of serving this page. Held here rather
  // than in Overview because a server that is going away takes every screen with
  // it, not only the one the button was on.
  const [leaving, setLeaving] = useState<{ choice: PowerChoice; job: Job } | null>(null);
  // Whether this machine has a local model at all.
  //
  // Asked once, and the tab is hidden until the answer is yes. Most
  // installations have no model, and a tab that opens onto "there is no
  // assistant here" is worse than no tab: it advertises a feature by way of
  // explaining its absence. Hidden also covers the failure cases — no
  // permission, model down, key unreadable — which is right for a navigation
  // decision, and the reasons stay available on the screen itself.
  const [assistantReady, setAssistantReady] = useState(false);

  useEffect(() => {
    if (!user.permissions.includes("assistant.use")) return;
    let cancelled = false;
    api
      .assistant()
      .then((status) => {
        if (!cancelled) setAssistantReady(status.available);
      })
      // A server with no assistant configured answers this happily; a failure
      // here means something else is wrong, and it is not this tab's job to
      // report it.
      .catch(() => {});
    return () => {
      cancelled = true;
    };
  }, [user.permissions]);

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

  if (leaving) {
    return (
      <Leaving
        choice={leaving.choice}
        job={leaving.job}
        onBack={() => void refresh().then(() => setLeaving(null))}
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
        {TABS.filter((entry) => entry.id !== "assistant" || assistantReady).map((entry) => (
          <button
            key={entry.id}
            className={tab === entry.id ? "tab tab-current" : "tab"}
            aria-current={tab === entry.id ? "page" : undefined}
            onClick={() => {
              // Cleared here so that pressing "Apps" in the navigation shows
              // the list, rather than reopening whatever Home last sent us to.
              setOpenApp(null);
              setTab(entry.id);
            }}
          >
            {entry.label}
          </button>
        ))}
      </nav>

      <main className="app-main">
        {error ? (
          <Message
            tone="error"
            title={error.title}
            technical={error.detail}
            recovery={error.recovery}
          />
        ) : null}

        {tab === "home" ? (
          system ? (
            <>
              {/*
                Above the machine's own numbers, because on a server that has
                just been claimed the useful thing is what to do next, not how
                much memory is free.
              */}
              <FirstSteps system={system} onGo={setTab} />
              <Home
                system={system}
                onGoToApps={() => setTab("apps")}
                onOpenApp={(id) => {
                  setOpenApp(id);
                  setTab("apps");
                }}
              />
            </>
          ) : (
            !error && <p className="muted">Reading your server…</p>
          )
        ) : tab === "apps" ? (
          <Applications
            canManage={user.permissions.includes("apps.manage")}
            initial={openApp}
          />
        ) : tab === "assistant" ? (
          <Assistant />
        ) : tab === "files" ? (
          <Shares
            canManage={user.permissions.includes("network.modify")}
            serverName={system?.hostname ?? ""}
          />
        ) : tab === "storage" ? (
          <>
            {/* Disks and backups on one screen, because a backup *is* a disk
                decision: it needs a second one, and the first question either
                way is which disks this server has. */}
            <Storage canManage={user.permissions.includes("storage.modify")} />
            <Backup canManage={user.permissions.includes("backup.run")} />
          </>
        ) : tab === "network" ? (
          <Network canManage={user.permissions.includes("network.modify")} />
        ) : tab === "settings" ? (
          system ? (
            <Settings
              user={user}
              system={system}
              onLeaving={(choice, job) => setLeaving({ choice, job })}
            />
          ) : (
            !error && <p className="muted">Reading your server…</p>
          )
        ) : (
          <Repair serverName={system?.hostname ?? ""} />
        )}
      </main>
    </div>
  );
}
