import { useCallback, useEffect, useState } from "react";
import { api, type Application, type SystemInfo } from "../api";
import { Message } from "../components/Message";
import { Health } from "./Health";
import { bytes, percentage } from "../format";

/**
 * What the server is doing, and a way into everything on it.
 *
 * The screen somebody opens ten times a week, so it is arranged for the reason
 * they opened it rather than for completeness. Almost always that reason is
 * "take me to Jellyfin" — which used to take four clicks through a list of
 * everything installable, sorted alphabetically, with the thing they wanted
 * indistinguishable from the ten things they had never installed.
 *
 * Three bands, in the order they are needed:
 *
 *   **Anything wrong**, and nothing at all when nothing is. A panel that is
 *   always present is a panel people stop reading, which is how every warning
 *   ever shipped stopped working.
 *
 *   **The applications**, as things to press. Each tile is a link to the
 *   application itself, not to a page about it — the page about it is one more
 *   click for the one time in twenty somebody wants to stop or reconfigure it.
 *
 *   **How the machine is**, last, because it is the answer to a question
 *   almost nobody arrives with.
 */

interface Props {
  system: SystemInfo;
  onGoToApps: () => void;
  onOpenApp: (id: string) => void;
}

export function Home({ system, onGoToApps, onOpenApp }: Props) {
  const [apps, setApps] = useState<Application[] | null>(null);
  const [reachable, setReachable] = useState(true);

  const refresh = useCallback(async () => {
    try {
      const list = await api.apps();
      setApps(list.items.filter((app) => app.installed !== false));
      setReachable(list.docker_available !== false);
    } catch {
      // The rest of this screen is still worth showing. A server whose
      // application list will not load is not a server with no memory reading.
      setApps([]);
    }
  }, []);

  useEffect(() => {
    // eslint-disable-next-line react-hooks/set-state-in-effect
    void refresh();
    const timer = setInterval(() => void refresh(), 10_000);
    return () => clearInterval(timer);
  }, [refresh]);

  const troubled = (apps ?? []).filter(
    (app) => app.state === "failed" || app.health === "unhealthy",
  );

  return (
    <>
      {!reachable ? (
        <Message
          tone="error"
          title="Homebase cannot reach the container service."
          detail="Nothing below can be started or stopped, and what it says may be out of date."
          recovery="This usually clears on its own. If it does not, restart the server."
        />
      ) : null}

      {troubled.length > 0 ? (
        <Message
          tone="warning"
          title={
            troubled.length === 1
              ? `${troubled[0]?.name} needs attention.`
              : `${troubled.length} applications need attention.`
          }
          /* Named only when the title cannot name them all. With one, the title
             already said it, and repeating it reads as a second problem. */
          detail={
            troubled.length === 1
              ? undefined
              : troubled.map((app) => app.name).join(", ")
          }
          recovery="Open it below to see why and start it again."
        />
      ) : null}

      {system.temperature.message ? (
        <Message
          tone={system.temperature.state === "hot" ? "error" : "warning"}
          title={
            system.temperature.state === "hot"
              ? "This server is getting too hot."
              : "This server is running warm."
          }
          detail={system.temperature.message}
          technical={`${system.temperature.celsius} °C`}
        />
      ) : null}

      <section className="card">
        <div className="row row-spread">
          <h2>Your applications</h2>
          <button className="quiet" onClick={onGoToApps}>
            Add or remove
          </button>
        </div>

        {apps === null ? (
          <p className="muted">Reading…</p>
        ) : apps.length === 0 ? (
          <>
            <p className="muted">
              Nothing is installed yet. An application is what makes the server
              useful — somewhere to keep files, or to watch what is on it.
            </p>
            <button className="primary" onClick={onGoToApps}>
              Browse applications
            </button>
          </>
        ) : (
          <ul className="tiles-grid">
            {apps.map((app) => (
              <AppTile key={app.id} app={app} onDetails={() => onOpenApp(app.id)} />
            ))}
          </ul>
        )}
      </section>

      <MemoryAndDisk system={system} />

      <Health system={system} />
    </>
  );
}

