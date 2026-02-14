#!/usr/bin/env python3
"""TCP proxy daemon for Experiment 5.

Binds 0.0.0.0:<listen_port> and proxies each connection to a configurable target.
The target is read from a file on each new connection, so "switching" is just
writing a new target to the file.

Usage:
    python3 exp5-proxy.py <listen_port> <target_file>

target_file format: one line, "host:port" (e.g., "127.0.0.1:9001")

To switch targets:
    echo "127.0.0.1:9002" > target.txt
    # Next connection will go to the new target.
"""
import os, socket, sys, threading, time

def proxy_data(src, dst, label):
    """Copy data from src to dst until EOF."""
    try:
        while True:
            data = src.recv(4096)
            if not data:
                break
            dst.sendall(data)
    except (ConnectionResetError, BrokenPipeError, OSError):
        pass
    finally:
        try: src.close()
        except: pass
        try: dst.close()
        except: pass

def read_target(target_file):
    """Read target host:port from file."""
    with open(target_file, 'r') as f:
        line = f.read().strip()
    host, port = line.rsplit(':', 1)
    return host, int(port)

def handle_connection(client_sock, client_addr, target_file):
    """Handle one incoming connection by proxying to the current target."""
    ts = time.strftime('%H:%M:%S')
    try:
        target_host, target_port = read_target(target_file)
    except Exception as e:
        print(f"PROXY: [{ts}] failed to read target from {target_file}: {e}", flush=True)
        client_sock.close()
        return

    try:
        upstream = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
        upstream.settimeout(5)
        upstream.connect((target_host, target_port))
        upstream.settimeout(None)
    except Exception as e:
        print(f"PROXY: [{ts}] failed to connect to {target_host}:{target_port}: {e}", flush=True)
        client_sock.close()
        return

    print(f"PROXY: [{ts}] {client_addr} → {target_host}:{target_port}", flush=True)

    t1 = threading.Thread(target=proxy_data, args=(client_sock, upstream, "client→upstream"), daemon=True)
    t2 = threading.Thread(target=proxy_data, args=(upstream, client_sock, "upstream→client"), daemon=True)
    t1.start()
    t2.start()
    t1.join()
    t2.join()

def main():
    if len(sys.argv) != 3:
        print(f"Usage: {sys.argv[0]} <listen_port> <target_file>")
        sys.exit(1)

    listen_port = int(sys.argv[1])
    target_file = sys.argv[2]

    # Verify target file exists
    try:
        host, port = read_target(target_file)
        print(f"PROXY: initial target = {host}:{port}", flush=True)
    except Exception as e:
        print(f"PROXY: WARNING: can't read {target_file}: {e}", flush=True)

    s = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
    s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
    # Explicitly NOT setting SO_REUSEPORT — we want to block TSI
    try:
        s.bind(("0.0.0.0", listen_port))
    except OSError as e:
        print(f"PROXY: bind(0.0.0.0:{listen_port}) FAILED: {e}", flush=True)
        sys.exit(1)

    s.listen(16)
    print(f"PROXY: listening on 0.0.0.0:{listen_port} PID={os.getpid()} [{time.strftime('%H:%M:%S')}]", flush=True)

    while True:
        try:
            client_sock, client_addr = s.accept()
            t = threading.Thread(target=handle_connection, args=(client_sock, client_addr, target_file), daemon=True)
            t.start()
        except KeyboardInterrupt:
            break

if __name__ == "__main__":
    main()
