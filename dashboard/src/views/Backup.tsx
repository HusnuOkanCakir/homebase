import { useCallback, useEffect, useRef, useState } from "react";
import {
  api,
  isTerminal,
  watchJob,
  type BackupSummary,
  type Job,
  type RestorePreview,
  type StorageLocation,
} from "../api";
import { describeError } from "../App";
import { Message } from "../components/Message";
import { bytes } from "../format";

/**
 * Backup and restore.
 *
 * The screen is built around restoring rather than around backing up, because
 * that is the half people need and the half that goes wrong. Backing up is one
 * button; restoring is a conversation.
 *
 * Nothing here lets somebody restore without first being shown what it would do.
 * The preview is not a confirmation dialogue — it is a page of facts, fetched
 * from the server, about a specific backup and this specific machine: how much
 * would be replaced, whether the backup is still intact, and which applications
 * come back.
 */

const REFRESH_MS = 10_000;

interface Props {
  canManage: boolean;
}

export function Backup({ canManage }: Props) {
  const [locations, setLocations] = useState<StorageLocation[]>([]);
  const [chosen, setChosen] = useState<string | null>(null);
  const [backups, setBackups] = useState<BackupSummary[] | null>(null);
  const [error, setError] = useState<ReturnType<typeof describeError> | null>(null);
  const [job, setJob] = useState<Job | null>(null);
  const [preview, setPreview] = useState<RestorePreview | null>(null);
  const [deleting, setDeleting] = useState<BackupSummary | null>(null);

  const stopWatching = useRef<(() => void) | null>(null);
  useEffect(() => () => stopWatching.current?.(), []);

  const refresh = useCallback(async () => {
    try {
      const list = await api.locations();
      setLocations(list.items);

      // Never chosen for the user. Storage taught us that lesson: "the obvious
      // disk" is how the wrong one gets picked. But with exactly one set up
      // there is no choice to make, so it is not made into one.
      const place = chosen ?? (list.items.length === 1 ? list.items[0]!.id : null);
      if (place !== chosen) setChosen(place);

      if (place) {
        setBackups((await api.backups(place)).items);
      }
      setError(null);
    } catch (caught) {
      setError(describeError(caught));
    }
  }, [chosen]);

  useEffect(() => {
    // eslint-disable-next-line react-hooks/set-state-in-effect
    void refresh();
    const timer = setInterval(() => void refresh(), REFRESH_MS);
    return () => clearInterval(timer);
  }, [refresh]);

  const run = useCallback(
    (start: () => Promise<Job>) => {
      setError(null);
      void (async () => {
        let submitted: Job;
        try {
          submitted = await start();
        } catch (caught) {
          setError(describeError(caught));
          return;
        }

        setJob(submitted);
        stopWatching.current?.();
        stopWatching.current = watchJob(
          submitted.job_id,
          (update) => {
            setJob(update);
            if (isTerminal(update.state)) void refresh();
          },
          (caught) => setError(describeError(caught)),
        );
      })();
    },
    [refresh],
  );

  const busy = job !== null && !isTerminal(job.state);
  const usable = locations.filter((location) => location.mounted);

  return (
    <>
      {error ? (
        <Message tone="error" title={error.title} detail={error.detail} recovery={error.recovery} />
      ) : null}

      {job ? <JobProgress job={job} /> : null}

      {usable.length === 0 ? (
        <Message
          tone="warning"
          title="There is nowhere to keep a backup yet."
          detail="A backup needs a second disk — a USB drive is fine."
          recovery="Set one up under Storage, then come back here."
        />
      ) : null}

      <section className="card">
        <h2>Make a backup</h2>
        <p className="muted">
          A backup is a second copy of your server, on a disk you can unplug. Keep it
          somewhere other than next to the server.
        </p>

        {usable.length > 1 ? (
          <>
            <label htmlFor="backup-disk">Which disk?</label>
            <div className="row">
              {usable.map((location) => (
                <button
                  key={location.id}
                  className={location.id === chosen ? "quiet tab-current" : "quiet"}
                  onClick={() => setChosen(location.id)}
                >
                  {location.name}
                </button>
              ))}
            </div>
          </>
        ) : null}

        {canManage && chosen ? (
          <div className="row">
            <button
              className="quiet"
              disabled={busy}
              onClick={() => run(() => api.createBackup(chosen, false))}
            >
              Settings only
            </button>
            <button
              className="primary"
              disabled={busy}
              onClick={() => run(() => api.createBackup(chosen, true))}
            >
              Everything
            </button>
          </div>
        ) : null}

        <p className="muted">
          <strong>Settings only</strong> takes a few seconds and saves your accounts and your
          applications&rsquo; settings. <strong>Everything</strong> also copies your files, and
          on a large collection can take hours.
        </p>
      </section>

      {chosen ? (
        <section className="card">
          <h2>Backups on this disk</h2>

          {backups && backups.length === 0 ? (
            <p className="muted">None yet.</p>
          ) : (
            <ul className="app-list">
              {backups?.map((backup) => (
                <BackupRow
                  key={backup.id}
                  backup={backup}
                  canManage={canManage}
                  busy={busy}
                  onVerify={() => run(() => api.verifyBackup(chosen, backup.id))}
                  onPreview={async () => {
                    setError(null);
                    try {
                      setPreview(await api.previewRestore(chosen, backup.id));
                    } catch (caught) {
                      setError(describeError(caught));
                    }
                  }}
                  onDelete={() => setDeleting(backup)}
                />
              ))}
            </ul>
          )}
        </section>
      ) : null}

      {preview ? (
        <RestoreDialogue
          preview={preview}
          onCancel={() => setPreview(null)}
          onConfirm={() => {
            const target = preview;
            setPreview(null);
            run(() => api.restoreBackup(target.location, target.id, target.id));
          }}
        />
      ) : null}

      {deleting && chosen ? (
        <DeleteDialogue
          backup={deleting}
          onCancel={() => setDeleting(null)}
          onConfirm={() => {
            const target = deleting;
            setDeleting(null);
            run(() => api.deleteBackup(chosen, target.id, target.id));
          }}
        />
      ) : null}
    </>
  );
}

