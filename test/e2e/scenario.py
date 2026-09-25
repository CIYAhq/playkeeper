#!/usr/bin/env python3
"""End-to-end scenario against an installed Playkeeper.

host-a: first-run setup, EULA gate, create server, invite and join with two
        protocol bots (offline-mode test harness), place a nonce marker,
        check sessions/charts/console, back up with a player online, check
        the archive independently, restore on the same host and roll back,
        and exercise the controls, CSRF protection, settings and audit log.
host-b: on a fresh second host, restore the downloaded archive through the
        guarded restore flow and verify the marker by console and client;
        the "tamper" step feeds damaged archives to a host with a live world.

Bots are mineflayer protocol clients, not official Minecraft clients.
"""
import argparse
import hashlib
import io
import json
import os
import secrets
import subprocess
import sys
import tarfile
import tempfile
import threading
import time
from datetime import datetime

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from pkclient import Client  # noqa: E402

HERE = os.path.dirname(os.path.abspath(__file__))
BOT = os.path.join(HERE, "bot", "bot.js")
PASSWORD = os.environ.get("PK_ADMIN_PASSWORD") or os.environ.get("PK_PASSWORD") or "e2e-" + secrets.token_hex(8)
BUILDER, FRIEND = "PkBotBuilder", "PkBotFriend"
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
    return c.ok("POST", c.sp("/command"), {"command": cmd})["output"]


def ts(s):
    return datetime.fromisoformat(s.replace("Z", "+00:00"))


def unauthenticated_checks(url, cacert):
    anon = Client(url, cacert, None)
    some = "/api/servers/abcdefghjk"
    for method, path in [("GET", "/api/servers"), ("GET", "/api/machines"), ("POST", f"{some}/start"), ("POST", f"{some}/command"),
                         ("GET", f"{some}/backups"), ("GET", f"{some}/backups/20260101-000000-abcdef/download"),
                         ("POST", "/api/machines/abcdefghjk/restore/upload"), ("GET", "/api/audit"), ("GET", "/api/players/Notch/head")]:
        status, _ = anon.request(method, path, {} if method == "POST" else None)
        check(status in (401, 403), f"{method} {path} without a session is refused ({status})")


def block_is(c, pos, block):
    return console(c, f"execute if block {pos[0]} {pos[1]} {pos[2]} minecraft:{block}").strip()


def check_marker_console(c, marker):
    gx, gy, gz = marker["gold"]
    sx, sy, sz = marker["sign"]
    console(c, f"forceload add {gx} {gz}")
    time.sleep(2)
    gold = block_is(c, marker["gold"], "gold_block")
    sign = console(c, f"data get block {sx} {sy} {sz} front_text.messages")
    console(c, f"forceload remove {gx} {gz}")
    check(gold == "Test passed", f"console: gold block at {gx} {gy} {gz} ({gold})")
    check(marker["nonce"] in sign, f"console: sign at {sx} {sy} {sz} carries nonce {marker['nonce']} ({sign.strip()})")
    return {"gold": gold, "sign": sign.strip()}


def sha256_file(path):
    h = hashlib.sha256()
    with open(path, "rb") as f:
        for chunk in iter(lambda: f.read(1 << 20), b""):
            h.update(chunk)
    return h.hexdigest()


def verify_archive_independently(path):
    """Recomputes every file's SHA-256 from the archive with Python's tarfile
    and hashlib and compares it with the archive's own manifest."""
    with tarfile.open(path, "r:gz") as tf:
        members = {m.name: m for m in tf.getmembers() if m.isfile()}
        manifest = json.load(tf.extractfile(members["playkeeper-backup/manifest.json"]))
        bad = []
        for f in manifest["files"]:
            data = tf.extractfile(members["playkeeper-backup/data/" + f["path"]]).read()
            if hashlib.sha256(data).hexdigest() != f["sha256"] or len(data) != f["size"]:
                bad.append(f["path"])
    return manifest, bad


def wait_idle(c, timeout=300):
    deadline = time.time() + timeout
    while time.time() < deadline:
        st = c.status()
        if not st.get("operation"):
            return st
        time.sleep(2)
    raise SystemExit("an operation is still running")


