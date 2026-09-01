import { useCallback, useEffect, useState } from "react";
import {
  api,
  type Account,
  type NetworkStatus,
  type ShareStatus,
  type SharedFolder,
  type StorageLocation,
} from "../api";
import { describeError } from "../App";
import { Message } from "../components/Message";

/**
 * Getting at this server's files from the computer you are sitting at.
 *
 * The whole screen is arranged around one sentence somebody has to type
 * somewhere else, so that sentence is the largest thing on it. Everything a
 * person needs from file sharing is: what do I type, and who do I sign in as.
 * Everything below that — which disk, read-only or not, when it was added — is
 * for the one visit in ten where something is wrong.
 *
 * Two addresses, always, because there is no single one that works everywhere.
 * Windows wants a UNC path with backslashes and finds the server by its plain
 * name; macOS and Linux want a URL and find it over mDNS, which needs the
 * `.local` suffix. Showing one and hoping is how somebody with the other kind of
 * computer concludes it does not work.
 *
 * The addresses are built from what the network actually reports rather than
 * assumed. A server whose mDNS is not answering cannot be reached by name at
 * all, and printing `smb://homebase.local/films` at somebody in that state sends
 * them to a dialog that will sit and time out.
 */

export function Shares({
  canManage,
  canSetAccess,
  serverName,
}: {
  canManage: boolean;
  /** Whether this person may choose who opens a folder. It is an accounts
   *  question, not a network one, so it is a separate permission. */
  canSetAccess: boolean;
  serverName: string;
}) {
  const [status, setStatus] = useState<ShareStatus | null>(null);
  const [network, setNetwork] = useState<NetworkStatus | null>(null);
  const [locations, setLocations] = useState<StorageLocation[]>([]);
  const [error, setError] = useState<ReturnType<typeof describeError> | null>(null);
  const [busy, setBusy] = useState(false);

  const refresh = useCallback(async () => {
    try {
      const shares = await api.shares();
      setStatus(shares);
      setError(null);
      // The network is only needed to spell the address nicely, and reading it
      // needs a permission somebody who only has files does not have. A failure
      // here costs the .local name and nothing else — the address falls back to
      // the server's IP, which is what happens on a network without mDNS too.
      try {
        setNetwork(await api.network());
      } catch {
        setNetwork(null);
      }
    } catch (caught) {
      setError(describeError(caught));
    }
    // Only for the "add a folder" form. A failure here is not worth a message:
    // the form is what needs disks, and it says so itself when there are none.
    api
      .locations()
      .then((list) => setLocations(list.items))
      .catch(() => setLocations([]));
  }, []);

  useEffect(() => {
    // eslint-disable-next-line react-hooks/set-state-in-effect
    void refresh();
  }, [refresh]);

  const run = async (action: () => Promise<unknown>) => {
    setBusy(true);
    setError(null);
    try {
      await action();
      await refresh();
    } catch (caught) {
      setError(describeError(caught));
    }
    setBusy(false);
  };

  if (!status) {
    return (
      <section className="card">
        <h3>Your files, from another computer</h3>
        {error ? (
          <Message
            tone="error"
            title={error.title}
            technical={error.detail}
            recovery={error.recovery}
          />
        ) : (
          <p className="muted">Checking…</p>
        )}
      </section>
    );
  }

  const host = names(status, network, serverName);

  return (
    <>
      {error ? (
        <Message
          tone="error"
          title={error.title}
          technical={error.detail}
          recovery={error.recovery}
        />
      ) : null}

      {/* Said first and loudly. A share that is configured and not being served
          looks, from a laptop in another room, exactly like one that was never
          made — and the person looking at it is staring at "cannot find the
          server" with no way to tell which it is. */}
      {status.shares.length > 0 && !status.running ? (
        <Message
          tone="error"
          title="Nothing here is reachable at the moment."
          recovery="The file server is not running, so none of these folders can be opened from another computer. Restarting the server usually starts it again; if it does not, this is worth reporting."
        />
      ) : null}

      {status.shares.length === 0 ? (
        <Nothing />
      ) : (
        <Reaching shares={status.shares} users={status.users} host={host} />
      )}

      {canManage ? (
        <>
          <People users={status.users} busy={busy} run={run} />
          <Folders
            shares={status.shares}
            locations={locations}
            busy={busy}
            installed={status.installed}
            canSetAccess={canSetAccess}
            run={run}
          />
        </>
      ) : (
        <section className="card">
          <p className="muted">
            You can see what is shared but not change it. Ask whoever set up this
            server.
          </p>
        </section>
      )}
    </>
  );
}

