# Using Wi-Fi

Under **Network** in the dashboard, below the connection check.

**Use a network cable if you can.** It is faster, it does not drop out, and it means a
mistake here costs you nothing. Wi-Fi is for the server that has to live somewhere without
a socket.

## Setting it up

1. Plug the server into your router with a network cable, if it is not already
2. Open the dashboard and go to **Network**
3. Press **Look for networks**
4. Pick yours, type the Wi-Fi password, and press **Join this network**

It takes up to a minute. If the password is wrong, Homebase tells you and puts everything
back as it was — your cable keeps working throughout, and you can try again.

Once it has joined, you can unplug the cable.

## Doing it without a cable

You can, and the screen will warn you, because this is the one thing in Homebase that can
leave you unable to reach your server.

If it does not work, Homebase puts the previous settings back by itself, and the server
comes back on the network it was on before. That takes a minute or two, during which the
dashboard will not load. Wait, then reload the page.

If it still does not come back, the server is on neither network. Plug in a cable — that
always works, because Homebase never changes anything about the cable.

## What Homebase does when you join a network

- It writes the network and password to one file, readable only by the machine's
  administrator. Nothing shows the password again, including this page and the diagnostic
  file
- It leaves your cable settings completely alone
- It tells the server to prefer the cable whenever one is plugged in, because a cable is
  faster than Wi-Fi
- It waits to see the server actually get an address on the new network, and undoes
  everything if it does not

## The signal

The list shows a signal strength for each network. **Good** or **excellent** is what you
want. On **weak**, the server will work and will be slow, and will drop out when somebody
uses the microwave.

If your server is going in a cupboard, check the signal there before you leave it — the
number is measured where the server is, not where you are.

## Things that go wrong

**"This server does not have wireless."** No wireless card was found. Most old laptops have
one, so if this one should, its card may need a driver Ubuntu does not include. A USB Wi-Fi
adapter or a cable are the two ways round it.

**No networks in range.** The server is too far from the router, or its card is disabled by
a physical switch — some laptops have one on the side or an Fn key.

**It joined, and then stopped.** Look at **Network**: if it says the server is set up for a
network it cannot reach, it has moved out of range or the router was changed. Rejoin it, or
move the server.

**Your network does not appear but your phone can see it.** Some routers publish a name
only to devices that already know it. Homebase cannot join a network it cannot see; turn
that setting off on the router.

## Turning it off

**Stop using wireless** removes the network from the server. If wireless is the only way it
is connected, do this with a cable plugged in — otherwise the server disconnects and you
cannot reach it to undo.

## What is not here yet

- **Networks that need a username as well as a password**, the kind used in offices and
  universities. Homebase supports the kind of Wi-Fi a house has
- **Hidden networks**, which have to be typed in rather than picked
- **Choosing between two routers with the same name.** Homebase joins whichever is stronger,
  which is what you want almost always and cannot be overridden yet
