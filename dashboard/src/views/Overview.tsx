import { useEffect, useState } from "react";
import { api, NetworkError, type Job, type NetworkStatus, type SystemInfo } from "../api";
import { describeError } from "../App";
import { Message } from "../components/Message";
import { RenameServer } from "../components/RenameServer";
import { Health } from "./Health";
import { bytes, duration, percentage } from "../format";

/** The two ways a server stops being available. */
export type PowerChoice = "reboot" | "shutdown";

interface Props {
  system: SystemInfo;
  onLeaving: (choice: PowerChoice, job: Job) => void;
}

export function Overview({ system, onLeaving }: Props) {
  const [renaming, setRenaming] = useState(false);

  return (
    <>
      <SystemCard
        system={system}
        renaming={renaming}
        onRename={() => setRenaming(true)}
      />
      <PowerCard hostname={system.hostname} onLeaving={onLeaving} />
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
          technical={`${system.temperature.celsius} °C`}
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

/**
 * Restarting the server, and switching it off.
 *
 * One card rather than two, because they are one decision: somebody arriving
 * here has decided the machine should stop doing what it is doing, and has not
 * yet decided for how long. Two cards in different places would make that a
 * question of which one they scrolled to.
 *
 * The confirmation asks for the server's name rather than offering a yes/no.
 * That is not friction for its own sake: the API requires the target to be
 * named so a confirmation cannot be replayed against a different machine, and
 * typing the name is what makes somebody notice which machine they are about to
 * take away.
 *
 * Switching off carries one thing a restart does not — how to undo it. A
 * restart explains itself by ending. A machine that is off explains nothing,
 * and this screen is the last one that can say how to bring it back, so it says
 * so *before* the button rather than after.
 */
function PowerCard({
  hostname,
  onLeaving,
}: {
  hostname: string;
  onLeaving: (choice: PowerChoice, job: Job) => void;
}) {
  const [choice, setChoice] = useState<PowerChoice | null>(null);
  const [typed, setTyped] = useState("");
  const [error, setError] = useState<ReturnType<typeof describeError> | null>(null);
  const [busy, setBusy] = useState(false);
  /** The hardware address to send a magic packet to, or null if nothing can. */
  const [waking, setWaking] = useState<string | null>(null);

  // Asked only when somebody is actually about to switch the machine off, and
  // not on every visit to this screen. It answers one question — can this be
  // switched on again without walking to it — and nothing else here needs it.
  useEffect(() => {
    if (choice !== "shutdown") return;
    let current = true;
    api
      .network()
      .then((status) => {
        if (current) setWaking(howToWake(status));
      })
      // A failure here is not worth showing. The fallback is the true and
      // useful sentence anyway: press its power button.
      .catch(() => {
        if (current) setWaking(null);
      });
    return () => {
      current = false;
    };
  }, [choice]);

  const matches = typed.trim() === hostname;

  function close() {
    setChoice(null);
    setTyped("");
    setError(null);
    setWaking(null);
  }

  async function go() {
    if (!matches || busy || choice === null) return;
    setBusy(true);
    setError(null);
    const kind = choice;
    try {
      const job =
        kind === "reboot"
          ? await api.reboot(hostname, "Restarted from the dashboard")
          : await api.shutdown(hostname, "Switched off from the dashboard");
      onLeaving(kind, job);
    } catch (caught) {
      // The server may go down before it answers, which is success rather than
      // failure. There is no way to tell from here, so treat losing the
      // connection as it having started — the job will settle it.
      if (caught instanceof NetworkError) {
        onLeaving(kind, {
          job_id: "",
          operation: kind === "reboot" ? "system.reboot" : "system.shutdown",
          state: "running",
          stage: kind === "reboot" ? "restarting" : "switching_off",
          progress: null,
          message:
            kind === "reboot"
              ? "The server is restarting."
              : "The server is switching off.",
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

  if (choice === null) {
    return (
      <section className="card card-quiet">
        <h2>Power</h2>
        <p className="muted">
          Both of these stop everything on this server until it is back — anything
          being watched, any download, any backup that is running.
        </p>
        <div className="row">
          <button className="danger" onClick={() => setChoice("reboot")}>
            Restart this server
          </button>
          <button className="danger" onClick={() => setChoice("shutdown")}>
            Switch this server off
          </button>
        </div>
      </section>
    );
  }

  const restarting = choice === "reboot";

  return (
    <section className="card card-danger">
      <h2>
        {restarting ? "Restart" : "Switch off"} {hostname}?
      </h2>
      <p>
        {restarting
          ? "Anything using this server will stop until it comes back, which takes a minute or two."
          : "Anything using this server will stop, and it will stay off until somebody switches it on."}
      </p>

      {/* Said before the button, not after it. Once the machine is off, this
          page is gone with it — so if the way back is only shown afterwards it
          is shown on a screen that no longer loads. */}
      {restarting ? null : waking === null ? (
        <Message
          tone="warning"
          title="You will have to switch it on by hand."
          recovery={
            "Press the power button on the server itself. Waking it over the " +
            "network works on most machines and is not switched on here — worth " +
            "setting up before you need it rather than after, under Network."
          }
        />
      ) : (
        <Message
          tone="info"
          title="You can switch it on again from another computer."
          recovery={
            "Run this from any computer on this network, or press the power " +
            "button on the server itself. It needs the machine left plugged in — " +
            "a laptop on its battery has nothing listening once it is off."
          }
          technical={`homebasectl wake ${waking}`}
        />
      )}

      {error ? (
        <Message tone="error" title={error.title} technical={error.detail} recovery={error.recovery} />
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
        <button className="quiet" onClick={close} disabled={busy}>
          Cancel
        </button>
        <button className="danger" onClick={() => void go()} disabled={!matches || busy}>
          {busy
            ? restarting
              ? "Restarting…"
              : "Switching off…"
            : restarting
              ? "Restart now"
              : "Switch off now"}
        </button>
      </div>
    </section>
  );
}

/**
 * Whether this machine can be switched on again over the network, and by what.
 *
 * Deliberately strict. `wake_on_lan_known` false means the card would not
 * answer, which is not a yes — and the cost of getting this wrong is somebody
 * switching off a server in another room on the strength of a promise that it
 * can be woken from this one.
 *
 * Wireless is excluded even where the card claims it: waking over Wi-Fi needs
 * the access point, the card's firmware and the BIOS all to agree, and on the
 * hardware Homebase is meant for it essentially never works.
 */
function howToWake(status: NetworkStatus): string | null {
  for (const iface of status.interfaces) {
    // "ethernet" is what the server calls a cable — not "wired", which is what
    // this asked for at first, on a machine where waking worked perfectly. The
    // screen then told its owner to walk to it. Nothing but a real server would
    // have caught that: every interface in every fixture was named correctly.
    if (iface.kind !== "ethernet") continue;
    if (!iface.wake_on_lan_known || !iface.wake_on_lan) continue;
    // A tunnel is reported as ethernet too and has no hardware address. It also
    // cannot be woken, which the check above already settles — this is the
    // second reason rather than the first.
    if (!iface.mac) continue;
    return iface.mac;
  }
  return null;
}
