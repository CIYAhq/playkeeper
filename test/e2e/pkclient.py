#!/usr/bin/env python3
"""Minimal Playkeeper panel API client for end-to-end tests (stdlib only).

TLS is verified against the panel's own certificate (--cacert), never skipped.
Session cookie, CSRF token and the machine and server in use are kept in a
JSON state file between calls.

Playkeeper 0.3 routes each server under /api/servers/{id} and machine-wide
calls under /api/machines/{id}. `legacy` speaks 0.2's single-server API, for
tests that start on an older release.

Examples:
  pkclient.py --url https://127.0.0.1:8443 --cacert cert.pem --state s.json setup CODE admin 'long password'
  pkclient.py ... create --version paper-26.1.2 --memory 1536
  pkclient.py ... wait-online --timeout 900
  pkclient.py ... call GET '/api/servers/{server}/logs?limit=50'   # {server} and {machine} are filled in
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
    def __init__(self, url, cacert, state_path, legacy=False):
        u = urllib.parse.urlparse(url)
        self.host, self.port = u.hostname, u.port or 443
        self.origin = f"https://{u.netloc}"
        self.ctx = ssl.create_default_context(cafile=cacert)
        self.state_path = state_path
        self.legacy = legacy
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

    # --- where things are ---
    def machine_id(self):
        if not self.state.get("machine"):
            self.state["machine"] = self.ok("GET", "/api/machines")[0]["id"]
            self.save()
        return self.state["machine"]

    def servers(self):
        return self.ok("GET", "/api/servers")

    def server_id(self):
        """The server these calls are about: the one set with use_server, or the first."""
        if not self.state.get("server"):
            servers = self.servers()
            if not servers:
                raise SystemExit("there is no server yet")
            self.use_server(servers[0]["id"])
        return self.state["server"]

    def use_server(self, sid):
        self.state["server"] = sid
        self.save()

    def sp(self, rest=""):
        """A path under the current server."""
        return f"/api/servers/{self.server_id()}{rest}"

    def mp(self, rest=""):
        """A path under this dashboard's machine."""
        return f"/api/machines/{self.machine_id()}{rest}"

    def status(self):
        return self.ok("GET", "/api/server" if self.legacy else self.sp())

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
        path = f"/api/operations/{op_id}" if self.legacy else self.mp(f"/operations/{op_id}")
        deadline = time.time() + timeout
        last = None
        while time.time() < deadline:
            op = self.ok("GET", path)
            if op.get("phase") != last:
                last = op.get("phase")
                print(f"  [{time.strftime('%H:%M:%S')}] {op['kind']}: {op['status']} {last}", flush=True)
            if op["status"] != "running":
                return op
            time.sleep(2)
        raise SystemExit(f"operation {op_id} still running after {timeout}s")

    def wait_online(self, timeout=900):
        deadline = time.time() + timeout
        st = {}
        while time.time() < deadline:
            st = self.status()
            if st["phase"] == "online" and st.get("reachable"):
                return st
            time.sleep(3)
        raise SystemExit(f"server not online+reachable after {timeout}s: {st.get('phase')} {st.get('lastError', '')}")

    def create(self, version, memory=0, motd="Playkeeper test server", name="", max_players=0):
        """Creates a server, remembers it as the current one and waits for the create to finish."""
        cat = self.ok("GET", self.mp("/catalog"))
        body = {"acceptEula": True, "versionId": version, "memoryMB": memory or cat["recommendedMemoryMB"], "motd": motd}
        if name:
            body["name"] = name
        if max_players:
            body["maxPlayers"] = max_players
        op = self.ok("POST", self.mp("/servers"), body)
        self.use_server(op["serverId"])
        return self.wait_op(op["id"], timeout=1200)


