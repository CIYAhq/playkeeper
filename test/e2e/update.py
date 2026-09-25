#!/usr/bin/env python3
"""Playkeeper update test against an installed Playkeeper (the CI update job).

prepare  on the release installed from GitHub: first-run setup, a server with
         non-default settings, a world marker (a scoreboard value set on the
         console) and a backup. Records them in OUT/before.json. It speaks
         whichever API the installed release has: 0.2's single-server API or
         0.3's servers and machines.
verify   after an upgrade or update: Playkeeper runs VERSION, the admin's
         session and password still work, the Minecraft server kept running
         (same container start) with the same settings and world marker,
         every backup is still listed and verifies, and the audit history from
         before is still there.
update   through the dashboard API: check for updates, see what changed,
         install VERSION, wait for the updater's result and check the outcome
         (updated, or rolled_back with the previous version running again).
"""
import argparse
import http.client
import json
import os
import secrets
import sys
import time

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from pkclient import Client  # noqa: E402

PASSWORD = os.environ.get("PK_ADMIN_PASSWORD") or os.environ.get("PK_PASSWORD") or ""
OBJECTIVE = "pkupdate"
MOTD = "Playkeeper update test"
MAX_PLAYERS = 7
CONFIG_KEYS = ["versionId", "minecraftVersion", "paperBuild", "memoryMB", "heapMB", "levelName", "motd", "maxPlayers",
               "whitelist", "eulaAcceptedAt", "eulaAcceptedBy", "createdAt", "image"]
results = {"checks": []}


def step(msg):
    print(f"\n[{time.strftime('%H:%M:%S')}] {msg}", flush=True)


def check(cond, msg):
    results["checks"].append({"ok": bool(cond), "check": msg})
    if not cond:
        print(f"  FAIL: {msg}", flush=True)
        raise SystemExit(f"update test failed: {msg}")
    print(f"  ok: {msg}", flush=True)


def console(c, cmd):
    return c.ok("POST", "/api/server/command" if c.legacy else c.sp("/command"), {"command": cmd})["output"]


def client(a, legacy=False):
    return Client(a.url, a.cacert, os.path.join(a.out, "state.json"), legacy=legacy)


def load(a, name):
    with open(os.path.join(a.out, name)) as f:
        return json.load(f)


def save(a, name, data):
    with open(os.path.join(a.out, name), "w") as f:
        json.dump(data, f, indent=2)


def agent_version(c):
    if c.legacy:
        return c.status().get("agentVersion")
    return (c.ok("GET", c.mp()).get("live") or {}).get("agentVersion")


def record(c):
    st = c.status()
    backups = c.ok("GET", "/api/backups" if c.legacy else c.sp("/backups"))
    return {
        "agentVersion": agent_version(c),
        "startedAt": st.get("startedAt"),
        "config": {k: st["config"].get(k) for k in CONFIG_KEYS},
        "backups": sorted([b["id"], b["sha256"], b["sizeBytes"]] for b in backups),
        "audit": sorted(f"{e['source']}:{e['id']}:{e['action']}" for e in c.ok("GET", "/api/audit")),
    }


def prepare(a):
    os.makedirs(a.out, exist_ok=True)
    c = client(a, legacy=True)
    step("First-run setup of the release installed from GitHub")
    c.setup(a.code, "admin", PASSWORD)
    status, _ = c.request("GET", "/api/machines")
    c.legacy = status == 404
    cat = c.ok("GET", "/api/catalog" if c.legacy else c.mp("/catalog"))
    version = next(v for v in cat["versions"] if v.get("recommended"))
    step(f"Create a server ({version['label']}) with settings to keep: name {MOTD!r}, at most {MAX_PLAYERS} players")
    if c.legacy:
        op = c.ok("POST", "/api/server", {"acceptEula": True, "versionId": version["id"], "memoryMB": cat["recommendedMemoryMB"],
                                          "motd": MOTD, "maxPlayers": MAX_PLAYERS})
        op = c.wait_op(op["id"], timeout=1200)
    else:
        op = c.create(version["id"], cat["recommendedMemoryMB"], MOTD, max_players=MAX_PLAYERS)
    check(op["status"] == "succeeded", f"server created ({op.get('error', '')})")
    c.wait_online(timeout=600)
    step("Set a world marker on the console, then take a backup")
    nonce = secrets.randbelow(2**31 - 2) + 1
    console(c, f"scoreboard objectives add {OBJECTIVE} dummy")
    out = console(c, f"scoreboard players set marker {OBJECTIVE} {nonce}")
    check(str(nonce) in out, f"marker set: {out.strip()}")
    console(c, "save-all flush")
    op = c.ok("POST", "/api/backups" if c.legacy else c.sp("/backups"), {"note": "before the update"})
    op = c.wait_op(op["id"], timeout=900)
    check(op["status"] == "succeeded", f"backup taken ({op.get('error', '')})")
    c.wait_online(timeout=600)
    before = record(c)
    before["nonce"] = nonce
    check(before["config"]["motd"] == MOTD and before["config"]["maxPlayers"] == MAX_PLAYERS, "the settings are in place")
    check(len(before["backups"]) == 1, f"one backup: {before['backups']}")
    save(a, "before.json", before)
    print(json.dumps(before, indent=2))


