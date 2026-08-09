import { useCallback, useEffect, useRef, useState } from "react";
import {
  api,
  isTerminal,
  watchJob,
  type Disk,
  type Job,
  type StorageLocation,
  type Volume,
} from "../api";
import { describeError } from "../App";
import { Message } from "../components/Message";
import { bytes } from "../format";

/**
 * Storage.
 *
 * The screen where a mistake destroys data, so it is built around three
 * refusals rather than around what it can do:
 *
 *   - The disk holding the server is never offered for anything. It is shown,
 *     labelled, and has no buttons.
 *   - A disk Homebase could not read is never offered for erasing. "I could not
 *     look" and "there is nothing on it" are different answers, and only one of
 *     them is safe to act on.
 *   - Nothing is ever chosen for the user. There is no "prepare my disk" that
 *     picks one, even when only one is plausible.
 *
 * The vocabulary avoids "mount", "unmount", "filesystem" and "device". A person
 * with a USB drive in their hand is asking whether their films are on it, not
 * which block device it is.
 */

const REFRESH_MS = 5000;

interface Props {
  canManage: boolean;
}

export function Storage({ canManage }: Props) {
  const [disks, setDisks] = useState<Disk[] | null>(null);
  const [locations, setLocations] = useState<StorageLocation[] | null>(null);
  const [error, setError] = useState<ReturnType<typeof describeError> | null>(null);
  const [job, setJob] = useState<Job | null>(null);

  const stopWatching = useRef<(() => void) | null>(null);
  useEffect(() => () => stopWatching.current?.(), []);

  const refresh = useCallback(async () => {
    try {
      const [diskList, locationList] = await Promise.all([api.disks(), api.locations()]);
      setDisks(diskList.items);
      setLocations(locationList.items);
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

  const busy = job !== null && !isTerminal(job.state);

  if (!disks && !error) {
    return <p className="muted">Looking at the disks…</p>;
  }

  // A volume is already set up if a location points at its UUID.
  const managed = new Set((locations ?? []).map((location) => location.uuid));

  return (
    <>
      {error ? (
        <Message tone="error" title={error.title} detail={error.detail} recovery={error.recovery} />
      ) : null}

      {job ? <JobProgress job={job} /> : null}

      <section className="card">
        <h2>Your storage</h2>
        {locations && locations.length === 0 ? (
          <p className="muted">
            Nothing set up yet. Plug in a disk and Homebase will offer it below.
          </p>
        ) : (
          <ul className="app-list">
            {locations?.map((location) => (
              <LocationRow
                key={location.id}
                location={location}
                canManage={canManage}
                busy={busy}
                onRun={run}
              />
            ))}
          </ul>
        )}
      </section>

      <section className="card">
        <h2>Disks in this server</h2>
        <p className="muted">
          Everything Homebase can see. The disk the server itself runs from is shown but
          cannot be changed.
        </p>

        <ul className="app-list">
          {disks?.map((disk) => (
            <DiskRow
              key={disk.device}
              disk={disk}
              managed={managed}
              canManage={canManage}
              busy={busy}
              onRun={run}
            />
          ))}
        </ul>
      </section>
    </>
  );
}

// --- A location Homebase manages ----------------------------------------------

function LocationRow({
  location,
  canManage,
  busy,
  onRun,
}: {
  location: StorageLocation;
  canManage: boolean;
  busy: boolean;
  onRun: (start: () => Promise<Job>) => void;
}) {
  const [confirming, setConfirming] = useState<null | "remove" | "unmount">(null);

  return (
    <li className="storage-row">
      <div className="row row-between">
        <div className="app-row-main">
          <span className="app-row-name">{location.name}</span>
          {location.mounted && location.total_bytes ? (
            <span className="muted">
              {bytes(location.available_bytes ?? 0)} free of {bytes(location.total_bytes)}
            </span>
          ) : null}
        </div>
        <LocationBadge location={location} />
      </div>

      {location.mounted && location.total_bytes ? (
        <div
          className="meter"
          role="img"
          aria-label={`${used(location)} per cent of ${location.name} is in use`}
        >
          <div className="meter-fill" style={{ width: `${used(location)}%` }} />
        </div>
      ) : null}

      {/* A disconnected disk is the ordinary case, not an error. It says what to
          do, and does not shout. */}
      {!location.connected ? (
        <p className="muted">
          Plug this disk back in and Homebase will pick it up on its own. Anything using it
          will start working again.
        </p>
      ) : null}

      {canManage ? (
        <div className="row">
          {location.connected && !location.mounted ? (
            <button
              className="quiet"
              disabled={busy}
              onClick={() => onRun(() => api.mountLocation(location.id))}
            >
              Open it
            </button>
          ) : null}
          {location.mounted ? (
            <button className="quiet" disabled={busy} onClick={() => setConfirming("unmount")}>
              Prepare to unplug
            </button>
          ) : null}
          <button className="danger" disabled={busy} onClick={() => setConfirming("remove")}>
            Stop using it
          </button>
        </div>
      ) : null}

      {confirming === "unmount" ? (
        <Confirm
          title={`Prepare ${location.name} to be unplugged?`}
          body={
            "Homebase will finish writing anything outstanding, then tell you when it is " +
            "safe to pull the disk out. Nothing is deleted. Anything using this disk has " +
            "to be stopped first."
          }
          confirmLabel="Get it ready"
          expected={location.id}
          onCancel={() => setConfirming(null)}
          onConfirm={() => {
            setConfirming(null);
            onRun(() => api.unmountLocation(location.id, location.id));
          }}
        />
      ) : null}

      {confirming === "remove" ? (
        <Confirm
          title={`Stop using ${location.name}?`}
          body={
            `Homebase will stop using this disk. **Everything on it is left exactly as it ` +
            `is** — nothing is deleted, and you can plug it into another computer and find ` +
            `your files. Applications keeping their files here will stop working until you ` +
            `give them somewhere else.`
          }
          confirmLabel="Stop using it"
          expected={location.id}
          onCancel={() => setConfirming(null)}
          onConfirm={() => {
            setConfirming(null);
            onRun(() => api.removeLocation(location.id, location.id));
          }}
        />
      ) : null}
    </li>
  );
}

function used(location: StorageLocation): number {
  if (!location.total_bytes) return 0;
  const usedBytes = location.total_bytes - (location.available_bytes ?? 0);
  return Math.min(100, Math.max(0, Math.round((usedBytes / location.total_bytes) * 100)));
}

function LocationBadge({ location }: { location: StorageLocation }) {
  if (!location.connected) {
    return <span className="badge badge-warning">Not connected</span>;
  }
  if (!location.mounted) {
    // Present but unusable. A different problem from being unplugged, with a
    // different remedy, so it gets different words.
    return <span className="badge badge-error">Connected, but not working</span>;
  }
  if (location.read_only) {
    return <span className="badge badge-warning">Read only</span>;
  }
  return <span className="badge badge-ok">Ready</span>;
}

// --- A disk in the machine ------------------------------------------------------

function DiskRow({
  disk,
  managed,
  canManage,
  busy,
  onRun,
}: {
  disk: Disk;
  managed: Set<string>;
  canManage: boolean;
  busy: boolean;
  onRun: (start: () => Promise<Job>) => void;
}) {
  const [setUp, setSetUp] = useState<Volume | null>(null);
  const [erasing, setErasing] = useState<Volume | null>(null);

  const description = [disk.model, disk.transport === "usb" ? "USB" : null]
    .filter(Boolean)
    .join(" · ");

  return (
    <li className="storage-row">
      <div className="row row-between">
        <div className="app-row-main">
          <span className="app-row-name">{disk.model || disk.device}</span>
          <span className="muted">
            {bytes(disk.size_bytes)}
            {description ? ` · ${description}` : ""}
          </span>
        </div>
        {disk.system ? (
          <span className="badge badge-quiet">The server itself</span>
        ) : null}
      </div>

      {/* Shown, labelled, and given no buttons at all. Homebase will not erase
          the disk it is running from, and the interface does not offer to. */}
      {disk.system ? (
        <p className="muted">
          Homebase runs from this disk, so it cannot be changed here.
        </p>
      ) : (
        <ul className="volume-list">
          {disk.volumes.map((volume) => (
            <li key={volume.device} className="row row-between">
              <span className="muted">
                {volume.label || describeVolume(volume)} · {bytes(volume.size_bytes)}
              </span>

              {canManage && !managed.has(volume.uuid ?? "") ? (
                <span className="row">
                  {volume.uuid && volume.filesystem ? (
                    <button className="quiet" disabled={busy} onClick={() => setSetUp(volume)}>
                      Use this
                    </button>
                  ) : null}
                  {/* Never offered for a volume Homebase could not read. */}
                  {!volume.unreadable ? (
                    <button className="danger" disabled={busy} onClick={() => setErasing(volume)}>
                      Erase and prepare
                    </button>
                  ) : null}
                </span>
              ) : null}
              {managed.has(volume.uuid ?? "") ? (
                <span className="badge badge-ok">In use by Homebase</span>
              ) : null}
            </li>
          ))}
        </ul>
      )}

      {setUp ? (
        <SetUpForm
          volume={setUp}
          onCancel={() => setSetUp(null)}
          onConfirm={(id, name) => {
            setSetUp(null);
            onRun(() => api.addLocation(setUp.uuid ?? "", id, name));
          }}
        />
      ) : null}

      {erasing ? (
        <EraseForm
          volume={erasing}
          onCancel={() => setErasing(null)}
          onConfirm={(label, confirm) => {
            setErasing(null);
            const target = erasing.uuid ? { uuid: erasing.uuid } : { device: erasing.path };
            onRun(() => api.formatDisk(target, label, confirm));
          }}
        />
      ) : null}
    </li>
  );
}

/**
 * What is on a volume, in words.
 *
 * "Could not be read" is deliberately not "empty". The difference decides
 * whether somebody is about to erase a blank disk or a failing one with their
 * photographs on it.
 */
function describeVolume(volume: Volume): string {
  if (volume.unreadable) return "Homebase could not read this";
  if (!volume.filesystem) return "Empty";
  if (volume.filesystem === "crypto_LUKS") return "Encrypted — Homebase cannot open this";
  if (!volume.uuid) return `${volume.filesystem}, with no identifier`;
  return volume.filesystem;
}

// --- Forms ----------------------------------------------------------------------

function SetUpForm({
  volume,
  onCancel,
  onConfirm,
}: {
  volume: Volume;
  onCancel: () => void;
  onConfirm: (id: string, name: string) => void;
}) {
  const [name, setName] = useState(volume.label || "");
  const id = slug(name);
  const valid = /^[a-z][a-z0-9-]{0,30}[a-z0-9]$/.test(id);

  return (
    <section className="card card-quiet">
      <h3>Use this disk</h3>
      <p>
        Homebase will keep this disk available, and reconnect it automatically every time
        the server starts. Nothing on it is changed.
      </p>

      <label htmlFor="location-name">What would you like to call it?</label>
      <input
        id="location-name"
        autoFocus
        autoComplete="off"
        placeholder="Films drive"
        value={name}
        onChange={(e) => setName(e.target.value)}
      />
      {name && !valid ? (
        <p className="hint hint-warning">
          Please use a couple of letters or more — letters, numbers and spaces.
        </p>
      ) : null}

      <div className="row">
        <button className="quiet" onClick={onCancel}>
          Cancel
        </button>
        <button className="primary" disabled={!valid} onClick={() => onConfirm(id, name)}>
          Use this disk
        </button>
      </div>
    </section>
  );
}

/**
 * Erasing a disk.
 *
 * The confirmation is the disk's own identifier, typed. Not a word like "yes":
 * this is the one action in Homebase that can destroy data Homebase never
 * created, and a confirmation somebody can satisfy by reflex is not one.
 */
function EraseForm({
  volume,
  onCancel,
  onConfirm,
}: {
  volume: Volume;
  onCancel: () => void;
  onConfirm: (label: string, confirm: string) => void;
}) {
  const [label, setLabel] = useState("Homebase");
  const [typed, setTyped] = useState("");

  const identifier = volume.uuid || volume.path;
  const matches = typed.trim() === identifier;
  const hasContents = Boolean(volume.filesystem);

  return (
    <section className="card card-danger">
      <h3>Erase this disk?</h3>

      {hasContents ? (
        <Message
          tone="error"
          title="There is already something on this disk."
          detail={`It holds a ${volume.filesystem} disk${volume.label ? ` called ${volume.label}` : ""}.`}
          recovery="Everything on it will be permanently deleted. If you are not certain what is on it, check on another computer first."
        />
      ) : (
        <p>Homebase found nothing on this disk. It will be prepared for use.</p>
      )}

      <p>
        <strong>This cannot be undone, and no backup is taken first.</strong>
      </p>

      <label htmlFor="disk-label">What would you like to call it?</label>
      <input
        id="disk-label"
        autoComplete="off"
        maxLength={16}
        value={label}
        onChange={(e) => setLabel(e.target.value)}
      />

      <label htmlFor="erase-confirm">
        Type <code>{identifier}</code> to confirm
      </label>
      <input
        id="erase-confirm"
        autoComplete="off"
        value={typed}
        onChange={(e) => setTyped(e.target.value)}
      />

      <div className="row">
        <button className="quiet" onClick={onCancel}>
          Cancel
        </button>
        <button className="danger" disabled={!matches} onClick={() => onConfirm(label, identifier)}>
          Erase it permanently
        </button>
      </div>
    </section>
  );
}

function Confirm({
  title,
  body,
  confirmLabel,
  expected,
  onCancel,
  onConfirm,
}: {
  title: string;
  body: string;
  confirmLabel: string;
  expected: string;
  onCancel: () => void;
  onConfirm: () => void;
}) {
  const [typed, setTyped] = useState("");
  const matches = typed.trim() === expected;

  return (
    <section className="card card-quiet">
      <h3>{title}</h3>
      <p>{body.replace(/\*\*/g, "")}</p>

      <label htmlFor="storage-confirm">
        Type <code>{expected}</code> to confirm
      </label>
      <input
        id="storage-confirm"
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

/** A name a person typed, turned into an id the API accepts. */
function slug(name: string): string {
  return name
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/^-+|-+$/g, "")
    .slice(0, 32);
}
