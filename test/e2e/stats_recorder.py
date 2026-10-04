#!/usr/bin/env python3
"""Stands in for the stats service (services/stats) in CI, and checks what an
install sent it.

  stats_recorder.py serve PORT LOG     answers reports on 127.0.0.1:PORT with
                                       204, and checks of the machine's port
                                       with "reachable", and appends each to
                                       LOG, one JSON line with its path and
                                       body
  stats_recorder.py check LOG CONFIG VERSION
                                       the installer's started and succeeded
                                       and the agent's heartbeats came under
                                       the ID in CONFIG (config.json), from
                                       VERSION, marked as a test (CI runs in
                                       an Actions job), with the fields
                                       README.md lists and nothing else, one
                                       of them sent as the first account was
                                       made and one as the first server the
                                       core flows started came online; and
                                       each check the Overview asked for
                                       named a game port and nothing else
"""
import json
import sys
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

SYSTEM = {"version", "os", "osVersion", "arch", "source", "channel", "kind", "test"}
INSTALL = SYSTEM | {"id", "event", "step"}
HEARTBEAT = SYSTEM | {"id", "address", "servers", "running", "reached"}
REACHED = {"account", "server", "played", "friends"}


def serve(port, log):
    class Handler(BaseHTTPRequestHandler):
        def do_POST(self):
            body = self.rfile.read(int(self.headers.get("Content-Length", "0")))
            with open(log, "a") as f:
                f.write(json.dumps({"path": self.path, "body": json.loads(body)}) + "\n")
            if self.path == "/v1/reach":
                answer = b'{"result":"reachable"}'
                self.send_response(200)
                self.send_header("Content-Type", "application/json")
                self.send_header("Content-Length", str(len(answer)))
                self.end_headers()
                self.wfile.write(answer)
                return
            self.send_response(204)
            self.end_headers()

        def log_message(self, *args):
            pass

    ThreadingHTTPServer(("127.0.0.1", port), Handler).serve_forever()


def check(log, config, version, source="source"):
    reports = [json.loads(line) for line in open(log)]
    usage_id = json.load(open(config))["usageID"]
    installs = [r["body"] for r in reports if r["path"] == "/v1/install"]
    beats = [r["body"] for r in reports if r["path"] == "/v1/heartbeat"]
    checks = [r["body"] for r in reports if r["path"] == "/v1/reach"]
    assert [r["path"] for r in reports if r["path"] not in ("/v1/install", "/v1/heartbeat", "/v1/reach")] == [], reports
    assert [e["event"] for e in installs] == ["started", "succeeded"], installs
    assert beats, "no heartbeat arrived"
    for r in installs + beats:
        assert r["id"] == usage_id, (r, usage_id)
        assert r["version"] == version and r["kind"] == "dashboard" and r["test"] is True, r
        assert r["os"] == "ubuntu" and r["arch"] in ("amd64", "arm64"), r
        # CI's own builds aren't releases, so they come from the source; the
        # Release check's release build comes from its tarball.
        assert r["source"] == source, (r, source)
    for e in installs:
        assert set(e) <= INSTALL, set(e) - INSTALL
    for b in beats:
        assert set(b) <= HEARTBEAT, set(b) - HEARTBEAT
        assert b["address"] == "ip" and 0 <= b["running"] <= b["servers"], b
        assert "reached" not in b or b["reached"] in REACHED, b
    # The first account and the machine's first server coming online each
    # send one at once, long before the 12 hours are up, saying how far the
    # setup got; the second counts that server running.
    assert any(b.get("reached") == "account" for b in beats), beats
    assert any(b["servers"] >= 1 and b["running"] >= 1 and b.get("reached") == "server" for b in beats), beats
    # A check of the machine's port names the port alone: no ID, nothing else.
    for c in checks:
        assert set(c) == {"port"} and 25565 <= c["port"] <= 26564, c
    print(f"The installer reported started and succeeded, and the agent {len(beats)} heartbeat(s), one as its first account was made and one as its first server came online, under {usage_id}, marked as a test, with only the fields README.md lists; {len(checks)} check(s) of the port named it alone.")


if __name__ == "__main__":
    if sys.argv[1] == "serve":
        serve(int(sys.argv[2]), sys.argv[3])
    else:
        check(*sys.argv[2:6])
