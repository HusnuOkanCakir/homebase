import { useCallback, useEffect, useRef, useState } from "react";
import { api, isTerminal, NetworkError, type Job, type SystemInfo, type User } from "../api";
import { describeError } from "../App";
import { Message } from "../components/Message";
import { bytes, duration, percentage } from "../format";

const REFRESH_MS = 5000;

interface Props {
  user: User;
  onSignOut: () => void;
}

export function Overview({ user, onSignOut }: Props) {
  const [system, setSystem] = useState<SystemInfo | null>(null);
  const [error, setError] = useState<ReturnType<typeof describeError> | null>(null);
  const [rebooting, setRebooting] = useState<Job | null>(null);
  const [confirming, setConfirming] = useState(false);

  const refresh = useCallback(async () => {
    try {
      setSystem(await api.system());
      setError(null);
    } catch (caught) {
      setError(describeError(caught));
    }
  }, []);

  useEffect(() => {
    // setState runs after the fetch resolves, not synchronously in the effect
    // body — the rule cannot see through the async boundary.
    // eslint-disable-next-line react-hooks/set-state-in-effect
    void refresh();
    // Polled rather than pushed. On a LAN, five seconds is imperceptible, and
    // it avoids a websocket that would have to be reconnected every time the
    // server restarts — which, on this screen, it is expected to.
    const timer = setInterval(() => void refresh(), REFRESH_MS);
    return () => clearInterval(timer);
  }, [refresh]);

  if (rebooting) {
    return <Restarting job={rebooting} onBack={() => void refresh().then(() => setRebooting(null))} />;
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

      <main className="app-main">
        {error ? (
          <Message
            tone="error"
            title={error.title}
            detail={error.detail}
            recovery={error.recovery}
          />
        ) : null}

        {system ? (
          <>
            <SystemCard system={system} />
            <DangerCard
              hostname={system.hostname}
              open={confirming}
              onOpen={() => setConfirming(true)}
              onCancel={() => setConfirming(false)}
              onConfirmed={(job) => {
                setConfirming(false);
                setRebooting(job);
              }}
            />
          </>
        ) : (
          !error && <p className="muted">Reading your server…</p>
        )}
      </main>
    </div>
  );
}

function SystemCard({ system }: { system: SystemInfo }) {
  const usedBytes = system.memory.total_bytes - system.memory.available_bytes;
  const usedPercent = percentage(usedBytes, system.memory.total_bytes);

  return (
    <section className="card">
      <h2>This server</h2>

      <dl className="facts">
        <dt>Running for</dt>
        <dd>{duration(system.uptime_seconds)}</dd>

        <dt>Operating system</dt>
        <dd>{system.os}</dd>

        <dt>Processor</dt>
        <dd>
          {system.cpu.model}
          <span className="muted">
            {" "}
            — {system.cpu.cores} core{system.cpu.cores === 1 ? "" : "s"}
            {system.cpu.threads !== system.cpu.cores ? `, ${system.cpu.threads} threads` : ""}
          </span>
        </dd>

        <dt>Memory</dt>
        <dd>
          <div className="meter" role="img" aria-label={`${usedPercent} per cent of memory in use`}>
            <div className="meter-fill" style={{ width: `${usedPercent}%` }} />
          </div>
          <span className="muted">
            {bytes(usedBytes)} of {bytes(system.memory.total_bytes)} in use
          </span>
        </dd>

        <dt>Busyness</dt>
        <dd>
          {system.load_average[0].toFixed(2)}
          <span className="muted"> over the last minute</span>
        </dd>

        {/* A machine with no battery reports null, which is different from "not
            on battery". Showing 0 % would be a confident lie. */}
        {system.power.battery_percent !== null ? (
          <>
            <dt>Battery</dt>
            <dd>
              {system.power.battery_percent}%
              {system.power.on_battery ? (
                <span className="muted"> — running on battery</span>
              ) : (
                <span className="muted"> — plugged in</span>
              )}
            </dd>
          </>
        ) : null}
      </dl>

      <details className="details">
        <summary>Technical details</summary>
        <dl className="facts facts-quiet">
          <dt>Name</dt>
          <dd>{system.hostname}</dd>
          <dt>Kernel</dt>
          <dd>{system.kernel}</dd>
          <dt>Architecture</dt>
          <dd>{system.architecture}</dd>
          {system.virtualised ? (
            <>
              <dt>Hardware</dt>
              <dd>Virtual machine</dd>
            </>
          ) : null}
        </dl>
      </details>
    </section>
  );
}

interface DangerProps {
  hostname: string;
  open: boolean;
  onOpen: () => void;
  onCancel: () => void;
  onConfirmed: (job: Job) => void;
}

