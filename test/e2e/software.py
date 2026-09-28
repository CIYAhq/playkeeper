#!/usr/bin/env python3
"""Every server type, the hand-picked add-ons, the map, pre-generation and a
modpack, on an installed Playkeeper, checked from inside each server's
container. The ARM64 workflow runs it on an ARM runner to prove that what
Playkeeper downloads and runs works on that CPU.

For each type (Paper, Purpur, Vanilla, Fabric, Quilt, NeoForge, Forge) it
creates a server on the catalog's recommended version, waits until it is
online and answers the console, and checks that its container and Java run as
this machine's CPU, on the Java the catalog names. On Paper and Fabric it also
installs the hand-picked add-ons that have a version for the server, turns on
the map and fetches a drawn tile, pre-generates until Chunky has made chunks,
and, where Simple Voice Chat is among them, checks that it listens on its UDP
port inside the container. A Modrinth modpack (Adrenaline for Minecraft
1.21.11, a Fabric pack that runs on Java 21) must install and come online, and
gets voice chat too. Each server is deleted after its checks, so one runner
fits them all.

Usage: software.py --url URL --cacert CERT --code SETUP_CODE --out DIR
                   [--types paper,fabric,...] [--no-modpack]
"""
import argparse
import json
import os
import re
import secrets
import subprocess
import sys
import time
import traceback

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from pkclient import Client  # noqa: E402

PASSWORD = os.environ.get("PK_ADMIN_PASSWORD") or "e2e-" + secrets.token_hex(8)
TYPES = ["paper", "purpur", "vanilla", "fabric", "quilt", "neoforge", "forge"]
WITH_ADDONS = {"paper", "fabric"}
# Mod loaders keep more memory outside Java's heap (internal/minecraft.HeapFor).
MEMORY_MB = {"fabric": 3072, "quilt": 3072, "neoforge": 4096, "forge": 4096}
MODPACK = {"source": "modrinth", "projectId": "BYN9yKrV", "versionId": "Cm3CfkQm", "name": "Adrenaline", "minecraft": "1.21.11", "java": 21}
# uname -m in a container, and the machine's arch as the dashboard names it.
CPUS = {"x86_64": "x86-64", "aarch64": "ARM64"}
results = {"servers": [], "checks": []}


class Failed(Exception):
    pass


def step(msg):
    print(f"\n[{time.strftime('%H:%M:%S')}] {msg}", flush=True)


def check(cond, msg):
    results["checks"].append({"ok": bool(cond), "check": msg})
    print(f"  {'ok' if cond else 'FAIL'}: {msg}", flush=True)
    if not cond:
        raise Failed(msg)


def docker(*args):
    r = subprocess.run(["sudo", "docker", *args], stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True, timeout=300)
    return r.returncode, r.stdout.strip()


def container(sid):
    rc, out = docker("ps", "--filter", f"label=io.playkeeper.server={sid}", "--format", "{{.Names}}")
    names = out.split()
    check(rc == 0 and len(names) == 1, f"one running container for the server ({out!r})")
    return names[0]


def java_major(text):
    m = re.search(r'version "(\d+)(?:\.(\d+))?', text)
    if not m:
        return None
    major = int(m.group(1))
    return int(m.group(2)) if major == 1 and m.group(2) else major


def wait_for(what, fn, timeout, every=5):
    """Calls fn until it returns something truthy; returns that, or fails."""
    deadline = time.time() + timeout
    last = None
    while time.time() < deadline:
        last = fn()
        if last:
            return last
        time.sleep(every)
    check(False, f"{what} within {timeout} s")


def console(c, cmd):
    return c.ok("POST", c.sp("/command"), {"command": cmd})["output"]


def memory_for(cat, typ):
    want = MEMORY_MB.get(typ, 2048)
    fits = [m for m in cat["memoryOptionsMB"] if m <= want]
    return max(fits) if fits else cat["recommendedMemoryMB"]


def create(c, body, label):
    t0 = time.time()
    op = c.ok("POST", c.mp("/servers"), body)
    c.use_server(op["serverId"])
    op = c.wait_op(op["id"], timeout=2400)
    check(op["status"] == "succeeded", f"{label}: created ({op.get('error', '')} {op.get('hint', '')})".rstrip())
    st = c.wait_online(timeout=900)
    return st, round(time.time() - t0, 1)


