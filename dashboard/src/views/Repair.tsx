import { useState } from "react";
import {
  api,
  type Diagnostics,
  type FactoryResetResult,
  type RepairResult,
} from "../api";
import { describeError } from "../App";
import { Message } from "../components/Message";
import { bytes } from "../format";

/**
 * When something is wrong.
 *
 * The screen is ordered by how drastic each thing is, and nothing further down
 * is reachable without having scrolled past what comes before it: find out
 * what is wrong, try to fix it, and — last, behind the machine's own name typed
 * by hand — start again.
 *
 * Two things here are unusual and deliberate.
 *
 * The diagnostic file shows **what it does not contain**, on the screen, at the
 * moment somebody is deciding whether to send it to a stranger. That question
 * is asked here, not in the documentation, so the answer belongs here.
 *
 * Repair reports **doing nothing** as a result rather than as success. "Nothing
 * needed fixing" is the honest answer when the thing that is wrong is not one of
 * the things this knows about, and dressing it up as a tick would send somebody
 * away believing their server was repaired.
 */

interface Props {
  serverName: string;
}

export function Repair({ serverName }: Props) {
  const [error, setError] = useState<ReturnType<typeof describeError> | null>(null);
  const [busy, setBusy] = useState<"" | "diagnostics" | "repair" | "reset">("");

  const [diagnostics, setDiagnostics] = useState<Diagnostics | null>(null);
  const [repaired, setRepaired] = useState<RepairResult | null>(null);
  const [reset, setReset] = useState<FactoryResetResult | null>(null);
  const [asking, setAsking] = useState(false);

  const run = async <T,>(
    what: "diagnostics" | "repair" | "reset",
    call: () => Promise<T>,
    then: (result: T) => void,
  ) => {
    setError(null);
    setBusy(what);
    try {
      then(await call());
    } catch (caught) {
      setError(describeError(caught));
    } finally {
      setBusy("");
    }
  };

  return (
    <>
      {error ? (
        <Message tone="error" title={error.title} technical={error.detail} recovery={error.recovery} />
      ) : null}

      {/* --- 1. Find out what is wrong ------------------------------------- */}

      <section className="card">
        <h2>Make a file to send to somebody</h2>
        <p className="muted">
          If something is wrong and you want help, this collects what somebody would need to
          work it out: which version this server is running, whether its services are running,
          what has failed, and the last day of messages.
        </p>

        <div className="row">
          <button
            className="primary"
            disabled={busy !== ""}
            onClick={() =>
              void run("diagnostics", api.collectDiagnostics, setDiagnostics)
            }
          >
            {busy === "diagnostics" ? "Collecting…" : "Make a diagnostic file"}
          </button>
          {diagnostics ? (
            <a className="button quiet" href={api.diagnosticsDownloadURL()} download>
              Download it
            </a>
          ) : null}
        </div>

        {diagnostics ? (
          <>
            <p>
              {diagnostics.message}
              {diagnostics.bytes ? (
                <span className="muted"> — {bytes(diagnostics.bytes)}</span>
              ) : null}
            </p>

            {/* The question somebody is actually asking, answered where they
                are asking it. */}
            <details className="details" open>
              <summary>What is <em>not</em> in this file</summary>
              <ul className="muted">
                {diagnostics.excludes.map((item) => (
                  <li key={item}>{item}</li>
                ))}
              </ul>
              <p className="muted">
                It <strong>does</strong> contain the name of this server, the names of your
                disks and applications, and error messages, which can include the names of
                files.
              </p>
            </details>

            <details className="details">
              <summary>What is in it</summary>
              <ul className="muted">
                {diagnostics.includes.map((item) => (
                  <li key={item}>{item}</li>
                ))}
              </ul>
            </details>
          </>
        ) : null}
      </section>

      {/* --- 2. Try to fix it ---------------------------------------------- */}

      <section className="card">
        <h2>Try to fix it</h2>
        <p className="muted">
          Homebase checks a short list of things that are often wrong after a power cut or an
          interrupted update — an unfinished installation, folders missing or belonging to the
          wrong account, services that are not running — and puts right the ones it can.
        </p>
        <p className="muted">
          <strong>Nothing is deleted.</strong> Every check only ever puts something back.
        </p>

        <button
          className="primary"
          disabled={busy !== ""}
          onClick={() => void run("repair", api.repair, setRepaired)}
        >
          {busy === "repair" ? "Checking…" : "Check and repair"}
        </button>

        {repaired ? <RepairReport result={repaired} /> : null}
      </section>

      {/* --- 3. Start again ------------------------------------------------ */}

      <section className="card card-danger">
        <h2>Start again</h2>
        <p>
          A factory reset removes <strong>every account</strong> and every setting on this
          server. Afterwards it asks to be set up again, like a new one.
        </p>
        <p className="muted">
          Your files are kept. Everything on your storage disks stays exactly where it is —
          you will see it again once you have made an account. Where this server gets its
          updates from is kept too, so it keeps receiving security fixes.
        </p>
        <p className="muted">
          <strong>Make a backup first if you can.</strong> The accounts and settings this
          removes cannot be brought back any other way.
        </p>
        <p className="muted">
          This server gets a new identity, so your browser will warn you about it once more —
          the same warning you saw the first time. That is deliberate: if you are passing this
          machine on, whoever had it before should not keep anything that still works as it.
        </p>

        {reset ? (
          <Message tone="info" title={reset.message} />
        ) : asking ? (
          <ConfirmReset
            serverName={serverName}
            busy={busy === "reset"}
            onCancel={() => setAsking(false)}
            onConfirm={(typed) => {
              setAsking(false);
              void run("reset", () => api.factoryReset(typed, true), setReset);
            }}
          />
        ) : (
          <button className="danger" disabled={busy !== ""} onClick={() => setAsking(true)}>
            Reset this server
          </button>
        )}
      </section>
    </>
  );
}