def main():
    p = argparse.ArgumentParser()
    p.add_argument("--url", required=True)
    p.add_argument("--cacert", required=True)
    p.add_argument("--state", default=".pkclient-state.json")
    sub = p.add_subparsers(dest="cmd", required=True)
    s = sub.add_parser("setup"); s.add_argument("code"); s.add_argument("username"); s.add_argument("password")
    s = sub.add_parser("login"); s.add_argument("username"); s.add_argument("password")
    s = sub.add_parser("create"); s.add_argument("--version", default="paper-26.1.2"); s.add_argument("--memory", type=int, default=0); s.add_argument("--motd", default="Playkeeper test server"); s.add_argument("--name", default="")
    s = sub.add_parser("wait-online"); s.add_argument("--timeout", type=int, default=900)
    s = sub.add_parser("wait-op"); s.add_argument("id"); s.add_argument("--timeout", type=int, default=900)
    s = sub.add_parser("action"); s.add_argument("verb", choices=["start", "stop", "restart"]); s.add_argument("--wait", action="store_true")
    s = sub.add_parser("command"); s.add_argument("text")
    s = sub.add_parser("invite"); s.add_argument("name")
    s = sub.add_parser("backup"); s.add_argument("--note", default="")
    s = sub.add_parser("download"); s.add_argument("id"); s.add_argument("path")
    s = sub.add_parser("upload"); s.add_argument("path"); s.add_argument("--new-server", action="store_true", help="restore as a new server instead of replacing the current one")
    s = sub.add_parser("apply"); s.add_argument("id"); s.add_argument("--confirm", required=True); s.add_argument("--accept-eula", action="store_true"); s.add_argument("--memory", type=int, default=0); s.add_argument("--name", default="")
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
        op = c.create(a.version, a.memory, a.motd, a.name)
        show(op)
        if op["status"] != "succeeded":
            sys.exit(1)
    elif a.cmd == "wait-online":
        show(c.wait_online(a.timeout))
    elif a.cmd == "wait-op":
        show(c.wait_op(a.id, a.timeout))
    elif a.cmd == "action":
        out = c.ok("POST", c.sp(f"/{a.verb}"), {})
        if a.wait and out and out.get("id"):
            out = c.wait_op(out["id"])
        show(out)
    elif a.cmd == "command":
        show(c.ok("POST", c.sp("/command"), {"command": a.text}))
    elif a.cmd == "invite":
        show(c.ok("POST", c.sp("/whitelist"), {"name": a.name}))
    elif a.cmd == "backup":
        op = c.ok("POST", c.sp("/backups"), {"note": a.note})
        op = c.wait_op(op["id"])
        show(op)
        if op["status"] != "succeeded":
            sys.exit(1)
    elif a.cmd == "download":
        status, headers = c.request("GET", c.sp(f"/backups/{a.id}/download"), stream_to=a.path, timeout=600)
        if status != 200:
            raise SystemExit(f"download failed: {status} {headers}")
        show({"path": a.path, "sha256": headers.get("X-Playkeeper-Sha256") or headers.get("X-Playkeeper-SHA256")})
    elif a.cmd == "upload":
        with open(a.path, "rb") as f:
            data = f.read()
        path = c.mp("/restore/upload") if a.new_server or not c.servers() else c.sp("/restore/upload")
        status, out = c.request("POST", path, raw=data, timeout=600)
        show(out)
        if status != 200:
            sys.exit(1)
    elif a.cmd == "apply":
        body = {"confirm": a.confirm, "acceptEula": a.accept_eula}
        if a.memory:
            body["memoryMB"] = a.memory
        if a.name:
            body["name"] = a.name
        op = c.ok("POST", c.mp(f"/restore/{a.id}/apply"), body)
        if op.get("serverId"):
            c.use_server(op["serverId"])
        op = c.wait_op(op["id"])
        show(op)
        if op["status"] != "succeeded":
            sys.exit(1)
    elif a.cmd == "call":
        body = json.loads(a.body) if a.body else None
        path = a.path
        if "{server}" in path:
            path = path.replace("{server}", c.server_id())
        if "{machine}" in path:
            path = path.replace("{machine}", c.machine_id())
        status, out = c.request(a.method, path, body)
        print(status)
        show(out)
    c.save()


if __name__ == "__main__":
    main()
