import { useState } from "react";

interface Props {
  code: string;
  /** What the person has to agree to before they can move on. */
  acknowledgement: string;
  onAcknowledged: () => void;
  busy?: boolean;
  /**
   * Which kind of code this is, because the two need different sentences.
   *
   * They looked the same for long enough that this screen told an
   * administrator, about somebody else's joining code, that it was "your
   * recovery code", that anyone holding it could "take over your server", and
   * that they could always create a new one while signed in. Three sentences,
   * none of them true of what was on the screen.
   */
  kind?: "recovery" | "joining";
  /** Whose joining code it is. Unused for a recovery code, which is your own. */
  name?: string;
}

/**
 * The one moment a recovery code is visible.
 *
 * ADR-0015. It is stored the way a password is, so this is not a preview of
 * something retrievable later — it is the only time it exists outside the piece
 * of paper the user is about to write it on.
 *
 * Which is why there is no "next" button. Somebody who clicks past this screen
 * without reading it has lost the code, and will find that out at the worst
 * possible moment. The tick box is the smallest honest obstacle: it takes a
 * second, and it makes the claim explicit rather than assumed.
 */
export function RecoveryCode({
  code,
  acknowledgement,
  onAcknowledged,
  busy,
  kind = "recovery",
  name,
}: Props) {
  const [written, setWritten] = useState(false);
  const [copied, setCopied] = useState(false);
  const joining = kind === "joining";
  const whose = name ?? "them";

  async function copy() {
    try {
      await navigator.clipboard.writeText(code);
      setCopied(true);
      window.setTimeout(() => setCopied(false), 3000);
    } catch {
      // Clipboard access is refused in plenty of ordinary situations, and the
      // code is on the screen regardless. Nothing to report.
    }
  }

  return (
    <section className="card">
      <h2>Write this down</h2>
      <p>
        {joining
          ? `This is ${whose}'s joining code. They use it to sign in for the first time and choose a password of their own — one you will not know.`
          : "This is your recovery code. If you ever forget your password, this is what gets you back into your server."}
      </p>

      <p className="recovery-code" data-testid="recovery-code">
        {code}
      </p>

      <div className="row">
        <button type="button" className="quiet" onClick={() => void copy()}>
          {copied ? "Copied" : "Copy"}
        </button>
        <button type="button" className="quiet" onClick={() => window.print()}>
          Print
        </button>
      </div>

      <p className="muted">
        {joining
          ? `It is shown once and cannot be displayed again, and it stops working a week
             after today. If ${whose} does not get to it in time, issue another — it costs
             nothing and there is no limit.`
          : `Keep it somewhere you will find it again — with your other important papers,
             not on the server itself. It is shown once and cannot be displayed again, but
             you can always create a new one while you are signed in.`}
      </p>

      <p className="muted">
        {joining
          ? `Anyone who has this code can take the account it is for, so hand it to
             ${whose} directly rather than leaving it somewhere.`
          : "Anyone who has this code can take over your server, so treat it like a spare key to your house."}
      </p>

      <label className="check">
        <input
          type="checkbox"
          checked={written}
          onChange={(e) => setWritten(e.target.checked)}
          disabled={busy}
        />
        {acknowledgement}
      </label>

      <button
        className="primary"
        type="button"
        disabled={!written || busy}
        onClick={onAcknowledged}
      >
        Continue
      </button>
    </section>
  );
}
