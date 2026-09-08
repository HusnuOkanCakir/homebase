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
to the server's **Tailscale address**, port 5900, with the password the setup printed.

You will see the television's screen. Your mouse and keyboard drive it.

!!! note "Why the tunnel and not the home network"

    VNC is not encrypted, and its password scheme carries eight characters and dates from the
    1990s. The first thing anybody does on that screen is type their Homebase password into
    the browser, and on the local network that would cross the wire in the clear.

    Inside Tailscale it is encrypted end to end — including between two machines in the same
    room, where Tailscale still connects directly, so nothing is slower for it.

    The cost is that with Tailscale down there is no remote control until you plug a keyboard
    into the server. That is the right way round for this to fail.

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
