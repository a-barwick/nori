#!/usr/bin/env python3
"""
Minimal local LAN/Tailscale TCP forwarder for testing the Nori iOS app on a
physical iPhone against `nori-local` (which intentionally binds 127.0.0.1 only).

Usage:
    python3 ios/scripts/lan-bridge.py --target-port 8088 --listen-port 8089

Then in the Nori iOS app -> More -> Settings -> Local Mac (LAN / Tailscale),
set Server URL to http://<your-mac-lan-or-tailscale-ip>:8089 and tap
"Test Connection" -> "Use This Server".
"""

import argparse
import socket
import threading


def pipe(src: socket.socket, dst: socket.socket) -> None:
    try:
        while True:
            chunk = src.recv(16384)
            if not chunk:
                break
            dst.sendall(chunk)
    except OSError:
        pass
    finally:
        try:
            dst.shutdown(socket.SHUT_WR)
        except OSError:
            pass


def handle_client(client: socket.socket, target_host: str, target_port: int) -> None:
    try:
        upstream = socket.create_connection((target_host, target_port), timeout=10)
        upstream.settimeout(None)
    except OSError:
        client.close()
        return
    t1 = threading.Thread(target=pipe, args=(client, upstream), daemon=True)
    t2 = threading.Thread(target=pipe, args=(upstream, client), daemon=True)
    t1.start()
    t2.start()
    t1.join()
    t2.join()
    client.close()
    upstream.close()


def main() -> None:
    parser = argparse.ArgumentParser(description="Forward LAN/Tailscale traffic to loopback nori-local")
    parser.add_argument("--listen-host", default="0.0.0.0")
    parser.add_argument("--listen-port", type=int, default=8089)
    parser.add_argument("--target-host", default="127.0.0.1")
    parser.add_argument("--target-port", type=int, default=8088)
    args = parser.parse_args()

    srv = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
    srv.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
    srv.bind((args.listen_host, args.listen_port))
    srv.listen(64)
    print(f"Nori iOS LAN bridge listening on {args.listen_host}:{args.listen_port} -> {args.target_host}:{args.target_port}")
    try:
        while True:
            client, _ = srv.accept()
            threading.Thread(
                target=handle_client,
                args=(client, args.target_host, args.target_port),
                daemon=True,
            ).start()
    except KeyboardInterrupt:
        pass
    finally:
        srv.close()


if __name__ == "__main__":
    main()