// --- One backup -----------------------------------------------------------------

function BackupRow({
  backup,
  canManage,
  busy,
  onVerify,
  onPreview,
  onDelete,
}: {
  backup: BackupSummary;
  canManage: boolean;
  busy: boolean;
  onVerify: () => void;
  onPreview: () => void;
  onDelete: () => void;
}) {
  return (
    <li className="storage-row">
      <div className="row row-between">
        <div className="app-row-main">
          <span className="app-row-name">{when(backup.created_at)}</span>
          <span className="muted">
            {backup.kind === "full" ? "Settings and files" : "Settings only"}
            {backup.files ? ` · ${backup.files} files` : ""}
            {backup.total_bytes ? ` · ${bytes(backup.total_bytes)}` : ""}
            {backup.hostname ? ` · from ${backup.hostname}` : ""}
          </span>
        </div>
        {backup.complete ? null : <span className="badge badge-error">Not finished</span>}
      </div>

      {/* An interrupted backup is shown with what is wrong with it, rather than
          hidden — a folder that looks like a backup and is not is exactly what
          nobody must rely on. */}
      {!backup.complete ? (
        <Message
          tone="error"
          title="This backup was never finished."
          detail={backup.problem}
          recovery="It cannot be restored. Delete it and make a new one."
        />
      ) : null}

      {backup.notes && backup.notes.length > 0 ? (
        <details className="details">
          <summary>What this backup does and does not contain</summary>
          <ul className="muted">
            {backup.notes.map((note) => (
              <li key={note}>{note}</li>
            ))}
          </ul>
        </details>
      ) : null}

      {canManage ? (
        <div className="row">
          {backup.complete ? (
            <>
              <button className="quiet" disabled={busy} onClick={onVerify}>
                Check this backup
              </button>
              <button className="quiet" disabled={busy} onClick={onPreview}>
                See what restoring would do
              </button>
            </>
          ) : null}
          <button className="danger" disabled={busy} onClick={onDelete}>
            Delete
          </button>
        </div>
      ) : null}
    </li>
  );
}

