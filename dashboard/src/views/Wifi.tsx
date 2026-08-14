import { useCallback, useEffect, useState } from "react";
import { api, type WifiNetwork, type WifiStatus } from "../api";
import { describeError } from "../App";
import { Message } from "../components/Message";

/**
 * Joining a wireless network.
 *
 * The one screen in Homebase whose worst outcome is not "that did not work" but
 * "you can no longer reach this server". Three things follow from that.
 *
 * **It says which situation you are in before you do anything.** With a cable
 * plugged in, a wrong password costs a minute — the cable is untouched and still
 * carries the dashboard. Without one, changing networks is how a machine
 * disappears. Those are different actions wearing the same button, and the
 * screen says which one this is.
 *
 * **A failure says what did not change.** The server puts the previous
 * configuration back before answering, so the honest message is "nothing has
 * changed" — which is the sentence that stops somebody going to look for a
 * monitor.
 *
 * **Nothing here shows a password back.** The server does not return one, and
 * the field is cleared as soon as it has been sent.
 */

interface Props {
  canManage: boolean;
}

export function Wifi({ canManage }: Props) {
  const [status, setStatus] = useState<WifiStatus | null>(null);
  const [networks, setNetworks] = useState<WifiNetwork[] | null>(null);
  const [emptyMessage, setEmptyMessage] = useState("");
  const [error, setError] = useState<ReturnType<typeof describeError> | null>(null);
  const [busy, setBusy] = useState<"" | "scanning" | "joining" | "forgetting">("");
  const [chosen, setChosen] = useState<WifiNetwork | null>(null);
  const [joined, setJoined] = useState("");

  const refresh = useCallback(async () => {
    try {
      setStatus(await api.wifiStatus());
      setError(null);
    } catch (caught) {
      setError(describeError(caught));
    }
  }, []);

  useEffect(() => {
    // eslint-disable-next-line react-hooks/set-state-in-effect
    void refresh();
  }, [refresh]);

  const scan = async () => {
    setError(null);
    setBusy("scanning");
    try {
      const found = await api.scanWifi();
      setNetworks(found.networks);
      setEmptyMessage(found.message ?? "");
    } catch (caught) {
      setError(describeError(caught));
    } finally {
      setBusy("");
    }
  };

  const join = async (ssid: string, passphrase: string) => {
    setError(null);
    setJoined("");
    setBusy("joining");
    try {
      const now = await api.joinWifi(ssid, passphrase);
      setStatus(now);
      setChosen(null);
      setJoined(ssid);
    } catch (caught) {
      setError(describeError(caught));
    } finally {
      setBusy("");
    }
  };

  const forget = async () => {
    setError(null);
    setBusy("forgetting");
    try {
      setStatus(await api.forgetWifi());
      setJoined("");
    } catch (caught) {
      setError(describeError(caught));
    } finally {
      setBusy("");
    }
  };

  if (status && !status.available) {
    return (
      <section className="card">
        <h2>Wireless</h2>
        <Message
          tone="info"
          title="This server does not have wireless."
          detail="No wireless card was found on this machine."
          recovery="Connect it to your router with a network cable. Most old laptops do have wireless — if this one should, its card may need a driver Ubuntu does not include."
        />
      </section>
    );
  }

  return (
    <section className="card">
      <h2>Wireless</h2>

      {error ? (
        <Message tone="error" title={error.title} detail={error.detail} recovery={error.recovery} />
      ) : null}

      {joined ? (
        <Message tone="info" title={`This server has joined ${joined}.`} />
      ) : null}

      {status ? <Current status={status} /> : <p className="muted">Looking…</p>}

      {/* The warning that decides how careful somebody needs to be, shown
          before the button rather than after the mistake. */}
      {status && !status.has_wired_connection ? (
        <Message
          tone="warning"
          title="This server is not connected by a cable."
          detail="Wireless is the only way it can be reached, so changing the network is how a server gets lost."
          recovery="If you can, plug in a network cable first. Then a wrong password costs nothing."
        />
      ) : null}

      {canManage ? (
        <>
          <div className="row">
            <button className="quiet" disabled={busy !== ""} onClick={() => void scan()}>
              {busy === "scanning" ? "Looking for networks…" : "Look for networks"}
            </button>
            {status?.configured ? (
              <button className="quiet" disabled={busy !== ""} onClick={() => void forget()}>
                {busy === "forgetting" ? "Stopping…" : "Stop using wireless"}
              </button>
            ) : null}
          </div>

          {networks && networks.length === 0 ? (
            <p className="muted">{emptyMessage || "No networks are in range."}</p>
          ) : null}

          {networks && networks.length > 0 ? (
            <ul className="app-list">
              {networks.map((network) => (
                <li key={network.ssid} className="storage-row">
                  <div className="row row-between">
                    <div className="app-row-main">
                      <span className="app-row-name">{network.ssid}</span>
                      <span className="muted">
                        {strength(network.bars)}
                        {network.security === "open" ? " · no password" : ""}
                        {network.security === "wep" ? " · old, weak security" : ""}
                      </span>
                    </div>
                    {network.current ? (
                      <span className="badge badge-ok">Connected</span>
                    ) : (
                      <button
                        className="quiet"
                        disabled={busy !== ""}
                        onClick={() => setChosen(network)}
                      >
                        Join
                      </button>
                    )}
                  </div>

                  {/* WEP is broken, and saying "it has a password" about it
                      would be a lie of omission on a network somebody is about
                      to put their photographs behind. */}
                  {network.security === "wep" && !network.current ? (
                    <p className="muted">
                      This network uses an old kind of security that can be broken in
                      minutes. If it is yours, it is worth changing the router&rsquo;s
                      settings to WPA2.
                    </p>
                  ) : null}
                </li>
              ))}
            </ul>
          ) : null}
        </>
      ) : null}

      {chosen ? (
        <JoinDialogue
          network={chosen}
          hasCable={status?.has_wired_connection ?? false}
          busy={busy === "joining"}
          onCancel={() => setChosen(null)}
          onJoin={(passphrase) => void join(chosen.ssid, passphrase)}
        />
      ) : null}
    </section>
  );
}