/** The two names this server can be reached by, and whether each is real. */
interface Names {
  /** The plain name, which is what Windows resolves. */
  windows: string;
  /** The mDNS name or an address — whichever will actually answer. */
  unix: string;
  /** True when the mDNS name is answering, so `.local` can be relied on. */
  byName: boolean;
  /**
   * Whether we were able to look at all.
   *
   * Reading the network needs a permission somebody who only has files does not
   * have. "We could not look" and "the name is not being published" are
   * different facts, and printing the second when the first is true says
   * something false underneath addresses that use the name.
   */
  known: boolean;
}

function names(
  status: ShareStatus,
  network: NetworkStatus | null,
  fallback: string,
): Names {
  const plain = status.server_name || network?.hostname || fallback || "homebase";
  if (network?.mdns_works && network.mdns_name) {
    return { windows: plain, unix: network.mdns_name, byName: true, known: true };
  }

  // No mDNS: the `.local` name will not resolve, so the address is the only
  // thing that will work. It changes from time to time, and saying so is better
  // than printing a name that quietly does not.
  const address = (network?.interfaces ?? [])
    .filter((i) => i.kind !== "loopback" && i.kind !== "container" && i.up)
    .flatMap((i) => i.addresses ?? [])
    .find((a) => !a.includes(":"));
  return { windows: plain, unix: address ?? plain, byName: false, known: network !== null };
}

function Nothing() {
  return (
    <section className="card">
      <h3>Your files, from another computer</h3>
      <p>
        Nothing is shared yet. A shared folder appears on the other computers in
        your house as a drive you can drag files into — no application to install,
        and it works the same from Windows, macOS, Linux and most phones.
      </p>
      <p className="muted">
        This is also how you back a computer up <em>to</em> this server rather
        than the other way round: Windows File History and most backup tools will
        write to a shared folder.
      </p>
    </section>
  );
}

/**
 * The address, which is the point of the whole screen.
 *
 * One share is shown in full — the first — rather than every address for every
 * folder. Once somebody has opened the server once, the rest are visible inside
 * it as ordinary folders, and a wall of near-identical paths is how the one that
 * matters gets lost.
 */
function Reaching({
  shares,
  users,
  host,
}: {
  shares: SharedFolder[];
  users: string[];
  host: Names;
}) {
  // The first one whose disk is actually there. An address for a folder on an
  // unplugged disk is a correct string that produces "cannot connect", which is
  // the worst kind of wrong answer to give somebody.
  const first = shares.find((share) => share.available) ?? shares[0];
  if (!first) return null;

  return (
    <section className="card">
      <h3>Your files, from another computer</h3>

      {users.length === 0 ? (
        <Message
          tone="warning"
          title="Nobody can open these yet."
          recovery="There is no file-sharing account to sign in with, so every attempt is refused. Add one below — it is separate from the account you use here, on purpose."
        />
      ) : null}

      <dl className="facts">
        <dt>On Windows</dt>
        <dd>
          <code className="address">{`\\\\${host.windows}\\${first.name}`}</code>
          <div className="muted">
            Paste it into the address bar of File Explorer. To keep it, right-click
            “This PC” and choose “Map network drive”.
          </div>
        </dd>

        <dt>On macOS</dt>
        <dd>
          <code className="address">{`smb://${host.unix}/${first.name}`}</code>
          <div className="muted">In Finder, Go menu, “Connect to Server”.</div>
        </dd>

        <dt>On Linux</dt>
        <dd>
          <code className="address">{`smb://${host.unix}/${first.name}`}</code>
          <div className="muted">
            In Files, “Other Locations”, into the box at the bottom.
          </div>
        </dd>

        {users.length > 0 ? (
          <>
            <dt>Sign in as</dt>
            <dd>
              {users.join(", ")}
              <div className="muted">
                With the file-sharing password, which is not the password you use
                here. Leave the domain or workgroup box empty.
              </div>
            </dd>
          </>
        ) : null}
      </dl>

      {/* Only when we actually know. An account without network.diagnose cannot
          read the network status, and saying "this server is not publishing its
          name" because we could not look is a different claim from the one it
          reads as — especially printed underneath addresses that use the name. */}
      {host.known && !host.byName ? (
        <p className="hint">
          This server is not publishing its name on the network, so the addresses
          above use its number instead — which changes from time to time. If they
          stop working, come back here for the current one.
        </p>
      ) : null}

      {shares.length > 1 ? (
        <p className="muted">
          The other {shares.length - 1} shared folder
          {shares.length === 2 ? "" : "s"} —{" "}
          {shares
            .filter((share) => share.name !== first.name)
            .map((share) => share.name)
            .join(", ")}{" "}
          — are reached the same way, with the name changed at the end.
        </p>
      ) : null}
    </section>
  );
}

