import { useCallback, useEffect, useState } from "react";
import { api, type SystemInfo } from "../api";
import { RenameServer } from "../components/RenameServer";

/**
 * What to do next, on a server that has just been claimed.
 *
 * A checklist rather than a wizard, and the difference matters. A wizard has to
 * be finished, so it either blocks somebody who has not bought a USB disk yet or
 * teaches them that skipping is normal. A list they can come back to says what
 * is worth doing and why, and stops mentioning it once it is done.
 *
 * It disappears when everything on it is done, because a permanent "getting
 * started" panel on a server somebody has run for a year is clutter that says
 * the software is not paying attention.
 */

interface Props {
  system: SystemInfo;
  onGo: (where: "storage" | "apps") => void;
}

interface Progress {
  named: boolean;
  hasStorage: boolean;
  hasBackup: boolean;
  hasApplication: boolean;
}

/** The name the installer gives every machine, until somebody chooses one. */
const DEFAULT_NAME = "homebase";

export function FirstSteps({ system, onGo }: Props) {
  const [progress, setProgress] = useState<Progress | null>(null);
  const [dismissed, setDismissed] = useState(
    () => window.localStorage.getItem("homebase.first-steps") === "dismissed",
  );

  const refresh = useCallback(async () => {
    // Each of these is a question the server can already answer. Nothing is
    // remembered about "having done" a step: the state *is* the answer, so a
    // disk that gets removed makes the step reappear, which is correct.
    const [locations, applications] = await Promise.all([
      api.locations().catch(() => ({ items: [] })),
      api.apps().catch(() => ({ items: [] })),
    ]);

    // Backups are kept per disk, so "is there a backup" means asking each one.
    // A disk that has gone away answers with a failure rather than an empty
    // list, and that is not the same as having no backups — so it is ignored
    // rather than counted as a no.
    const counts = await Promise.all(
      locations.items.map((location) =>
        api
          .backups(location.id)
          .then((list) => list.items.length)
          .catch(() => 0),
      ),
    );

    setProgress({
      named: system.hostname !== DEFAULT_NAME,
      hasStorage: locations.items.length > 0,
      hasBackup: counts.some((count) => count > 0),
      hasApplication: applications.items.some((app) => app.state === "running"),
    });
  }, [system.hostname]);

  useEffect(() => {
    // eslint-disable-next-line react-hooks/set-state-in-effect
    void refresh();
  }, [refresh]);

  if (dismissed || progress === null) return null;

  const steps = [
    {
      done: progress.named,
      title: "Give your server a name",
      why: "It is called “homebase” at the moment. A name of your own makes it easier to find, and easier to tell apart from anything else you add later.",
      // Nothing is refreshed when this succeeds: refreshing marks the step done,
      // which removes the step, which unmounts the thing holding the "your
      // server is now called…" confirmation. The list corrects itself the next
      // time it is opened, which is soon enough for a step that is finished.
      action: <RenameServer id="first-steps-name" current={system.hostname} />,
    },
    {
      done: progress.hasStorage,
      title: "Set up a disk for your files",
      why: "The disk the server runs from is small and holds the system. Photographs and films belong on a disk of their own.",
      action: (
        <button className="quiet" onClick={() => onGo("storage")}>
          Go to Storage
        </button>
      ),
    },
    {
      done: progress.hasApplication,
      title: "Install something",
      why: "An application is what makes the server useful — somewhere to keep files, or to watch what is on it.",
      action: (
        <button className="quiet" onClick={() => onGo("apps")}>
          Go to Apps
        </button>
      ),
    },
    {
      done: progress.hasBackup,
      title: "Make a backup",
      why: "Nothing here is backed up until you do this. A second disk you can unplug is what stands between a failed drive and losing everything on it.",
      action: (
        <button className="quiet" onClick={() => onGo("storage")}>
          Go to Storage
        </button>
      ),
    },
  ];

  const remaining = steps.filter((step) => !step.done);
  if (remaining.length === 0) return null;

  return (
    <section className="card">
      <h2>Getting started</h2>
      <p className="muted">
        {steps.length - remaining.length} of {steps.length} done. There is no hurry, and
        nothing here has to be done in order.
      </p>

      <ol className="steps">
        {steps.map((step) => (
          <li key={step.title} className={step.done ? "step step-done" : "step"}>
            <div className="step-title">
              <span aria-hidden="true">{step.done ? "✓" : "○"}</span>
              <span>{step.title}</span>
              {step.done ? <span className="visually-hidden"> — done</span> : null}
            </div>
            {step.done ? null : (
              <>
                <p className="muted">{step.why}</p>
                {step.action}
              </>
            )}
          </li>
        ))}
      </ol>

      {/*
        Hidden rather than finished. Somebody who does not want a backup should
        not be nagged for ever, and somebody who dismisses this by accident has
        lost nothing they cannot find again under the sections themselves.
      */}
      <button
        className="quiet"
        onClick={() => {
          window.localStorage.setItem("homebase.first-steps", "dismissed");
          setDismissed(true);
        }}
      >
        Hide this
      </button>
    </section>
  );
}
