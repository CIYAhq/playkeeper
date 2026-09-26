#!/usr/bin/env python3
"""Checks of a release rehearsal (scripts/e2e/vm-release.sh) against an
installed Playkeeper, through its dashboard API.

owner-prepare  update.py prepare on the previous release, with the server on
               the Minecraft version the protocol bot speaks and the bots on
               its allowlist: setup, a server with settings to keep, a world
               marker and a backup, recorded in OUT/before.json.
owner-players  then two players join, and a backup with a player online. The
               state update.py verify compares is recorded again.
still-running  the server started when OUT/before.json says (nothing since
               has restarted it).
owner-after    after the update: the players from before are still listed, a
               player joins and stays through a backup, and a backup the
               previous release made is restored.
signin         a wrong password is refused and the password signs in; then
               two-factor sign-in is turned on with an app code, after which
               the password alone asks for a code, a wrong code is refused,
               an app code signs in, and a recovery code signs in once.
after-reset    after `playkeeper reset-2fa admin`: the password alone signs in.
degrade        Discord and a free name where neither can be reached: clear
               answers, nothing connected or claimed, the IP address works.
logs           ERROR lines in a journal (journalctl -o short-iso), from
               --since on; earlier ones are listed only.
The session update.py made is kept in OUT/state.json and used again here.
"""
import argparse
import base64
import hashlib
import hmac
import http.client
import json
import os
import re
import secrets
import ssl
import struct
import subprocess
import sys
import time
import urllib.parse
from datetime import datetime

HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, HERE)
from pkclient import Client  # noqa: E402
import update  # noqa: E402

PASSWORD = os.environ.get("PK_ADMIN_PASSWORD") or os.environ.get("PK_PASSWORD") or ""
BOT = os.path.join(HERE, "bot", "bot.js")
PENDING = "__Host-playkeeper-2fa"
BOT_VERSION = "paper-26.1.2"  # the protocol bot speaks Minecraft 26.1
OWNER_BOTS = ("PkOwnerOne", "PkOwnerTwo", "PkOwnerThree", "PkOwnerFour")
# A webhook link in Discord's format with a random token, so no channel owns
# it; the guest's /etc/hosts sends discord.com to 127.0.0.1 anyway.
FAKE_WEBHOOK = "https://discord.com/api/webhooks/123456789012345678/" + secrets.token_urlsafe(51)


def step(msg):
    print(f"\n[{time.strftime('%H:%M:%S')}] {msg}", flush=True)


def check(cond, msg):
    print(("  ok: " if cond else "  FAIL: ") + msg, flush=True)
    if not cond:
        raise SystemExit(f"release check failed: {msg}")


def brief(body):
    text = json.dumps(body) if not isinstance(body, str) else body
    return text if len(text) <= 300 else text[:300] + "…"


def session(a):
    return Client(a.url, a.cacert, os.path.join(a.out, "state.json"))


class Web:
    """A browser's session with the dashboard: every cookie it sets is kept,
    including the one a sign-in holds while it waits for a second factor.
    TLS is checked against the dashboard's own certificate."""

    def __init__(self, url, cacert):
        u = urllib.parse.urlparse(url)
        self.host, self.port, self.origin = u.hostname, u.port or 443, f"https://{u.netloc}"
        self.ctx = ssl.create_default_context(cafile=cacert)
        self.cookies, self.csrf = {}, ""

    def call(self, method, path, body=None):
        h = {"Origin": self.origin, "X-Requested-With": "playkeeper"}
        if self.cookies:
            h["Cookie"] = "; ".join(f"{k}={v}" for k, v in self.cookies.items())
        if self.csrf and method not in ("GET", "HEAD"):
            h["X-CSRF-Token"] = self.csrf
        data = None
        if body is not None:
            data, h["Content-Type"] = json.dumps(body).encode(), "application/json"
        c = http.client.HTTPSConnection(self.host, self.port, context=self.ctx, timeout=60)
        c.request(method, path, body=data, headers=h)
        r = c.getresponse()
        self.headers = {k.lower(): v for k, v in r.getheaders()}
        for k, v in r.getheaders():
            if k.lower() == "set-cookie":
                name, _, value = v.split(";", 1)[0].partition("=")
                if value:
                    self.cookies[name] = value
                else:
                    self.cookies.pop(name, None)
        payload = r.read()
        try:
            out = json.loads(payload) if payload else {}
        except ValueError:
            out = payload.decode(errors="replace")
        if isinstance(out, dict) and out.get("csrfToken"):
            self.csrf = out["csrfToken"]
        return r.status, out

    def attempt(self, path, body):
        """POSTs to a sign-in step, waiting out the dashboard's limit on
        sign-in attempts (10 in 15 minutes from one address). body() builds
        each try's body, so an app code is a fresh one after the wait."""
        for _ in range(8):
            status, out = self.call("POST", path, body())
            if status != 429:
                return status, out
            wait = int(self.headers.get("retry-after", "30")) + 1
            print(f"  (sign-in attempts are limited: waiting {wait} s)", flush=True)
            time.sleep(wait)
        return status, out

    def login(self, password=PASSWORD):
        return self.attempt("/api/auth/login", lambda: {"username": "admin", "password": password})

    def browser_session(self, file):
        """Hands the session to the browser tests (test/e2e/ui/helpers.ts login)."""
        os.makedirs(os.path.dirname(os.path.abspath(file)), exist_ok=True)
        with open(file, "w") as f:
            json.dump([{"name": "__Host-playkeeper", "value": self.cookies["__Host-playkeeper"], "url": self.origin,
                        "secure": True, "httpOnly": True, "sameSite": "Strict"}], f)