/**
 * The accounts that may connect.
 *
 * Deliberately its own thing, and said so. These are not Homebase logins: a
 * file-sharing password is typed into a Windows dialog once and saved there for
 * ever, which makes it exactly the kind nobody ever changes — so it must not
 * also be a way to administer the machine.
 */
function People({
  users,
  busy,
  run,
}: {
  users: string[];
  busy: boolean;
  run: (action: () => Promise<unknown>) => Promise<void>;
}) {
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [done, setDone] = useState<string | null>(null);
  const [removing, setRemoving] = useState<string | null>(null);

  return (
    <section className="card">
      <h3>Who can open them</h3>

      {users.length > 0 ? (
        <ul className="list">
          {users.map((user) => (
            <li key={user}>
              <div className="row row-spread">
                <strong>{user}</strong>
                <button
                  className="quiet"
                  disabled={busy}
                  onClick={() => setRemoving(user)}
                >
                  Remove
                </button>
              </div>
              {removing === user ? (
                <>
                  <Message
                    tone="warning"
                    title={`Stop ${user} opening the shared folders?`}
                    recovery="Any computer signed in as them is disconnected. Nothing in the folders is touched."
                  />
                  <div className="row">
                    <button
                      className="danger"
                      disabled={busy}
                      onClick={() => {
                        setRemoving(null);
                        void run(() => api.removeShareUser(user));
                      }}
                    >
                      Remove {user}
                    </button>
                    <button className="quiet" onClick={() => setRemoving(null)}>
                      Keep them
                    </button>
                  </div>
                </>
              ) : null}
            </li>
          ))}
        </ul>
      ) : null}

      {done ? (
        <Message
          tone="info"
          title={`${done} can now open the shared folders.`}
          recovery="Use that name and the password you just chose when the other computer asks. Homebase cannot show you the password again — it is not kept anywhere it could be read back, and setting it again here is how to change it."
        />
      ) : null}

      <form
        className="stack"
        onSubmit={(event) => {
          event.preventDefault();
          const name = username.trim();
          setPassword("");
          void run(async () => {
            await api.setSharePassword(name, password);
            setDone(name);
            setUsername("");
          });
        }}
      >
        <label htmlFor="share-user">
          Add somebody, or change a password
          <span className="muted">
            {" "}
            — a name they will type on the other computer. Using the same one as
            here is fine; the password is separate either way.
          </span>
        </label>
        <input
          id="share-user"
          value={username}
          onChange={(event) => setUsername(event.target.value)}
          placeholder="alex"
          autoComplete="off"
        />
        <label htmlFor="share-password">File-sharing password</label>
        <input
          id="share-password"
          type="password"
          value={password}
          onChange={(event) => setPassword(event.target.value)}
          autoComplete="new-password"
        />
        <button
          className="primary"
          disabled={busy || username.trim() === "" || password === ""}
        >
          {busy ? "Saving…" : "Save"}
        </button>
      </form>
    </section>
  );
}

