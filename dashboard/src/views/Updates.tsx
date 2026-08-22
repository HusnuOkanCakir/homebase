import { useCallback, useEffect, useRef, useState } from "react";
import { api, type UpdateCheck, type UpdateProgress, type UpdateStatus } from "../api";
import { describeError } from "../App";
import { Message } from "../components/Message";

/**
 * Keeping the server up to date.
 *
 * The unusual thing about this screen is that the server stops answering part
 * way through the thing it is doing. An update replaces the services that serve
 * this page and restarts them, so "the connection failed" is the *expected*
 * middle of a successful update, not a sign that something went wrong.
 *
 * Everything here follows from that. The screen polls rather than waits, treats
 * a failed request during an update as normal, and reads the outcome from the
 * server afterwards rather than remembering what it started — because the thing
 * that would have remembered was restarted.
 */
export function Updates({ canManage }: { canManage: boolean }) {
  const [status, setStatus] = useState<UpdateStatus | null>(null);
  const [available, setAvailable] = useState<UpdateCheck | null>(null);
  const [progress, setProgress] = useState<UpdateProgress | null>(null);
  const [error, setError] = useState<ReturnType<typeof describeError> | null>(null);
  const [checking, setChecking] = useState(false);
  const [starting, setStarting] = useState(false);

  // Set once an update has been started here, so the screen keeps polling
  // through the window where the server is unreachable instead of concluding
  // that something failed.
  //
  // Kept in both a state value and a ref on purpose. The state is what the
  // render reads; the ref is what the async load below reads, because that
  // closure outlives the render it was created in and would otherwise decide
  // whether a failure is expected using a value from before the update started.
  const [watching, setWatching] = useState(false);
  const watchingRef = useRef(false);

  const load = useCallback(async () => {
    try {
      const [next, running] = await Promise.all([
        api.updateStatus(),
        api.updateProgress(),
      ]);
      setStatus(next);
      setProgress(running);
      setError(null);
      return running;
    } catch (caught) {
      // While an update is running this is expected: the server is restarting
      // itself. Reporting it as an error would tell somebody their update had
      // broken at the exact moment it was working.
      if (!watchingRef.current) setError(describeError(caught));
      return null;
    }
  }, []);

  useEffect(() => {
    // eslint-disable-next-line react-hooks/set-state-in-effect
    void load();
  }, [load]);

  // Poll while an update is in flight. `result` being empty is what "still
  // running" means — there is no client-side timer that could survive the
  // server restarting.
  useEffect(() => {
    if (!progress || progress.result || !watching) return;
    const timer = setTimeout(() => void load(), 3000);
    return () => clearTimeout(timer);
  }, [progress, watching, load]);

  const check = async () => {
    setChecking(true);
    try {
      setAvailable(await api.checkForUpdate());
      setError(null);
      await load();
    } catch (caught) {
      setError(describeError(caught));
    }
    setChecking(false);
  };

  const apply = async () => {
    setStarting(true);
    watchingRef.current = true;
    setWatching(true);
    try {
      await api.applyUpdate();
      setProgress({ stage: "refreshing", running: true });
      setError(null);
    } catch (caught) {
      watchingRef.current = false;
      setWatching(false);
      setError(describeError(caught));
    }
    setStarting(false);
  };

  if (!status && !error) {
    return <p className="muted">Reading what this server is running…</p>;
  }

  const updating = Boolean(progress && progress.stage && !progress.result && watching);
  const finished = progress?.result;

  return (
    <>
      {error ? (
        <Message tone="error" title={error.title} technical={error.detail} recovery={error.recovery} />
      ) : null}

      {status && status.interrupted ? (
        <Message
          tone="error"
          title="An earlier update did not finish."
          detail="The server is working, but it is part way between two versions and cannot install anything until that is sorted out."
          recovery="On the server itself, sign in and run: sudo dpkg --configure -a"
        />
      ) : null}

      {updating ? (
        <Message
          tone="info"
          title="Updating."
          detail={stageInWords(progress?.stage ?? "")}
          recovery="This page will stop responding for a moment while the server restarts itself. That is part of it. Leave this window open."
        />
      ) : null}

      {finished === "failed" ? (
        <Message
          tone="error"
          title="The update did not work, and the server was put back."
          detail={progress?.detail || "The new version did not come up healthy."}
          recovery="Your server is running the version it was on before, with its settings restored. Nothing was lost. It is worth trying again later — if it fails the same way, the release is at fault rather than your machine."
        />
      ) : null}

      {finished === "ok" && progress?.to ? (
        <Message
          tone="info"
          title={`This server was updated to ${progress.to}.`}
          detail={progress.from ? `It was on ${progress.from} before.` : undefined}
        />
      ) : null}

      <section className="card">
        <h2>Updates</h2>

        <dl className="facts">
          <dt>Version</dt>
          <dd>{status?.version || "unknown"}</dd>

          <dt>Updates come from</dt>
          <dd>
            {status?.channel ? (
              <>
                the <strong>{status.channel}</strong> channel
              </>
            ) : (
              "nowhere yet — this server has no update source set up"
            )}
          </dd>
        </dl>

        {!status?.channel ? (
          <p className="hint">
            Until an update source is set up, this server will not receive fixes,
            including security fixes.
          </p>
        ) : null}

        {available ? (
          available.reachable ? (
            available.update_available ? (
              <p>
                <strong>Version {available.available} is available.</strong>{" "}
                <span className="muted">You are on {available.current}.</span>
              </p>
            ) : (
              <p className="muted">This server is up to date.</p>
            )
          ) : (
            <Message
              tone="warning"
              title="Homebase could not reach the update service."
              detail={
                available.detail ||
                "Nothing answered, or what answered could not be verified."
              }
              recovery="This is usually the internet rather than the server. Everything already installed keeps working."
            />
          )
        ) : null}

        <div className="row">
          <button className="quiet" onClick={() => void check()} disabled={checking || updating}>
            {checking ? "Checking…" : "Check for updates"}
          </button>

          {canManage && available?.update_available && !updating ? (
            <button onClick={() => void apply()} disabled={starting}>
              {starting ? "Starting…" : `Update to ${available.available}`}
            </button>
          ) : null}
        </div>

        {canManage && available?.update_available ? (
          <p className="hint">
            Updating takes a few minutes and restarts the parts of the server that
            serve this page. Your files and your applications are not touched. If
            the new version does not come up working, the server puts itself back
            on the one it is running now.
          </p>
        ) : null}
      </section>

      {status && !status.consistent ? (
        <section className="card">
          <h2>The parts of this server disagree</h2>
          <p>
            These should all be the same version. That they are not means an
            update stopped part way through.
          </p>
          <ul className="plain">
            {status.components.map((component) => (
              <li key={component.package}>
                <code>{component.package}</code>{" "}
                <span className="muted">
                  {component.version || "not installed"}
                  {component.state !== "installed" ? ` — ${component.state}` : ""}
                </span>
              </li>
            ))}
          </ul>
        </section>
      ) : null}
    </>
  );
}

/**
 * The stages, in words somebody can act on.
 *
 * Not the internal names. "applying" is accurate and tells a person nothing;
 * what they want to know is whether it is safe to walk away, and the answer
 * differs between downloading and installing.
 */
function stageInWords(stage: string): string {
  switch (stage) {
    case "refreshing":
      return "Looking at what is available.";
    case "downloading":
      return "Downloading. Nothing on the server has changed yet.";
    case "snapshot":
      return "Taking a copy of the settings, so they can be put back if anything goes wrong.";
    case "applying":
      return "Installing. The server will restart parts of itself now.";
    case "health":
      return "Checking the new version actually works.";
    case "rolling-back":
      return "That did not work. Putting the server back on the version it was running.";
    default:
      return "Working.";
  }
}