function when(timestamp?: string): string {
  if (!timestamp) return "A backup";
  const parsed = new Date(timestamp);
  if (Number.isNaN(parsed.getTime())) return timestamp;
  return parsed.toLocaleString(undefined, {
    day: "numeric",
    month: "long",
    year: "numeric",
    hour: "2-digit",
    minute: "2-digit",
  });
}

// --- Restoring --------------------------------------------------------------------

/**
 * The preview, and only then the button.
 *
 * Everything here comes from the server having actually looked at this backup
 * and at this machine. It is not a warning dialogue with a generic sentence in
 * it — the numbers are real, and the one that matters is how much would be
 * replaced.
 */
function RestoreDialogue({
  preview,
  onCancel,
  onConfirm,
}: {
  preview: RestorePreview;
  onCancel: () => void;
  onConfirm: () => void;
}) {
  const [typed, setTyped] = useState("");
  const matches = typed.trim() === preview.id;

  return (
    <section className="card card-danger">
      <h3>Restore the backup from {when(preview.created_at)}?</h3>

      {!preview.verified ? (
        <Message
          tone="error"
          title="This backup is damaged."
          detail={
            preview.integrity_issues && preview.integrity_issues.length > 0
              ? `${preview.integrity_issues.length} files are missing or have changed.`
              : undefined
          }
          recovery="Restoring it will not bring everything back. If you have a more recent backup, use that one."
        />
      ) : null}

      <dl className="facts">
        <dt>Taken from</dt>
        <dd>{preview.hostname ?? "another server"}</dd>

        <dt>Files to restore</dt>
        <dd>
          {preview.files_to_write}
          {preview.bytes_to_write ? (
            <span className="muted"> — {bytes(preview.bytes_to_write)}</span>
          ) : null}
        </dd>

        {/* The number somebody actually needs before agreeing. */}
        <dt>Would be replaced</dt>
        <dd>
          {preview.would_overwrite === 0
            ? "Nothing on this server"
            : `${preview.would_overwrite} files on this server`}
        </dd>

        {preview.applications_to_install && preview.applications_to_install.length > 0 ? (
          <>
            <dt>Applications</dt>
            <dd>{preview.applications_to_install.join(", ")}</dd>
          </>
        ) : null}
      </dl>

      {preview.unavailable_applications && preview.unavailable_applications.length > 0 ? (
        <Message
          tone="warning"
          title="Some applications in this backup are not available on this server."
          detail={preview.unavailable_applications.join(", ")}
          recovery="Their files will be restored, but they cannot be installed again here."
        />
      ) : null}

      <p>
        <strong>Nothing else is deleted.</strong> Anything on this server that is not in the
        backup stays exactly as it is — restoring last month&rsquo;s backup does not remove
        what you have added since.
      </p>

      <p className="muted">
        Applications are not reinstalled automatically. Homebase will tell you which ones it
        can bring back, and you install them afterwards.
      </p>

      <label htmlFor="restore-confirm">
        Type <code>{preview.id}</code> to confirm
      </label>
      <input
        id="restore-confirm"
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
          Restore this backup
        </button>
      </div>
    </section>
  );
}

function DeleteDialogue({
  backup,
  onCancel,
  onConfirm,
}: {
  backup: BackupSummary;
  onCancel: () => void;
  onConfirm: () => void;
}) {
  const [typed, setTyped] = useState("");
  const matches = typed.trim() === backup.id;

  return (
    <section className="card card-danger">
      <h3>Delete the backup from {when(backup.created_at)}?</h3>
      <p>
        This backup will be permanently deleted from the disk. Your other backups are not
        affected, and nothing on this server changes.
      </p>

      <label htmlFor="delete-confirm">
        Type <code>{backup.id}</code> to confirm
      </label>
      <input
        id="delete-confirm"
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
          Delete it
        </button>
      </div>
    </section>
  );
}

function JobProgress({ job }: { job: Job }) {
  if (job.state === "failed") {
    return (
      <Message
        tone="error"
        title={job.error?.message ?? "That did not work."}
        detail={job.error?.detail}
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
        <div className="meter" role="img" aria-label={`${job.progress} per cent complete`}>
          <div className="meter-fill" style={{ width: `${job.progress}%` }} />
        </div>
      )}
    </div>
  );
}
