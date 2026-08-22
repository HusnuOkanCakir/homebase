interface Props {
  tone: "error" | "warning" | "info";
  title: string;
  detail?: string | undefined;
  recovery?: string | undefined;
  technical?: string | undefined;
}

/**
 * A message to a person who does not know what a mount point is.
 *
 * Four slots, and they are read in the order they are written:
 *
 *   `title`      the sentence somebody reads
 *   `detail`     the rest of what happened, in ordinary words
 *   `recovery`   what they can do about it
 *   `technical`  a path, a command, an exit code — for whoever is diagnosing
 *
 * That order took a screenshot to notice. `detail` used to be the last thing on
 * screen and set in small grey monospace, on the reasoning that it was for
 * diagnostics. Every author since has reached for it as "the second sentence",
 * because that is what the word means — so most of the dashboard was rendering
 * plain English as if it were a stack trace, *underneath* the advice it was
 * supposed to lead into.
 *
 * The lesson is not that twenty-eight call sites were wrong. A slot that is used
 * the same wrong way by everyone who touches it is a slot whose name promises
 * something its rendering does not, and the honest fix was to make `detail`
 * behave the way it reads and give the diagnostics a name of their own.
 */
export function Message({ tone, title, detail, recovery, technical }: Props) {
  return (
    <div className={`message message-${tone}`} role={tone === "error" ? "alert" : "status"}>
      <p className="message-title">{title}</p>
      {detail ? <p className="message-detail">{detail}</p> : null}
      {recovery ? <p className="message-recovery">{recovery}</p> : null}
      {technical ? <p className="message-technical">{technical}</p> : null}
    </div>
  );
}
