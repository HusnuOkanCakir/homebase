#!/usr/bin/env python3
"""Enough of an SMB2 client to prove somebody can open a Homebase share.

Written because neither machine here has smbclient and neither can install one,
and "the port is open and the configuration parses" is not the same claim as
"a person can connect to this".

It does exactly one thing: NEGOTIATE, then SESSION_SETUP with NTLMv2, then
TREE_CONNECT to the share. If that succeeds, a Windows or Linux machine can
open the folder.
"""
import hmac, os, socket, struct, sys, time
from hashlib import md5

# --- MD4, because OpenSSL 3 no longer offers it and NTLM still needs it -------
def md4(data: bytes) -> bytes:
    h = [0x67452301, 0xefcdab89, 0x98badcfe, 0x10325476]
    n = len(data)
    data += b"\x80" + b"\x00" * ((55 - n) % 64) + struct.pack("<Q", n * 8)
    mask = 0xffffffff
    rot = lambda v, s: ((v << s) | (v >> (32 - s))) & mask
    for off in range(0, len(data), 64):
        X = struct.unpack("<16I", data[off:off + 64])
        a, b, c, d = h
        for i in range(16):
            k, s = i, [3, 7, 11, 19][i % 4]
            f = (b & c) | (~b & d)
            a, b, c, d = d, rot((a + f + X[k]) & mask, s), b, c
        for i in range(16):
            k = (i // 4) + (i % 4) * 4
            s = [3, 5, 9, 13][i % 4]
            f = (b & c) | (b & d) | (c & d)
            a, b, c, d = d, rot((a + f + X[k] + 0x5a827999) & mask, s), b, c
        for i in range(16):
            k = [0, 8, 4, 12, 2, 10, 6, 14, 1, 9, 5, 13, 3, 11, 7, 15][i]
            s = [3, 9, 11, 15][i % 4]
            f = b ^ c ^ d
            a, b, c, d = d, rot((a + f + X[k] + 0x6ed9eba1) & mask, s), b, c
        h = [(x + y) & mask for x, y in zip(h, [a, b, c, d])]
    return struct.pack("<4I", *h)


def send(sock, payload):
    sock.sendall(struct.pack(">I", len(payload)) + payload)


def recv(sock):
    header = sock.recv(4)
    if len(header) < 4:
        raise SystemExit("the server closed the connection")
    length = struct.unpack(">I", header)[0]
    body = b""
    while len(body) < length:
        chunk = sock.recv(length - len(body))
        if not chunk:
            raise SystemExit("the server closed the connection mid-message")
        body += chunk
    return body


def smb2(command, message_id, session_id=0, tree_id=0):
    return (b"\xfeSMB" + struct.pack("<HHIHHIIQIIQ16s",
            64, 0, 0, command, 31, 0, 0, message_id, 0, tree_id, session_id, b""))


def main(host, share, user, password):
    sock = socket.create_connection((host, 445), timeout=10)

    # NEGOTIATE — 2.0.2 through 3.0.2, deliberately not 3.1.1 so there are no
    # negotiate contexts to build.
    dialects = [0x0202, 0x0210, 0x0300, 0x0302]
    body = struct.pack("<HHHHI16sQ", 36, len(dialects), 1, 0, 0, os.urandom(16), 0)
    body += b"".join(struct.pack("<H", d) for d in dialects)
    send(sock, smb2(0x0000, 0) + body)
    reply = recv(sock)
    status = struct.unpack("<I", reply[8:12])[0]
    if status != 0:
        raise SystemExit(f"NEGOTIATE failed: 0x{status:08x}")
    dialect = struct.unpack("<H", reply[68:70])[0]
    print(f"  negotiated SMB dialect 0x{dialect:04x}")

    # SESSION_SETUP, first leg: raw NTLMSSP negotiate.
    flags = 0x1 | 0x4 | 0x200 | 0x8000 | 0x80000 | 0x20000000 | 0x80000000
    type1 = b"NTLMSSP\x00" + struct.pack("<II", 1, flags) + b"\x00" * 16
    body = struct.pack("<HBBIIHHQ", 25, 0, 1, 0, 0, 88, len(type1), 0) + type1
    send(sock, smb2(0x0001, 1) + body)
    reply = recv(sock)
    status = struct.unpack("<I", reply[8:12])[0]
    session_id = struct.unpack("<Q", reply[40:48])[0]
    if status != 0xC0000016:  # MORE_PROCESSING_REQUIRED
        raise SystemExit(f"the server refused the first session setup: 0x{status:08x}")

    start = reply.index(b"NTLMSSP\x00")
    type2 = reply[start:]
    challenge = type2[24:32]
    ti_len, _, ti_off = struct.unpack("<HHI", type2[40:48])
    target_info = type2[ti_off:ti_off + ti_len]

    # NTLMv2.
    nt_hash = md4(password.encode("utf-16-le"))
    ntlmv2 = hmac.new(nt_hash, user.upper().encode("utf-16-le"), md5).digest()
    stamp = struct.pack("<Q", int((time.time() + 11644473600) * 10_000_000))
    blob = (b"\x01\x01\x00\x00" + b"\x00" * 4 + stamp + os.urandom(8) +
            b"\x00" * 4 + target_info + b"\x00" * 4)
    proof = hmac.new(ntlmv2, challenge + blob, md5).digest()
    nt_response = proof + blob

    user_b = user.encode("utf-16-le")
    host_b = b"HOMEBASECHECK".decode().encode("utf-16-le")
    offset = 64
    parts, fields = [], []
    for item in (b"", user_b, host_b, b"\x00" * 24, nt_response, b""):
        parts.append(item)
    lm, dom, usr, wks = b"\x00" * 24, b"", user_b, host_b
    payload = b""
    def field(data):
        nonlocal payload, offset
        f = struct.pack("<HHI", len(data), len(data), offset + len(payload))
        payload += data
        return f
    f_lm, f_nt = field(lm), field(nt_response)
    f_dom, f_usr, f_wks, f_key = field(dom), field(usr), field(wks), field(b"")
    type3 = (b"NTLMSSP\x00" + struct.pack("<I", 3) + f_lm + f_nt + f_dom +
             f_usr + f_wks + f_key + struct.pack("<I", flags) + payload)

    body = struct.pack("<HBBIIHHQ", 25, 0, 1, 0, 0, 88, len(type3), 0) + type3
    send(sock, smb2(0x0001, 2, session_id) + body)
    reply = recv(sock)
    status = struct.unpack("<I", reply[8:12])[0]
    if status != 0:
        raise SystemExit(f"  AUTHENTICATION FAILED: 0x{status:08x}")
    print(f"  signed in as {user}")

    # TREE_CONNECT to the share itself.
    path = f"\\\\{host}\\{share}".encode("utf-16-le")
    body = struct.pack("<HHHH", 9, 0, 72, len(path)) + path
    send(sock, smb2(0x0003, 3, session_id) + body)
    reply = recv(sock)
    status = struct.unpack("<I", reply[8:12])[0]
    if status != 0:
        raise SystemExit(f"  could not open \\\\{host}\\{share}: 0x{status:08x}")
    print(f"  opened \\\\{host}\\{share}")
    sock.close()


if __name__ == "__main__":
    main(*sys.argv[1:5])
