import { useCallback, useEffect, useState } from "react";
import { api, type Account, type JoiningCode, type Role } from "../api";
import { describeError } from "../App";
import { Message } from "../components/Message";
import { RecoveryCode } from "../components/RecoveryCode";

/**
 * The people who use this server.
 *
 * The screen is arranged around one fact somebody has to carry out of it: the
 * joining code. Everything else here — the list, the roles, the removals — is
 * housekeeping that can be done again tomorrow. The code cannot: it is shown
 * once, and somebody who clicks past it has left a person unable to sign in.
 * So it takes over the screen when it appears, using the same treatment as the
 * recovery code, because it is the same kind of thing.
 *
 * Roles are described by what they let a person do, not by naming permissions.
 * "Can read every file on this server, including other people's" is the sentence
 * that matters about an administrator, and it is not visible in a list of
 * fourteen strings.
 */

const ROLES: { id: Role; label: string; description: string }[] = [
  {
    id: "administrator",
    label: "Administrator",
    // Said plainly. Somebody making their brother an administrator so he can
    // install something should know what else it carries.
    description:
      "Can change anything about this server, and can reach every file on it.",
  },
  {
    id: "member",
    label: "Member",
    description:
      "Can use the shared folders and see how the server is doing. Cannot " +
      "install things, change disks, or add people.",
  },
  {
    id: "limited",
    label: "Limited",
    description: "Can use the shared folders. Nothing else.",
  },
];

function roleLabel(role: Role): string {
  return ROLES.find((entry) => entry.id === role)?.label ?? role;
}

