package hostd

import (
	"encoding/binary"
	"errors"
	"syscall"
	"testing"
)

// The netlink framing is tested against bytes rather than against a kernel,
// because the kernel is not available in CI and the mistakes here are all in the
// framing. The one bug this code shipped with was of exactly that kind: replies
// were kept as slices of the receive buffer, which the next datagram overwrote.

func TestAttributeEncoding(t *testing.T) {
	// A string attribute is terminated and then padded to four bytes, and the
	// recorded length counts the terminator but not the padding. Getting that
	// boundary wrong is silent: the kernel reads the next attribute from the
	// wrong offset and answers a question nobody asked.
	encoded := stringAttribute(nil, ctrlAttrFamilyName, "ethtool")
	if len(encoded) != 12 {
		t.Fatalf("encoded to %d bytes, want 12: % x", len(encoded), encoded)
	}
	if length := binary.NativeEndian.Uint16(encoded[0:]); length != 12 {
		t.Errorf("recorded length %d, want 12", length)
	}
	if kind := binary.NativeEndian.Uint16(encoded[2:]); kind != ctrlAttrFamilyName {
		t.Errorf("recorded type %d, want %d", kind, ctrlAttrFamilyName)
	}
	if got := string(encoded[4:12]); got != "ethtool\x00" {
		t.Errorf("payload %q, want a terminated string", got)
	}

	// Five bytes of payload means three bytes of padding, and the length must
	// still say nine.
	padded := attribute(nil, 7, []byte{1, 2, 3, 4, 5})
	if len(padded) != 12 {
		t.Fatalf("padded to %d bytes, want 12", len(padded))
	}
	if length := binary.NativeEndian.Uint16(padded[0:]); length != 9 {
		t.Errorf("recorded length %d, want 9 — padding must not be counted", length)
	}
}

func TestNestedAttributesRoundTrip(t *testing.T) {
	inner := stringAttribute(nil, ethtoolAttrHeaderDevName, "enp5s0")
	inner = uint32Attribute(inner, ethtoolAttrHeaderFlags, ethtoolFlagCompactBitsets)
	message := nestedAttribute(nil, ethtoolAttrWOLHeader, inner)

	// The nesting flag must not survive into the parsed type, or every nested
	// attribute is filed under a number nothing looks for.
	outer := attributesOf(message)
	header, ok := outer[ethtoolAttrWOLHeader]
	if !ok {
		t.Fatalf("no header attribute; got types %v", keysOf(outer))
	}

	fields := attributesOf(header)
	if name := string(fields[ethtoolAttrHeaderDevName]); name != "enp5s0\x00" {
		t.Errorf("device name %q", name)
	}
	if flags := binary.NativeEndian.Uint32(fields[ethtoolAttrHeaderFlags]); flags != ethtoolFlagCompactBitsets {
		t.Errorf("flags %d, want %d", flags, ethtoolFlagCompactBitsets)
	}
}

func TestAttributesOfRefusesToRunOffTheEnd(t *testing.T) {
	// A length larger than what is there is what a truncated read looks like.
	// It must stop, not panic: this is on the path of `homebasectl network`,
	// which is what somebody runs when the machine is already misbehaving.
	broken := []byte{0xff, 0xff, 0x01, 0x00, 0x01, 0x02}
	if found := attributesOf(broken); len(found) != 0 {
		t.Errorf("parsed %d attributes out of a malformed list", len(found))
	}

	// Zero length would loop for ever if it were not rejected.
	if found := attributesOf([]byte{0x00, 0x00, 0x01, 0x00}); len(found) != 0 {
		t.Errorf("parsed %d attributes out of a zero-length one", len(found))
	}
}

func TestBitIsSetReadsTheMagicBit(t *testing.T) {
	// Bit 5 of a little-endian word array lives in byte 0. This is the whole
	// answer the netlink round trip exists to produce, so it is worth an
	// assertion of its own rather than being tested only through the socket.
	magic := []byte{1 << 5, 0, 0, 0}
	if !bitIsSet(magic, wolMagicBit) {
		t.Error("the magic bit did not read back")
	}
	if bitIsSet([]byte{0, 0, 0, 0}, wolMagicBit) {
		t.Error("an empty bitset reported the magic bit set")
	}
	// Out of range must be false rather than a panic: a kernel that declares
	// fewer modes than this one expects sends a shorter bitmap.
	if bitIsSet(nil, wolMagicBit) || bitIsSet([]byte{}, wolMagicBit) {
		t.Error("a missing bitmap reported the magic bit set")
	}
	// The neighbouring bits must not be mistaken for it. ETHTOOL_WOL_ARP is 4
	// and ETHTOOL_WOL_MAGICSECURE is 6; reading either as "magic" would report
	// a card as wakeable by a packet nobody is going to send.
	if bitIsSet([]byte{1 << 4, 0, 0, 0}, wolMagicBit) ||
		bitIsSet([]byte{1 << 6, 0, 0, 0}, wolMagicBit) {
		t.Error("a neighbouring wake mode was read as the magic bit")
	}
}