def players_online(c, n, tries=40):
    for _ in range(tries):
        if ((c.status().get("players") or {}).get("online")) == n:
            return True
        time.sleep(2)
    return False


def player_names(c):
    return sorted({s["player"] for s in c.ok("GET", c.sp("/players/sessions?range=7d")).get("sessions", [])})


def bot(a, name, stay):
    log = open(os.path.join(a.out, f"bot-{name}.log"), "w")
    return subprocess.Popen(["node", BOT, "visit", "--host", a.game_host, "--port", str(a.game_port), "--name", name, "--stay", str(stay)],
                            stdout=log, stderr=subprocess.STDOUT)


def owner_prepare(a):
    c = session(a)
    step("First-run setup of the release installed from GitHub")
    c.setup(a.code, "admin", PASSWORD)
    cat = c.ok("GET", c.mp("/catalog"))
    offered = [v["id"] for v in cat["versions"]]
    check(BOT_VERSION in offered, f"{BOT_VERSION} is offered ({', '.join(offered[:6])}…)")
    step(f"Create a server on {BOT_VERSION} with settings to keep: name {update.MOTD!r}, at most {update.MAX_PLAYERS} players")
    op = c.create(BOT_VERSION, cat["recommendedMemoryMB"], update.MOTD, max_players=update.MAX_PLAYERS)
    check(op["status"] == "succeeded", f"server created ({op.get('error', '')})")
    c.wait_online(timeout=600)
    step("The players go on the allowlist; a world marker is set on the console, then a backup")
    for name in OWNER_BOTS:
        r = c.ok("POST", c.sp("/whitelist"), {"name": name})
        check(name in r.get("message", ""), f"allowlist {name}: {r.get('message')}")
    nonce = secrets.randbelow(2**31 - 2) + 1
    update.console(c, f"scoreboard objectives add {update.OBJECTIVE} dummy")
    out = update.console(c, f"scoreboard players set marker {update.OBJECTIVE} {nonce}")
    check(str(nonce) in out, f"marker set: {out.strip()}")
    update.console(c, "save-all flush")
    op = c.wait_op(c.ok("POST", c.sp("/backups"), {"note": "before the update"})["id"], timeout=900)
    check(op["status"] == "succeeded", f"backup taken ({op.get('error', '')})")
    c.wait_online(timeout=600)
    before = update.record(c)
    before["nonce"] = nonce
    check(before["config"]["motd"] == update.MOTD and before["config"]["maxPlayers"] == update.MAX_PLAYERS, "the settings are in place")
    check(len(before["backups"]) == 1, f"one backup: {before['backups']}")
    update.save(a, "before.json", before)


def owner_players(a):
    c = session(a)
    step("Two players join the previous release")
    first = bot(a, "PkOwnerOne", 35)
    check(players_online(c, 1), "the first player is online")
    time.sleep(6)  # Paper refuses a second connection from one address within 4 s
    second = bot(a, "PkOwnerTwo", 25)
    check(players_online(c, 2), "both players are online")
    for b in (first, second):
        b.wait(timeout=120)
    time.sleep(6)
    step("A backup with a player online")
    b = bot(a, "PkOwnerThree", 90)
    check(players_online(c, 1), "a player is online")
    op = c.wait_op(c.ok("POST", c.sp("/backups"), {"note": "with a player online"})["id"], timeout=900)
    check(op["status"] == "succeeded", f"backup taken ({op.get('error', '')})")
    b.wait(timeout=200)
    c.wait_online(timeout=600)
    before = update.load(a, "before.json")
    now = update.record(c)
    now["nonce"], now["players"] = before["nonce"], player_names(c)
    check({"PkOwnerOne", "PkOwnerTwo", "PkOwnerThree"} <= set(now["players"]), f"the players are listed: {now['players']}")
    check(len(now["backups"]) == 2, f"two backups: {now['backups']}")
    update.save(a, "before.json", now)


