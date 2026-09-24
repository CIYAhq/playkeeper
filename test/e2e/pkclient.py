#!/usr/bin/env python3
"""Minimal Playkeeper panel API client for end-to-end tests (stdlib only).

TLS is verified against the panel's own certificate (--cacert), never skipped.
Session cookie and CSRF token are kept in a JSON state file between calls.

Examples:
  pkclient.py --url https://127.0.0.1:8443 --cacert cert.pem --state s.json setup CODE admin 'long password'
  pkclient.py ... create --version paper-26.1.2 --memory 1536
  pkclient.py ... wait-online --timeout 900
  pkclient.py ... call GET /api/server
"""
import argparse
import http.client
import json
import os
import ssl
import sys
import time
import urllib.parse


class Client:
    def __init__(self, url, cacert, state_path):
        u = urllib.parse.urlparse(url)
        self.host, self.port = u.hostname, u.port or 443
        self.origin = f"https://{u.netloc}"
        self.ctx = ssl.create_default_context(cafile=cacert)
        self.state_path = state_path
        self.state = {}
        if state_path and os.path.exists(state_path):
            with open(state_path) as f:
                self.state = json.load(f)

    def save(self):
        if self.state_path:
            with open(self.state_path, "w") as f:
                json.dump(self.state, f)

    def _conn(self, timeout=120):
        return http.client.HTTPSConnection(self.host, self.port, context=self.ctx, timeout=timeout)

    def request(self, method, path, body=None, raw=None, headers=None, timeout=120, stream_to=None):
        h = {"Origin": self.origin, "X-Requested-With": "playkeeper"}
        if self.state.get("cookie"):
            h["Cookie"] = self.state["cookie"]
        if self.state.get("csrf") and method not in ("GET", "HEAD"):
            h["X-CSRF-Token"] = self.state["csrf"]
        data = None
        if raw is not None:
            data = raw
            h["Content-Type"] = "application/gzip"
        elif body is not None:
            data = json.dumps(body).encode()
            h["Content-Type"] = "application/json"
        if headers:
            h.update(headers)
        c = self._conn(timeout)
        c.request(method, path, body=data, headers=h)
        r = c.getresponse()
        for k, v in r.getheaders():
            if k.lower() == "set-cookie" and v.startswith("__Host-playkeeper="):
                cookie = v.split(";", 1)[0]
                self.state["cookie"] = cookie if cookie.split("=", 1)[1] else ""
        if stream_to is not None and r.status == 200:
            with open(stream_to, "wb") as f:
                while True:
                    chunk = r.read(1 << 20)
                    if not chunk:
                        break
                    f.write(chunk)
            return r.status, dict(r.getheaders())
        payload = r.read()
        try:
            out = json.loads(payload) if payload else None
        except ValueError:
            out = payload.decode(errors="replace")
        return r.status, out

    def ok(self, method, path, body=None, expect=(200, 202, 204), **kw):
        status, out = self.request(method, path, body, **kw)
        if status not in expect:
            raise SystemExit(f"{method} {path} -> {status}: {json.dumps(out)}")
        return out

    # --- flows ---
    def setup(self, code, username, password):
        out = self.ok("POST", "/api/setup", {"token": code, "username": username, "password": password})
        self.state["csrf"] = out["csrfToken"]
        self.save()
        return out

    def login(self, username, password):
        out = self.ok("POST", "/api/auth/login", {"username": username, "password": password})
        self.state["csrf"] = out["csrfToken"]
        self.save()
        return out

    def wait_op(self, op_id, timeout=900):
        deadline = time.time() + timeout
        last = None
        while time.time() < deadline:
            op = self.ok("GET", f"/api/operations/{op_id}")
            if op.get("phase") != last:
                last = op.get("phase")
                print(f"  [{time.strftime('%H:%M:%S')}] {op['kind']}: {op['status']} {last}", flush=True)
            if op["status"] != "running":
                return op
            time.sleep(2)
        raise SystemExit(f"operation {op_id} still running after {timeout}s")

    def wait_online(self, timeout=900):
        deadline = time.time() + timeout
        while time.time() < deadline:
            st = self.ok("GET", "/api/server")
            if st["phase"] == "online" and st.get("reachable"):
                return st
            time.sleep(3)
        raise SystemExit(f"server not online+reachable after {timeout}s: {st['phase']} {st.get('lastError', '')}")