func TestWakeOnLANIsUnknownWithoutPrivilege(t *testing.T) {
	// Reading the setting needs CAP_NET_ADMIN, which the test process does not
	// have. The answer must be "cannot tell" rather than "cannot be woken" —
	// the distinction the whole three-state report exists for, and the one the
	// first implementation got wrong on every machine it ever ran on.
	//
	// This asserts nothing about hardware, so it holds on any machine: loopback
	// exists everywhere and has no wake-on-LAN either way.
	enabled, supported, _ := readWakeOnLAN("lo")
	if enabled || supported {
		t.Errorf("loopback reported wakeable: enabled=%v supported=%v", enabled, supported)
	}

	// A name no kernel has. It must not be reported as a card that cannot be
	// woken, which would be a statement about hardware that does not exist.
	if _, _, known := readWakeOnLAN("homebase-not-a-card"); known {
		t.Error("claimed to know about an interface that does not exist")
	}
}

func TestInterfaceExistsRejectsPathsAndTypos(t *testing.T) {
	if !interfaceExists("lo") {
		t.Error("loopback was not found")
	}
	if interfaceExists("homebase-not-a-card") {
		t.Error("found an interface that does not exist")
	}
	// The name is used to build a path under /sys, so a name that can climb out
	// of it must be refused before it gets there.
	for _, name := range []string{"", "../../etc", "lo/../../etc/shadow", "."} {
		if interfaceExists(name) {
			t.Errorf("%q was accepted as an interface name", name)
		}
	}
}

func keysOf[V any](m map[uint16]V) []uint16 {
	var keys []uint16
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

// netlinkMessage builds one datagram the way the kernel does.
func netlinkMessage(kind uint16, sequence uint32, payload []byte) []byte {
	message := make([]byte, nlMsgHdrLen+len(payload))
	binary.NativeEndian.PutUint32(message[0:], uint32(len(message)))
	binary.NativeEndian.PutUint16(message[4:], kind)
	binary.NativeEndian.PutUint32(message[8:], sequence)
	copy(message[nlMsgHdrLen:], payload)
	return message
}

func TestRepliesSurviveTheDatagramAfterThem(t *testing.T) {
	// The bug this file shipped with, reproduced exactly.
	//
	// A family lookup is answered with two datagrams: the answer, then the
	// acknowledgement. Both are read into the same buffer, so a reply kept as a
	// slice of that buffer is rewritten by the acknowledgement that follows it.
	// What came back was a reply whose first attribute had become the request
	// header echoed inside the acknowledgement — a well-formed attribute of the
	// wrong length, which shifted every attribute after it and lost the family
	// id. No error, no panic, no short read: just the wrong answer.
	//
	// The reader below reuses one buffer, which is the only detail that matters.
	genlHeader := []byte{1, 2, 0, 0}
	answer := append(genlHeader, stringAttribute(nil, ctrlAttrFamilyName, "ethtool")...)
	answer = attribute(answer, ctrlAttrFamilyID, []byte{22, 0})

	acknowledgement := make([]byte, 4+nlMsgHdrLen)

	datagrams := [][]byte{
		netlinkMessage(16, 1, answer),
		netlinkMessage(nlMsgError, 1, acknowledgement),
	}
	sent := 0
	replies, err := collectReplies(1, func(into []byte) (int, error) {
		if sent >= len(datagrams) {
			return 0, errors.New("read past the end")
		}
		n := copy(into, datagrams[sent])
		sent++
		return n, nil
	})
	if err != nil {
		t.Fatalf("collectReplies: %v", err)
	}
	if len(replies) != 1 {
		t.Fatalf("got %d replies, want 1", len(replies))
	}

	id, ok := attributesOf(replies[0])[ctrlAttrFamilyID]
	if !ok {
		t.Fatalf("the family id was lost — the reply was overwritten by the "+
			"acknowledgement: % x", replies[0])
	}
	if got := binary.NativeEndian.Uint16(id); got != 22 {
		t.Errorf("family id %d, want 22", got)
	}
}

func TestCollectRepliesReturnsTheKernelsError(t *testing.T) {
	// A non-zero error must come back as the errno, because one of them —
	// EOPNOTSUPP — is the difference between "this card cannot be woken" and
	// "Homebase cannot tell", which is the whole point of the three states.
	payload := make([]byte, 4+nlMsgHdrLen)
	// A variable, because the kernel sends a negative errno and the conversion
	// of a negative constant to uint32 will not compile.
	code := -int32(syscall.EOPNOTSUPP)
	binary.NativeEndian.PutUint32(payload[0:], uint32(code))

	_, err := collectReplies(1, func(into []byte) (int, error) {
		return copy(into, netlinkMessage(nlMsgError, 1, payload)), nil
	})
	if !errors.Is(err, syscall.EOPNOTSUPP) {
		t.Errorf("got %v, want EOPNOTSUPP", err)
	}
}

func TestCollectRepliesIgnoresAnotherRequestsTraffic(t *testing.T) {
	// Sequence numbers are checked because a netlink socket carries whatever
	// the kernel decides to send it. Reading somebody else's answer as this
	// one's would report another interface's setting under this one's name.
	other := append([]byte{1, 2, 0, 0}, stringAttribute(nil, ctrlAttrFamilyID, "x")...)
	datagrams := [][]byte{
		netlinkMessage(16, 99, other),
		netlinkMessage(nlMsgError, 1, make([]byte, 4+nlMsgHdrLen)),
	}
	sent := 0
	replies, err := collectReplies(1, func(into []byte) (int, error) {
		n := copy(into, datagrams[sent])
		sent++
		return n, nil
	})
	if err != nil {
		t.Fatalf("collectReplies: %v", err)
	}
	if len(replies) != 0 {
		t.Errorf("kept %d replies belonging to another request", len(replies))
	}
}