def still_running(a):
    before = update.load(a, "before.json")
    st = session(a).wait_online(timeout=300)
    check(st.get("startedAt") == before["startedAt"], f"the Minecraft server kept running (container started at {st.get('startedAt')})")


def owner_after(a):
    c = session(a)
    before = update.load(a, "before.json")
    step("The players from before are still listed")
    now = player_names(c)
    check(set(before["players"]) <= set(now), f"players from before: {before['players']}; listed now: {now}")
    step("A player joins and stays online through a backup")
    b = bot(a, "PkOwnerFour", 150)
    check(players_online(c, 1), "a player is online")
    op = c.wait_op(c.ok("POST", c.sp("/backups"), {"note": "with a player online, after the update"})["id"], timeout=900)
    check(op["status"] == "succeeded", f"backup taken ({op.get('error', '')})")
    check(players_online(c, 1, tries=5) and b.poll() is None, "the player is still online after the backup")
    backups = sorted(c.ok("GET", c.sp("/backups")), key=lambda x: datetime.fromisoformat(x["createdAt"]))
    new = backups[-1]
    check(new.get("method") in ("online_copy", "online_in_place"),
          f"the backup was made with the server running ({new.get('method')}, world saving paused {new.get('savingPausedMs')} ms)")
    v = c.ok("POST", c.sp(f"/backups/{new['id']}/verify"), {})
    check(v.get("verified") is True, f"the new backup verifies file by file ({v.get('verifyError', '')})")
    b.terminate()
    b.wait(timeout=30)
    step("A backup the previous release made is restored")
    first = backups[0]
    check(first["id"] in {x[0] for x in before["backups"]}, f"the oldest backup, {first['id']}, is one the previous release made")
    update.console(c, f"scoreboard players set marker {update.OBJECTIVE} {before['nonce'] + 1}")
    status, preview = c.request("POST", c.sp(f"/backups/{first['id']}/restore"), {})
    check(status == 200 and preview.get("confirmPhrase"), f"restore preview ({status}): {brief(preview)}")
    op = c.wait_op(c.ok("POST", c.mp(f"/restore/{preview['id']}/apply"), {"confirm": preview["confirmPhrase"]})["id"], timeout=1200)
    check(op["status"] == "succeeded", f"restore succeeded ({op.get('error', '')})")
    c.wait_online(timeout=600)
    out = update.console(c, f"scoreboard players get marker {update.OBJECTIVE}")
    check(str(before["nonce"]) in out, f"the restored world has the marker from before the update: {out.strip()}")


def otp_params(uri):
    u = urllib.parse.urlparse(uri)
    q = urllib.parse.parse_qs(u.query)
    return {"secret": q["secret"][0], "algorithm": q.get("algorithm", ["SHA1"])[0].upper(),
            "digits": int(q.get("digits", ["6"])[0]), "period": int(q.get("period", ["30"])[0])}


def totp(p, counter):
    key = base64.b32decode(p["secret"].upper() + "=" * (-len(p["secret"]) % 8))
    mac = hmac.new(key, struct.pack(">Q", counter), getattr(hashlib, p["algorithm"].lower())).digest()
    o = mac[-1] & 0x0F
    return str((struct.unpack(">I", mac[o:o + 4])[0] & 0x7FFFFFFF) % 10 ** p["digits"]).zfill(p["digits"])


