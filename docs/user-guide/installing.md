# Installing Homebase

Homebase turns a computer you are no longer using into a server. The usual candidate is a
laptop that still works but is too slow for whatever you bought it for — it does not need to
be fast, and it does not need a working screen hinge.

!!! danger "This erases the whole disk"

    Everything on the machine you install onto is deleted: Windows, the files on it, the
    photographs somebody put in a folder in 2014. There is no dual boot and nothing is kept.

    If there is anything on that laptop you would miss, copy it off **first**, onto a USB
    drive or another computer. Once this starts there is no undo.

## What you need

- **The old computer.** 64-bit, 4 GB of memory or more, and able to boot from USB. Anything
  from about 2015 onwards.
- **A USB stick, 8 GB or larger.** This gets erased too.
- **A network cable.** Wi-Fi comes in a later version; for now the server needs to be
  plugged into your router.
- **The computer you use every day**, to make the USB stick and to open the dashboard
  afterwards.

## Making the USB stick

Today this is a command, on Linux, and that is a real gap: the graphical version that runs
on Windows and macOS is being built and is not ready. If you are not comfortable with a
terminal, this is the point to ask somebody who is — it is the only step that needs one.

```console
$ homebasectl installer devices
/dev/sda       512 GB  Samsung SSD 970           REFUSED — this computer is running from it
/dev/sdb        16 GB  SanDisk Ultra             can be written to
```

Then write the media, naming the drive it says can be written to:

```console
$ sudo homebasectl installer create --device /dev/sdb
```

It will tell you what it is about to erase and ask you to type the device's name before it
does anything.

## Installing

1. **Plug the stick into the old computer**, along with the network cable.
2. **Switch it on and tell it to boot from USB.** This is the one step that differs between
   machines: press <kbd>F12</kbd>, <kbd>F2</kbd>, <kbd>Esc</kbd> or <kbd>Del</kbd> as it
   starts, and choose the USB drive from the list. Which key it is usually flashes on screen
   for a second.
3. **Wait.** A lot of text goes past. None of it needs reading.
4. **It stops once and asks a question:**

    ```
    Continue with autoinstall? (yes|no)
    ```

    This is the last moment to change your mind. Type `yes` and press enter, and the disk is
    erased and Homebase is installed.

    The question comes from Ubuntu, which Homebase is built on, and it does not say which
    disk it is about to erase. If the machine has more than one drive in it, take the others
    out before you start.

5. **Wait again**, about ten minutes. The machine restarts by itself when it is done.

## Finding it afterwards

When it comes back, the machine's own screen shows something like:

```
  Homebase is installed on this machine.

  Open a browser on your phone or computer, on the same
  network, and go to:

      http://192.168.1.42:8080

  You do not need to type anything here.
```

Type that address into a browser on your phone or laptop. You will be asked to create your
account, and then given a **recovery code to write down** — that is the only thing that gets
you back in if you forget your password, so do not skip it. See
[If you forget your password](passwords.md).

If the screen says the machine is not on a network yet, check the cable at both ends.

You can close the lid now. Homebase ignores it, and the machine will not go to sleep.

## What the installer set up for you

- **A firewall**, which allows the dashboard and nothing else.
- **The lid and sleep settings**, so a closed laptop keeps running.
- **An account on the machine itself** that logs in without a password, on the machine's own
  keyboard only. It is there so that somebody standing in front of the server can recover a
  lost password. It cannot be used over the network.

## When something goes wrong

**It booted into Windows instead.** The machine did not choose the USB stick. Restart and
press the boot-menu key sooner, or look in the BIOS settings for "boot order".

**The USB stick is not in the boot list.** Some machines will not show it unless "Secure
Boot" is turned off, or need "UEFI" rather than "Legacy" boot enabled. Both are in the BIOS
settings.

**It asks for a username and password on the machine's screen.** Type `console` and press
enter twice — but you should not need to. Homebase is managed from the browser.

**The address in the browser does not load.** Check the phone and the server are on the same
network. The address changes if your router restarts; look at the machine's screen again.