function RepairReport({ result }: { result: RepairResult }) {
  return (
    <>
      {/* Doing nothing is a result, not a success. Somebody whose server is
          broken must not be sent away believing it was repaired. */}
      <Message
        tone={result.healthy ? (result.changed === 0 ? "warning" : "info") : "error"}
        title={result.message}
        recovery={
          result.changed === 0 && result.healthy
            ? "Make a diagnostic file and send it to somebody who can look at it."
            : undefined
        }
      />

      <details className="details">
        <summary>What was checked</summary>
        <ul className="app-list">
          {result.steps.map((step) => (
            <li key={step.what} className="storage-row">
              <div className="row row-between">
                <span>{step.what}</span>
                {step.problem ? (
                  <span className="badge badge-error">Could not fix</span>
                ) : step.done ? (
                  <span className="badge badge-ok">Fixed</span>
                ) : (
                  <span className="muted">Already fine</span>
                )}
              </div>
              {step.done ? <p className="muted">{step.done}</p> : null}
              {step.problem ? <p className="muted">{step.problem}</p> : null}
            </li>
          ))}
        </ul>
      </details>
    </>
  );
}

/**
 * The server's own name, typed.
 *
 * Not a word like "yes". It is the one string specific to this machine, so it
 * cannot be typed out of habit, and it is what stops a reset meant for one
 * server landing on another.
 */
function ConfirmReset({
  serverName,
  busy,
  onCancel,
  onConfirm,
}: {
  serverName: string;
  busy: boolean;
  onCancel: () => void;
  onConfirm: (typed: string) => void;
}) {
  const [typed, setTyped] = useState("");
  const matches = typed.trim() === serverName;

  return (
    <>
      <label htmlFor="reset-confirm">
        Type <code>{serverName}</code> to confirm
      </label>
      <input
        id="reset-confirm"
        autoFocus
        autoComplete="off"
        value={typed}
        onChange={(e) => setTyped(e.target.value)}
      />
      <div className="row">
        <button className="quiet" onClick={onCancel}>
          Cancel
        </button>
        <button className="danger" disabled={!matches || busy} onClick={() => onConfirm(typed.trim())}>
          {busy ? "Resetting…" : "Reset this server"}
        </button>
      </div>
    </>
  );
}
