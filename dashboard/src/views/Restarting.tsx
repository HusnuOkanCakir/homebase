import { useEffect, useRef, useState } from "react";
import { api, isTerminal, type Job } from "../api";
import { Message } from "../components/Message";

/**
 * The screen shown while the server is away.
 *
 * The connection dies with the machine, so the dashboard cannot watch the job
 * finish. It waits for the server to answer again and then asks what happened —
 * which is the same thing core does internally, for the same reason.
 */
export function Restarting({ job, onBack }: { job: Job; onBack: () => void }) {
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
