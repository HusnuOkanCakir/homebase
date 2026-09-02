import { useCallback, useEffect, useState } from "react";
import { api, type Account, type RemoteFolder } from "../api";
import { describeError } from "../App";
import { Message } from "../components/Message";

/**
 * A disk that is plugged into somebody else's computer.
 *
 * The thing this is for, in the words it was asked in: a disk is in a drawer, a
 * person at home plugs it into their own laptop, and a person in another
 * country needs a file off it. Copying it to the server first does not answer
 * that, because nobody knows in advance which files are wanted.
 *
 * What keeps it small is that the laptop needs no Homebase software at all.
 * Windows has shared folders for thirty years; what was missing was this server
 * being able to *open* one. So the screen asks for the three things Windows
 * already knows — the computer, the share name, and an account on it — and the
 * folder then appears in Files like any other, reachable over Tailscale from
 * anywhere.
 */
export function OtherComputers({ canConnect }: { canConnect: boolean }) {
  const [folders, setFolders] = useState<RemoteFolder[] | null>(null);
  const [accounts, setAccounts] = useState<Account[]>([]);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<ReturnType<typeof describeError> | null>(null);
  const [adding, setAdding] = useState(false);
  const [removing, setRemoving] = useState<string | null>(null);

  const [name, setName] = useState("");
  const [host, setHost] = useState("");
  const [share, setShare] = useState("");
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");

  // Bumped by anything that changes what is connected, so that reading the list
  // happens in one place — the effect below, which knows how to abandon a read
  // that has been overtaken.
  const [reload, setReload] = useState(0);
  const refresh = useCallback(() => setReload((n) => n + 1), []);

  useEffect(() => {
    let current = true;
    void (async () => {
      try {
        const result = await api.remoteFolders();
        if (current) {
          setFolders(result.folders);
          setError(null);
        }
      } catch (cause) {
        if (current) setError(describeError(cause));
      }
    })();
    return () => {
      current = false;
    };
  }, [reload]);

  useEffect(() => {
    if (!canConnect) return;
    void api
      .accounts()
      .then((result) => setAccounts(result.accounts))
      .catch(() => setAccounts([]));
  }, [canConnect]);

  const run = async (action: () => Promise<unknown>) => {
    setBusy(true);
    setError(null);
    try {
      await action();
      refresh();
    } catch (cause) {
      setError(describeError(cause));
    } finally {
      setBusy(false);
    }
  };

  // Nothing connected and nothing to connect it with: a card explaining a
  // feature nobody is using is a card in the way.
  if (folders !== null && folders.length === 0 && !canConnect) return null;

  return (
    <section className="card">
      <h3>Folders on other computers</h3>
      <p className="muted">
        A disk plugged into somebody's own computer at home can be opened here, and
        then read from anywhere — including from away, over the tunnel. The
        computer it is plugged into needs nothing installed: it shares the folder
        the way Windows already does.
      </p>

      {error && <Message tone="error" {...error} />}

      {folders === null ? (
        <p className="muted">Checking…</p>
      ) : folders.length === 0 ? (
        <p className="muted">Nothing from another computer is open here.</p>
      ) : (
        <ul className="list">
          {folders.map((folder) => (
            <li key={folder.name}>
              <div className="row row-spread">
                <div>
                  <strong>{folder.name}</strong>
                  <div className="muted">
                    {folder.share} on {folder.host}
                    {folder.connected
                      ? " · open"
                      : " · that computer is not answering"}
                  </div>
                </div>
                <div className="row">
                  {!folder.connected && canConnect ? (
                    <button
                      className="quiet"
                      disabled={busy}
                      onClick={() => void run(() => api.reconnectRemoteFolder(folder.name))}
                    >
                      Try again
                    </button>
                  ) : null}
                  {canConnect ? (
                    <button
                      className="quiet"
                      disabled={busy}
                      onClick={() => setRemoving(folder.name)}
                    >
                      Disconnect
                    </button>
                  ) : null}
                </div>
              </div>

              {/* Said before it happens, and the reassuring half said too:
                  people hesitate over this button because it sounds like it
                  reaches into the other computer. It cannot. */}
              {removing === folder.name ? (
                <>
                  <Message
                    tone="warning"
                    title={`Stop opening ${folder.name}?`}
                    recovery="Nothing on the other computer is touched — Homebase opened it read-only and could not change it even if something here tried. Connecting it again needs the password for that computer."
                  />
                  <div className="row">
                    <button
                      className="danger"
                      disabled={busy}
                      onClick={() => {
                        setRemoving(null);
                        void run(() => api.disconnectRemoteFolder(folder.name));
                      }}
                    >
                      Disconnect it
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

      {canConnect ? (
        <details className="details" open={adding} onToggle={(e) => setAdding(e.currentTarget.open)}>
          <summary>Open a folder from another computer</summary>

          <p className="hint">
            On the computer with the disk: right-click the drive or folder,
            Properties, Sharing, and share it. Note the <strong>share name</strong>{" "}
            it gives you — that is not the drive letter — and make sure the computer
            is set to stay awake while somebody is reading from it.
          </p>

          <form
            className="stack"
            onSubmit={(event) => {
              event.preventDefault();
              void run(async () => {
                await api.connectRemoteFolder({
                  name: name.trim(),
                  host: host.trim(),
                  share: share.trim(),
                  username: username.trim(),
                  password,
                  access: [],
                });
                setName("");
                setHost("");
                setShare("");
                setUsername("");
                setPassword("");
                setAdding(false);
              });
            }}
          >
            <label htmlFor="remote-name">
              Call it
              <span className="muted"> — the name it appears under in Files.</span>
            </label>
            <input
              id="remote-name"
              value={name}
              onChange={(event) => setName(event.target.value)}
              placeholder="dads-disk"
              autoComplete="off"
            />

            <label htmlFor="remote-host">
              The computer
              <span className="muted"> — its name on the network, or its address.</span>
            </label>
            <input
              id="remote-host"
              value={host}
              onChange={(event) => setHost(event.target.value)}
              placeholder="dads-laptop"
              autoComplete="off"
            />

            <label htmlFor="remote-share">
              Shared as
              <span className="muted"> — the share name, not the drive letter.</span>
            </label>
            <input
              id="remote-share"
              value={share}
              onChange={(event) => setShare(event.target.value)}
              placeholder="sandisk"
              autoComplete="off"
            />

            <label htmlFor="remote-user">
              An account on that computer
              <span className="muted"> — the one that may open the folder.</span>
            </label>
            <input
              id="remote-user"
              value={username}
              onChange={(event) => setUsername(event.target.value)}
              autoComplete="off"
            />

            <label htmlFor="remote-password">Its password</label>
            <input
              id="remote-password"
              type="password"
              value={password}
              onChange={(event) => setPassword(event.target.value)}
              autoComplete="new-password"
            />

            <button
              className="primary"
              disabled={busy || !name.trim() || !host.trim() || !share.trim()}
            >
              {busy ? "Opening it…" : "Open it"}
            </button>

            <p className="hint">
              Homebase opens it <strong>read-only</strong>. Nothing here can change
              or delete anything on that computer, which is the point: the disk
              belongs to whoever plugged it in.
            </p>
            <p className="hint">
              The password is kept on this server so the folder can be opened again
              after a restart. It is stored where only the server itself can read
              it, and it is never shown again — but it is a password for somebody
              else's computer, so use an account on it that has no more access than
              this needs.
            </p>
            {accounts.length > 1 ? (
              <p className="hint">
                Everybody with an account here will be able to read it.
              </p>
            ) : null}
          </form>
        </details>
      ) : null}
    </section>
  );
}