def restore_backup(c, backup_id, label):
    """Stages a backup for restore, refuses a wrong confirmation, then applies it."""
    status, preview = c.request("POST", c.sp(f"/backups/{backup_id}/restore"), {})
    check(status == 200 and preview["compatible"], f"{label}: backup validated file by file before anything changed")
    check(preview["currentWorld"]["exists"] and preview["willCreateRollback"] and preview["confirmPhrase"] == "replace world",
          f"{label}: preview shows the world to be replaced, a rollback archive, and the phrase {preview['confirmPhrase']!r}")
    status, body = c.request("POST", c.mp(f"/restore/{preview['id']}/apply"), {"confirm": "replace wrold"})
    check(status == 400, f"{label}: a mistyped confirmation is refused ({status})")
    t0 = time.time()
    op = c.ok("POST", c.mp(f"/restore/{preview['id']}/apply"), {"confirm": preview["confirmPhrase"]})
    op = c.wait_op(op["id"], timeout=900)
    check(op["status"] == "succeeded", f"{label}: restore succeeded ({op.get('error', '')})")
    c.wait_online(timeout=300)
    return preview, op, round(time.time() - t0, 1)


def host_a(a):
    c = Client(a.url, a.cacert, os.path.join(a.out, "state-a.json"))
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
    status, body = c.request("POST", c.mp("/servers"), {"acceptEula": False, "versionId": "paper-26.1.2", "memoryMB": 1536})
    check(status == 400 and body.get("code") == "eula_required", f"create without EULA refused ({status} {body.get('code')})")
    check(c.servers() == [], "no server exists after the refusal")

    step("Create the server (pinned Paper 26.1.2) and wait until it is joinable")
    cat = c.ok("GET", c.mp("/catalog"))
    mem = a.memory or cat["recommendedMemoryMB"]
    t0 = time.time()
    op = c.create("paper-26.1.2", mem, "Playkeeper E2E", name="E2E")
    check(op["status"] == "succeeded", f"create operation succeeded ({op.get('error', '')})")
    st = c.wait_online(timeout=300)
    results["createSeconds"] = round(time.time() - t0, 1)
    check(st["config"]["memoryMB"] == mem and st["config"]["jarVerifiedAt"], f"memory budget {mem} MB applied and Paper jar checksum verified")
    check(st["offlineModeTest"] is True, "test-harness offline mode is visible in the API (bots need it)")

    step("Invite the two bots through the audited allowlist")
    for name in (BUILDER, FRIEND):
        r = c.ok("POST", c.sp("/whitelist"), {"name": name})
        check(name in r["message"], f"invite {name}: {r['message']}")
    host_a_play(a, c, anon)


