package hostd

import (
	"encoding/binary"
	"errors"
	"fmt"
	"syscall"
	"time"
)

// ethtool, spoken over netlink.
//
// Every example of reading the wake-on-LAN setting from Go uses the SIOCETHTOOL
// ioctl on an AF_INET socket, and that is what this used to do. It never once
// worked: `homebase-hostd.service` sets RestrictAddressFamilies=AF_UNIX
// AF_NETLINK, so the socket cannot be opened at all, and every installation of
// Homebase that has ever run reported "cannot tell whether this card can be
// woken" about hardware that does magic packets perfectly well.
//
// The same restriction is what broke the internet check, and the fix there was
// to move the work to a process allowed to do it. There is no such process here:
// wake-on-LAN is a property of the card and reading it is privileged. So the fix
// is the kernel's second ethtool interface, over generic netlink — a family
// hostd is permitted, and one that exists precisely so this does not need a
// socket of an unrelated family.
//
// The cost is that generic netlink has to be spoken by hand, because hostd
// carries no third-party Go code (ADR-0002). That is the rest of this file: a
// few hundred bytes of message framing, for one question and one answer.

const (
	// From the kernel's include/uapi/linux/netlink.h. Lengths are of the fixed
	// headers: struct nlmsghdr, then struct genlmsghdr on top of it.
	nlMsgHdrLen   = 16
	genlMsgHdrLen = 4

	nlmRequest = 0x001
	nlmAck     = 0x004

	nlMsgNoop  = 0x1
	nlMsgError = 0x2
	nlMsgDone  = 0x3

	nlaNested   = 0x8000
	nlaTypeMask = 0x3fff
)

// nlAlign rounds up to the four-byte boundary netlink puts everything on.
func nlAlign(n int) int { return (n + 3) &^ 3 }

// netlinkConn is one generic netlink socket.
type netlinkConn struct {
	fd  int
	seq uint32
}

func dialGenetlink() (*netlinkConn, error) {
	fd, err := syscall.Socket(syscall.AF_NETLINK,
		syscall.SOCK_RAW|syscall.SOCK_CLOEXEC, syscall.NETLINK_GENERIC)
	if err != nil {
		return nil, err
	}
	conn := &netlinkConn{fd: fd}

	if err := syscall.Bind(fd, &syscall.SockaddrNetlink{
		Family: syscall.AF_NETLINK,
	}); err != nil {
		conn.close()
		return nil, err
	}

	// A read with no deadline is an operation with no way out. The kernel
	// answers this in microseconds or not at all, and hostd is answering a
	// person waiting at a terminal — two seconds is already far longer than
	// anything correct will take.
	timeout := syscall.NsecToTimeval(int64(2 * time.Second))
	if err := syscall.SetsockoptTimeval(fd, syscall.SOL_SOCKET,
		syscall.SO_RCVTIMEO, &timeout); err != nil {
		conn.close()
		return nil, err
	}
	return conn, nil
}

func (c *netlinkConn) close() { _ = syscall.Close(c.fd) }

// do sends one command and returns the payload of every reply that arrived
// before the acknowledgement, each starting after its generic netlink header.
//
// NLM_F_ACK is set on every request, including the ones that expect a reply of
// their own. It costs one extra message and it means this loop always has a
// definite end: without it, a command the kernel answers with nothing leaves the
// socket blocking until the timeout, and "no answer" and "the answer is empty"
// become the same event.
func (c *netlinkConn) do(family uint16, command, version uint8, attributes []byte) ([][]byte, error) {
	c.seq++
	sequence := c.seq

	message := make([]byte, nlMsgHdrLen+genlMsgHdrLen+len(attributes))
	binary.NativeEndian.PutUint32(message[0:], uint32(len(message)))
	binary.NativeEndian.PutUint16(message[4:], family)
	binary.NativeEndian.PutUint16(message[6:], nlmRequest|nlmAck)
	binary.NativeEndian.PutUint32(message[8:], sequence)
	message[nlMsgHdrLen] = command
	message[nlMsgHdrLen+1] = version
	copy(message[nlMsgHdrLen+genlMsgHdrLen:], attributes)

	if err := syscall.Sendto(c.fd, message, 0, &syscall.SockaddrNetlink{
		Family: syscall.AF_NETLINK,
	}); err != nil {
		return nil, err
	}

	return collectReplies(sequence, func(into []byte) (int, error) {
		n, _, err := syscall.Recvfrom(c.fd, into, 0)
		return n, err
	})
}

