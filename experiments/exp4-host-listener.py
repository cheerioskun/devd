#!/usr/bin/env python3
"""Host listener on 0.0.0.0:<port> for pre-emption testing.

Usage:
    python3 exp4-host-listener.py [port] [--reuseport]

Binds 0.0.0.0:<port> (default 8080). Serves "from-host" on every request.
With --reuseport, sets SO_REUSEPORT on the socket.
"""
import os, socket, sys, time

port = 8080
use_reuseport = False
for arg in sys.argv[1:]:
    if arg == "--reuseport":
        use_reuseport = True
    else:
        port = int(arg)

s = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
if use_reuseport:
    s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEPORT, 1)

try:
    s.bind(("0.0.0.0", port))
except OSError as e:
    print(f"HOST: bind(0.0.0.0:{port}) FAILED: {e}", flush=True)
    sys.exit(1)

s.listen(5)
rp = "yes" if use_reuseport else "no"
print(f"HOST: listening on 0.0.0.0:{port} [SO_REUSEPORT={rp}] PID={os.getpid()} [{time.strftime('%H:%M:%S')}]", flush=True)

while True:
    conn, addr = s.accept()
    try:
        req = conn.recv(4096)
        body = b"from-host\n"
        resp = (
            b"HTTP/1.0 200 OK\r\n"
            b"Content-Length: 10\r\n"
            b"Connection: close\r\n\r\n"
            + body
        )
        conn.sendall(resp)
    except Exception as e:
        print(f"HOST: error {e}", flush=True)
    finally:
        conn.close()
    print(f"HOST: served {addr} [{time.strftime('%H:%M:%S')}]", flush=True)