def host_a_play(a, c, anon):
    out = a.out
    game = ["--host", a.game_host, "--port", str(a.game_port)]
    wl = {w["name"] for w in c.ok("GET", c.sp("/whitelist"))}
    check({BUILDER, FRIEND} <= wl, f"allowlist contains the bots ({sorted(wl)})")

    step("Builder bot joins by the copied address and places the nonce marker")
    nonce = "pk-" + secrets.token_hex(6)
    rc = bot(["place"] + game + ["--name", BUILDER, "--nonce", nonce, "--out", os.path.join(out, "marker.json"),
                                 "--panel", a.url, "--cacert", a.cacert, "--state", os.path.join(out, "state-a.json")])
    check(rc == 0, "builder bot placed a gold block and a sign with the nonce")
    marker = json.load(open(os.path.join(out, "marker.json")))

    step("Two bots online at once; the friend's chat imitates a join message")
    # Both bots connect from the test host's single address, and Paper throttles
    # repeat connections from one address within 4 s, so the joins are staggered.
    # The friend stays through the online backup below; the stopped one disconnects it.
    friend = bot(["visit"] + game + ["--name", FRIEND, "--stay", "420", "--say", "Foo joined the game"], background=True)
    time.sleep(6)
    builder = bot(["visit"] + game + ["--name", BUILDER, "--stay", "40"], background=True)
    both = None
    for _ in range(40):
        time.sleep(1.5)
        st = c.status()
        p = st.get("players") or {}
        if p.get("online") == 2:
            both = st
            break
    json.dump(both or st, open(os.path.join(out, "status-two-players.json"), "w"), indent=2)
    check(both is not None and sorted(both["players"]["names"]) == [BUILDER, FRIEND], f"dashboard shows 2 players online: {BUILDER}, {FRIEND} (rcon list)")
    builder.wait(timeout=120)
    print(builder.stdout.read(), end="")

    step("Sessions, events and charts agree with what happened")
    time.sleep(4)
    sessions = c.ok("GET", c.sp("/players/sessions?range=1h"))["sessions"]
    events = c.ok("GET", c.sp("/events?limit=200"))
    names = {s["player"] for s in sessions}
    check({BUILDER, FRIEND} <= names, f"sessions recorded for {sorted(names)}")
    check(all(s.get("end") and s["endReason"] == "left" for s in sessions if s["player"] == BUILDER), f"every {BUILDER} session closed by a real leave event")
    check(any(s["player"] == FRIEND and not s.get("end") for s in sessions), f"{FRIEND}'s session is open while it is still online")
    check(not any(e.get("player") == "Foo" for e in events), "chat text 'Foo joined the game' created no event")
    metrics = c.ok("GET", c.sp("/metrics?range=1h"))
    json.dump(metrics, open(os.path.join(out, "metrics-a.json"), "w"), indent=2)
    check(any((b.get("playersMax") or 0) >= 2 for b in metrics["buckets"]), "player chart has a bucket with 2 players")

    step("Console commands are Minecraft-only and audited")
    for text in (";id", "$(id)"):
        o = console(c, text)
        check("Unknown or incomplete command" in o, f"{text!r} reached Minecraft as literal text")
    listing = console(c, "list")
    check(listing.startswith("There are 1 of a max") and FRIEND in listing, f"list: {listing.strip()}")
    marker_before = check_marker_console(c, marker)

    step(f"Online backup while {FRIEND} is online: nobody is disconnected, saving paused only while copying, verification")
    op = c.ok("POST", c.sp("/backups"), {"note": "e2e host A"})
    op = c.wait_op(op["id"], timeout=600)
    check(op["status"] == "succeeded", f"online backup operation succeeded ({op.get('error', '')})")
    d = op["detail"]
    check(d.get("method") in ("online_copy", "online_in_place") and d.get("downtimeMs") == 0,
          f"backed up with the server running ({d.get('method')}, downtime {d.get('downtimeMs')} ms)")
    check(0 < d.get("savingPausedMs", 0) <= d.get("durationMs", 0),
          f"world saving was paused for {d.get('savingPausedMs')} ms of the {d.get('durationMs')} ms backup")
    st = c.status()
    check(FRIEND in ((st.get("players") or {}).get("names") or []) and st["phase"] == "online", f"{FRIEND} is still online after the backup")
    check(not st.get("savingPausedSince"), "world saving is not left paused")
    saving = console(c, "save-on")
    check("already turned on" in saving, f"the server confirms saving is on ({saving.strip()})")
    lines = c.ok("GET", c.sp("/logs?limit=2000"))["lines"]
    online_window = [ln for ln in lines if ts(op["startedAt"]) <= ts(ln["ts"]) <= ts(op["finishedAt"])]
    check(not any("Stopping server" in ln["text"] or "lost connection" in ln["text"] for ln in online_window), "the log shows no stop and no lost connection during the backup")
    fs = [s for s in c.ok("GET", c.sp("/players/sessions?range=1h"))["sessions"] if s["player"] == FRIEND]
    check(any(not s.get("end") for s in fs), f"{FRIEND}'s session stays open through the backup")
    b = next((x for x in c.ok("GET", c.sp("/backups")) if x["id"] == d.get("backupId")), None)
    check(b is not None and b.get("verified") is True and b["location"] == "on-host" and b.get("method") == d.get("method"),
          "online backup verified, labelled on-host, and recorded with its method")
    online = {"method": d.get("method"), "savingPausedMs": d.get("savingPausedMs"), "durationMs": d.get("durationMs")}

    step(f"Backup with the server stopped while {FRIEND} is online: graceful stop, recorded downtime, verification")
    op = c.ok("POST", c.sp("/backups"), {"note": "e2e host A, stopped", "stopped": True})
    op = c.wait_op(op["id"], timeout=600)
    check(op["status"] == "succeeded", f"backup operation succeeded ({op.get('error', '')})")
    check(op["detail"].get("method") == "stopped", f"backed up with the server stopped ({op['detail'].get('method')})")
    friend.wait(timeout=120)
    friend_log = friend.stdout.read()
    print(friend_log, end="")
    check("connection ended" in friend_log, f"{FRIEND} was disconnected by the stop")
    stopped = next((x for x in c.ok("GET", c.sp("/backups")) if x["id"] == op["detail"].get("backupId")), None)
    check(stopped is not None and stopped.get("verified") is True and stopped["location"] == "on-host", "backup verified and labelled on-host")
    lines = c.ok("GET", c.sp("/logs?limit=2000"))["lines"]
    t_start, t_end = ts(op["startedAt"]), ts(op["finishedAt"])
    window = [ln for ln in lines if t_start <= ts(ln["ts"]) <= t_end]
    shutdown = ["Stopping server", "Saving players", "Saving worlds", "All dimensions are saved"]
    shown = [ln for ln in window if any(k in ln["text"] for k in shutdown + ["lost connection", "left the game", "Done ("])]
    for ln in shown:
        print(f"    {ln['ts'][11:23]}  {ln['text']}")
    texts = [ln["text"] for ln in window]
    pos, in_order = 0, True
    for k in shutdown:
        pos = next((i for i in range(pos, len(texts)) if k in texts[i]), None)
        if pos is None:
            in_order = False
            break
    stop_line = next((ln for ln in window if "Stopping server" in ln["text"]), None)
    done_line = next((ln for ln in window if "Done (" in ln["text"] and stop_line and ts(ln["ts"]) > ts(stop_line["ts"])), None)
    check(in_order, "server log shows a graceful stop: " + " → ".join(shutdown))
    log_span = (ts(done_line["ts"]) - ts(stop_line["ts"])).total_seconds() if done_line else None
    downtime = op["detail"].get("downtimeMs")
    check(log_span is not None and downtime and 0 <= downtime / 1000 - log_span < 10,
          f"recorded downtime {downtime / 1000:.1f} s covers the logged stop-to-ready span {log_span:.1f} s (it also includes the save and the reachability check)")
    time.sleep(3)
    fs = [s for s in c.ok("GET", c.sp("/players/sessions?range=1h"))["sessions"] if s["player"] == FRIEND]
    check(fs and all(s.get("end") and not s["endUncertain"] for s in fs), f"{FRIEND}'s session closed at the graceful stop ({fs[-1].get('endReason') if fs else None})")

    step("Authenticated download of the online backup, and an independent check of the archive")
    path = os.path.join(out, "world-backup.tar.gz")
    download = c.sp(f"/backups/{b['id']}/download")
    status, headers = anon.request("GET", download, stream_to=path)
    check(status == 401, f"download without a session is refused ({status})")
    status, headers = c.request("GET", download, stream_to=path, timeout=600)
    check(status == 200 and sha256_file(path) == b["sha256"], f"downloaded archive SHA-256 matches the record ({b['sha256'][:16]}…)")
    manifest, bad = verify_archive_independently(path)
    check(not bad and len(manifest["files"]) == b["fileCount"],
          f"all {len(manifest['files'])} files match the manifest's SHA-256 values (recomputed with Python hashlib, not Playkeeper)")
    st = c.wait_online(timeout=300)
    results.update({"marker": marker, "markerConsole": marker_before, "backup": b, "onlineBackup": online, "stoppedBackup": stopped,
                    "downtimeMs": downtime, "logStopToReadySeconds": log_span, "archive": path})

    step("Restore on the same host: preview, typed confirmation, rollback archive, and rolling back")
    gx, gy, gz = marker["gold"]
    diamond = [gx + 1, gy, gz]
    console(c, f"forceload add {gx} {gz}")
    time.sleep(2)
    console(c, f"setblock {diamond[0]} {diamond[1]} {diamond[2]} minecraft:diamond_block")
    check(block_is(c, diamond, "diamond_block") == "Test passed", f"a later change: diamond block placed at {diamond[0]} {diamond[1]} {diamond[2]} after the backup")
    console(c, f"forceload remove {gx} {gz}")
    preview, op, secs = restore_backup(c, b["id"], "restore the backup")
    rollback_id = op["detail"].get("rollbackBackupId")
    check(rollback_id, f"a rollback archive of the replaced world was saved first ({rollback_id})")
    check_marker_console(c, marker)
    console(c, f"forceload add {gx} {gz}")
    time.sleep(2)
    check(block_is(c, diamond, "diamond_block") == "Test failed", "the diamond block placed after the backup is gone")
    console(c, f"forceload remove {gx} {gz}")
    _, op2, secs2 = restore_backup(c, rollback_id, "restore the rollback archive")
    console(c, f"forceload add {gx} {gz}")
    time.sleep(2)
    check(block_is(c, diamond, "diamond_block") == "Test passed", "restoring the rollback archive brought the diamond block back")
    console(c, f"forceload remove {gx} {gz}")
    results["sameHostRestore"] = {"previewNotRestored": preview.get("notRestored"), "restoreSeconds": secs, "rollbackRestoreSeconds": secs2, "rollbackBackupId": rollback_id}

    step("Controls are idempotent, serialized and state-aware")
    status, body = c.request("POST", c.sp("/start"), {})
    check(status == 200 and body.get("noop") is True, f"start while running is a no-op ({status})")
    codes = {}

    def fire(verb):
        codes[verb] = c.request("POST", c.sp(f"/{verb}"), {})[0]
    threads = [threading.Thread(target=fire, args=(v,)) for v in ("start", "stop")]
    for t in threads:
        t.start()
    for t in threads:
        t.join()
    st = wait_idle(c)
    consistent = (st["desired"] == "stopped" and st["phase"] == "stopped") or (st["desired"] == "running" and st["phase"] in ("online", "starting"))
    check(all(v in (200, 202, 409) for v in codes.values()) and consistent,
          f"concurrent start and stop: responses {codes}, then desired={st['desired']} phase={st['phase']}")
    if st["desired"] == "running":
        c.wait_online(timeout=300)
        op = c.ok("POST", c.sp("/stop"), {})
        c.wait_op(op["id"], timeout=300)
    status, body = c.request("POST", c.sp("/stop"), {})
    check(status == 200 and body.get("noop") is True, f"stop while stopped is a no-op ({status})")
    status, body = c.request("POST", c.sp("/restart"), {})
    check(status == 409, f"restart while stopped is refused as a conflict ({status}: {body.get('error', '') if isinstance(body, dict) else ''})")
    op = c.ok("POST", c.sp("/start"), {})
    check(c.wait_op(op["id"], timeout=600)["status"] == "succeeded", "start after stop succeeded")
    c.wait_online(timeout=300)

    step("State changes need the session's CSRF token and a same-origin request")
    no_token = Client(a.url, a.cacert, None)
    no_token.state = {"cookie": c.state["cookie"]}
    for path in (c.sp("/stop"), c.sp("/backups"), c.sp("/command"), c.sp("/settings"), c.mp("/servers"), "/api/auth/logout"):
        status, _ = no_token.request("POST", path, {})
        check(status == 403, f"POST {path} with the session cookie but no CSRF token is refused ({status})")
    status, _ = c.request("POST", c.sp("/stop"), {}, headers={"Origin": "https://attacker.example"})
    check(status == 403, f"POST with a valid token from another origin is refused ({status})")

    step("A settings change is applied and audited")
    status, body = c.request("POST", c.sp("/settings"), {"motd": "Playkeeper E2E (renamed)"})
    check(status == 200, f"MOTD change accepted ({status})")
    check(c.status()["config"]["motd"] == "Playkeeper E2E (renamed)", "new MOTD recorded")

    step("Audit log and daily summary")
    audit = c.ok("GET", "/api/audit")
    json.dump(audit, open(os.path.join(out, "audit-a.json"), "w"), indent=2)
    actions = {x["action"] for x in audit}
    wanted = ["setup", "eula.accepted", "create", "whitelist.add", "console.command", "backup.created", "backup.downloaded",
              "restore.staged", "restore.applied", "start", "stop", "settings.changed"] + (["login"] if a.existing else [])
    for want in wanted:
        check(want in actions, f"audit log has {want}")
    summary = c.ok("GET", c.sp("/players/summary?days=1&tz=UTC"))
    json.dump(summary, open(os.path.join(out, "summary-a.json"), "w"), indent=2)
    today = summary["days"][-1]
    day_sessions = c.ok("GET", c.sp("/players/sessions?range=24h"))["sessions"]
    total = sum(s["durationSeconds"] for s in day_sessions)
    check(today["uniquePlayers"] == len({s["player"] for s in day_sessions}) and abs(today["playtimeSeconds"] - total) <= len(day_sessions),
          f"daily summary recomputes from sessions: {today['uniquePlayers']} players, {today['sessions']} sessions, {today['playtimeSeconds']} s observed, {today['coverage']:.0%} collected")


