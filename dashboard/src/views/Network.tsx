import { useCallback, useEffect, useState } from "react";
import { api, type NetworkStatus } from "../api";
import { describeError } from "../App";
import { Message } from "../components/Message";
import { RemoteAccess } from "./RemoteAccess";
import { Wifi } from "./Wifi";

/**
 * How this server is connected, and what to do when it is not.
 *
 * The whole point of this screen is telling three faults apart that are
 * indistinguishable from a browser that will not load:
 *
 *   the server has no address       — nothing is plugged in, or nothing gave it one
 *   the server has no internet      — it is on the network; the world is not there
 *   nothing here is wrong           — the problem is at the other end
 *
 * Somebody without this information restarts their router for an hour over a
 * problem with their phone's Wi-Fi. So the screen leads with which of those it
 * is, and puts the numbers underneath for the one person in a hundred who wants
 * them.
 */
export function Network({ canManage }: { canManage: boolean }) {
  const [status, setStatus] = useState<NetworkStatus | null>(null);
  const [error, setError] = useState<ReturnType<typeof describeError> | null>(null);
  const [checking, setChecking] = useState(false);

  const refresh = useCallback(async () => {
    setChecking(true);
    try {
      setStatus(await api.network());
      setError(null);
    } catch (caught) {
      setError(describeError(caught));
    }
    setChecking(false);
  }, []);

  useEffect(() => {
    // eslint-disable-next-line react-hooks/set-state-in-effect
    void refresh();
  }, [refresh]);

  if (error) {
    return <Message tone="error" title={error.title} detail={error.detail} recovery={error.recovery} />;
  }
  if (!status) {
    return <p className="muted">Checking how this server is connected…</p>;
  }

  // Loopback is how the machine talks to itself and container bridges belong to
  // Docker. Neither is a way anybody reaches this server, and listing them
  // invites somebody to type an address that cannot work from another room.
  const real = status.interfaces.filter(
    (i) => i.kind !== "loopback" && i.kind !== "container",
  );
  const connected = real.filter((i) => i.up && (i.addresses?.length ?? 0) > 0);

  return (
    <>
      {!status.reachable ? (
        <Message
          tone="error"
          title="This server is not on a network."
          detail="It has no address, so nothing else in the house can reach it."
          recovery="Plug a network cable into the server and into your router, then check again."
        />
      ) : !status.online ? (
        <Message
          tone="warning"
          title="Your server is fine. The internet is not reachable."
          detail="Everything already on this server keeps working, including your files and your backups. Installing new applications needs the internet, because they are downloaded."
          recovery="This is usually the broadband rather than the server. If other things in the house are also offline, that is the answer."
        />
      ) : (
        <Message
          tone="info"
          title="This server is connected."
          detail="It can be reached from the rest of the house, and it can reach the internet."
        />
      )}

      <section className="card">
        <h2>Reaching this server</h2>

        {status.mdns_works ? (
          <p>
            From any device on your network, open{" "}
            <code>https://{status.mdns_name}</code>
          </p>
        ) : (
          <p className="muted">
            This server is not publishing its name on the network yet, so it has to
            be reached by address.
          </p>
        )}

        {connected.length > 0 ? (
          <>
            <p className="muted">
              {status.mdns_works ? "Or by address, which changes from time to time:" : "Its address:"}
            </p>
            <ul className="plain">
              {connected.map((iface) =>
                (iface.addresses ?? []).map((address) => (
                  <li key={iface.name + address}>
                    <code>https://{address}</code>{" "}
                    <span className="muted">
                      — {iface.kind === "wireless" ? "Wi-Fi" : "cable"}
                    </span>
                  </li>
                )),
              )}
            </ul>
          </>
        ) : null}

        <p className="hint">
          Your browser will warn you the first time. This server signed its own
          certificate, because it has no name on the public internet to get one
          for. The letters it shows you can be checked against the ones on the
          server's own screen.
        </p>
      </section>

      <section className="card">
        <h2>The details</h2>
        <p className="muted">
          Worth having if you are asking somebody for help, and safe to ignore
          otherwise.
        </p>

        <dl className="facts">
          <dt>This server is called</dt>
          <dd>{status.hostname}</dd>

          <dt>On the network as</dt>
          <dd>
            {status.mdns_works ? status.mdns_name : "not published"}
          </dd>

          <dt>Router</dt>
          <dd>{status.gateway || "none — this server has no way out to the internet"}</dd>

          <dt>Looks up names using</dt>
          <dd>{status.nameservers?.join(", ") || "nothing configured"}</dd>

          <dt>Internet</dt>
          <dd>{status.online ? "reachable" : "not reachable"}</dd>
        </dl>

        {real.length > 0 ? (
          <ul className="plain">
            {real.map((iface) => (
              <li key={iface.name}>
                <strong>{iface.kind === "wireless" ? "Wi-Fi" : "Network socket"}</strong>{" "}
                <span className="muted">({iface.name})</span> —{" "}
                {iface.up ? "connected" : "nothing plugged in"}
                {iface.addresses?.length ? ` · ${iface.addresses.join(", ")}` : ""}
                {iface.mac ? (
                  <div className="muted">
                    Hardware address {iface.mac} — this is what your router lists it
                    under
                  </div>
                ) : null}
              </li>
            ))}
          </ul>
        ) : null}

        <button className="quiet" onClick={() => void refresh()} disabled={checking}>
          {checking ? "Checking…" : "Check again"}
        </button>
      </section>

      {/* Below the diagnosis, not above it. Somebody arriving here usually
          cannot reach their server, and the first thing they need is which of
          the three faults it is — not a list of networks to join. */}
      <Wifi canManage={canManage} />

      {/* Last, because it is the only thing on this page somebody can reach
          without already being on the network — and therefore the only thing
          they are never looking at when they cannot. */}
      <RemoteAccess canManage={canManage} />
    </>
  );
}