def check_runs_here(c, st, label, java, cpu):
    """The server answers the console, and its container and Java run as this CPU."""
    listing = console(c, "list")
    check("There are 0 of a max" in listing, f"{label}: the console answers ({listing.strip()[:80]})")
    name = container(st["id"])
    rc, machine = docker("exec", name, "uname", "-m")
    check(rc == 0 and machine == cpu, f"{label}: the container runs as {machine}, this machine's CPU")
    rc, version = docker("exec", name, "java", "-version")
    got = java_major(version)
    check(rc == 0 and got == java, f"{label}: Java {got} runs in it, the Java {java} the catalog names ({version.splitlines()[0] if version else ''})")
    rc, image = docker("inspect", "--format", "{{.Image}}", name)
    _, arch = docker("image", "inspect", "--format", "{{.Os}}/{{.Architecture}}", image)
    check(rc == 0 and arch.endswith("/" + {"x86_64": "amd64", "aarch64": "arm64"}[cpu]), f"{label}: its image is the {arch} one of {st['config']['image']}")
    return {"container": name, "java": version.splitlines()[0] if version else "", "image": st["config"]["image"], "imagePlatform": arch}


def install_picks(c, label, rec, only=None):
    """Installs the hand-picked add-ons (those in only, when given) and returns
    the ids of the picks installed. Playkeeper offers only the picks with a
    version for the server's type and Minecraft version, so each must
    install."""
    picks = c.ok("GET", c.sp("/addons/curated"))["picks"]
    print(f"  picks for {label}: {', '.join(p['id'] for p in picks)}", flush=True)
    installed = []
    for p in picks:
        if only is not None and p["id"] not in only:
            continue
        card = p["card"]
        d = c.ok("GET", c.sp(f"/addons/project/{card['source']}/{card['projectId']}"))
        plan = d.get("plan") or {}
        why = json.dumps(d.get("planError") or plan.get("blockers"))
        check(plan.get("ready") and plan.get("fingerprint"), f"{label}: {card['name']} can be installed ({why})")
        op = c.ok("POST", c.sp("/addons/install"), {"source": card["source"], "projectId": card["projectId"], "fingerprint": plan["fingerprint"], "openPorts": bool(p.get("ports"))})
        op = c.wait_op(op["id"], timeout=900)
        check(op["status"] == "succeeded", f"{label}: {card['name']} installed, every file checked ({op.get('error', '')})")
        installed.append(p["id"])
        rec.setdefault("addons", []).append(card["name"])
    return installed


def restart(c, label, what):
    op = c.ok("POST", c.sp("/restart"), {})
    check(c.wait_op(op["id"], timeout=900)["status"] == "succeeded", f"{label}: restarted with {what}")
    st = c.wait_online(timeout=900)
    check(not st.get("crash") and st["phase"] == "online", f"{label}: online with {what}")
    return st


def check_voice_chat(c, st, label):
    port = st["config"].get("voiceChatPort")
    check(port, f"{label}: voice chat has a UDP port ({port})")
    name = container(st["id"])
    hexport = f":{port:04X} "

    def listening():
        _, out = docker("exec", name, "cat", "/proc/net/udp", "/proc/net/udp6")
        return hexport in out
    wait_for(f"{label}: voice chat listening on UDP {port} inside the container", listening, 300)
    check(True, f"{label}: voice chat listens on UDP {port} inside the container")
    return port


def check_map(c, label):
    def drawn():
        m = c.ok("GET", c.sp("/map"))
        print(f"    map: {m['state']} {m.get('message', '')}", flush=True)
        return m if m["state"] == "ready" and m.get("areas", 0) > 0 else None
    m = wait_for(f"{label}: the map drawn", drawn, 900, every=10)
    worlds = c.ok("GET", c.sp("/map/worlds"))
    check(worlds["worlds"], f"{label}: the map lists its worlds")
    world = next((w for w in worlds["worlds"] if w["dimension"] == "overworld"), worlds["worlds"][0])
    size, top = worlds["tileSize"], world["zoom"]["max"]
    x, z = world["spawn"]["x"] // size, world["spawn"]["z"] // size
    status, _ = c.request("GET", c.sp(f"/map/tiles/{world['name']}/{top}/{x}_{z}.png"), stream_to=os.devnull)
    check(status == 200, f"{label}: squaremap drew the tile at spawn ({world['name']} zoom {top} {x}_{z}: {status}); {m['message']}")
    return {"state": m["state"], "areas": m.get("areas"), "plugin": f"{m.get('plugin')} {m.get('pluginVersion', '')}".strip()}


def settle(c, label):
    """Waits until the server runs everything installed: no operation under
    way, no restart still to come (Restart is pressed for one the add-ons
    need, as the dashboard asks), the same run on two looks in a row, and
    Chunky answering the console. A task Chunky has just started is lost to a
    restart that comes before Chunky saves it (ARM64 run 36357996413)."""
    run = [None]

    def ready():
        st = c.status()
        if st["phase"] != "online" or not st.get("reachable") or st.get("operation"):
            run[0] = None
            return None
        if st.get("pendingRestart") or c.ok("GET", c.sp("/addons")).get("restartNeeded"):
            restart(c, label, "every add-on loaded")
            run[0] = None
            return None
        if st.get("startedAt") != run[0]:
            run[0] = st.get("startedAt")
            return None
        return st if "[Chunky]" in console(c, "chunky progress") else None
    return wait_for(f"{label}: running every add-on, with Chunky answering", ready, 600)


