import { useCallback, useEffect, useState } from "react";
import { api, type NewVPNDevice, type VPNStatus } from "../api";
import { describeError } from "../App";
import { Message } from "../components/Message";

/**
 * Reaching this server from outside the house, over Wireguard.
 *
 * The whole screen is arranged around the one thing that usually goes wrong,
 * which is not on this machine at all: a router that has not been told to
 * forward the port. Homebase cannot do that, cannot check it from here, and
 * cannot tell a firewalled port from a wrong key — so instead of implying
 * success it says plainly what is left, and keeps saying it until a device has
 * actually connected once.
 *
 * `ever_connected` is what makes that possible. Configured, running and never
 * used is a completely different state from working, and they look identical
 * from every other field.
 */
export function RemoteAccess({ canManage }: { canManage: boolean }) {
  const [status, setStatus] = useState<VPNStatus | null>(null);
  const [error, setError] = useState<ReturnType<typeof describeError> | null>(null);
  const [busy, setBusy] = useState(false);
  const [hostname, setHostname] = useState("");
  const [deviceName, setDeviceName] = useState("");
  const [issued, setIssued] = useState<NewVPNDevice | null>(null);
  const [confirming, setConfirming] = useState<string | null>(null);

  const refresh = useCallback(async () => {
    try {
      setStatus(await api.vpn());
      setError(null);
    } catch (caught) {
      setError(describeError(caught));
    }
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

  // Defended rather than trusted. The server is fixed so this is an array, and
  // it was not: removing the last device made it null and `.length` took the
  // whole page down. A screen that cannot be opened is also the screen somebody
  // would use to put the problem right.
  const devices = status?.devices ?? [];

  if (!status) {
    return (
      <section className="card">
        <h3>Reaching this server from outside</h3>
        <p className="muted">Checking…</p>
      </section>
    );
  }

  return (
    <section className="card">
      <h3>Reaching this server from outside</h3>

      {error ? (
        <Message
          tone="error"
          title={error.title}
          technical={error.detail}
          recovery={error.recovery}
        />
      ) : null}

      {/*
        Tailscale first, when it is carrying the traffic.

        Not because it is better, but because it is *true*: on a connection
        behind carrier-grade NAT the Wireguard section below describes something
        that cannot work, and burying the working answer underneath it is how
        somebody spends an evening re-checking a port forward that was never the
        problem.
      */}
      {status.tailscale.installed ? (
        status.tailscale.running ? (
          <div className="remote-live">
            <p className="remote-live-label">Reachable from anywhere, over Tailscale</p>
            {status.tailscale.name ? (
              <p className="remote-live-name">{status.tailscale.name}</p>
            ) : null}
            {status.tailscale.addresses?.length ? (
              <p className="muted remote-live-addresses">
                {status.tailscale.addresses.join(" · ")}
              </p>
            ) : null}
            <p className="muted">
              Install Tailscale on your phone or laptop, sign in to the same
              account, and this server is on its network wherever it is. Use the
              name above — <strong>not</strong> the .local one, which only works
              beside the server and never over a tunnel.
            </p>
          </div>
        ) : (
          <Message
            tone="warning"
            title="Tailscale is installed but not signed in."
            detail={
              "Until it is, this server cannot be reached from outside through it. " +
              "This is the state that looks finished and is not."
            }
            recovery="Run `sudo tailscale up` on the server and open the link it prints."
            technical={status.tailscale.state ? `state: ${status.tailscale.state}` : undefined}
          />
        )
      ) : null}

      {!status.configured ? (
        <>
          <p>
            Wireguard puts your phone or laptop on this house's network from anywhere,
            over an encrypted tunnel. Nothing else on this server is exposed to the
            internet — the tunnel is the only way in, and it answers nothing at all
            without a key.
          </p>
          {canManage ? (
            <form
              className="stack"
              onSubmit={(event) => {
                event.preventDefault();
                void run(() => api.setUpVPN(hostname.trim()));
              }}
            >
              <label htmlFor="vpn-hostname">
                What will devices connect to?
                <span className="muted">
                  {" "}
                  A name that follows your home connection — something like
                  yours.duckdns.org. A fixed address works too, if you have one.
                </span>
              </label>
              <input
                id="vpn-hostname"
                value={hostname}
                onChange={(event) => setHostname(event.target.value)}
                placeholder="yours.duckdns.org"
                autoComplete="off"
              />
              <button className="primary" disabled={busy || hostname.trim() === ""}>
                {busy ? "Switching on…" : "Switch on remote access"}
              </button>
            </form>
          ) : (
            <p className="muted">
              You can see this but not change it. Ask whoever set up this server.
            </p>
          )}
        </>
      ) : (
        <>
          <dl className="facts">
            <dt>Devices connect to</dt>
            <dd>
              {status.hostname}:{status.port}
            </dd>
            <dt>Tunnel</dt>
            <dd>
              {status.running ? "running" : "not running"}
              {status.running && !status.ever_connected ? (
                <span className="badge badge-warning"> Never used</span>
              ) : null}
            </dd>
            {status.dns.configured ? (
              <>
                <dt>Name kept up to date</dt>
                <dd>
                  {status.dns.name}
                  {status.dns.last_result === "ok" ? null : (
                    <span className="badge badge-warning"> {status.dns.last_result}</span>
                  )}
                </dd>
              </>
            ) : null}
          </dl>

          {/* The one thing left, said until it has demonstrably been done.
              A port the router has not forwarded looks from a phone exactly
              like a wrong key: no error, no reply, nothing.

              And sometimes it is neither. An internet connection behind
              carrier-grade NAT cannot receive a forwarded port at all, however
              correctly the router is configured — the public address belongs to
              the provider and is shared. That case is named here because
              everything about it looks like a mistake the reader made, and no
              amount of care in the router will fix it. */}
          {!status.ever_connected ? (
            <Message
              tone="warning"
              title="One thing is left, and this server cannot do it."
              detail={`Forward UDP port ${status.port} on your router to this server.`}
              recovery={
                "Look for “port forwarding” in your router's settings. Until that is " +
                "done, a device will sit there trying and never connect — which looks " +
                "exactly like a wrong key, because Wireguard deliberately answers " +
                "nothing it does not recognise. Give the server a fixed address in the " +
                "router at the same time, or the forwarding will break the next time it " +
                "restarts. If it is already forwarded and still nothing connects, check " +
                "the router's own internet address: one starting 100.64 to 100.127 means " +
                "your provider shares it with other homes, and no port can be forwarded " +
                "to you until they give you one of your own."
              }
            />
          ) : null}

          <h4>Devices</h4>
          {devices.length === 0 ? (
            <p className="muted">
              No devices yet. Add one, then scan the code from inside the Wireguard
              app — not with the phone's camera, which only shows you the text.
            </p>
          ) : (
            <ul className="list">
              {devices.map((device) => (
                <li key={device.public_key}>
                  <div className="row row-spread">
                    <div>
                      <strong>{device.name}</strong>
                      <div className="muted">
                        {device.address}
                        {device.last_handshake
                          ? ` — last connected ${device.last_handshake}`
                          : " — has never connected"}
                      </div>
                    </div>
                    {canManage ? (
                      <button
                        className="quiet"
                        disabled={busy}
                        onClick={() => setConfirming(device.name)}
                      >
                        Remove
                      </button>
                    ) : null}
                  </div>
                  {confirming === device.name ? (
                    <Message
                      tone="warning"
                      title={`Stop ${device.name} connecting?`}
                      recovery={
                        "Its key stops working immediately. Adding it again issues a " +
                        "new one — the old configuration cannot be brought back, so " +
                        "the device has to be set up from scratch."
                      }
                    />
                  ) : null}
                  {confirming === device.name ? (
                    <div className="row">
                      <button
                        className="danger"
                        disabled={busy}
                        onClick={() => {
                          setConfirming(null);
                          void run(() => api.removeVPNDevice(device.name));
                        }}
                      >
                        Remove {device.name}
                      </button>
                      <button className="quiet" onClick={() => setConfirming(null)}>
                        Keep it
                      </button>
                    </div>
                  ) : null}
                </li>
              ))}
            </ul>
          )}

          {/* Shown once, and said so. The private key is in it and is stored
              nowhere — this is the only moment it exists outside the device
              that will hold it. */}
          {issued ? (
            <div className="issued">
              <h4>{issued.name}</h4>
              <p>{issued.message}</p>
              {issued.qr_image ? (
                <img
                  className="qr"
                  src={issued.qr_image}
                  alt={`Configuration for ${issued.name}, as a QR code`}
                />
              ) : null}
              <details>
                <summary>Or copy the configuration</summary>
                <pre className="config">{issued.config}</pre>
              </details>
              <button className="quiet" onClick={() => setIssued(null)}>
                I have saved it
              </button>
            </div>
          ) : canManage ? (
            <form
              className="stack"
              onSubmit={(event) => {
                event.preventDefault();
                const name = deviceName.trim();
                setDeviceName("");
                void run(async () => {
                  setIssued(await api.addVPNDevice(name));
                });
              }}
            >
              <label htmlFor="vpn-device">Add a device</label>
              <input
                id="vpn-device"
                value={deviceName}
                onChange={(event) => setDeviceName(event.target.value)}
                placeholder="phone"
                autoComplete="off"
              />
              <button className="primary" disabled={busy || deviceName.trim() === ""}>
                {busy ? "Adding…" : "Add device"}
              </button>
            </form>
          ) : null}

          {canManage ? (
            <>
              <button
                className="quiet"
                disabled={busy}
                onClick={() => setConfirming("__off__")}
              >
                Switch remote access off
              </button>
              {confirming === "__off__" ? (
                <Message
                  tone="warning"
                  title="Switch remote access off?"
                  recovery={
                    "The port closes and nothing outside the house can reach this " +
                    "server. Anybody using it right now is disconnected. The devices " +
                    "you have set up keep their keys and work again the moment it is " +
                    "switched back on."
                  }
                />
              ) : null}
              {confirming === "__off__" ? (
                <div className="row">
                  <button
                    className="danger"
                    disabled={busy}
                    onClick={() => {
                      setConfirming(null);
                      void run(() => api.disableVPN());
                    }}
                  >
                    Switch it off
                  </button>
                  <button className="quiet" onClick={() => setConfirming(null)}>
                    Leave it on
                  </button>
                </div>
              ) : null}
            </>
          ) : null}
        </>
      )}
    </section>
  );
}
