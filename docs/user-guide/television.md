# Using the server as a television

If the server sits near a television, an HDMI cable turns that television into a smart one.
The server plays the picture; you drive it from a computer somewhere else in the room.

!!! warning "This is opt-in, and it is a departure"

    Homebase is otherwise a headless appliance: no desktop, no browser, and a deliberately
    small set of privileged operations between the network and the machine. A graphical
    session with a browser on it is the opposite of that, on the computer that holds the
    household's files.

    So there is no button for this in the dashboard and no operation for it in the API. It
    is a script you run yourself, at the machine, and one command undoes it.

## What you end up with

Turn the television on and choose the HDMI input. Nothing, because the server is off. Turn
the server on — the button, or a wake-up packet from a computer at home — and about a minute
later the dashboard appears on the television.

From then on it is a browser on a big screen: the shared folders, Jellyfin, a video site,
anything. You control it from your own computer across the room, seeing the television's
screen in a window.

**The video plays on the server, not over the network.** The television gets a direct HDMI
signal at full quality, and the only thing crossing the network is your mouse and keyboard.
That is why it stays smooth on a laptop from 2013.

## Setting it up

On the server, once:

```sh
sudo /usr/libexec/homebase/tv-kiosk install
```

It installs X, one window manager and Firefox, makes an unprivileged account for the
television session, and prints a password for the remote control. Write that down.

Then plug in the HDMI cable and restart the server.

## Driving it from another computer

Install any VNC viewer — TightVNC, RealVNC and UltraVNC are all fine on Windows — and connect
to the server on **port 5900** with the password the setup printed:

| | |
|---|---|
| **At home** | `192.168.1.177:5900`, or `homebase.local:5900` |
| **From away** | the server's Tailscale address, port 5900 |

You will see the television's screen in a window. Your own mouse and keyboard drive it.

!!! warning "This one is not encrypted"

    VNC has no encryption, and its password scheme carries eight characters and dates from
    the 1990s. On your home network, anybody who can already put a device on it could watch
    this screen.

    For a television showing films that is not much. The thing to avoid is **typing a
    password into the browser on the television** while somebody you do not trust is on the
    network — sign in once and the session lasts a fortnight.

    Reaching it through Tailscale instead is encrypted end to end, including between two
    machines in the same room, and costs nothing in speed because Tailscale still connects
    directly. Both are open; use whichever suits.

The port is open on the server's real network cards and on the tunnel, and nowhere else. The
applications running on the server cannot reach it, for the same reason they cannot reach the
file server.

## Sound

Sound goes over the same HDMI cable. If the television is silent, the usual cause is that
the server is sending sound to its own speakers instead:

```sh
sudo -u homebase-tv pactl list short sinks
sudo -u homebase-tv pactl set-default-sink <the HDMI one>
```

## Turning it off again

```sh
sudo /usr/libexec/homebase/tv-kiosk remove
```

The television session stops at the next restart. The account and its browser profile are
left alone — removing an account is a decision about somebody's files, even when that
somebody is a browser, and a script should not make it for you.

## What it will not do

**4K.** The graphics in this generation of laptop decode 1080p comfortably and 4K not at all.
On a 4K television the picture will be 1080p stretched, which looks fine from a sofa.

**Netflix, Disney+ and the rest.** Those need a copy-protection component that either is not
available on Linux or limits the picture to 720p. Anything not wrapped in DRM — your own
files, Jellyfin, YouTube — plays properly.

**Be a quiet appliance.** A laptop playing video runs its fan. It is next to a television, so
this is worth knowing before you plug it in rather than after.
