#!/usr/bin/env python3
"""End-to-end scenario against an installed Playkeeper.

host-a: first-run setup, EULA gate, create server, invite and join with two
        protocol bots (offline-mode test harness), place a nonce marker,
        check sessions/charts/console, back up and download the archive.
host-b: on a fresh second host, restore the downloaded archive through the
        guarded restore flow and verify the marker by console and client.

Bots are mineflayer protocol clients, not official Minecraft clients.
"""
import argparse
import hashlib
import json
import os
import secrets
import subprocess
import sys
import time

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from pkclient import Client  # noqa: E402

HERE = os.path.dirname(os.path.abspath(__file__))
BOT = os.path.join(HERE, "bot", "bot.js")
PASSWORD = os.environ.get("PK_ADMIN_PASSWORD", "e2e-" + secrets.token_hex(8))
results = {"checks": []}


def step(msg):
    print(f"\n[{time.strftime('%H:%M:%S')}] {msg}", flush=True)


def check(cond, msg):
    results["checks"].append({"ok": bool(cond), "check": msg})
    if not cond:
        print(f"  FAIL: {msg}", flush=True)
        raise SystemExit(f"scenario failed: {msg}")
    print(f"  ok: {msg}", flush=True)


def bot(args, background=False):
    cmd = ["node", BOT] + args
    if background:
        return subprocess.Popen(cmd, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True)
    r = subprocess.run(cmd, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True, timeout=240)
    print(r.stdout, end="", flush=True)
    return r.returncode


def console(c, cmd):
    return c.ok("POST", "/api/server/command", {"command": cmd})["output"]


def unauthenticated_checks(url, cacert):
    anon = Client(url, cacert, None)
    for method, path in [("GET", "/api/server"), ("POST", "/api/server/start"), ("POST", "/api/server/command"),
                         ("GET", "/api/backups"), ("GET", "/api/backups/20260101-000000-abcdef/download"),
                         ("POST", "/api/restore/upload"), ("GET", "/api/audit")]:
        status, _ = anon.request(method, path, {} if method == "POST" else None)
        check(status in (401, 403), f"{method} {path} without a session is refused ({status})")


def check_marker_console(c, marker):
    gx, gy, gz = marker["gold"]
    sx, sy, sz = marker["sign"]
    console(c, f"forceload add {gx} {gz}")
    time.sleep(2)
    gold = console(c, f"execute if block {gx} {gy} {gz} minecraft:gold_block")
    sign = console(c, f"data get block {sx} {sy} {sz} front_text.messages")
    console(c, f"forceload remove {gx} {gz}")
    check(gold.strip() == "Test passed", f"console: gold block at {gx} {gy} {gz} ({gold.strip()})")
    check(marker["nonce"] in sign, f"console: sign at {sx} {sy} {sz} carries nonce {marker['nonce']} ({sign.strip()})")
    return {"gold": gold.strip(), "sign": sign.strip()}


def sha256_file(path):
    h = hashlib.sha256()
    with open(path, "rb") as f:
        for chunk in iter(lambda: f.read(1 << 20), b""):
            h.update(chunk)
    return h.hexdigest()