def verify(a):
    c = client(a)
    before = load(a, "before.json")
    step(f"Playkeeper {a.expect_version} runs and kept everything")
    me = c.ok("GET", "/api/auth/me")
    check(me.get("version") == a.expect_version, f"the admin's session from before still works; the dashboard runs {me.get('version')}")
    servers = c.servers()
    check(len(servers) == 1, f"the release's server is this install's one server now ({[s['name'] for s in servers]})")
    st = c.wait_online(timeout=300)
    check(agent_version(c) == a.expect_version, f"the agent runs {agent_version(c)}")
    check(st.get("startedAt") == before["startedAt"], f"the Minecraft server kept running (container started at {st.get('startedAt')})")
    check(not st.get("pendingRestart"), "the running server needs no restart")
    now = record(c)
    check(now["config"] == before["config"], f"server settings kept: {now['config']}")
    out = console(c, f"scoreboard players get marker {OBJECTIVE}")
    check(str(before["nonce"]) in out, f"world marker kept: {out.strip()}")
    check(now["backups"] == before["backups"], f"backups kept: {now['backups']}")
    for b in c.ok("GET", c.sp("/backups")):
        v = c.ok("POST", c.sp(f"/backups/{b['id']}/verify"), {})
        check(v.get("verified") is True, f"backup {b['id']} still verifies file by file ({v.get('verifyError', '')})")
    missing = sorted(set(before["audit"]) - set(now["audit"]))
    check(not missing, f"audit history kept ({len(before['audit'])} entries from before; missing: {missing})")
    fresh = Client(a.url, a.cacert, None)
    fresh.login("admin", PASSWORD)
    check(True, "the admin password from before signs in")
    upd = c.ok("GET", c.mp("/update"))
    check(upd.get("current") == a.expect_version and upd.get("supported"), f"updates from the dashboard: {upd.get('supported')} {upd.get('reason', '')}")
    results["after"] = now
    save(a, f"verify-{a.expect_version}.json", results)


def wait_result(c, version, timeout):
    """Waits until the agent reports the updater's result for version. The
    dashboard and agent restart meanwhile, so failed requests are expected."""
    deadline = time.time() + timeout
    last = None
    while time.time() < deadline:
        try:
            status, info = c.request("GET", c.mp("/update"), timeout=15)
        except (OSError, http.client.HTTPException) as e:
            status, info = None, f"{type(e).__name__}: {e}"
        if status == 200 and isinstance(info, dict):
            result = info.get("lastResult") or {}
            if not info.get("installing") and result.get("to") == version:
                return info
            state = f"installing {info['installing']}" if info.get("installing") else f"running {info.get('current')}"
        else:
            state = f"dashboard unavailable ({status or info})"
        if state != last:
            print(f"  [{time.strftime('%H:%M:%S')}] {state}", flush=True)
            last = state
        time.sleep(2)
    raise SystemExit(f"no update result for {version} after {timeout}s")


def update(a):
    c = client(a)
    step(f"The dashboard offers Playkeeper {a.to} and shows what changed")
    info = c.ok("POST", c.mp("/update/check"), {})
    previous = info.get("current")
    check(info.get("latest") == a.to and info.get("available"), f"update found: {info.get('latest')} (checkError: {info.get('checkError', '')})")
    check(bool((info.get("notes") or "").strip()), f"what changed: {info.get('notes')!r}")
    live = c.ok("GET", c.mp()).get("live") or {}
    check(live.get("updateAvailable") == a.to, f"the dashboard's update row names {live.get('updateAvailable')}")
    step(f"Install Playkeeper {a.to} from the dashboard (expected: {a.expect})")
    t0 = time.time()
    op = c.ok("POST", c.mp("/update/apply"), {"version": a.to})
    info = wait_result(c, a.to, a.timeout)
    took = round(time.time() - t0, 1)
    result = info["lastResult"]
    check(result.get("outcome") == a.expect, f"outcome {result.get('outcome')} after {took}s: {result.get('error', '')}")
    done = c.ok("GET", c.mp(f"/operations/{op['id']}"))
    want = "succeeded" if a.expect == "updated" else "failed"
    check(done["status"] == want and done["phase"] == a.expect, f"the update operation is {done['status']} ({done['phase']}): {done.get('error', '')}")
    running = a.to if a.expect == "updated" else previous
    check(info.get("current") == running, f"Playkeeper {info.get('current')} is running")
    if a.expect == "rolled_back":
        check(f"{previous} was put back" in done.get("error", ""), "the dashboard says the previous version was put back")
    results.update({"update": {"from": previous, "to": a.to, "outcome": result.get("outcome"), "seconds": took, "operation": done, "info": info}})
    save(a, f"update-{a.to}.json", results)


def main():
    p = argparse.ArgumentParser()
    p.add_argument("cmd", choices=["prepare", "verify", "update"])
    p.add_argument("--url", required=True)
    p.add_argument("--cacert", required=True)
    p.add_argument("--out", required=True)
    p.add_argument("--code")
    p.add_argument("--expect-version")
    p.add_argument("--to")
    p.add_argument("--expect", choices=["updated", "rolled_back"], default="updated")
    p.add_argument("--timeout", type=int, default=900)
    a = p.parse_args()
    if not PASSWORD:
        raise SystemExit("set PK_ADMIN_PASSWORD")
    {"prepare": prepare, "verify": verify, "update": update}[a.cmd](a)


if __name__ == "__main__":
    main()