// collectReplies reads datagrams until the kernel acknowledges the request.
//
// Separated from the socket so that it can be tested against canned bytes. That
// is not an abstraction for its own sake: the one bug this code shipped with was
// in here and needed two datagrams to show itself, which is not something a
// caller can arrange against a real kernel.
func collectReplies(sequence uint32, read func([]byte) (int, error)) ([][]byte, error) {
	var replies [][]byte

	// One buffer, reused. That is the trap — see the copy below.
	buffer := make([]byte, 8192)
	for {
		n, err := read(buffer)
		if err != nil {
			return nil, err
		}
		if n < 0 || n > len(buffer) {
			return nil, errors.New("netlink: impossible read length")
		}
		rest := buffer[:n]

		for len(rest) >= nlMsgHdrLen {
			length := int(binary.NativeEndian.Uint32(rest[0:]))
			if length < nlMsgHdrLen || length > len(rest) {
				return nil, errors.New("netlink: the kernel sent a truncated message")
			}
			kind := binary.NativeEndian.Uint16(rest[4:])
			seq := binary.NativeEndian.Uint32(rest[8:])
			body := rest[nlMsgHdrLen:length]

			if step := nlAlign(length); step < len(rest) {
				rest = rest[step:]
			} else {
				rest = nil
			}

			// Anything belonging to another request on this socket. There is
			// only ever one in flight, so this is the kernel's own traffic.
			if seq != sequence {
				continue
			}

			switch kind {
			case nlMsgError:
				if len(body) < 4 {
					return nil, errors.New("netlink: the kernel sent a malformed error")
				}
				// An error of zero is the acknowledgement, not a failure.
				if code := int32(binary.NativeEndian.Uint32(body[0:])); code != 0 {
					return nil, syscall.Errno(-code)
				}
				return replies, nil
			case nlMsgDone:
				return replies, nil
			case nlMsgNoop:
			default:
				if len(body) < genlMsgHdrLen {
					return nil, errors.New("netlink: the kernel sent a short reply")
				}
				// Copied, not referenced. The kernel answers a family lookup
				// with two datagrams — the answer, then the acknowledgement —
				// and keeping a slice of the buffer means the second read
				// rewrites the first answer underneath it. That produced a
				// reply whose leading attribute had become the request header
				// echoed back inside the acknowledgement, parsed as a valid
				// attribute of the wrong length, which threw off every
				// attribute after it and lost the one field being looked for.
				replies = append(replies, append([]byte(nil), body[genlMsgHdrLen:]...))
			}
		}
	}
}

// attribute appends one netlink attribute, padded so the next one starts on the
// boundary the kernel's parser expects. The recorded length excludes that
// padding, which is the part that is easy to get wrong and silent when wrong.
func attribute(dst []byte, kind uint16, value []byte) []byte {
	header := make([]byte, 4)
	binary.NativeEndian.PutUint16(header[0:], uint16(4+len(value)))
	binary.NativeEndian.PutUint16(header[2:], kind)
	dst = append(dst, header...)
	dst = append(dst, value...)
	for len(dst)%4 != 0 {
		dst = append(dst, 0)
	}
	return dst
}

func stringAttribute(dst []byte, kind uint16, value string) []byte {
	return attribute(dst, kind, append([]byte(value), 0))
}

func uint32Attribute(dst []byte, kind uint16, value uint32) []byte {
	encoded := make([]byte, 4)
	binary.NativeEndian.PutUint32(encoded, value)
	return attribute(dst, kind, encoded)
}

func nestedAttribute(dst []byte, kind uint16, value []byte) []byte {
	return attribute(dst, kind|nlaNested, value)
}

// attributesOf indexes an attribute list by type. A malformed list is truncated
// rather than rejected: this is a read of hardware state, and half an answer
// about one interface is better than no answer about any of them.
func attributesOf(payload []byte) map[uint16][]byte {
	found := make(map[uint16][]byte)
	for len(payload) >= 4 {
		length := int(binary.NativeEndian.Uint16(payload[0:]))
		kind := binary.NativeEndian.Uint16(payload[2:]) & nlaTypeMask
		if length < 4 || length > len(payload) {
			break
		}
		found[kind] = payload[4:length]
		step := nlAlign(length)
		if step > len(payload) {
			break
		}
		payload = payload[step:]
	}
	return found
}

const (
	// The generic netlink controller, which is the only family with a fixed
	// number. Every other family has to be looked up by name through it.
	genlControlFamily    = 0x10
	genlControlVersion   = 1
	ctrlCommandGetFamily = 3
	ctrlAttrFamilyID     = 1
	ctrlAttrFamilyName   = 2
)

// family resolves a generic netlink family name to the number this kernel
// happens to have given it. The numbers are assigned at registration and are not
// stable across boots, let alone across machines.
func (c *netlinkConn) family(name string) (uint16, error) {
	replies, err := c.do(genlControlFamily, ctrlCommandGetFamily, genlControlVersion,
		stringAttribute(nil, ctrlAttrFamilyName, name))
	if err != nil {
		return 0, err
	}
	for _, reply := range replies {
		if id, ok := attributesOf(reply)[ctrlAttrFamilyID]; ok && len(id) >= 2 {
			return binary.NativeEndian.Uint16(id), nil
		}
	}
	return 0, fmt.Errorf("netlink: this kernel has no %q family", name)
}