def host_a(a):
    out = a.out
    c = Client(a.url, a.cacert, os.path.join(out, "state-a.json"))
    step("Unauthenticated requests fail closed")
    unauthenticated_checks(a.url, a.cacert)

    anon = Client(a.url, a.cacert, None)
    if a.existing:
        step("Sign in (the browser walkthrough already created the admin and the server)")
        c.login("admin", PASSWORD)
        status, body = anon.request("POST", "/api/setup", {"token": a.code, "username": "intruder", "password": "intruder-password"})
        check(status in (403, 409), f"setup cannot be repeated once an admin exists ({status})")
        st = c.wait_online(timeout=300)
        check(st["offlineModeTest"] is True, "test-harness offline mode is visible in the API (bots need it)")
        return host_a_play(a, c, anon)
    step("First-run setup with the installer's one-time code")
    c.setup(a.code, "admin", PASSWORD)
    status, body = anon.request("POST", "/api/setup", {"token": a.code, "username": "intruder", "password": "intruder-password"})
    check(status in (403, 409), f"setup code cannot be reused ({status})")

    step("EULA gate: nothing is created or downloaded before acceptance")
    status, body = c.request("POST", "/api/server", {"acceptEula": False, "versionId": "paper-26.1.2", "memoryMB": 1536})
    check(status == 400 and body.get("code") == "eula_required", f"create without EULA refused ({status} {body.get('code')})")
    st = c.ok("GET", "/api/server")
    check(st["phase"] == "not_created" and not st.get("config"), "no server exists after the refusal")

    step("Create the server (pinned Paper 26.1.2) and wait until it is joinable")
    cat = c.ok("GET", "/api/catalog")
    mem = a.memory or cat["recommendedMemoryMB"]
    t0 = time.time()
    op = c.ok("POST", "/api/server", {"acceptEula": True, "versionId": "paper-26.1.2", "memoryMB": mem, "motd": "Playkeeper E2E"})
    op = c.wait_op(op["id"], timeout=1200)
    check(op["status"] == "succeeded", f"create operation succeeded ({op.get('error', '')})")
    st = c.wait_online(timeout=300)
    results["createSeconds"] = round(time.time() - t0, 1)
    check(st["config"]["memoryMB"] == mem and st["config"]["jarVerifiedAt"], f"memory budget {mem} MB applied and Paper jar checksum verified")
    check(st["offlineModeTest"] is True, "test-harness offline mode is visible in the API (bots need it)")

    step("Invite the two bots through the audited allowlist")
    for name in ("PkBuilder", "PkFriend"):
        r = c.ok("POST", "/api/server/whitelist", {"name": name})
        check(name in r["message"], f"invite {name}: {r['message']}")
    host_a_play(a, c, anon)


