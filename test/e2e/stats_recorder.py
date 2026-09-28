#!/usr/bin/env python3
"""Stands in for the stats service (services/stats) in CI, and checks what an
install sent it.

  stats_recorder.py serve PORT LOG     answers reports on 127.0.0.1:PORT with
                                       204 and appends each to LOG, one JSON
                                       line with its path and body
  stats_recorder.py check LOG CONFIG VERSION
                                       the installer's started and succeeded
                                       and the agent's first heartbeat came
                                       under the ID in CONFIG (config.json),
                                       from VERSION, marked as a test (CI runs
                                       in an Actions job), with the fields
                                       README.md lists and nothing else
"""
import json
import sys
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

SYSTEM = {"version", "os", "osVersion", "arch", "source", "channel", "kind", "test"}
INSTALL = SYSTEM | {"id", "event", "step"}
HEARTBEAT = SYSTEM | {"id", "address", "servers", "running"}


def serve(port, log):
    class Handler(BaseHTTPRequestHandler):
        def do_POST(self):
            body = self.rfile.read(int(self.headers.get("Content-Length", "0")))
            with open(log, "a") as f:
                f.write(json.dumps({"path": self.path, "body": json.loads(body)}) + "\n")
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
    assert [r["path"] for r in reports if r["path"] not in ("/v1/install", "/v1/heartbeat")] == [], reports
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
    print(f"The installer reported started and succeeded, and the agent {len(beats)} heartbeat(s), under {usage_id}, marked as a test, with only the fields README.md lists.")


if __name__ == "__main__":
    if sys.argv[1] == "serve":
        serve(int(sys.argv[2]), sys.argv[3])
    else:
        check(*sys.argv[2:6])
