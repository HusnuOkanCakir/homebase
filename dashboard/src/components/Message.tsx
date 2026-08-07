interface Props {
  tone: "error" | "warning" | "info";
  title: string;
  detail?: string | undefined;
  recovery?: string | undefined;
}

/**
 * A message to a person who does not know what a mount point is.
 *
 * `title` is the sentence they read. `detail` is for whoever is diagnosing and
 * is deliberately quieter — it may name paths and devices. `recovery` is what
 * they can actually do, and an error that claims to be recoverable without one
 * is worse than saying nothing.
 */
export function Message({ tone, title, detail, recovery }: Props) {
  return (
    <div className={`message message-${tone}`} role={tone === "error" ? "alert" : "status"}>
      <p className="message-title">{title}</p>
      {recovery ? <p className="message-recovery">{recovery}</p> : null}
      {detail ? <p className="message-detail">{detail}</p> : null}
    </div>
  );
}
