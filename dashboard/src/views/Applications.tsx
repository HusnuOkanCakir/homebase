import { useCallback, useEffect, useRef, useState } from "react";
import {
  api,
  isTerminal,
  watchJob,
  type Application,
  type ApplicationList,
  type ApplicationStorage,
  type Job,
  type StorageLocation,
} from "../api";
import { describeError } from "../App";
import { Message } from "../components/Message";

/**
 * Applications.
 *
 * The vocabulary here is deliberately not the container one. A person installing
 * a media server is not choosing an image tag, and the interface that asks them
 * to is an interface for somebody who did not need it. There is no field
 * anywhere on this screen that describes a container, because the API has no
 * way to accept one — see ADR-0012.
 *
 * Two distinctions the screen refuses to blur:
 *
 *   - `health: null` is not "unhealthy". An application with no health check and
 *     one that is failing its check are different facts.
 *   - Removing an application keeps its data. Deleting the data is a separate
 *     act with its own confirmation, because "I have stopped using this" and
 *     "delete my photographs" are not the same intention.
 */

const REFRESH_MS = 5000;

interface Props {
  canManage: boolean;
}

export function Applications({ canManage }: Props) {
  const [list, setList] = useState<ApplicationList | null>(null);
  const [error, setError] = useState<ReturnType<typeof describeError> | null>(null);
  const [selected, setSelected] = useState<string | null>(null);

  // The job currently running, if any. One at a time on purpose: two installs at
  // once on a home connection makes both slow and neither explicable.
  const [job, setJob] = useState<{ app: string; job: Job } | null>(null);

  // How to stop watching it. Held in a ref rather than state because losing it
  // is the bug: a poll nobody cancelled keeps running after the user navigates
  // away, calling setState on a component that is gone, for as long as the tab
  // is open.
  const stopWatching = useRef<(() => void) | null>(null);

  useEffect(() => () => stopWatching.current?.(), []);

  const refresh = useCallback(async () => {
    try {
      setList(await api.apps());
      setError(null);
    } catch (caught) {
      setError(describeError(caught));
    }
  }, []);

  useEffect(() => {
    // eslint-disable-next-line react-hooks/set-state-in-effect
    void refresh();
    const timer = setInterval(() => void refresh(), REFRESH_MS);
    return () => clearInterval(timer);
  }, [refresh]);

  const run = useCallback(
    (appId: string, start: () => Promise<Job>) => {
      setError(null);
      void (async () => {
        let submitted: Job;
        try {
          submitted = await start();
        } catch (caught) {
          setError(describeError(caught));
          return;
        }

        setJob({ app: appId, job: submitted });

        // Whatever was being watched before is not what the user is waiting for
        // now.
        stopWatching.current?.();
        stopWatching.current = watchJob(
          submitted.job_id,
          (update) => {
            setJob({ app: appId, job: update });
            // Refresh as soon as it finishes: the list is polled every five
            // seconds, and a state that lags the thing the user just did by
            // several seconds reads as the action having failed.
            if (isTerminal(update.state)) {
              void refresh();
            }
          },
          (caught) => setError(describeError(caught)),
        );
      })();
    },
    [refresh],
  );

  if (!list && !error) {
    return <p className="muted">Looking at what is installed…</p>;
  }

  const application = selected ? list?.items.find((a) => a.id === selected) : undefined;

  // `installed` is null when the runtime did not answer, which is not the same
  // as false — an application Homebase cannot see the state of is kept with the
  // installed ones, because reporting it as merely available would be offering
  // to install something that may already be running.
  const items = list?.items ?? [];
  const installed = items.filter((app) => app.installed !== false);
  const available = items.filter((app) => app.installed === false);

  if (application) {
    return (
      <ApplicationDetail
        application={application}
        canManage={canManage}
        job={job?.app === application.id ? job.job : null}
        onBack={() => setSelected(null)}
        onRun={run}
        error={error}
      />
    );
  }

  return (
    <>
      {error ? (
        <Message tone="error" title={error.title} technical={error.detail} recovery={error.recovery} />
      ) : null}

      {/* Docker being down is stated once, at the top, rather than by making
          every application look uninstalled with no explanation. */}
      {list && !list.docker_available ? (
        <Message
          tone="warning"
          title="Homebase cannot see what is running."
          detail="The container service is not responding, so the states below may be out of date."
          recovery="This often clears on its own shortly after the server starts. If it persists, restart the server."
        />
      ) : null}

      {list?.unavailable && Object.keys(list.unavailable).length > 0 ? (
        <Message
          tone="warning"
          title="Some applications could not be read."
          detail={Object.entries(list.unavailable)
            .map(([file, reason]) => `${file}: ${reason}`)
            .join("; ")}
          recovery="These will not appear below. This is a fault in Homebase rather than something you did — please report it."
        />
      ) : null}

      <section className="card">
        <h2>Applications</h2>

        {list && list.items.length === 0 ? (
          <p className="muted">This server has no applications available to install.</p>
        ) : (
          <>
            {/* What is on this server, first and separately.

                It used to be one alphabetical list of everything, installed or
                not. On a server running eight applications that is eight rows
                worth reading scattered through four that are not, each carrying
                a badge saying "Not installed" — so the loudest, most repeated
                thing on the page was the word for nothing having happened.

                Somebody opening this screen has one of two questions, and they
                are not the same question: what is running, or what else could
                I have. */}
            <AppGroup
              title="On this server"
              apps={installed}
              onChoose={setSelected}
              empty="Nothing is installed yet."
            />
            <AppGroup
              title={installed.length > 0 ? "You could also add" : "Available"}
              subtitle={
                installed.length > 0
                  ? undefined
                  : "Homebase installs and looks after these for you. Choose one to see what it does."
              }
              apps={available}
              onChoose={setSelected}
              /* Only reachable with an empty catalogue, which the branch above
                 already handles — but a group that can render nothing should
                 say what nothing means rather than showing a bare heading. */
              empty="Everything in the catalogue is already installed."
            />
          </>
        )}
      </section>
    </>
  );
}

