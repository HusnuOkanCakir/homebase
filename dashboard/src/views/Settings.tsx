import { useState } from "react";
import { type Job, type SystemInfo, type User } from "../api";
import { RenameServer } from "../components/RenameServer";
import { People } from "./People";
import { Security } from "./Security";
import { Updates } from "./Updates";
import { PowerCard, type PowerChoice } from "../components/PowerCard";
import { bytes, duration } from "../format";

/**
 * Everything about the server itself rather than about what is on it.
 *
 * It was four places: the machine's name and its power buttons under "This
 * server", updates under "Updates", the recovery code under "Security". Each is
 * something somebody does once or twice a year, and three tabs' worth of
 * permanent navigation was being spent on them — while the applications, which
 * is what the server is *for*, had one tab shared with the catalogue.
 *
 * Ordered by how often it is wanted, which puts the two irreversible things last
 * and the identity of the machine first.
 */

interface Props {
  user: User;
  system: SystemInfo;
  onLeaving: (choice: PowerChoice, job: Job) => void;
}

export function Settings({ user, system, onLeaving }: Props) {
  // Left open after a successful rename rather than closed. Closing it unmounts
  // the component holding the confirmation, so the form would vanish with
  // nothing saying the rename had worked.
  const [renaming, setRenaming] = useState(false);

  return (
    <>
      <section className="card">
        <h2>This server</h2>

        <dl className="facts">
          <dt>Name</dt>
          <dd>{system.hostname}</dd>

          <dt>Running for</dt>
          <dd>{duration(system.uptime_seconds ?? 0)}</dd>

          <dt>Operating system</dt>
          <dd>{system.os}</dd>

          <dt>Processor</dt>
          <dd>
            {system.cpu?.model ?? "—"}
            {system.cpu && (
              <span className="muted">
                {" "}
                — {system.cpu.cores} core{system.cpu.cores === 1 ? "" : "s"}
              </span>
            )}
          </dd>

          <dt>Memory</dt>
          <dd>{bytes(system.memory.total_bytes)}</dd>

          {/* A machine with no battery reports null, which is not the same as
              "not on battery". Showing 0 % would be a confident lie. */}
          {system.power.battery_percent !== null ? (
            <>
              <dt>Battery</dt>
              <dd>
                {system.power.battery_percent}%
                <span className="muted">
                  {system.power.on_battery ? " — running on battery" : " — plugged in"}
                </span>
              </dd>
            </>
          ) : null}
        </dl>

        {/* Only where there is something to say. A machine with one graphics
            chip and nothing configured against it does not need a lecture; a
            machine with two needs to know which is which, because the obvious
            name for them is the one that moves. */}
        {(system.graphics?.length ?? 0) > 0 ? (
          <>
            <h3>Graphics</h3>
            <ul className="list">
              {system.graphics?.map((card) => (
                <li key={card.render_node}>
                  <strong>{card.name}</strong>{" "}
                  <span className="muted">({card.driver})</span>
                  {card.accelerates_video ? null : (
                    <span className="badge badge-warning"> No video acceleration</span>
                  )}
                  {card.stable_path ? (
                    <div className="muted">
                      Point applications at <code>{card.stable_path}</code>
                      {" — "}it is <code>{card.render_node}</code> today, and that
                      number changes.
                    </div>
                  ) : (
                    <div className="muted">{card.render_node}</div>
                  )}
                </li>
              ))}
            </ul>
          </>
        ) : null}

        <details className="details">
          <summary>Technical details</summary>
          <dl className="facts facts-quiet">
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

        {renaming ? (
          <RenameServer id="server-name" current={system.hostname} />
        ) : (
          <button className="quiet" onClick={() => setRenaming(true)}>
            Rename this server
          </button>
        )}
      </section>

      <Updates canManage={user.permissions.includes("update.manage")} />

      {/* Only for somebody who can actually add people. A screen that lists
          the household and refuses every button is worse than no screen. */}
      {user.permissions.includes("accounts.manage") && <People me={user.username} />}

      <Security username={user.username} />

      <PowerCard hostname={system.hostname} onLeaving={onLeaving} />
    </>
  );
}