def host_a_play(a, c, anon):
    out = a.out
    wl = {w["name"] for w in c.ok("GET", "/api/server/whitelist")}
    check({"PkBuilder", "PkFriend"} <= wl, f"allowlist contains the bots ({sorted(wl)})")

    step("Builder bot joins by the copied address and places the nonce marker")
    nonce = "pk-" + secrets.token_hex(6)
    rc = bot(["place", "--host", a.game_host, "--port", str(a.game_port), "--name", "PkBuilder", "--nonce", nonce,
              "--out", os.path.join(out, "marker.json"), "--panel", a.url, "--cacert", a.cacert, "--state", os.path.join(out, "state-a.json")])
    check(rc == 0, "builder bot placed a gold block and a sign with the nonce")
    marker = json.load(open(os.path.join(out, "marker.json")))

    step("Two bots online at once; the friend's chat imitates a join message")
    friend = bot(["visit", "--host", a.game_host, "--port", str(a.game_port), "--name", "PkFriend", "--stay", "45", "--say", "Foo joined the game"], background=True)
    builder = bot(["visit", "--host", a.game_host, "--port", str(a.game_port), "--name", "PkBuilder", "--stay", "35"], background=True)
    both = None
    for _ in range(40):
        time.sleep(1.5)
        st = c.ok("GET", "/api/server")
        p = st.get("players") or {}
        if p.get("online") == 2:
            both = st
            break
    json.dump(both or st, open(os.path.join(out, "status-two-players.json"), "w"), indent=2)
    check(both is not None and sorted(both["players"]["names"]) == ["PkBuilder", "PkFriend"], "dashboard shows 2 players online: PkBuilder, PkFriend (rcon list)")
    for p in (friend, builder):
        p.wait(timeout=120)
        print(p.stdout.read(), end="")

    step("Sessions, events and charts agree with what happened")
    time.sleep(4)
    sessions = c.ok("GET", "/api/players/sessions?range=1h")["sessions"]
    events = c.ok("GET", "/api/events?limit=200")
    json.dump(sessions, open(os.path.join(out, "sessions.json"), "w"), indent=2)
    json.dump(events, open(os.path.join(out, "events.json"), "w"), indent=2)
    names = {s["player"] for s in sessions}
    check({"PkBuilder", "PkFriend"} <= names, f"sessions recorded for {sorted(names)}")
    check(all(s.get("end") and s["endReason"] == "left" for s in sessions), "every session closed by a real leave event")
    check(not any(e.get("player") == "Foo" for e in events), "chat text 'Foo joined the game' created no event")
    metrics = c.ok("GET", "/api/metrics?range=1h")
    json.dump(metrics, open(os.path.join(out, "metrics-a.json"), "w"), indent=2)
    check(any((b.get("playersMax") or 0) >= 2 for b in metrics["buckets"]), "player chart has a bucket with 2 players")

    step("Console commands are Minecraft-only and audited")
    for text in (";id", "$(id)"):
        o = console(c, text)
        check("Unknown or incomplete command" in o, f"{text!r} reached Minecraft as literal text")
    listing = console(c, "list")
    check(listing.startswith("There are"), f"list: {listing.strip()}")
    marker_before = check_marker_console(c, marker)

    step("Stop-and-archive backup, verification and authenticated download")
    op = c.ok("POST", "/api/backups", {"note": "e2e host A"})
    op = c.wait_op(op["id"], timeout=600)
    check(op["status"] == "succeeded", f"backup operation succeeded ({op.get('error', '')})")
    b = [x for x in c.ok("GET", "/api/backups") if x["kind"] == "manual"][0]
    check(b.get("verified") is True and b["location"] == "on-host", "backup verified and labelled on-host")
    path = os.path.join(out, "world-backup.tar.gz")
    status, headers = anon.request("GET", f"/api/backups/{b['id']}/download", stream_to=path)
    check(status == 401, f"download without a session is refused ({status})")
    status, headers = c.request("GET", f"/api/backups/{b['id']}/download", stream_to=path, timeout=600)
    check(status == 200 and sha256_file(path) == b["sha256"], f"downloaded archive SHA-256 matches the record ({b['sha256'][:16]}…)")
    st = c.wait_online(timeout=300)
    results.update({"marker": marker, "markerConsole": marker_before, "backup": b, "downtimeMs": op["detail"].get("downtimeMs"), "archive": path})
    audit = c.ok("GET", "/api/audit")
    json.dump(audit, open(os.path.join(out, "audit-a.json"), "w"), indent=2)
    actions = {x["action"] for x in audit}
    for want in ("setup", "eula.accepted", "create", "whitelist.add", "console.command", "backup.created", "backup.downloaded"):
        check(want in actions, f"audit log has {want}")


def host_b(a):
    out = a.out
    steps = set(a.steps.split(","))
    c = Client(a.url, a.cacert, os.path.join(out, "state-b.json"))
    if "setup" in steps:
        step("Second host: unauthenticated requests fail closed")
        unauthenticated_checks(a.url, a.cacert)
        step("First-run setup on the second host")
        c.setup(a.code, "admin", PASSWORD)
        st = c.ok("GET", "/api/server")
        check(st["phase"] == "not_created", "fresh host has no server")
        metrics = c.ok("GET", "/api/metrics?range=24h")
        check(all(b["state"] in ("not_collected", "no_data", "offline") for b in metrics["buckets"][:-2]), "fresh host shows no earlier history (no backfill)")
    else:
        c.login("admin", PASSWORD)
    if "refusals" in steps:
        host_b_refusals(a, c)
    if "restore" in steps:
        host_b_restore(a, c)
    if "verify" in steps:
        host_b_verify(a, c)