/**
 * Restarting the server.
 *
 * The confirmation asks for the server's name rather than offering a yes/no.
 * That is not friction for its own sake: the API requires the target to be
 * named so a confirmation cannot be replayed against a different machine, and
 * typing the name is what makes the person notice which machine they are about
 * to restart.
 */
function DangerCard({ hostname, open, onOpen, onCancel, onConfirmed }: DangerProps) {
  const [typed, setTyped] = useState("");
  const [error, setError] = useState<ReturnType<typeof describeError> | null>(null);
  const [busy, setBusy] = useState(false);

  const matches = typed.trim() === hostname;

  async function restart() {
    if (!matches || busy) return;
    setBusy(true);
    setError(null);
    try {
      const job = await api.reboot(hostname, "Restarted from the dashboard");
      onConfirmed(job);
    } catch (caught) {
      // The server may go down before it answers, which is success rather than
      // failure. There is no way to tell from here, so treat losing the
      // connection as the restart having started — the job will settle it.
      if (caught instanceof NetworkError) {
        onConfirmed({
          job_id: "",
          operation: "system.reboot",
          state: "running",
          stage: "restarting",
          progress: null,
          message: "The server is restarting.",
          cancellable: false,
          error: null,
          created_at: new Date().toISOString(),
          started_at: null,
          finished_at: null,
        });
        return;
      }
      setError(describeError(caught));
      setBusy(false);
    }
  }

  if (!open) {
    return (
      <section className="card card-quiet">
        <h2>Restart</h2>
        <p className="muted">
          Restarting takes a minute or two. Anything using your server will be unavailable
          until it comes back.
        </p>
        <button className="danger" onClick={onOpen}>
          Restart this server
        </button>
      </section>
    );
  }

  return (
    <section className="card card-danger">
      <h2>Restart {hostname}?</h2>
      <p>
        Anything using this server will stop until it comes back. Type its name to confirm.
      </p>

      {error ? (
        <Message tone="error" title={error.title} detail={error.detail} recovery={error.recovery} />
      ) : null}

      <label htmlFor="confirm-name">
        Server name — <code>{hostname}</code>
      </label>
      <input
        id="confirm-name"
        autoFocus
        autoComplete="off"
        value={typed}
        onChange={(e) => setTyped(e.target.value)}
        disabled={busy}
      />

      <div className="row">
        <button className="quiet" onClick={onCancel} disabled={busy}>
          Cancel
        </button>
        <button className="danger" onClick={() => void restart()} disabled={!matches || busy}>
          {busy ? "Restarting…" : "Restart now"}
        </button>
      </div>
    </section>
  );
}

/**
 * The screen shown while the server is away.
 *
 * The connection dies with the machine, so the dashboard cannot watch the job
 * finish. It waits for the server to answer again and then asks what happened —
 * which is the same thing core does internally, for the same reason.
 */
function Restarting({ job, onBack }: { job: Job; onBack: () => void }) {
  const [status, setStatus] = useState<"restarting" | "back">("restarting");
  const [resolved, setResolved] = useState<Job | null>(null);
  const wentAway = useRef(false);

  useEffect(() => {
    let cancelled = false;

    const poll = async () => {
      while (!cancelled) {
        await new Promise((resolve) => setTimeout(resolve, 2000));
        if (cancelled) return;

        try {
          await api.health();
          // Only trust "it is back" after we have seen it go away. Immediately
          // after asking for a restart the server is still up, and treating
          // that as recovery would declare success before anything happened.
          if (wentAway.current) {
            if (job.job_id) {
              try {
                setResolved(await api.job(job.job_id));
              } catch {
                // The job is gone or we are signed out; the restart itself
                // still succeeded.
              }
            }
            setStatus("back");
            return;
          }
        } catch {
          wentAway.current = true;
        }
      }
    };

    void poll();
    return () => {
      cancelled = true;
    };
  }, [job.job_id]);

  return (
    <main className="centred">
      <div className="card">
        {status === "restarting" ? (
          <>
            <h1>Restarting…</h1>
            <p className="muted">
              Your server is restarting. This usually takes a minute or two. You do not need
              to do anything — this page will notice when it is back.
            </p>
            <div className="spinner" aria-label="Waiting for the server" />
          </>
        ) : (
          <>
            <h1>Your server is back</h1>
            {resolved && isTerminal(resolved.state) ? (
              <Message
                tone={resolved.state === "succeeded" ? "info" : "error"}
                title={resolved.message ?? "The restart finished."}
                detail={resolved.error?.detail}
                recovery={resolved.error?.recovery}
              />
            ) : (
              <p className="muted">It restarted and is answering again.</p>
            )}
            <button className="primary" onClick={onBack}>
              Continue
            </button>
          </>
        )}
      </div>
    </main>
  );
}
