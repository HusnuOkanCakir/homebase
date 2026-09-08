import { useEffect, useState } from "react";
import { api, type PluggedDisk } from "../api";
import { describeError } from "../App";
import { Message } from "../components/Message";

/**
 * Disks somebody has plugged into the server.
 *
 * There is no form here and that is the point. The flow this replaced asked a
 * person at home to share a folder in Windows, find its share name, find a
 * Windows account, and find that account's password — four things Windows does
 * not readily tell anybody, and the reason two evenings went nowhere. Plugged
 * into the server there is nothing to fill in: hostd notices the disk, mounts
 * it read-only, and it appears in Files a few seconds later.
 *
 * So this card exists to answer one question — is it there yet? — and to offer
 * the one button that matters, which is the one you press before pulling the
 * disk back out.
 */
export function PluggedDisks() {
  const [disks, setDisks] = useState<PluggedDisk[] | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<ReturnType<typeof describeError> | null>(null);
  const [reload, setReload] = useState(0);

  // Polled, because the thing being waited for happens at the server rather
  // than on this screen: somebody in another room pushes a plug in. A person
  // watching this page should see it appear without being told to refresh.
  useEffect(() => {
    let current = true;
    const read = async () => {
      try {
        const result = await api.pluggedDisks();
        if (current) {
          setDisks(result.disks);
          setError(null);
        }
      } catch (cause) {
        if (current) setError(describeError(cause));
      }
    };
    void read();
    const timer = setInterval(() => void read(), 5000);
    return () => {
      current = false;
      clearInterval(timer);
    };
  }, [reload]);

  // Nothing plugged in and nothing to say. A card explaining a feature nobody
  // is using is a card in the way.
  if (disks !== null && disks.length === 0) return null;

  return (
    <section className="card">
      <h3>Disks plugged into the server</h3>
      <p className="muted">
        Plug a disk into the server and it appears here by itself, and in Files
        just above. Anybody with an account can read it, including from away.
      </p>

      {error && <Message tone="error" {...error} />}

      {disks === null ? (
        <p className="muted">Looking…</p>
      ) : (
        <ul className="list">
          {disks.map((disk) => (
            <li key={disk.name}>
              <div className="row row-spread">
                <div>
                  <strong>{disk.name}</strong>
                  <div className="muted">
                    {describeSize(disk.size_bytes)}
                    {disk.filesystem ? ` · ${disk.filesystem}` : ""}
                    {disk.connected ? "" : " · not readable"}
                  </div>
                </div>
                {disk.connected ? (
                  <button
                    className="quiet"
                    disabled={busy}
                    onClick={() => {
                      setBusy(true);
                      void api
                        .ejectPluggedDisk(disk.name)
                        .catch((cause) => setError(describeError(cause)))
                        .finally(() => {
                          setBusy(false);
                          setReload((n) => n + 1);
                        });
                    }}
                  >
                    Finish with it
                  </button>
                ) : null}
              </div>
            </li>
          ))}
        </ul>
      )}

      <p className="hint">
        Homebase only ever reads these. Nothing here can change or delete
        anything on a disk somebody plugged in — press <strong>Finish with it</strong>
        before pulling one out, so that nothing is half-read.
      </p>
    </section>
  );
}

/** A size somebody reads rather than counts. */
function describeSize(bytes: number): string {
  if (!bytes) return "";
  const units = ["kB", "MB", "GB", "TB"];
  let value = bytes / 1000;
  let unit = 0;
  while (value >= 1000 && unit < units.length - 1) {
    value /= 1000;
    unit += 1;
  }
  return `${value < 10 ? value.toFixed(1) : Math.round(value)} ${units[unit]}`;
}