def check_pregen(c, label):
    op = c.ok("POST", c.sp("/pregen/start"), {"preset": "small", "pauseForPlayers": False})
    op = c.wait_op(op["id"], timeout=900)
    check(op["status"] == "succeeded", f"{label}: pre-generation started ({op.get('error', '')})")

    def progress():
        p = c.ok("GET", c.sp("/pregen"))
        print(f"    pregen: {p['state']} {p.get('chunks', 0)}/{p.get('total', 0)} chunks", flush=True)
        return p if p["state"] in ("running", "finished") and p.get("chunks", 0) >= 200 else None
    p = wait_for(f"{label}: Chunky generated 200 chunks", progress, 900, every=10)
    check(True, f"{label}: Chunky generated {p['chunks']} of {p['total']} chunks at {p.get('rate', 0):.0f} a second")
    if p["state"] == "running":
        c.ok("POST", c.sp("/pregen/cancel"), {})
    return {"chunks": p["chunks"], "total": p["total"], "rate": p.get("rate")}


def delete(c, st):
    op = c.ok("POST", c.sp("/delete"), {"confirm": st["name"]})
    op = c.wait_op(op["id"], timeout=600)
    check(op["status"] == "succeeded", f"{st['name']}: deleted ({op.get('error', '')})")
    rc, out = docker("ps", "-a", "--filter", f"label=io.playkeeper.server={st['id']}", "--format", "{{.Names}}")
    check(rc == 0 and not out, f"{st['name']}: its container is gone")


def one_type(c, typ, cpu):
    label = typ
    cat = c.ok("GET", c.mp(f"/catalog?type={typ}"))
    usable = [v for v in cat["versions"] if not v.get("experimental")]
    check(usable, f"{label}: the catalog lists versions ({cat.get('versionsError', '')})")
    entry = next((v for v in usable if v.get("recommended")), usable[0])
    label = f"{typ} {entry['minecraftVersion']}" + (f" ({entry['build']})" if entry.get("build") else "")
    step(f"{label}: create on Java {entry['java']} and wait until it is online")
    body = {"acceptEula": True, "type": typ, "versionId": entry["id"], "memoryMB": memory_for(cat, typ), "name": f"ARM {typ}"[:32], "motd": f"Playkeeper {typ}"}
    st, secs = create(c, body, label)
    rec = {"type": typ, "version": entry["id"], "minecraft": entry["minecraftVersion"], "build": entry.get("build", ""), "createSeconds": secs, "memoryMB": body["memoryMB"]}
    results["servers"].append(rec)
    rec.update(check_runs_here(c, st, label, entry["java"], cpu))
    if typ in WITH_ADDONS:
        step(f"{label}: every hand-picked add-on, the map, pre-generation and voice chat")
        picked = install_picks(c, label, rec)
        check("pregenerate" in picked, f"{label}: Chunky is among the add-ons installed ({', '.join(picked)})")
        # Turning the map on restarts the server, which loads the add-ons
        # too. Another restart right away could interrupt squaremap's first
        # full render, which Playkeeper asks for only once.
        op = c.ok("POST", c.sp("/map/enable"), {})
        op = c.wait_op(op["id"], timeout=900)
        check(op["status"] == "succeeded", f"{label}: the map turned on, squaremap installed, the server restarted ({op.get('error', '')})")
        st = c.wait_online(timeout=900)
        check(not st.get("crash") and st["phase"] == "online", f"{label}: online with {len(picked)} add-ons and the map")
        rec["map"] = check_map(c, label)
        settle(c, label)
        rec["pregen"] = check_pregen(c, label)
        st = c.wait_online(timeout=600)
        if "voice-chat" in picked:
            rec["voiceChatPort"] = check_voice_chat(c, st, label)
        else:
            print(f"  {label}: Playkeeper doesn't offer Simple Voice Chat, which has no version for it yet; the modpack's server checks voice chat on Fabric", flush=True)
    rec["ok"] = True
    delete(c, st)


