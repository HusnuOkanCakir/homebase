import { useEffect, useRef, useState } from "react";
import { api, isTerminal, type Job } from "../api";
import { Message } from "../components/Message";
import type { PowerChoice } from "./Overview";

/**
 * The screen shown while the server is going away, and after it has gone.
 *
 * The connection dies with the machine, so the dashboard cannot watch the job
 * finish. It watches the machine instead — which is the same thing core does
 * internally, for the same reason.
 *
 * Both restarting and switching off wait for the same thing first: the server to
 * stop answering. Until that happens nothing has been proved. A screen that says
 * "your server is off" the instant the button was pressed is describing an
 * intention, and if the shutdown failed it is describing a machine that is still
 * running — which somebody would find out by walking to it.
 *
 * They part after that. A restart is finished when the server answers again, so
 * this waits for it. A shutdown is finished when it stops, so this stops too:
 * polling a machine that has been switched off deliberately is a spinner that
 * runs until the tab is closed.
 */
export function Leaving({
  choice,
  job,
  onBack,
}: {
  choice: PowerChoice;
  job: Job;
  onBack: () => void;
}) {
  const restarting = choice === "reboot";
  const [status, setStatus] = useState<"going" | "gone" | "back">("going");
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
          // after asking, the server is still up, and treating that as recovery
          // would declare success before anything had happened.
          if (wentAway.current) {
            if (job.job_id) {
              try {
                setResolved(await api.job(job.job_id));
              } catch {
                // The job is gone or we are signed out; what happened to the
                // machine still happened.
              }
            }
            setStatus("back");
            return;
          }
        } catch {
          wentAway.current = true;
          // For a shutdown this is the end of the story, and the last moment
          // anything here can be sure of it.
          if (!restarting) {
            setStatus("gone");
            return;
          }
        }
      }
    };

    void poll();
    return () => {
      cancelled = true;
    };
  }, [job.job_id, restarting]);

  return (
    <main className="centred">
      <div className="card">
        {status === "going" ? (
          <>
            <h1>{restarting ? "Restarting…" : "Switching off…"}</h1>
            <p className="muted">
              {restarting
                ? "Your server is restarting. This usually takes a minute or two. You do not need to do anything — this page will notice when it is back."
                : "Your server is switching off. It is finishing what it was writing first, which takes a few seconds."}
            </p>
            <div className="spinner" aria-label="Waiting for the server" />
          </>
        ) : status === "gone" ? (
          <>
            <h1>Your server is off</h1>
            <p className="muted">
              It stopped answering, so it has shut down properly rather than been
              cut off. Nothing on it was left half-written.
            </p>
            {/* The one thing somebody needs from this screen. Said here because
                there is nowhere else left to say it: every other page in
                Homebase is served by the machine that has just gone. */}
            <Message
              tone="info"
              title="To switch it on again"
              recovery={
                "Press the power button on the server itself. From another " +
                "computer on this network you can also run the command below — " +
                "which works only if waking over the network was switched on " +
                "before it went off, and the machine is still plugged in."
              }
              technical="homebasectl wake"
            />
            <button className="primary" onClick={onBack}>
              Done
            </button>
          </>
        ) : (
          <>
            <h1>Your server is back</h1>
            {resolved && isTerminal(resolved.state) ? (
              <Message
                tone={resolved.state === "succeeded" ? "info" : "error"}
                title={resolved.message ?? "It finished."}
                technical={resolved.error?.detail}
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
