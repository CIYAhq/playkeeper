#!/usr/bin/env python3
"""Records each TLS connection to an address with the host name it asks for
(SNI), then closes it. A release rehearsal sends the names service and
Discord to 127.0.0.1 in the guest's /etc/hosts and runs this on
127.0.0.1:443, so an attempt to reach them is written down here instead of
leaving the lab. Usage: outbound_recorder.py ADDRESS PORT LOGFILE
"""
import socket
import sys
import time


def server_name(hello):
    """The SNI host name in a TLS ClientHello, or "?"."""
    try:
        if hello[0] != 0x16:
            return "?"
        p = 5 + 4 + 2 + 32  # record header, handshake header, version, random
        p += 1 + hello[p]  # session id
        p += 2 + int.from_bytes(hello[p:p + 2], "big")  # cipher suites
        p += 1 + hello[p]  # compression methods
        end = p + 2 + int.from_bytes(hello[p:p + 2], "big")
        p += 2
        while p + 4 <= end:
            kind, size = int.from_bytes(hello[p:p + 2], "big"), int.from_bytes(hello[p + 2:p + 4], "big")
            p += 4
            if kind == 0:  # server_name: list length (2), name type (1), name length (2), name
                n = int.from_bytes(hello[p + 3:p + 5], "big")
                return hello[p + 5:p + 5 + n].decode(errors="replace")
            p += size
    except (IndexError, ValueError):
        pass
    return "?"


def main():
    addr, port, log = sys.argv[1], int(sys.argv[2]), sys.argv[3]
    s = socket.socket()
    s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
    s.bind((addr, port))
    s.listen(16)
    while True:
        conn, _ = s.accept()
        conn.settimeout(5)
        try:
            hello = conn.recv(4096)
        except OSError:
            hello = b""
        with open(log, "a") as f:
            f.write(f"{time.strftime('%Y-%m-%dT%H:%M:%SZ', time.gmtime())} {server_name(hello)}\n")
        conn.close()


if __name__ == "__main__":
    main()