/**
 * One heading and the applications under it.
 *
 * The state badge is dropped for anything not installed. It said the same word
 * on every row in that group, which is the definition of a label carrying no
 * information — and the heading above them already says it once.
 */
function AppGroup({
  title,
  subtitle,
  apps,
  empty,
  onChoose,
}: {
  title: string;
  subtitle?: string | undefined;
  apps: Application[];
  empty: string;
  onChoose: (id: string) => void;
}) {
  return (
    <div className="app-group">
      <h3>
        {title}
        {apps.length > 0 ? <span className="muted"> · {apps.length}</span> : null}
      </h3>
      {subtitle ? <p className="muted">{subtitle}</p> : null}
      {apps.length === 0 ? (
        <p className="muted">{empty}</p>
      ) : (
        <ul className="app-list">
          {apps.map((app) => (
            <li key={app.id}>
              <button className="app-row" onClick={() => onChoose(app.id)}>
                <span className="app-row-main">
                  <span className="app-row-name">{app.name}</span>
                  {app.summary ? <span className="muted">{app.summary}</span> : null}
                </span>
                {app.installed ? <StateBadge application={app} /> : null}
              </button>
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}

/**
 * What state an application is in, in words a person can act on.
 *
 * `failed` and `stopped` get different words because they are different
 * situations: one of them nobody asked for.
 */
function StateBadge({ application }: { application: Application }) {
  const { state, health } = application;

  if (state === "running" && health === "unhealthy") {
    return <span className="badge badge-warning">Running, but not responding</span>;
  }
  if (state === "running" && health === "starting") {
    return <span className="badge badge-info">Starting…</span>;
  }

  switch (state) {
    case "running":
      return <span className="badge badge-ok">Running</span>;
    case "stopped":
      return <span className="badge badge-quiet">Stopped</span>;
    case "failed":
      return <span className="badge badge-error">Stopped unexpectedly</span>;
    case "not_installed":
      return <span className="badge badge-quiet">Not installed</span>;
    case "unknown":
      // Not "Not installed". Homebase could not ask, and saying it is not there
      // would invite installing it again on top of a working one.
      return <span className="badge badge-warning">Cannot tell</span>;
  }
}

interface DetailProps {
  application: Application;
  canManage: boolean;
  job: Job | null;
  error: ReturnType<typeof describeError> | null;
  onBack: () => void;
  onRun: (appId: string, start: () => Promise<Job>) => void;
}

function ApplicationDetail({
  application,
  canManage,
  job,
  error,
  onBack,
  onRun,
}: DetailProps) {
  const [confirming, setConfirming] = useState<null | "stop" | "uninstall" | "remove-data">(null);
  const [logs, setLogs] = useState<string | null>(null);
  const [storage, setStorage] = useState<ApplicationStorage | null>(null);
  const [locations, setLocations] = useState<StorageLocation[]>([]);

  const busy = job !== null && !isTerminal(job.state);

  const refreshStorage = useCallback(async () => {
    try {
      const [appStorage, locationList] = await Promise.all([
        api.appStorage(application.id),
        api.locations(),
      ]);
      setStorage(appStorage);
      setLocations(locationList.items);
    } catch {
      // Not fatal to the rest of the screen. An application with no
      // user-selected storage does not need this to have worked.
    }
  }, [application.id]);

  useEffect(() => {
    // eslint-disable-next-line react-hooks/set-state-in-effect
    void refreshStorage();
  }, [refreshStorage, job?.state]);

  // Which storage the user has to make a decision about. Storage Homebase places
  // itself is not shown: there is no choice to make, and listing it would turn a
  // question into a list.
  const needsADisk = (storage?.storage ?? []).filter((slot) => slot.type === "user-selected");

  return (
    <>
      <button className="quiet back" onClick={onBack}>
        ← All applications
      </button>

      <section className="card">
        <h2>{application.name}</h2>
        {application.summary ? <p>{application.summary}</p> : null}

        <div className="row row-between">
          <StateBadge application={application} />
          {application.version ? (
            <span className="muted">Version {application.version}</span>
          ) : null}
        </div>

        {error ? (
          <Message tone="error" title={error.title} technical={error.detail} recovery={error.recovery} />
        ) : null}

        {job ? <JobProgress job={job} /> : null}

        {/* Before anything else, because it is why the application will not
            start. An install button above an unexplained failure is worse than
            no button. */}
        {storage && !storage.ready ? (
          <Message
            tone="warning"
            title={`${application.name} needs somewhere to keep your files.`}
            detail={needsADisk
              .filter((slot) => !slot.ready)
              .map((slot) => slot.description || slot.id)
              .join("; ")}
            recovery={
              locations.length === 0
                ? "Set up a disk under Storage first, then come back here."
                : "Choose a disk below."
            }
          />
        ) : null}

        {/* An application that exited on its own has a reason, and the reason is
            in its logs. Saying so beats leaving somebody to guess. */}
        {application.state === "failed" ? (
          <Message
            tone="error"
            title={`${application.name} stopped on its own.`}
            detail={
              application.exit_code === null
                ? undefined
                : `It exited with code ${application.exit_code}.`
            }
            recovery="Look at its recent activity below for the reason, then start it again."
          />
        ) : null}

        {/* The privilege it holds, said before it is installed rather than
            after. The point of declaring a relaxation per application is that
            somebody can decline it, and a disclosure that arrives once the
            container is running is a notification rather than a choice.

            Kept quiet once it *is* installed — a warning that is always lit is
            one people stop reading, and the decision has been made by then. */}
        {application.elevation && !application.installed ? (
          <Message
            tone="warning"
            title={`${application.name} needs more than most applications.`}
            recovery={application.elevation.summary}
            detail={application.elevation.reason}
          />
        ) : null}

        {/* What is still left for a person to do, from the manifest. Above the
            buttons, because it is the answer to "it is running and I cannot get
            in" — which otherwise looks exactly like a broken application. */}
        {application.installed && application.after_install ? (
          <Message
            tone="info"
            title={`One thing left to set up in ${application.name}.`}
            recovery={application.after_install}
          />
        ) : null}

        {!canManage ? (
          <p className="muted">
            You can see this application but not change it. Ask whoever set up this server for
            permission to manage applications.
          </p>
        ) : (
          <div className="row">
            {/* Open comes first, because it is the thing somebody came here to do.
                Until this existed there was no way to reach an installed
                application from anywhere in Homebase — it would install, start,
                pass its health check, and sit at an address nothing reported. */}
            {application.state === "running" && application.url && application.reachable_from_network ? (
              <a
                className="button primary"
                href={application.url}
                target="_blank"
                rel="noreferrer noopener"
              >
                Open {application.name}
              </a>
            ) : null}
            {application.installed === null ? (
              <p className="muted">
                Homebase cannot see whether {application.name} is installed, so it is not
                offering to change it. This usually clears on its own.
              </p>
            ) : !application.installed ? (
              <button
                className="primary"
                disabled={busy}
                onClick={() => onRun(application.id, () => api.installApp(application.id))}
              >
                Install
              </button>
            ) : (
              <>
                {application.state === "running" ? (
                  <button className="quiet" disabled={busy} onClick={() => setConfirming("stop")}>
                    Stop
                  </button>
                ) : (
                  <button
                    className="primary"
                    disabled={busy}
                    onClick={() => onRun(application.id, () => api.startApp(application.id))}
                  >
                    Start
                  </button>
                )}
                <button
                  className="quiet"
                  disabled={busy}
                  onClick={() =>
                    onRun(application.id, () => api.restartApp(application.id, application.id))
                  }
                >
                  Restart
                </button>
                <button className="danger" disabled={busy} onClick={() => setConfirming("uninstall")}>
                  Remove
                </button>
              </>
            )}
          </div>
        )}
      </section>

      {confirming === "stop" ? (
        <Confirm
          title={`Stop ${application.name}?`}
          body="Anyone using it will lose access until it is started again. Nothing is deleted."
          confirmLabel="Stop it"
          expected={application.id}
          onCancel={() => setConfirming(null)}
          onConfirm={() => {
            setConfirming(null);
            onRun(application.id, () => api.stopApp(application.id, application.id));
          }}
        />
      ) : null}

      {confirming === "uninstall" ? (
        <Confirm
          title={`Remove ${application.name}?`}
          body={
            `${application.name} will be removed from this server. ` +
            "Its data is kept, so you can install it again later and find everything where it was."
          }
          confirmLabel="Remove it"
          expected={application.id}
          onCancel={() => setConfirming(null)}
          onConfirm={() => {
            setConfirming(null);
            onRun(application.id, () => api.uninstallApp(application.id, application.id));
          }}
        />
      ) : null}

      {confirming === "remove-data" ? (
        <Confirm
          title={`Delete ${application.name}'s data?`}
          body={
            `Everything ${application.name} has stored will be deleted permanently. ` +
            "This cannot be undone and no backup is taken first."
          }
          confirmLabel="Delete it permanently"
          expected={application.id}
          danger
          onCancel={() => setConfirming(null)}
          onConfirm={() => {
            setConfirming(null);
            onRun(application.id, () => api.removeAppData(application.id, application.id));
          }}
        />
      ) : null}

      {canManage && needsADisk.length > 0 ? (
        <section className="card">
          <h3>Where {application.name} keeps your files</h3>
          <ul className="app-list">
            {needsADisk.map((slot) => (
              <li key={slot.id} className="storage-row">
                <div className="app-row-main">
                  <span className="app-row-name">{slot.description || slot.id}</span>
                  {slot.ready ? (
                    <span className="muted">On {slot.location_name}</span>
                  ) : slot.location ? (
                    <span className="muted">
                      On {slot.location_name ?? slot.location}, which is not connected
                    </span>
                  ) : (
                    <span className="muted">No disk chosen yet</span>
                  )}
                </div>

                {locations.length === 0 ? (
                  <p className="muted">
                    There are no disks set up yet. Add one under Storage.
                  </p>
                ) : (
                  <div className="row">
                    {/* Every disk is listed and none is preselected. Homebase
                        does not choose a disk for somebody, even when there is
                        only one — "the obvious disk" is how the wrong one gets
                        picked. */}
                    {locations.map((location) => (
                      <button
                        key={location.id}
                        className={location.id === slot.location ? "quiet tab-current" : "quiet"}
                        disabled={busy || !location.mounted}
                        onClick={() =>
                          onRun(application.id, () =>
                            api.assignStorage(application.id, slot.id, location.id),
                          )
                        }
                      >
                        {location.name}
                        {!location.mounted ? " (not connected)" : ""}
                      </button>
                    ))}
                  </div>
                )}
              </li>
            ))}
          </ul>
          {/* Not "the next time it starts". That was false and was believed for
              a milestone: a container's folders are fixed when it is built, so
              restarting one keeps the folders it was built with. Changing this
              rebuilds it, which is why it takes a moment and why it is worth
              saying that nothing is moved. */}
          <p className="muted">
            Changing this rebuilds {application.name} so it can see the new disk, which
            takes a moment. Nothing already saved is moved.
          </p>
        </section>
      ) : null}

      <section className="card card-quiet">
        <h3>Recent activity</h3>
        <p className="muted">
          What {application.name} has been saying. Useful if it is not working as expected.
        </p>
        <button
          className="quiet"
          onClick={() => {
            void api
              .appLogs(application.id)
              .then((result) => setLogs(result.logs || "(nothing yet)"))
              .catch((caught: unknown) => setLogs(describeError(caught).title));
          }}
        >
          {logs === null ? "Show recent activity" : "Refresh"}
        </button>
        {logs !== null ? <pre className="logs">{logs}</pre> : null}
      </section>

      <details className="card card-quiet">
        <summary>Technical details</summary>
        <dl className="facts facts-quiet">
          <dt>Identifier</dt>
          <dd>
            <code>{application.id}</code>
          </dd>
          <dt>Image</dt>
          <dd>
            <code>
              {application.image}
              {application.version ? `:${application.version}` : ""}
            </code>
          </dd>
          <dt>Data</dt>
          <dd>
            <code>{application.data_path}</code>
          </dd>
          {application.started_at ? (
            <>
              <dt>Started</dt>
              <dd>{new Date(application.started_at).toLocaleString()}</dd>
            </>
          ) : null}
          {/* Only offered where there is something to delete, and only in here,
              behind the technical details. Nobody looking for a stop button
              should find this by accident. */}
          {canManage && application.installed === false ? (
            <>
              <dt>Delete data</dt>
              <dd>
                <button className="danger" onClick={() => setConfirming("remove-data")}>
                  Delete {application.name}'s data permanently
                </button>
              </dd>
            </>
          ) : null}
        </dl>
      </details>
    </>
  );
}

function JobProgress({ job }: { job: Job }) {
  if (job.state === "failed") {
    return (
      <Message
        tone="error"
        title={job.error?.message ?? "That did not work."}
        technical={job.error?.detail}
        recovery={job.error?.recovery}
      />
    );
  }

  if (job.state === "succeeded") {
    return <Message tone="info" title={job.message ?? "Done."} />;
  }

  return (
    <div className="progress" role="status">
      <p>{job.message ?? "Working…"}</p>
      {job.progress === null ? (
        <div className="meter meter-indeterminate" />
      ) : (
        <div
          className="meter"
          role="img"
          aria-label={`${job.progress} per cent complete`}
        >
          <div className="meter-fill" style={{ width: `${job.progress}%` }} />
        </div>
      )}
    </div>
  );
}

interface ConfirmProps {
  title: string;
  body: string;
  confirmLabel: string;
  /** What has to be typed. The API requires the request to name its target. */
  expected: string;
  danger?: boolean;
  onCancel: () => void;
  onConfirm: () => void;
}

/**
 * A confirmation that has to be typed.
 *
 * The API will not accept a boolean — the request must name the application it
 * is acting on, so a confirmation cannot be replayed against a different one.
 * Typing it is also the part that makes somebody read which application they
 * chose, which a second button does not.
 */
function Confirm({
  title,
  body,
  confirmLabel,
  expected,
  danger,
  onCancel,
  onConfirm,
}: ConfirmProps) {
  const [typed, setTyped] = useState("");
  const matches = typed.trim() === expected;

  return (
    <section className={danger ? "card card-danger" : "card card-quiet"}>
      <h3>{title}</h3>
      <p>{body}</p>

      <label htmlFor="confirm-app">
        Type <code>{expected}</code> to confirm
      </label>
      <input
        id="confirm-app"
        autoFocus
        autoComplete="off"
        value={typed}
        onChange={(e) => setTyped(e.target.value)}
      />

      <div className="row">
        <button className="quiet" onClick={onCancel}>
          Cancel
        </button>
        <button className="danger" disabled={!matches} onClick={onConfirm}>
          {confirmLabel}
        </button>
      </div>
    </section>
  );
}
