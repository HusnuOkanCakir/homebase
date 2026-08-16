import { useState } from "react";
import { api, NetworkError, type Job, type SystemInfo } from "../api";
import { describeError } from "../App";
import { Message } from "../components/Message";
import { RenameServer } from "../components/RenameServer";
import { Health } from "./Health";
import { bytes, duration, percentage } from "../format";

interface Props {
  system: SystemInfo;
  onRebootStarted: (job: Job) => void;
}

export function Overview({ system, onRebootStarted }: Props) {
  const [confirming, setConfirming] = useState(false);
  const [renaming, setRenaming] = useState(false);

  return (
    <>
      <SystemCard
        system={system}
        renaming={renaming}
        onRename={() => setRenaming(true)}
      />
      <DangerCard
        hostname={system.hostname}
        open={confirming}
        onOpen={() => setConfirming(true)}
        onCancel={() => setConfirming(false)}
        onConfirmed={(job) => {
          setConfirming(false);
          onRebootStarted(job);
        }}
      />
    </>
  );
}

interface SystemCardProps {
  system: SystemInfo;
  renaming: boolean;
  onRename: () => void;
}

function SystemCard({ system, renaming, onRename }: SystemCardProps) {
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

        {/* Same rule as the battery: a machine with no sensors reports null, and
            showing 0 °C would look wonderfully cool. */}
        {system.temperature.celsius !== null ? (
          <>
            <dt>Temperature</dt>
            <dd>
              {system.temperature.celsius}&nbsp;&deg;C
              {system.temperature.state === "hot" ? (
                <span className="badge badge-error"> Too hot</span>
              ) : system.temperature.state === "warm" ? (
                <span className="badge badge-warning"> Warm</span>
              ) : null}
            </dd>
          </>
        ) : null}

        {/* Beside the temperature, never instead of it. Neither number means
            anything alone: loud and cool is a fan fault, loud and hot is a
            heatsink full of dust, and they are the same sound from a doorway. */}
        {system.fan.rpm !== null || system.fan.percent !== null ? (
          <>
            <dt>Fan</dt>
            <dd>
              {system.fan.rpm !== null ? `${system.fan.rpm} rpm` : null}
              {system.fan.rpm !== null && system.fan.percent !== null ? " " : null}
              {system.fan.percent !== null ? (
                <span className="muted">({system.fan.percent}%)</span>
              ) : null}
              {system.fan.controlled === "manual" ? (
                <span className="badge badge-warning"> Overridden</span>
              ) : null}
            </dd>
          </>
        ) : null}
      </dl>

      {/* Only when it is worth saying. A machine at an ordinary temperature says
          nothing at all — an indicator that is always lit is one people stop
          seeing, which is how every temperature warning ever shipped failed. */}
      {system.temperature.message ? (
        <Message
          tone={system.temperature.state === "hot" ? "error" : "warning"}
          title={
            system.temperature.state === "hot"
              ? "This server is getting too hot."
              : "This server is running warm."
          }
          detail={`${system.temperature.celsius} °C`}
          recovery={system.temperature.message}
        />
      ) : null}

      <Health system={system} />

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

      {/*
        Renaming lives here as well as in the getting-started list, because a
        machine that can only be renamed during its first week is one nobody can
        rename. The list stops offering it once the server has a name of its
        own; this does not.
      */}
      {/*
        Left open after a successful rename, rather than closed. Closing it
        unmounts the component that is holding the confirmation — so the form
        vanished, nothing said the rename had worked, and the only evidence was
        the heading changing a few seconds later when the next poll arrived.
      */}
      {renaming ? (
        <RenameServer id="server-name" current={system.hostname} />
      ) : (
        <button className="quiet" onClick={onRename}>
          Rename this server
        </button>
      )}
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