def modpack(c, cpu):
    label = f"{MODPACK['name']} for {MODPACK['minecraft']}"
    step(f"Modpack: {label} from Modrinth, on Java {MODPACK['java']}")
    d = c.ok("GET", c.mp(f"/modpacks/{MODPACK['source']}/{MODPACK['projectId']}"))
    v = next((v for v in d["versions"] if v["id"] == MODPACK["versionId"]), None)
    check(v and not v.get("unsupported") and v["minecraftVersion"] == MODPACK["minecraft"], f"{label}: the pack version is on offer ({v and v.get('unsupported')})")
    preview = c.ok("GET", c.mp(f"/modpacks/{MODPACK['source']}/{MODPACK['projectId']}/versions/{MODPACK['versionId']}/preview"))
    check(preview["ready"], f"{label}: the preview is ready: {preview['files']} files, {preview['type']} {preview['minecraftVersion']}, Java {preview.get('java') or 'newest'}")
    cat = c.ok("GET", c.mp(f"/catalog?type={preview['type']}"))
    body = {"acceptEula": True, "memoryMB": memory_for(cat, preview["type"]), "name": "ARM modpack", "modpack": {k: MODPACK[k] for k in ("source", "projectId", "versionId")}}
    st, secs = create(c, body, label)
    rec = {"type": "modpack", "modpack": label, "createSeconds": secs, "files": preview["files"]}
    results["servers"].append(rec)
    check((st["config"].get("modpack") or {}).get("versionId") == MODPACK["versionId"], f"{label}: the server runs the pack")
    rec.update(check_runs_here(c, st, label, MODPACK["java"], cpu))
    files = c.ok("GET", c.sp("/addons"))["files"]
    from_pack = [f["fileName"] for f in files if f["status"] == "pack"]
    check(from_pack, f"{label}: {len(from_pack)} mods from the pack are in place")
    step(f"{label}: voice chat, the Fabric mod, next to the pack's mods")
    check(install_picks(c, label, rec, only={"voice-chat"}) == ["voice-chat"], f"{label}: Simple Voice Chat installed next to the pack")
    st = restart(c, label, "voice chat and the pack")
    rec["voiceChatPort"] = check_voice_chat(c, st, label)
    rec["ok"] = True
    delete(c, st)


def main():
    p = argparse.ArgumentParser()
    p.add_argument("--url", required=True)
    p.add_argument("--cacert", required=True)
    p.add_argument("--code", required=True, help="one-time setup code printed by the installer")
    p.add_argument("--out", required=True)
    p.add_argument("--types", default=",".join(TYPES))
    p.add_argument("--no-modpack", action="store_true")
    a = p.parse_args()
    os.makedirs(a.out, exist_ok=True)
    started = time.time()
    c = Client(a.url, a.cacert, os.path.join(a.out, "state-software.json"))
    failures = []
    try:
        step("First-run setup and the machine's CPU")
        c.setup(a.code, "admin", PASSWORD)
        cpu = subprocess.run(["uname", "-m"], stdout=subprocess.PIPE, text=True, check=True).stdout.strip()
        machine = c.ok("GET", c.mp(""))["live"]
        results["machine"] = {"arch": machine["arch"], "os": machine["os"], "cpus": machine["cpus"], "memoryTotalMB": machine["memoryTotalMB"]}
        check(machine["arch"] == CPUS.get(cpu), f"the dashboard says this machine is {machine['arch']} ({machine['os']}, {machine['cpus']} CPUs)")
        jobs = [(t, lambda t=t: one_type(c, t, cpu)) for t in a.types.split(",") if t]
        if not a.no_modpack:
            jobs.append(("modpack", lambda: modpack(c, cpu)))
        for name, job in jobs:
            try:
                job()
            except (Exception, SystemExit) as e:
                failures.append(f"{name}: {type(e).__name__}: {e}")
                print(f"  {name} failed: {e}", flush=True)
                traceback.print_exc()
                try:
                    st = c.status()
                    print(f"  last status: {st['phase']} {st.get('lastError', '')} {json.dumps(st.get('crash'))}", flush=True)
                    lines = c.ok("GET", c.sp("/logs?limit=80"))["lines"]
                    print("\n".join("    " + ln["text"] for ln in lines), flush=True)
                    delete(c, st)
                except (Exception, SystemExit) as e2:
                    print(f"  could not clean up after {name}: {e2}", flush=True)
                c.state.pop("server", None)
    finally:
        results["failures"] = failures
        results["ok"] = not failures and bool(results["servers"])
        results["seconds"] = round(time.time() - started, 1)
        with open(os.path.join(a.out, "software-results.json"), "w") as f:
            json.dump(results, f, indent=2)
    step("Summary")
    for r in results["servers"]:
        print(f"  {'ok  ' if r.get('ok') else 'FAIL'} {r.get('modpack') or r['type'] + ' ' + r.get('minecraft', '')}: {r.get('java', '')}, {r.get('imagePlatform', '')}, created in {r['createSeconds']} s")
    if failures:
        raise SystemExit("software checks failed:\n  " + "\n  ".join(failures))
    print(f"\nsoftware: PASSED ({sum(1 for x in results['checks'] if x['ok'])} checks) in {results['seconds']} s")


if __name__ == "__main__":
    main()
