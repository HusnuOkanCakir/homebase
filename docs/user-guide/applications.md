# Applications

An application is something your server does for you: a place to keep films, a way to get
files onto it from your phone. Homebase installs it, keeps it running, and starts it again
after a power cut, without you needing to know how any of that works.

!!! warning "Pre-alpha"

    There is no installable release yet, and nothing here backs anything up. Do not put
    something on this server that you do not have another copy of.

## Installing one

Open the dashboard, choose **Apps**, and pick one from the list. Each one has a
sentence saying what it is for.

Press **Install**. Downloading takes a few minutes on a home connection — a media server is
a large download — and the page tells you what it is doing while it happens. You can leave
the page; it carries on without you.

When it finishes, the application is running.

## What you can install

Homebase ships a small list, and that list is the whole of what you can install.

That is a deliberate limit and it is worth being straight about the cost: **if the
application you want is not on the list, you cannot add it.** Homebase does not have a way
to install something arbitrary, and it is not a missing feature that will arrive later.

What you get in exchange is that everything on the list has been tested — installed,
stopped, restarted, rebooted, removed — by the people who wrote Homebase, and that nothing
running on your server was described by anything except the Homebase project. The
reasoning is in
[ADR-0012](https://github.com/HusnuOkanCakir/homebase/blob/main/docs/decisions/0012-hostd-owns-the-catalogue.md).

### The media applications, and how they fit together

Several of them are one system rather than several things, and it is not obvious from the
list which. There are two routes to watching something, and they are for different wants.

**To keep it.** Ask in **Jellyseerr**; **Sonarr** and **Radarr** decide what to fetch;
**Prowlarr** knows where to look; **qBittorrent** does the fetching; **Jellyfin** plays it,
on a television, a phone or a browser, and remembers where you stopped. This takes as long
as the download takes. Afterwards the file is yours: watch it again, on anything, with no
connection at all.

Install them together and Homebase wires them to each other. They still need their own
first-run setup — each has its own idea of accounts and API keys, and Homebase does not
invent those for you.

**To watch it once.** **Stremio** plays while it fetches, so it starts in about half a
minute and keeps nothing. Watch something twice and it is fetched twice. It also finds
nothing by itself until you install an addon from inside it.

Both can be installed at once and they do not conflict. Stremio is the one to reach for when
you want to see one thing now; the rest are for a series you are following.

!!! note "About your processor"

    Playing a film usually costs nothing — the file is sent as it is and the television
    decodes it. Converting one *while* it plays is what costs, and an old laptop with no
    hardware video support will struggle with HEVC (also called H.265 or x265) in
    particular. If playback stutters, that is usually why. In Sonarr and Radarr you can set
    a release profile that does not contain `x265`, `HEVC` or `h265`, and get H.264 files
    instead, which every machine of the last fifteen years decodes without effort.

## Starting, stopping and restarting

- **Stop** takes the application away until you start it again. Homebase asks you to type
  its name first, because somebody else in the house may be in the middle of using it.
- **Start** does not ask. It is not a change anybody needs protecting from.
- **Restart** is what to try when something is behaving oddly. Nothing is lost.

An application you stopped stays stopped, including through a restart of the server.
Homebase will not quietly start it again behind your back.

## When something is wrong

The list shows what state each application is in:

| What it says | What it means |
|---|---|
| **Running** | Working normally. |
| **Starting…** | It is up but not ready yet. Some applications take a few minutes on older hardware. |
| **Running, but not responding** | It is up, but its own health check is failing. Try restarting it. |
| **Stopped** | You stopped it. |
| **Stopped unexpectedly** | It stopped on its own, which means something went wrong. |
| **Not installed** | It is available to install. |
| **Cannot tell** | Homebase could not check. This is about Homebase, not about the application — it may well be running fine. |

**Cannot tell** is deliberately not the same as **Not installed**. If Homebase cannot see
what is happening it says so, rather than guessing and possibly telling you an application
is missing when it is running perfectly well.

Every application has a **Recent activity** section showing what it has been saying. That
is usually where the reason for "Stopped unexpectedly" is.

## Removing one

**Remove** takes the application off the server **and keeps its files**.

This is the important part: removing an application does not delete anything you put in it.
If you remove your media server and install it again next month, your films and your
settings are where you left them.

Homebase asks you to type the application's name to confirm. Not a second button — typing
it is what makes you read which application you chose.

## Deleting an application's files

If you genuinely want the files gone, there is a separate action for it. It lives under
**Technical details** on the application's page, and only appears once the application has
been removed.

It is deliberately harder to reach than removing the application, because it is a different
thing to want, and because **it cannot be undone**. No backup is taken first. When it says
the files are gone, they are gone.

## After a power cut

Your applications start themselves again when the server comes back. You do not need to do
anything, and nothing is lost — with the exception of anything an application was in the
middle of writing at the moment the power went, which no server can promise.

The applications you had stopped stay stopped.

## What Homebase does not do yet

- **No updating an application to a newer version.** Each one is pinned to a tested version,
  and moving it is Milestone 8. A version in the catalogue is a combination that has been
  run together, not the newest one published.
- **No proxy in front of them.** An application reachable from the rest of the house is
  reached on its own port, over plain HTTP, and guards itself with its own accounts. Which
  applications may do that is decided per application in the catalogue and reviewed in a
  diff.

## From outside the house

You cannot reach these from outside your home network, and that is on purpose.

An application published on your network is protected by whatever login it ships with — some
have a good one, some have a weak default, and some have none. That is a reasonable trade for
something reachable only from the rooms in your house. It is not a reasonable trade for
something reachable from the internet, and it becomes a worse one the moment you give
somebody else [access to your server](remote-access.md): sharing the machine would otherwise
hand them every application on it.

So the tunnel carries the dashboard and your files, both of which have Homebase accounts
behind them, and stops at the applications.

!!! note "This was not always true"

    Until recently these ports were reachable from anywhere the server was, and Homebase's
    firewall had no say in it: a published container port is redirected before it reaches the
    place firewall rules are applied, so the rules never saw it. The firewall now writes a
    rule in the one chain that is consulted first.