def host_b(a):
    out = a.out
    steps = set(a.steps.split(","))
    c = Client(a.url, a.cacert, os.path.join(out, "state-b.json"))
    if "setup" in steps:
        step("Second host: unauthenticated requests fail closed")
        unauthenticated_checks(a.url, a.cacert)
        step("First-run setup on the second host")
        anon = Client(a.url, a.cacert, None)
        status, _ = anon.request("POST", "/api/setup", {"token": "aaaaaa-bbbbbb-cccccc-dddddd", "username": "admin", "password": PASSWORD})
        check(status in (401, 403), f"setup with a wrong code is refused ({status})")
        c.setup(a.code, "admin", PASSWORD)
        status, _ = anon.request("POST", "/api/setup", {"token": a.code, "username": "intruder", "password": "intruder-password"})
        check(status in (403, 409), f"the setup code cannot be used twice ({status})")
        check(c.servers() == [], "fresh host has no server")
    else:
        c.login("admin", PASSWORD)
    if "refusals" in steps:
        host_b_refusals(a, c)
    if "restore" in steps:
        host_b_restore(a, c)
    if "verify" in steps:
        host_b_verify(a, c)
    if "tamper" in steps:
        host_b_tamper(a, c)


def host_b_refusals(a, c):
    out = a.out
    step("EULA gate on the second host: nothing is created before acceptance")
    status, body = c.request("POST", c.mp("/servers"), {"acceptEula": False, "versionId": "paper-26.1.2", "memoryMB": 1536})
    check(status == 400 and body.get("code") == "eula_required", f"create without EULA refused ({status} {body.get('code')})")

    step("A corrupted copy of the archive is refused before anything changes")
    data = bytearray(open(a.archive, "rb").read())
    data[len(data) // 2] ^= 0xFF
    status, body = c.request("POST", c.mp("/restore/upload"), raw=bytes(data), timeout=600)
    check(status == 422, f"flipped-byte archive refused ({status}: {body.get('error', '')[:80] if isinstance(body, dict) else body})")

    step("Upload the archive (moved as a file) and review the preview")
    status, preview = c.request("POST", c.mp("/restore/upload"), raw=open(a.archive, "rb").read(), timeout=600)
    json.dump(preview, open(os.path.join(out, "restore-preview.json"), "w"), indent=2)
    check(status == 200 and preview["compatible"], "archive validated file by file")
    check(preview["sha256"] == a.expect_sha256, f"archive SHA-256 on host B matches host A ({preview['sha256'][:16]}…)")
    check(preview["needsEula"] and not preview["currentWorld"]["exists"], "preview asks for the EULA and shows no existing world")
    status, body = c.request("POST", c.mp(f"/restore/{preview['id']}/apply"), {"confirm": preview["confirmPhrase"], "acceptEula": False})
    check(status == 400 and body.get("code") == "eula_required", "restore without EULA acceptance refused")
    c.ok("DELETE", c.mp(f"/restore/{preview['id']}"))
    check(c.servers() == [], "refusals left the host unchanged (still no server)")


def host_b_restore(a, c):
    step("Upload the archive (moved as a file) for the restore")
    status, preview = c.request("POST", c.mp("/restore/upload"), raw=open(a.archive, "rb").read(), timeout=600)
    check(status == 200 and preview["compatible"] and preview["sha256"] == a.expect_sha256, "archive validated; SHA-256 matches host A")

    step("Apply the restore as this host's first server and wait until it is joinable")
    t0 = time.time()
    op = c.ok("POST", c.mp(f"/restore/{preview['id']}/apply"), {"confirm": preview["confirmPhrase"], "acceptEula": True})
    check(op.get("serverId"), f"the restore created a server ({op.get('serverId')})")
    c.use_server(op["serverId"])
    op = c.wait_op(op["id"], timeout=1200)
    check(op["status"] == "succeeded", f"restore succeeded ({op.get('error', '')})")
    c.wait_online(timeout=300)
    results["restoreSeconds"] = round(time.time() - t0, 1)


def host_b_verify(a, c):
    c.wait_online(timeout=600)
    step("The distinctive world state survived: console and client view")
    marker = json.load(open(a.marker))
    results["markerConsole"] = check_marker_console(c, marker)
    rc = bot(["verify", "--host", a.game_host, "--port", str(a.game_port), "--name", BUILDER, "--marker", a.marker])
    check(rc == 0, f"client view: gold block and sign with nonce {marker['nonce']} at the recorded coordinates")
    results["marker"] = marker
    metrics = c.ok("GET", c.sp("/metrics?range=24h"))
    check(all(b["state"] in ("not_collected", "no_data", "offline") for b in metrics["buckets"][:-6]), "the restored server shows no history from before it came here (no backfill)")
    restores = [op for op in [c.status().get("lastOperation")] if op and op["kind"] == "restore"]
    if restores:
        op = restores[0]
        results["restoreOperation"] = {"startedAt": op["startedAt"], "finishedAt": op.get("finishedAt"),
                                       "seconds": round((ts(op["finishedAt"]) - ts(op["startedAt"])).total_seconds(), 1)}
        print(f"  restore operation took {results['restoreOperation']['seconds']} s from confirmation to joinable")


def repack(src, edit):
    """Returns a new .tar.gz built from src's members after edit(members)."""
    with tarfile.open(src, "r:gz") as tf:
        members = [(m, tf.extractfile(m).read() if m.isfile() else None) for m in tf.getmembers()]
    members = edit(members)
    buf = io.BytesIO()
    with tarfile.open(fileobj=buf, mode="w:gz") as tf:
        for m, data in members:
            tf.addfile(m, io.BytesIO(data) if data is not None else None)
    return buf.getvalue()


def host_b_tamper(a, c):
    step("Damaged archives are refused while a world is live (the server is stopped so its files hold still)")
    raw = open(a.archive, "rb").read()

    def extra(name):
        def edit(members):
            info = tarfile.TarInfo(name)
            info.size = 4
            return members[:-1] + [(info, b"evil")] + members[-1:]
        return edit

    def changed_level(members):
        out = []
        for m, data in members:
            if m.name.endswith("/level.dat") and data is not None:
                data = data + b"\0"
                m.size = len(data)
            out.append((m, data))
        return out

    cases = [("truncated to half its size", raw[: len(raw) // 2]),
             ("an entry that climbs out with ../", repack(a.archive, extra("playkeeper-backup/data/../../../etc/cron.d/evil"))),
             ("an absolute path entry", repack(a.archive, extra("/etc/cron.d/evil"))),
             ("level.dat changed after the manifest was written", repack(a.archive, changed_level))]
    for label, data in cases:
        with tempfile.NamedTemporaryFile(suffix=".tar.gz") as f:
            f.write(data)
            f.flush()
            status, body = c.request("POST", c.sp("/restore/upload"), raw=open(f.name, "rb").read(), timeout=600)
        msg = body.get("error", "") if isinstance(body, dict) else str(body)
        check(status == 422, f"archive {label}: refused ({status}: {msg[:90]})")


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
    p.add_argument("--steps", default="setup,refusals,restore,verify", help="host-b steps to run (also: tamper)")
    a = p.parse_args()
    os.makedirs(a.out, exist_ok=True)
    started = time.time()
    try:
        host_a(a) if a.role == "host-a" else host_b(a)
        results["ok"] = True
    finally:
        results["seconds"] = round(time.time() - started, 1)
        results.setdefault("ok", False)
        name = f"{a.role}-results.json" if a.role == "host-a" or a.steps == p.get_default("steps") else f"{a.role}-{a.steps.replace(',', '-')}-results.json"
        with open(os.path.join(a.out, name), "w") as f:
            json.dump(results, f, indent=2)
        print(f"\n{a.role}: {'PASSED' if results['ok'] else 'FAILED'} ({sum(1 for x in results['checks'] if x['ok'])} checks ok) in {results['seconds']} s")


if __name__ == "__main__":
    main()