def host_b_refusals(a, c):
    out = a.out
    step("A corrupted copy of the archive is refused before anything changes")
    data = bytearray(open(a.archive, "rb").read())
    data[len(data) // 2] ^= 0xFF
    status, body = c.request("POST", "/api/restore/upload", raw=bytes(data), timeout=600)
    check(status == 422, f"flipped-byte archive refused ({status}: {body.get('error', '')[:80] if isinstance(body, dict) else body})")

    step("Upload the archive (moved as a file) and review the preview")
    status, preview = c.request("POST", "/api/restore/upload", raw=open(a.archive, "rb").read(), timeout=600)
    json.dump(preview, open(os.path.join(out, "restore-preview.json"), "w"), indent=2)
    check(status == 200 and preview["compatible"], "archive validated file by file")
    check(preview["sha256"] == a.expect_sha256, f"archive SHA-256 on host B matches host A ({preview['sha256'][:16]}…)")
    check(preview["needsEula"] and not preview["currentWorld"]["exists"], "preview asks for the EULA and shows no existing world")
    status, body = c.request("POST", f"/api/restore/{preview['id']}/apply", {"confirm": preview["confirmPhrase"], "acceptEula": False})
    check(status == 400 and body.get("code") == "eula_required", "restore without EULA acceptance refused")
    c.ok("DELETE", f"/api/restore/{preview['id']}")
    st = c.ok("GET", "/api/server")
    check(st["phase"] == "not_created", "refusals left the host unchanged (still no server)")


def host_b_restore(a, c):
    out = a.out
    step("Upload the archive (moved as a file) for the restore")
    status, preview = c.request("POST", "/api/restore/upload", raw=open(a.archive, "rb").read(), timeout=600)
    check(status == 200 and preview["compatible"] and preview["sha256"] == a.expect_sha256, "archive validated; SHA-256 matches host A")

    step("Apply the restore and wait until the server is joinable")
    t0 = time.time()
    op = c.ok("POST", f"/api/restore/{preview['id']}/apply", {"confirm": preview["confirmPhrase"], "acceptEula": True})
    op = c.wait_op(op["id"], timeout=1200)
    check(op["status"] == "succeeded", f"restore succeeded ({op.get('error', '')})")
    c.wait_online(timeout=300)
    results["restoreSeconds"] = round(time.time() - t0, 1)


def host_b_verify(a, c):
    c.wait_online(timeout=600)
    step("The distinctive world state survived: console and client view")
    marker = json.load(open(a.marker))
    results["markerConsole"] = check_marker_console(c, marker)
    rc = bot(["verify", "--host", a.game_host, "--port", str(a.game_port), "--name", "PkBuilder", "--marker", a.marker])
    check(rc == 0, f"client view: gold block and sign with nonce {marker['nonce']} at the recorded coordinates")
    results["marker"] = marker


def main():
    p = argparse.ArgumentParser()
    p.add_argument("role", choices=["host-a", "host-b"])
    p.add_argument("--url", required=True)
    p.add_argument("--cacert", required=True)
    p.add_argument("--code", required=True, help="one-time setup code printed by the installer")
    p.add_argument("--game-host", required=True)
    p.add_argument("--game-port", type=int, default=25565)
    p.add_argument("--memory", type=int, default=0)
    p.add_argument("--out", required=True)
    p.add_argument("--archive")
    p.add_argument("--marker")
    p.add_argument("--expect-sha256")
    p.add_argument("--existing", action="store_true", help="host-a: admin and server were created in the browser")
    p.add_argument("--steps", default="setup,refusals,restore,verify", help="host-b steps to run")
    a = p.parse_args()
    os.makedirs(a.out, exist_ok=True)
    started = time.time()
    try:
        host_a(a) if a.role == "host-a" else host_b(a)
        results["ok"] = True
    finally:
        results["seconds"] = round(time.time() - started, 1)
        results.setdefault("ok", False)
        with open(os.path.join(a.out, f"{a.role}-results.json"), "w") as f:
            json.dump(results, f, indent=2)
        print(f"\n{a.role}: {'PASSED' if results['ok'] else 'FAILED'} ({sum(1 for x in results['checks'] if x['ok'])} checks ok) in {results['seconds']} s")


if __name__ == "__main__":
    main()
