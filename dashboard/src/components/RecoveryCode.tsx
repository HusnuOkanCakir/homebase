import { useState } from "react";

interface Props {
  code: string;
  /** What the person has to agree to before they can move on. */
  acknowledgement: string;
  onAcknowledged: () => void;
  busy?: boolean;
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
export function RecoveryCode({ code, acknowledgement, onAcknowledged, busy }: Props) {
  const [written, setWritten] = useState(false);
  const [copied, setCopied] = useState(false);

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
        This is your recovery code. If you ever forget your password, this is what gets
        you back into your server.
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
        Keep it somewhere you will find it again — with your other important papers, not
        on the server itself. It is shown once and cannot be displayed again, but you can
        always create a new one while you are signed in.
      </p>

      <p className="muted">
        Anyone who has this code can take over your server, so treat it like a spare key
        to your house.
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