const (
	// From include/uapi/linux/ethtool_netlink.h.
	ethtoolFamilyName = "ethtool"
	ethtoolVersion    = 1

	ethtoolMsgWOLGet = 9
	ethtoolMsgWOLSet = 10

	ethtoolAttrWOLHeader = 1
	ethtoolAttrWOLModes  = 2

	ethtoolAttrHeaderDevName = 2
	ethtoolAttrHeaderFlags   = 3

	// Ask for bitsets as words rather than as a list of named bits. The verbose
	// form means matching strings from the kernel; the compact form is the two
	// numbers actually wanted.
	ethtoolFlagCompactBitsets = 1 << 0

	bitsetAttrSize  = 2
	bitsetAttrValue = 4
	bitsetAttrMask  = 5

	// ETHTOOL_WOL_MAGIC — the `g` in `ethtool`'s output, and the only mode this
	// is about. The others wake a machine on ordinary traffic: broadcast waking
	// means a server in a cupboard starts up every time anything on the network
	// says anything, which is not being able to sleep at all.
	wolMagicBit = 5

	// Six bits declared rather than the eight this kernel has, because bit 5 is
	// the last one that matters and a bitset larger than the kernel's own is
	// rejected outright. Kernels have gained wake-on-LAN modes since 5.6 and may
	// gain more; asking about the ones that existed when magic packets did is
	// the version-independent question.
	wolModesDeclared = 6
)

// readWakeOnLAN asks a card whether it will wake the machine, and whether it
// could — the three states the answer actually has.
//
// "Cannot tell" is a real answer and is reported as one. It is what a container,
// a kernel older than 5.6, or a virtual interface with no driver behind it will
// produce, and it is different in kind from "this card cannot be woken" — which
// is the sentence the old implementation printed about every card on every
// machine, and which was false on the first laptop anyone tried.
func readWakeOnLAN(name string) (enabled, supported, known bool) {
	conn, err := dialGenetlink()
	if err != nil {
		return false, false, false
	}
	defer conn.close()

	family, err := conn.family(ethtoolFamilyName)
	if err != nil {
		return false, false, false
	}

	header := stringAttribute(nil, ethtoolAttrHeaderDevName, name)
	header = uint32Attribute(header, ethtoolAttrHeaderFlags, ethtoolFlagCompactBitsets)

	replies, err := conn.do(family, ethtoolMsgWOLGet, ethtoolVersion,
		nestedAttribute(nil, ethtoolAttrWOLHeader, header))
	switch {
	// The driver has no wake-on-LAN at all — what loopback, a bridge, and the
	// wireless card in the first real laptop all return. That is the kernel
	// stating a fact about the hardware, so it is knowledge rather than the
	// absence of it, and reporting "cannot tell" here would send somebody into
	// a BIOS looking for a setting that would not help them.
	case errors.Is(err, syscall.EOPNOTSUPP):
		return false, false, true
	case err != nil:
		return false, false, false
	}

	for _, reply := range replies {
		modes, ok := attributesOf(reply)[ethtoolAttrWOLModes]
		if !ok {
			continue
		}
		bits := attributesOf(modes)
		on := bitIsSet(bits[bitsetAttrValue], wolMagicBit)

		// A bitset may arrive with no mask, meaning the kernel is stating the
		// value without stating what is supported. A card with the setting
		// switched on evidently supports it.
		return on, on || bitIsSet(bits[bitsetAttrMask], wolMagicBit), true
	}
	return false, false, false
}

// setWakeOnLANMagic switches magic-packet waking on or off for one card, now.
//
// The mask covers one bit, so the card's other wake modes are left exactly as
// the driver set them. Sending a value with no mask would silently switch off
// whatever else was on.
//
// This does not survive a reboot by itself — the setting lives in the card and
// the driver reinitialises it — which is why the caller writes a systemd .link
// file too. Both are needed and for opposite reasons: a change that only takes
// effect after a reboot cannot be checked by the person who made it, and one
// that disappears on the next reboot is worse than one that never happened.
func setWakeOnLANMagic(name string, on bool) error {
	conn, err := dialGenetlink()
	if err != nil {
		return err
	}
	defer conn.close()

	family, err := conn.family(ethtoolFamilyName)
	if err != nil {
		return err
	}

	var value byte
	if on {
		value = 1 << wolMagicBit
	}
	modes := uint32Attribute(nil, bitsetAttrSize, wolModesDeclared)
	modes = attribute(modes, bitsetAttrValue, []byte{value, 0, 0, 0})
	modes = attribute(modes, bitsetAttrMask, []byte{1 << wolMagicBit, 0, 0, 0})

	request := nestedAttribute(nil, ethtoolAttrWOLHeader,
		stringAttribute(nil, ethtoolAttrHeaderDevName, name))
	request = nestedAttribute(request, ethtoolAttrWOLModes, modes)

	_, err = conn.do(family, ethtoolMsgWOLSet, ethtoolVersion, request)
	return err
}

// bitIsSet reads one bit out of a compact netlink bitset, which is an array of
// 32-bit words in little-endian order — so byte n carries bits 8n to 8n+7.
func bitIsSet(bitmap []byte, bit int) bool {
	index := bit / 8
	return index < len(bitmap) && bitmap[index]&(1<<(bit%8)) != 0
}