def main():
    p = argparse.ArgumentParser()
    p.add_argument("--url", required=True)
    p.add_argument("--cacert", required=True)
    p.add_argument("--state", default=".pkclient-state.json")
    sub = p.add_subparsers(dest="cmd", required=True)
    s = sub.add_parser("setup"); s.add_argument("code"); s.add_argument("username"); s.add_argument("password")
    s = sub.add_parser("login"); s.add_argument("username"); s.add_argument("password")
    s = sub.add_parser("create"); s.add_argument("--version", default="paper-26.1.2"); s.add_argument("--memory", type=int, default=0); s.add_argument("--motd", default="Playkeeper test server")
    s = sub.add_parser("wait-online"); s.add_argument("--timeout", type=int, default=900)
    s = sub.add_parser("wait-op"); s.add_argument("id"); s.add_argument("--timeout", type=int, default=900)
    s = sub.add_parser("action"); s.add_argument("verb", choices=["start", "stop", "restart"]); s.add_argument("--wait", action="store_true")
    s = sub.add_parser("command"); s.add_argument("text")
    s = sub.add_parser("invite"); s.add_argument("name")
    s = sub.add_parser("backup"); s.add_argument("--note", default="")
    s = sub.add_parser("download"); s.add_argument("id"); s.add_argument("path")
    s = sub.add_parser("upload"); s.add_argument("path")
    s = sub.add_parser("apply"); s.add_argument("id"); s.add_argument("--confirm", required=True); s.add_argument("--accept-eula", action="store_true"); s.add_argument("--memory", type=int, default=0)
    s = sub.add_parser("call"); s.add_argument("method"); s.add_argument("path"); s.add_argument("body", nargs="?")
    a = p.parse_args()
    c = Client(a.url, a.cacert, a.state)

    def show(x):
        print(json.dumps(x, indent=2) if not isinstance(x, str) else x)

    if a.cmd == "setup":
        show(c.setup(a.code, a.username, a.password))
    elif a.cmd == "login":
        show(c.login(a.username, a.password))
    elif a.cmd == "create":
        cat = c.ok("GET", "/api/catalog")
        mem = a.memory or cat["recommendedMemoryMB"]
        op = c.ok("POST", "/api/server", {"acceptEula": True, "versionId": a.version, "memoryMB": mem, "motd": a.motd})
        op = c.wait_op(op["id"])
        show(op)
        if op["status"] != "succeeded":
            sys.exit(1)
    elif a.cmd == "wait-online":
        show(c.wait_online(a.timeout))
    elif a.cmd == "wait-op":
        show(c.wait_op(a.id, a.timeout))
    elif a.cmd == "action":
        out = c.ok("POST", f"/api/server/{a.verb}", {})
        if a.wait and out and out.get("id"):
            out = c.wait_op(out["id"])
        show(out)
    elif a.cmd == "command":
        show(c.ok("POST", "/api/server/command", {"command": a.text}))
    elif a.cmd == "invite":
        show(c.ok("POST", "/api/server/whitelist", {"name": a.name}))
    elif a.cmd == "backup":
        op = c.ok("POST", "/api/backups", {"note": a.note})
        op = c.wait_op(op["id"])
        show(op)
        if op["status"] != "succeeded":
            sys.exit(1)
    elif a.cmd == "download":
        status, headers = c.request("GET", f"/api/backups/{a.id}/download", stream_to=a.path, timeout=600)
        if status != 200:
            raise SystemExit(f"download failed: {status} {headers}")
        show({"path": a.path, "sha256": headers.get("X-Playkeeper-Sha256") or headers.get("X-Playkeeper-SHA256")})
    elif a.cmd == "upload":
        with open(a.path, "rb") as f:
            data = f.read()
        status, out = c.request("POST", "/api/restore/upload", raw=data, timeout=600)
        show(out)
        if status != 200:
            sys.exit(1)
    elif a.cmd == "apply":
        body = {"confirm": a.confirm, "acceptEula": a.accept_eula}
        if a.memory:
            body["memoryMB"] = a.memory
        op = c.ok("POST", f"/api/restore/{a.id}/apply", body)
        op = c.wait_op(op["id"])
        show(op)
        if op["status"] != "succeeded":
            sys.exit(1)
    elif a.cmd == "call":
        body = json.loads(a.body) if a.body else None
        status, out = c.request(a.method, a.path, body)
        print(status)
        show(out)
    c.save()


if __name__ == "__main__":
    main()