function Current({ status }: { status: WifiStatus }) {
  if (status.connected) {
    return (
      <p>
        Connected to <strong>{status.ssid}</strong>
        <span className="muted"> — {strength(status.bars ?? 0)}</span>
        {status.addresses && status.addresses.length > 0 ? (
          <span className="muted"> · {status.addresses[0]}</span>
        ) : null}
      </p>
    );
  }

  // Configured and not connected is the state worth naming. It looks like
  // nothing at all otherwise, and it is what a server out of range looks like.
  if (status.configured) {
    return (
      <Message
        tone="warning"
        title="This server is set up for a wireless network it cannot reach."
        detail="It has a network saved and has not joined it."
        recovery="Move the server closer to the router, or choose a different network below."
      />
    );
  }

  return <p className="muted">Not using wireless.</p>;
}

function strength(bars: number): string {
  switch (bars) {
    case 4:
      return "excellent signal";
    case 3:
      return "good signal";
    case 2:
      return "weak signal";
    case 1:
      return "very weak signal";
    default:
      return "no signal";
  }
}

function JoinDialogue({
  network,
  hasCable,
  busy,
  onCancel,
  onJoin,
}: {
  network: WifiNetwork;
  hasCable: boolean;
  busy: boolean;
  onCancel: () => void;
  onJoin: (passphrase: string) => void;
}) {
  const [passphrase, setPassphrase] = useState("");
  const open = network.security === "open";
  const usable = open || (passphrase.length >= 8 && passphrase.length <= 63);

  return (
    <div className={hasCable ? "card" : "card card-danger"}>
      <h3>Join {network.ssid}?</h3>

      {open ? (
        <Message
          tone="warning"
          title="This network has no password."
          detail="Anything else on it can see this server."
          recovery="Only use an open network if you know whose it is."
        />
      ) : null}

      {!open ? (
        <>
          <label htmlFor="wifi-password">The Wi-Fi password</label>
          <input
            id="wifi-password"
            type="password"
            autoFocus
            autoComplete="off"
            value={passphrase}
            onChange={(e) => setPassphrase(e.target.value)}
          />
          <p className="muted">
            The one printed on your router, unless somebody changed it. Between 8 and 63
            characters.
          </p>
        </>
      ) : null}

      {hasCable ? (
        <p className="muted">
          Your network cable is still plugged in and will keep working. If the password is
          wrong, nothing changes and you can try again.
        </p>
      ) : (
        <p>
          <strong>There is no cable.</strong> If this does not work, Homebase puts the previous
          settings back by itself — but the server may be unreachable for a minute or two
          while it does.
        </p>
      )}

      <div className="row">
        <button className="quiet" onClick={onCancel} disabled={busy}>
          Cancel
        </button>
        <button
          className={hasCable ? "primary" : "danger"}
          disabled={!usable || busy}
          onClick={() => onJoin(passphrase)}
        >
          {busy ? "Joining…" : "Join this network"}
        </button>
      </div>

      {busy ? (
        <p className="muted">
          This takes up to a minute. If it does not work, that minute includes putting your
          old settings back.
        </p>
      ) : null}
    </div>
  );
}
