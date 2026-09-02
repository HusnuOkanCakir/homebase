import { useEffect, useRef, useState } from "react";
import {
  api,
  type FileArea,
  type FileEntry,
  type FileListing,
} from "../api";
import { describeError } from "../App";
import { Message } from "../components/Message";

/**
 * The files on this server, in the browser.
 *
 * The reason this exists rather than pointing people at a mapped drive: a
 * mapped drive needs a computer that can map one. A phone cannot, a borrowed
 * laptop should not, and somebody's father should not have to be talked through
 * Explorer over the telephone. This works anywhere the dashboard works, which
 * includes over Tailscale from another country.
 *
 * Arranged around one question — what is in here, and how do I get it — so the
 * listing is the screen and everything else is a button beside it.
 */
export function FileBrowser() {
  const [areas, setAreas] = useState<FileArea[] | null>(null);
  const [area, setArea] = useState<FileArea | null>(null);
  const [path, setPath] = useState("");
  const [listing, setListing] = useState<FileListing | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<ReturnType<typeof describeError> | null>(null);
  const [renaming, setRenaming] = useState<FileEntry | null>(null);
  const [newName, setNewName] = useState("");
  const [removing, setRemoving] = useState<FileEntry | null>(null);
  const [confirmName, setConfirmName] = useState("");
  const [makingFolder, setMakingFolder] = useState(false);
  const [folderName, setFolderName] = useState("");
  const chooser = useRef<HTMLInputElement>(null);

  useEffect(() => {
    void api
      .fileAreas()
      .then((result) => {
        setAreas(result.areas);
        setArea((current) => current ?? result.areas[0] ?? null);
      })
      .catch((cause) => setError(describeError(cause)));
  }, []);

  // Bumped by anything that changes files, to ask for the folder again. A
  // number rather than a function call, so that reading a folder happens in one
  // place: the effect below, which knows how to abandon a read that has been
  // overtaken.
  const [reload, setReload] = useState(0);

  useEffect(() => {
    if (!area) return;

    // Abandoned if the person has moved on. Clicking through three folders
    // quickly starts three reads, and without this the slowest one wins and the
    // screen shows a folder nobody is looking at.
    let current = true;
    void (async () => {
      try {
        const result = await api.files(area.id, path);
        if (current) {
          setListing(result);
          setError(null);
        }
      } catch (cause) {
        if (current) setError(describeError(cause));
      }
    })();
    return () => {
      current = false;
    };
  }, [area, path, reload]);

  /** Runs something that changes files, then shows the folder as it now is. */
  const run = async (action: () => Promise<unknown>) => {
    if (!area) return;
    setBusy(true);
    setError(null);
    try {
      await action();
      setReload((n) => n + 1);
    } catch (cause) {
      setError(describeError(cause));
    } finally {
      setBusy(false);
    }
  };

  if (areas === null) {
    return (
      <section className="card">
        <h2>Files</h2>
        <p className="muted">Reading your folders…</p>
      </section>
    );
  }

  // Nothing to show is a real state and a common one: a server with no shared
  // folder yet, and an account whose owner has not signed in since private
  // folders existed.
  if (areas.length === 0) {
    return (
      <section className="card">
        <h2>Files</h2>
        <p className="muted">
          There is nothing here to open yet. A shared folder appears here once
          somebody makes one, and your own folder appears the first time you
          sign in after it is created.
        </p>
      </section>
    );
  }

  const writable = area !== null && !area.read_only;
  const crumbs = path === "" ? [] : path.split("/").filter(Boolean);

  return (
    <section className="card">
      <h2>Files</h2>

      {error && <Message tone="error" {...error} />}

      {/* Only when there is a choice to make. With one area the buttons say the
          same thing as the line below them, and two identical labels one above
          the other read as a mistake rather than as navigation. */}
      {areas.length > 1 ? (
        <div className="row file-areas">
          {areas.map((one) => (
            <button
              key={one.id}
              className={one.id === area?.id ? "" : "quiet"}
              onClick={() => {
                setArea(one);
                setPath("");
              }}
            >
              {one.name}
              {one.read_only ? <span className="muted"> · read only</span> : null}
            </button>
          ))}
        </div>
      ) : null}

      {/* Where you are, and every step back to the top. A file browser without
          this is one where the only way out of a folder is the back button of
          the browser, which also signs you out of the screen you were on. */}
      <div className="row file-crumbs">
        <button className="quiet" onClick={() => setPath("")} disabled={path === ""}>
          {area?.name ?? "Top"}
          {area?.read_only ? <span className="muted"> · read only</span> : null}
        </button>
        {crumbs.map((crumb, index) => (
          <span key={index}>
            <span className="muted"> / </span>
            <button
              className="quiet"
              disabled={index === crumbs.length - 1}
              onClick={() => setPath(crumbs.slice(0, index + 1).join("/"))}
            >
              {crumb}
            </button>
          </span>
        ))}
      </div>

      {writable ? (
        <div className="row">
          <button disabled={busy} onClick={() => chooser.current?.click()}>
            Add files
          </button>
          <button
            className="quiet"
            disabled={busy}
            onClick={() => {
              setMakingFolder(!makingFolder);
              setFolderName("");
            }}
          >
            New folder
          </button>
          <input
            ref={chooser}
            type="file"
            multiple
            hidden
            onChange={(event) => {
              const chosen = Array.from(event.target.files ?? []);
              event.target.value = "";
              if (chosen.length > 0) {
                void run(() => api.uploadFiles(area.id, path, chosen));
              }
            }}
          />
        </div>
      ) : null}

      {makingFolder && writable ? (
        <form
          className="row"
          onSubmit={(event) => {
            event.preventDefault();
            const name = folderName.trim();
            if (!name) return;
            setMakingFolder(false);
            setFolderName("");
            void run(() => api.createFolder(area.id, path, name));
          }}
        >
          <input
            value={folderName}
            onChange={(event) => setFolderName(event.target.value)}
            placeholder="Holiday photographs"
            autoFocus
          />
          <button disabled={busy || folderName.trim() === ""}>Make it</button>
        </form>
      ) : null}

      {busy ? <p className="muted">Working…</p> : null}

      {listing === null ? (
        <p className="muted">Reading…</p>
      ) : listing.entries.length === 0 ? (
        <p className="muted">This folder is empty.</p>
      ) : (
        <ul className="list file-list">
          {[...listing.entries]
            // Folders first. It is what every file browser does, and the reason
            // is that a folder is a place to go and a file is a thing to take.
            .sort((a, b) =>
              a.directory === b.directory
                ? a.name.localeCompare(b.name)
                : a.directory
                  ? -1
                  : 1,
            )
            .map((entry) => (
              <li key={entry.path}>
                <div className="row row-spread">
                  <div className="file-name">
                    {entry.directory ? (
                      <button className="quiet" onClick={() => setPath(entry.path)}>
                        📁 {entry.name}
                      </button>
                    ) : (
                      <a
                        href={api.fileContentUrl(area!.id, entry.path)}
                        download={entry.name}
                      >
                        {entry.name}
                      </a>
                    )}
                    {!entry.directory ? (
                      <span className="muted"> {describeSize(entry.size)}</span>
                    ) : null}
                  </div>
                  {writable ? (
                    <div className="row">
                      <button
                        className="quiet"
                        disabled={busy}
                        onClick={() => {
                          setRenaming(entry);
                          setNewName(entry.name);
                          setRemoving(null);
                        }}
                      >
                        Rename
                      </button>
                      <button
                        className="quiet"
                        disabled={busy}
                        onClick={() => {
                          setRemoving(entry);
                          setConfirmName("");
                          setRenaming(null);
                        }}
                      >
                        Delete
                      </button>
                    </div>
                  ) : null}
                </div>

                {renaming?.path === entry.path ? (
                  <form
                    className="row"
                    onSubmit={(event) => {
                      event.preventDefault();
                      const name = newName.trim();
                      if (!name) return;
                      setRenaming(null);
                      void run(() => api.renameFile(area!.id, entry.path, name));
                    }}
                  >
                    <input
                      value={newName}
                      onChange={(event) => setNewName(event.target.value)}
                      autoFocus
                    />
                    <button disabled={busy || newName.trim() === ""}>Rename it</button>
                    <button type="button" className="quiet" onClick={() => setRenaming(null)}>
                      Cancel
                    </button>
                  </form>
                ) : null}

                {removing?.path === entry.path ? (
                  <>
                    {/* Said before it happens, because it cannot be undone.
                        Homebase has no wastebasket: this is the last screen
                        between somebody and the only copy of a photograph. */}
                    <Message
                      tone="warning"
                      title={`Delete ${entry.name}?`}
                      recovery={
                        entry.directory
                          ? "Everything inside it goes too. There is no wastebasket to take it back out of, on this server or on any computer that opened it."
                          : "There is no wastebasket to take it back out of."
                      }
                    />
                    {entry.directory ? (
                      <div className="row">
                        <input
                          value={confirmName}
                          onChange={(event) => setConfirmName(event.target.value)}
                          placeholder={entry.name}
                          autoFocus
                        />
                        <span className="muted">Type the folder's name</span>
                      </div>
                    ) : null}
                    <div className="row">
                      <button
                        className="danger"
                        disabled={
                          busy || (entry.directory && confirmName !== entry.name)
                        }
                        onClick={() => {
                          setRemoving(null);
                          void run(() =>
                            api.removeFile(
                              area!.id,
                              entry.path,
                              entry.directory ? entry.name : undefined,
                            ),
                          );
                        }}
                      >
                        Delete it
                      </button>
                      <button className="quiet" onClick={() => setRemoving(null)}>
                        Keep it
                      </button>
                    </div>
                  </>
                ) : null}
              </li>
            ))}
        </ul>
      )}

      {listing?.truncated ? (
        <p className="hint">
          This folder holds more than Homebase will list at once. Everything is
          still there and still reachable from a mapped drive.
        </p>
      ) : null}

      {area?.kind === "plugged" ? (
        <p className="hint">
          This is a disk somebody plugged into the server. Homebase can only read
          it — nothing here can change what is on it. Press <strong>Finish with
          it</strong> below before unplugging it.
        </p>
      ) : null}

      {area?.kind === "personal" ? (
        <p className="hint">
          This folder is yours. The other people on this server cannot open it,
          and neither can the applications running on it.
        </p>
      ) : null}
    </section>
  );
}

/** A size somebody reads rather than counts. */
function describeSize(bytes: number): string {
  if (bytes < 1000) return `${bytes} bytes`;
  const units = ["kB", "MB", "GB", "TB"];
  let value = bytes / 1000;
  let unit = 0;
  while (value >= 1000 && unit < units.length - 1) {
    value /= 1000;
    unit += 1;
  }
  return `${value < 10 ? value.toFixed(1) : Math.round(value)} ${units[unit]}`;
}