/**
 * One application, as a thing to press.
 *
 * The tile opens the application. The small button opens Homebase's page about
 * it. That split is the whole design: pressing Jellyfin should start Jellyfin,
 * and the ratio of "I want to watch something" to "I want to change its storage
 * slot" is not close.
 *
 * A tile with nowhere to go is not a link. An application on loopback has a real
 * address that is not there from the computer showing this page, and an
 * application that is stopped has nothing listening on it — in both cases the
 * tile still opens its page, which is where the reason is.
 */
function AppTile({ app, onDetails }: { app: Application; onDetails: () => void }) {
  const openable = app.state === "running" && !!app.url && app.reachable_from_network;

  const dot =
    app.state === "running" && app.health === "unhealthy"
      ? "warn"
      : app.state === "running"
        ? "ok"
        : app.state === "failed"
          ? "bad"
          : "off";

  return (
    <li className="tile-app">
      {openable ? (
        <a className="tile-app-open" href={app.url} target="_blank" rel="noreferrer noopener">
          <TileFace app={app} dot={dot} />
        </a>
      ) : (
        <button className="tile-app-open" onClick={onDetails}>
          <TileFace app={app} dot={dot} />
        </button>
      )}
      <button className="tile-app-more" onClick={onDetails}>
        Details
      </button>
    </li>
  );
}

function TileFace({ app, dot }: { app: Application; dot: string }) {
  return (
    <>
      <span className="tile-app-icon" aria-hidden="true">
        {app.icon || app.name.slice(0, 1)}
      </span>
      <span className="tile-app-name">{app.name}</span>
      <span className={`tile-app-state tile-app-state-${dot}`}>
        {app.state === "running" && app.health === "unhealthy"
          ? "Not responding"
          : app.state === "running"
            ? "Running"
            : app.state === "failed"
              ? "Stopped unexpectedly"
              : app.state === "stopped"
                ? "Stopped"
                : "Unknown"}
      </span>
    </>
  );
}

/**
 * The two numbers a person acts on, as bars rather than as figures.
 *
 * Memory and disk both answer "is it about to run out", and a proportion answers
 * that faster than a pair of quantities does — 6.1 of 7.7 GB needs arithmetic
 * before it means anything.
 */
function MemoryAndDisk({ system }: { system: SystemInfo }) {
  const [used, setUsed] = useState<{ used: number; total: number } | null>(null);

  useEffect(() => {
    api
      .locations()
      .then((list) => {
        // The internal disk, which is the one that fills up and takes the
        // server down with it. A USB disk running out is somebody's problem;
        // this one is Homebase's.
        const internal = list.items.find((one) => one.id === "internal") ?? list.items[0];
        if (internal?.total_bytes && internal.available_bytes !== undefined) {
          setUsed({
            used: internal.total_bytes - internal.available_bytes,
            total: internal.total_bytes,
          });
        }
      })
      .catch(() => setUsed(null));
  }, []);

  const memoryUsed = system.memory.total_bytes - system.memory.available_bytes;

  return (
    <section className="card">
      <h2>Room left on this server</h2>
      <Bar
        label="Memory"
        used={memoryUsed}
        total={system.memory.total_bytes}
        note="Freed as soon as something needs it. A server using most of its memory is a server doing its job."
      />
      {used ? (
        <Bar
          label="Disk"
          used={used.used}
          total={used.total}
          note="Films and photographs live here. This is the one worth watching."
        />
      ) : null}
    </section>
  );
}

function Bar({
  label,
  used,
  total,
  note,
}: {
  label: string;
  used: number;
  total: number;
  note: string;
}) {
  const percent = percentage(used, total);
  return (
    <div className="usage">
      <div className="row row-spread">
        <strong>{label}</strong>
        <span className="muted">
          {bytes(used)} of {bytes(total)}
        </span>
      </div>
      <div
        className="meter"
        role="img"
        aria-label={`${label}: ${percent} per cent used`}
      >
        <div
          className={percent >= 90 ? "meter-fill meter-fill-full" : "meter-fill"}
          style={{ width: `${percent}%` }}
        />
      </div>
      <p className="muted usage-note">{note}</p>
    </div>
  );
}
