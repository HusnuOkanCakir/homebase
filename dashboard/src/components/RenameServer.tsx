import { useState } from "react";
import { api } from "../api";
import { describeError } from "../App";
import { Message } from "./Message";

/**
 * Changing what the server calls itself.
 *
 * Used in two places, which is deliberate rather than accidental. The
 * getting-started list offers it on a server that still has the name the
 * installer gave it, because that is when most people want to change it — and
 * "This server" offers it for ever, because a machine that can only be renamed
 * during its first week is one nobody can rename.
 *
 * The id is passed in so the two can coexist on one screen without sharing a
 * label between them, which would leave a screen reader pointing at the wrong
 * field.
 */

interface Props {
  id: string;
  current: string;
}

/** The rule hostd enforces: RFC 1123, and nothing else gets to /etc/hostname. */
const VALID = /^[a-zA-Z0-9]([a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?$/;

export function RenameServer({ id, current }: Props) {
  const [name, setName] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<ReturnType<typeof describeError> | null>(null);
  const [done, setDone] = useState<string | null>(null);

  const trimmed = name.trim();
  const valid = VALID.test(trimmed);
  // Only complain once there is something to complain about. An empty field is
  // not a mistake, it is a field somebody has not filled in yet.
  const wrong = trimmed.length > 0 && !valid;

  async function submit() {
    if (!valid || busy) return;
    setBusy(true);
    setError(null);
    try {
      const result = await api.rename(trimmed);
      setDone(result.message);
    } catch (caught) {
      setError(describeError(caught));
    }
    setBusy(false);
  }

  if (done) {
    return <Message tone="info" title={done} />;
  }

  return (
    <>
      {error ? (
        <Message tone="error" title={error.title} technical={error.detail} recovery={error.recovery} />
      ) : null}

      <label htmlFor={id}>What would you like to call it?</label>
      <input
        id={id}
        value={name}
        placeholder={current}
        onChange={(e) => setName(e.target.value)}
        disabled={busy}
        aria-describedby={`${id}-hint`}
      />
      <p id={`${id}-hint`} className={wrong ? "hint hint-warning" : "hint"}>
        Letters, digits and hyphens — no spaces or accents. Something like “living-room” or
        “bookshelf”.
      </p>

      <button className="primary" disabled={!valid || busy} onClick={() => void submit()}>
        {busy ? "Renaming…" : "Use this name"}
      </button>
    </>
  );
}