def fresh_code(p, used):
    """An app code for a time step no code was used in yet: a used code is refused."""
    while int(time.time() // p["period"]) in used:
        time.sleep(p["period"] - time.time() % p["period"] + 0.5)
    counter = int(time.time() // p["period"])
    used.add(counter)
    return totp(p, counter)


def signin(a):
    step("Sign-in: a wrong password is refused, the password signs in")
    status, body = Web(a.url, a.cacert).login(PASSWORD + "-wrong")
    check(status == 401, f"a wrong password is refused ({status}: {brief(body)})")
    w = Web(a.url, a.cacert)
    status, body = w.login()
    check(status == 200 and w.csrf and "secondFactor" not in body, f"the password signs in, with two-factor sign-in off ({status})")
    status, st = w.call("GET", "/api/auth/2fa")
    check(status == 200 and st.get("state") != "on", f"two-factor sign-in starts off ({brief(st)})")
    step("Turn on two-factor sign-in with an authenticator app")
    status, setup = w.call("POST", "/api/auth/2fa/setup", {"password": PASSWORD})
    check(status == 200 and str(setup.get("uri", "")).startswith("otpauth://totp/") and setup.get("manualKey") and setup.get("qrCodeSvg"),
          f"setup shows a QR code, a key to type and an otpauth:// link ({status})")
    p = otp_params(setup["uri"])
    print(f"  the app is asked for {p['digits']}-digit {p['algorithm']} codes every {p['period']} s", flush=True)
    used = set()
    status, conf = w.call("POST", "/api/auth/2fa/confirm", {"code": fresh_code(p, used)})
    codes = conf.get("recoveryCodes") or [] if isinstance(conf, dict) else []
    check(status == 200 and len(codes) == 10 and conf.get("status", {}).get("state") == "on",
          f"an app code turns it on, with {len(codes)} recovery codes ({status}: state {conf.get('status', {}).get('state') if isinstance(conf, dict) else conf})")
    step("Sign-in asks for a code now")
    w2 = Web(a.url, a.cacert)
    status, body = w2.login()
    check(status == 200 and "secondFactor" in body and PENDING in w2.cookies and not w2.csrf,
          f"the password alone asks for a second factor ({status}: {sorted(body) if isinstance(body, dict) else body})")
    status, _ = w2.call("GET", "/api/auth/me")
    check(status == 401, f"no session before the code ({status})")
    now = int(time.time() // p["period"])
    valid = {totp(p, now + d) for d in (-2, -1, 0, 1, 2)}
    wrong = next(c for c in (str(secrets.randbelow(10 ** p["digits"])).zfill(p["digits"]) for _ in range(100)) if c not in valid)
    status, body = w2.attempt("/api/auth/second-factor", lambda: {"code": wrong})
    check(status == 401, f"a wrong code is refused ({status}: {brief(body)})")
    status, body = w2.attempt("/api/auth/second-factor", lambda: {"code": fresh_code(p, used)})
    check(status == 200 and w2.csrf, f"an app code signs in ({status}: {brief(body)})")
    status, me = w2.call("GET", "/api/auth/me")
    check(status == 200 and "admin" in json.dumps(me), f"signed in as admin ({status})")
    step("A recovery code signs in, once")
    w3 = Web(a.url, a.cacert)
    w3.login()
    status, body = w3.attempt("/api/auth/second-factor", lambda: {"code": codes[0]})
    check(status == 200 and w3.csrf, f"a recovery code signs in ({status}: {brief(body)})")
    w4 = Web(a.url, a.cacert)
    w4.login()
    status, body = w4.attempt("/api/auth/second-factor", lambda: {"code": codes[0]})
    check(status == 401, f"the same recovery code is refused the second time ({status}: {brief(body)})")
    status, st = w2.call("GET", "/api/auth/2fa")
    check(st.get("state") == "on" and st.get("recoveryCodesLeft") == 9, f"two-factor sign-in is on with 9 recovery codes left ({brief(st)})")


def after_reset(a):
    w = Web(a.url, a.cacert)
    status, body = w.login()
    check(status == 200 and w.csrf and "secondFactor" not in body, f"after reset-2fa the password alone signs in ({status}: {brief(body)})")
    status, st = w.call("GET", "/api/auth/2fa")
    check(st.get("state") == "off", f"two-factor sign-in is off ({brief(st)})")
    status, audit = w.call("GET", "/api/audit")
    actions = [e.get("action") for e in audit] if isinstance(audit, list) else []
    check("2fa.reset" in actions and "2fa.enable" in actions, f"the audit log has the enable and the reset: {sorted(x for x in set(actions) if str(x).startswith('2fa'))}")


def refused(status, body):
    return 400 <= status < 600 and status != 500 and isinstance(body, dict) and bool(body.get("error"))


def degrade(a):
    w = Web(a.url, a.cacert)
    status, _ = w.login()
    check(status == 200 and w.csrf, f"signed in ({status})")
    step("Discord with no webhook")
    status, d = w.call("GET", "/api/discord")
    check(status == 200 and d.get("connected") is False, f"Discord is not connected ({status}: {brief(d)})")
    status, body = w.call("POST", "/api/discord/test", {})
    check(refused(status, body), f"a test message is refused with a reason ({status}: {brief(body)})")
    step("Discord with a webhook link it can't reach")
    status, body = w.call("POST", "/api/discord/connect", {"webhookUrl": FAKE_WEBHOOK})
    check(refused(status, body), f"connecting is refused with a reason ({status}: {brief(body)})")
    status, d = w.call("GET", "/api/discord")
    check(status == 200 and d.get("connected") is False, "nothing was saved: Discord is still not connected")
    step("A free name while the names service can't be reached")
    status, machines = w.call("GET", "/api/machines")
    mid = machines[0]["id"]
    status, addr = w.call("GET", f"/api/machines/{mid}/address")
    check(status == 200, f"the address page's data ({status}): kind {addr.get('kind')!r} (empty: the IP address only), "
          f"names service {(addr.get('names') or {}).get('url')}, free {brief(addr.get('free'))}")
    name = f"pk-rehearsal-{secrets.token_hex(3)}"
    status, av = w.call("GET", f"/api/machines/{mid}/address/available?name={name}")
    check(status == 200 or refused(status, av), f"checking {name} answers cleanly ({status}: {brief(av)})")
    status, cl = w.call("POST", f"/api/machines/{mid}/address/claim", {"name": name, "acceptTerms": True, "panelHost": urllib.parse.urlparse(a.url).netloc})
    check(refused(status, cl), f"claiming it is refused with a reason ({status}: {brief(cl)})")
    status, after = w.call("GET", f"/api/machines/{mid}/address")
    held = (after.get("free") or {}).get("name") if isinstance(after.get("free"), dict) else None
    check(status == 200 and after.get("kind") == addr.get("kind") and not held, f"nothing was claimed: kind {after.get('kind')}, free {brief(after.get('free'))}")
    status, _ = w.call("GET", "/healthz")
    check(status == 200, "the dashboard still answers at the IP address")
    if a.browser_session:
        w.browser_session(a.browser_session)


def logs(a):
    since = a.since or ""
    errors, early, warns = [], [], {}
    for line in open(a.journal, errors="replace"):
        m = re.search(r"\btime=(\S+)", line)
        stamp = m.group(1) if m else line[:24]
        if re.search(r"\blevel=ERROR\b|\bpanic:|fatal error:", line):
            (errors if stamp >= since else early).append(line.rstrip())
        elif re.search(r"\blevel=WARN\b", line) and stamp >= since:
            msg = re.search(r'msg=("[^"]*"|\S+)', line)
            key = msg.group(1) if msg else line.rstrip()[-120:]
            warns[key] = warns.get(key, 0) + 1
    if early:
        print(f"ERROR lines before {since} (the previous release):")
        print("\n".join("  " + x for x in early))
    print(f"WARN messages{' from ' + since if since else ''}:")
    print("\n".join(f"  {n} x {k}" for k, n in sorted(warns.items(), key=lambda kv: -kv[1])) or "  none")
    print(f"ERROR lines{' from ' + since if since else ''}:")
    print("\n".join("  " + x for x in errors) or "  none")
    check(not errors, f"no ERROR lines in the agent, panel and updater logs{' from ' + since if since else ''} ({len(errors)})")


def main():
    p = argparse.ArgumentParser()
    p.add_argument("cmd", choices=["owner-prepare", "owner-players", "still-running", "owner-after", "signin", "after-reset", "degrade", "logs"])
    p.add_argument("--url")
    p.add_argument("--code", help="owner-prepare: the setup code the installer printed")
    p.add_argument("--cacert")
    p.add_argument("--out", default=".")
    p.add_argument("--game-host")
    p.add_argument("--game-port", type=int, default=25565)
    p.add_argument("--journal")
    p.add_argument("--since")
    p.add_argument("--browser-session", help="degrade: write the session for the browser tests here")
    a = p.parse_args()
    if a.cmd != "logs" and not PASSWORD:
        raise SystemExit("set PK_ADMIN_PASSWORD")
    os.makedirs(a.out, exist_ok=True)
    {"owner-prepare": owner_prepare, "owner-players": owner_players, "still-running": still_running, "owner-after": owner_after, "signin": signin,
     "after-reset": after_reset, "degrade": degrade, "logs": logs}[a.cmd](a)


if __name__ == "__main__":
    main()
