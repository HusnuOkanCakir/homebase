package hostd

import (
	"fmt"
	"net"
	"os"
	"strconv"
	"syscall"
)

// Socket activation and readiness notification, implemented directly against
// the two protocols rather than by importing a systemd library.
//
// Both are small and stable, and ADR-0002 commits hostd to the standard library
// — every dependency in this process is code running as root. Twenty lines here
// is a better trade than a module in the privileged service.

// listenFDStart is systemd's first passed file descriptor. 0, 1 and 2 remain
// stdin, stdout and stderr.
const listenFDStart = 3

// ListenerFromSystemd returns the socket systemd passed us, if it did.
//
// Socket activation is what lets core start before hostd is ready: systemd
// creates and owns the socket, so a connection made before hostd is listening
// queues rather than being refused. It also means the socket's mode and group —
// the privilege boundary itself — come from the unit file, where they are
// reviewable, rather than from whatever this process happens to set.
func ListenerFromSystemd() (net.Listener, bool, error) {
	pidEnv := os.Getenv("LISTEN_PID")
	fdsEnv := os.Getenv("LISTEN_FDS")
	if pidEnv == "" || fdsEnv == "" {
		return nil, false, nil
	}

	// LISTEN_PID guards against inheriting these variables through an exec into
	// a child that was never given the descriptors.
	pid, err := strconv.Atoi(pidEnv)
	if err != nil {
		return nil, false, fmt.Errorf("LISTEN_PID is not a number: %q", pidEnv)
	}
	if pid != os.Getpid() {
		return nil, false, nil
	}

	count, err := strconv.Atoi(fdsEnv)
	if err != nil {
		return nil, false, fmt.Errorf("LISTEN_FDS is not a number: %q", fdsEnv)
	}
	if count < 1 {
		return nil, false, nil
	}
	if count > 1 {
		return nil, false, fmt.Errorf(
			"systemd passed %d sockets; hostd expects exactly one", count)
	}

	// Do not leak the descriptor into anything we later exec.
	syscall.CloseOnExec(listenFDStart)

	file := os.NewFile(listenFDStart, "homebase-hostd.socket")
	if file == nil {
		return nil, false, fmt.Errorf("file descriptor %d is not valid", listenFDStart)
	}
	defer file.Close()

	listener, err := net.FileListener(file)
	if err != nil {
		return nil, false, fmt.Errorf("adopting the systemd socket: %w", err)
	}

	if _, ok := listener.(*net.UnixListener); !ok {
		listener.Close()
		return nil, false, fmt.Errorf("systemd passed a %T; hostd needs a Unix socket", listener)
	}

	return listener, true, nil
}

// NotifyReady tells systemd the service is up.
//
// Required by Type=notify. Without it systemd waits for a readiness message
// that never arrives and eventually declares the unit failed — while the
// process is running perfectly well, which is a confusing way to spend an
// evening.
//
// Silently does nothing when not run under systemd.
func NotifyReady() error { return notify("READY=1") }

// NotifyStopping tells systemd shutdown has begun, so a slow clean shutdown is
// not mistaken for a hang.
func NotifyStopping() error { return notify("STOPPING=1") }

func notify(state string) error {
	addr := os.Getenv("NOTIFY_SOCKET")
	if addr == "" {
		return nil
	}

	// A leading '@' means an abstract socket, which is represented by a leading
	// NUL byte in the address.
	if addr[0] == '@' {
		addr = "\x00" + addr[1:]
	}

	conn, err := net.DialUnix("unixgram", nil, &net.UnixAddr{Name: addr, Net: "unixgram"})
	if err != nil {
		return fmt.Errorf("connecting to the systemd notify socket: %w", err)
	}
	defer conn.Close()

	if _, err := conn.Write([]byte(state)); err != nil {
		return fmt.Errorf("notifying systemd: %w", err)
	}
	return nil
}