/** The folders themselves — which disk, and whether they can be written to. */
function Folders({
  shares,
  locations,
  installed,
  busy,
  canSetAccess,
  run,
}: {
  shares: SharedFolder[];
  locations: StorageLocation[];
  installed: boolean;
  busy: boolean;
  canSetAccess: boolean;
  run: (action: () => Promise<unknown>) => Promise<void>;
}) {
  const [name, setName] = useState("");
  const [location, setLocation] = useState("");
  const [readOnly, setReadOnly] = useState(false);
  const [removing, setRemoving] = useState<string | null>(null);
  const [choosing, setChoosing] = useState<string | null>(null);
  const [accounts, setAccounts] = useState<Account[]>([]);

  // Fetched once, and only by somebody who may act on it. A list of everybody
  // on the server is not something to hand to a screen that cannot use it.
  useEffect(() => {
    if (!canSetAccess) return;
    void api
      .accounts()
      .then((result) => setAccounts(result.accounts))
      .catch(() => setAccounts([]));
  }, [canSetAccess]);

  const usable = locations.filter((one) => one.mounted && !one.read_only);
  const chosen = location || usable[0]?.id || "";

  return (
    <section className="card">
      <h3>Shared folders</h3>

      {shares.length > 0 ? (
        <ul className="list">
          {shares.map((share) => (
            <li key={share.name}>
              <div className="row row-spread">
                <div>
                  <strong>{share.name}</strong>
                  <div className="muted">
                    {share.read_only ? "read only" : "read and write"} · on{" "}
                    {share.location}
                    {share.available ? "" : " — that disk is not connected"}
                  </div>
                </div>
                <button
                  className="quiet"
                  disabled={busy}
                  onClick={() => setRemoving(share.name)}
                >
                  Stop sharing
                </button>
              </div>

              {/* Who may open it, said on the row rather than behind the
                  button. "Everyone" is the answer for most folders and the one
                  worth being unable to miss: somebody restricting one folder
                  should be able to see, without clicking anything, that the
                  other five are not. */}
              <div className="row row-spread">
                <span className="muted">{describeAccess(share.access)}</span>
                {canSetAccess ? (
                  <button
                    className="quiet"
                    disabled={busy}
                    onClick={() =>
                      setChoosing(choosing === share.name ? null : share.name)
                    }
                  >
                    {choosing === share.name ? "Cancel" : "Who can open it"}
                  </button>
                ) : null}
              </div>

              {choosing === share.name ? (
                <AccessChooser
                  share={share}
                  accounts={accounts}
                  busy={busy}
                  onDone={() => setChoosing(null)}
                  run={run}
                />
              ) : null}
              {removing === share.name ? (
                <>
                  <Message
                    tone="warning"
                    title={`Stop sharing ${share.name}?`}
                    recovery="Nothing in it is deleted — the folder stays on the server exactly as it is, and simply stops appearing on other computers. Sharing it again later brings it back with everything still in it."
                  />
                  <div className="row">
                    <button
                      className="danger"
                      disabled={busy}
                      onClick={() => {
                        setRemoving(null);
                        void run(() => api.removeShare(share.name));
                      }}
                    >
                      Stop sharing it
                    </button>
                    <button className="quiet" onClick={() => setRemoving(null)}>
                      Keep sharing it
                    </button>
                  </div>
                </>
              ) : null}
            </li>
          ))}
        </ul>
      ) : null}

      {usable.length === 0 ? (
        <p className="muted">
          There is no disk to share a folder from yet. Set one up under Storage
          first.
        </p>
      ) : (
        <form
          className="stack"
          onSubmit={(event) => {
            event.preventDefault();
            const folder = name.trim();
            setName("");
            void run(() => api.addShare(folder, chosen, readOnly));
          }}
        >
          <label htmlFor="share-name">
            Share a folder
            {!installed ? (
              <span className="muted">
                {" "}
                — the first one installs the file server, which takes a few
                minutes.
              </span>
            ) : null}
          </label>
          <input
            id="share-name"
            value={name}
            onChange={(event) => setName(event.target.value)}
            placeholder="backup"
            autoComplete="off"
          />

          {usable.length > 1 ? (
            <>
              <label htmlFor="share-location">On which disk</label>
              <select
                id="share-location"
                value={chosen}
                onChange={(event) => setLocation(event.target.value)}
              >
                {usable.map((one) => (
                  <option key={one.id} value={one.id}>
                    {one.name}
                  </option>
                ))}
              </select>
            </>
          ) : null}

          <label className="check">
            <input
              type="checkbox"
              checked={readOnly}
              onChange={(event) => setReadOnly(event.target.checked)}
            />
            Read only — other computers can open and copy from it, but not change
            it
          </label>

          <button className="primary" disabled={busy || name.trim() === ""}>
            {busy ? "Sharing…" : "Share it"}
          </button>
        </form>
      )}
    </section>
  );
}