export function People({ me }: { me: string }) {
  const [accounts, setAccounts] = useState<Account[] | null>(null);
  const [error, setError] = useState<ReturnType<typeof describeError> | null>(null);
  const [busy, setBusy] = useState(false);

  // Set while a code is on screen. Nothing else is reachable until it is
  // acknowledged, because there is no second chance to read it.
  const [issued, setIssued] = useState<JoiningCode | null>(null);

  const [newName, setNewName] = useState("");
  const [newRole, setNewRole] = useState<Role>("member");
  const [removing, setRemoving] = useState<Account | null>(null);
  const [typedName, setTypedName] = useState("");

  const refresh = useCallback(async () => {
    try {
      const reply = await api.accounts();
      setAccounts(reply.accounts);
      setError(null);
    } catch (caught) {
      setError(describeError(caught));
    }
  }, []);

  useEffect(() => {
    // setState runs after the fetch resolves rather than synchronously in the
    // effect body — the rule cannot see through the async boundary. Same as
    // every other screen here.
    // eslint-disable-next-line react-hooks/set-state-in-effect
    void refresh();
  }, [refresh]);

  const run = useCallback(
    async (work: () => Promise<void>) => {
      setBusy(true);
      setError(null);
      try {
        await work();
        await refresh();
      } catch (caught) {
        setError(describeError(caught));
      } finally {
        setBusy(false);
      }
    },
    [refresh],
  );

  if (issued) {
    return (
      <RecoveryCode
        code={issued.joining_code}
        acknowledgement={`I have written this down, or given it to ${issued.username}.`}
        onAcknowledged={() => setIssued(null)}
        busy={busy}
      />
    );
  }

  return (
    <section className="card">
      <h2>People</h2>
      <p className="muted">
        Everybody who uses this server signs in as themselves.
      </p>
      {/* Said plainly rather than implied by three role names. A role decides
          what somebody can change about the server; the sentence below is the
          one thing it does not decide, and the one people assume it does. */}
      <p className="hint">
        Folders shared on this server are open to everyone with an account.
        Each person also gets a folder of their own, which the others cannot
        open.
      </p>

      {error && <Message tone="error" {...error} />}

      {accounts === null ? (
        <p className="muted">Reading the list…</p>
      ) : (
        <ul className="app-list">
          {accounts.map((account) => (
            <li className="app-row people-row" key={account.id}>
              <div className="app-row-main">
                <span className="app-row-name">
                  {account.username}
                  {account.username === me && <span className="muted"> — you</span>}
                </span>
                <span className="muted">
                  {roleLabel(account.role)}
                  {/* An invitation nobody has accepted is the thing an
                      administrator most often wants to notice here. */}
                  {!account.has_signed_in && " · has not signed in yet"}
                </span>
              </div>

              <div className="row people-actions">
                <select
                  value={account.role}
                  disabled={busy}
                  aria-label={`Role for ${account.username}`}
                  onChange={(event) =>
                    void run(() =>
                      api.setAccountRole(account.id, event.target.value as Role).then(() => {}),
                    )
                  }
                >
                  {ROLES.map((role) => (
                    <option key={role.id} value={role.id}>
                      {role.label}
                    </option>
                  ))}
                </select>

                <button
                  type="button"
                  className="quiet"
                  disabled={busy}
                  onClick={() =>
                    void run(() =>
                      api.reissueJoiningCode(account.id).then((code) => setIssued(code)),
                    )
                  }
                >
                  New sign-in code
                </button>

                {account.username !== me && (
                  <button
                    type="button"
                    className="danger"
                    disabled={busy}
                    onClick={() => {
                      setRemoving(account);
                      setTypedName("");
                    }}
                  >
                    Remove
                  </button>
                )}
              </div>
            </li>
          ))}
        </ul>
      )}

      {removing && (
        <div className="card card-danger">
          <h3>Remove {removing.username}?</h3>
          <p>
            They will not be able to sign in, and anything they are signed into
            now stops working. <strong>Their files are kept.</strong> Removing
            somebody and deleting their files are different things, and this is
            only the first one.
          </p>
          <label htmlFor="confirm-remove">
            Type <strong>{removing.username}</strong> to confirm
          </label>
          <input
            id="confirm-remove"
            value={typedName}
            onChange={(event) => setTypedName(event.target.value)}
            autoComplete="off"
          />
          <div className="row">
            <button
              type="button"
              className="danger"
              disabled={busy || typedName !== removing.username}
              onClick={() =>
                void run(() =>
                  api.removeAccount(removing.id, typedName).then(() => {
                    setRemoving(null);
                    setTypedName("");
                  }),
                )
              }
            >
              Remove {removing.username}
            </button>
            <button type="button" className="quiet" onClick={() => setRemoving(null)}>
              Keep them
            </button>
          </div>
        </div>
      )}

      <details className="details">
        <summary>Add somebody</summary>
        <form
          className="stack"
          onSubmit={(event) => {
            event.preventDefault();
            void run(() =>
              api.createAccount(newName.trim(), newRole).then((code) => {
                setIssued(code);
                setNewName("");
                setNewRole("member");
              }),
            );
          }}
        >
          <label htmlFor="new-person">
            Their name
            <span className="muted">
              {" "}
              Lower case, no spaces — it becomes their file-sharing name too, and
              Windows cannot tell “Father” from “father”.
            </span>
          </label>
          <input
            id="new-person"
            value={newName}
            onChange={(event) => setNewName(event.target.value)}
            placeholder="father"
            autoComplete="off"
          />

          <fieldset className="roles">
            <legend>What they can do</legend>
            {ROLES.map((role) => (
              <label key={role.id} className="role-choice">
                <input
                  type="radio"
                  name="role"
                  value={role.id}
                  checked={newRole === role.id}
                  onChange={() => setNewRole(role.id)}
                />{" "}
                <strong>{role.label}</strong>
                <span className="muted"> — {role.description}</span>
              </label>
            ))}
          </fieldset>

          <p className="hint">
            You will get a code to give them. They use it to sign in for the
            first time and choose their own password —{" "}
            <strong>you never see it</strong>.
          </p>

          <button className="primary" disabled={busy || newName.trim() === ""}>
            {busy ? "Adding…" : "Add them"}
          </button>
        </form>
      </details>
    </section>
  );
}