/** Who may open a folder, in words rather than as a list of nothing. */
function describeAccess(access?: string[]): string {
  if (!access || access.length === 0) {
    return "Anybody with an account can open it";
  }
  if (access.length === 1) {
    return `Only ${access[0]} can open it`;
  }
  return `Only ${access.slice(0, -1).join(", ")} and ${access[access.length - 1]} can open it`;
}

/**
 * Choosing who may open one folder.
 *
 * Two states rather than a list that happens to be empty. "Everybody with an
 * account" is not the same thought as "these people, and at the moment there
 * are none of them" — and the second, saved, would be a folder nobody can
 * open. The server refuses that anyway; this makes it unaskable.
 */
function AccessChooser({
  share,
  accounts,
  busy,
  onDone,
  run,
}: {
  share: SharedFolder;
  accounts: Account[];
  busy: boolean;
  onDone: () => void;
  run: (action: () => Promise<unknown>) => Promise<void>;
}) {
  const current = share.access ?? [];
  const [everyone, setEveryone] = useState(current.length === 0);
  const [chosen, setChosen] = useState<string[]>(current);

  const toggle = (username: string) =>
    setChosen((people) =>
      people.includes(username)
        ? people.filter((one) => one !== username)
        : [...people, username],
    );

  return (
    <div className="roles">
      <label className="role-choice">
        <input
          type="radio"
          checked={everyone}
          onChange={() => setEveryone(true)}
        />
        <span>
          <strong>Anybody with an account</strong>
          <span className="muted"> — how every folder starts</span>
        </span>
      </label>

      <label className="role-choice">
        <input
          type="radio"
          checked={!everyone}
          onChange={() => setEveryone(false)}
        />
        <span>
          <strong>Only the people I choose</strong>
        </span>
      </label>

      {!everyone ? (
        accounts.length === 0 ? (
          <p className="muted">
            Nobody else has an account on this server yet. Add somebody under
            Settings, People.
          </p>
        ) : (
          <ul className="list">
            {accounts.map((account) => (
              <li key={account.username}>
                <label className="role-choice">
                  <input
                    type="checkbox"
                    checked={chosen.includes(account.username)}
                    onChange={() => toggle(account.username)}
                  />
                  <span>{account.username}</span>
                </label>
              </li>
            ))}
          </ul>
        )
      ) : null}

      {/* Said before it is done, not after. Widening this is the one change
          here that puts somebody else's files in front of the whole house. */}
      {everyone && current.length > 0 ? (
        <Message
          tone="warning"
          title={`Everybody will be able to open ${share.name}.`}
          recovery="It is restricted at the moment. This does not move or copy anything — it stops the folder being kept for the people it was kept for."
        />
      ) : null}

      <div className="row">
        <button
          disabled={busy || (!everyone && chosen.length === 0)}
          onClick={() => {
            onDone();
            void run(() =>
              api.setShareAccess(share.name, everyone ? [] : chosen),
            );
          }}
        >
          Save
        </button>
        <button className="quiet" onClick={onDone}>
          Cancel
        </button>
      </div>

      {!everyone && chosen.length === 0 ? (
        <p className="hint">
          Choose at least one person. A folder nobody can open is not something
          Homebase will make.
        </p>
      ) : null}
    </div>
  );
}
