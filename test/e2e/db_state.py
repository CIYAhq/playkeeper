#!/usr/bin/env python3
"""Prints, as JSON, what a release rehearsal (scripts/e2e/vm-release.sh)
records about Playkeeper's two databases on a guest: each one's schema version
(PRAGMA user_version, the number of migrations applied), SQLite's integrity
and foreign key checks, its tables and columns, and its schema. Runs on the
guest as root and opens the databases read-only, so Playkeeper keeps running:
    sudo python3 - /var/lib/playkeeper < db_state.py
"""
import json
import os
import sqlite3
import sys

root = sys.argv[1] if len(sys.argv) > 1 else "/var/lib/playkeeper"
out = {}
for name in ("panel", "agent"):
    path = os.path.join(root, name, f"{name}.db")
    if not os.path.exists(path):
        out[name] = {"path": path, "missing": True}
        continue
    db = sqlite3.connect(f"file:{path}?mode=ro", uri=True, timeout=30)
    tables = [r[0] for r in db.execute("SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%' ORDER BY name")]
    d = {
        "path": path,
        "userVersion": db.execute("PRAGMA user_version").fetchone()[0],
        "integrity": [r[0] for r in db.execute("PRAGMA integrity_check")],
        "foreignKeyProblems": [list(r) for r in db.execute("PRAGMA foreign_key_check")],
        "tables": {t: [c[1] for c in db.execute(f'PRAGMA table_info("{t}")')] for t in tables},
        "schema": [list(r) for r in db.execute("SELECT type, name, sql FROM sqlite_master WHERE name NOT LIKE 'sqlite_%' ORDER BY type, name")],
    }
    if "server_machines" in tables:
        cols = [c for c in ("server_id", "machine_id", "slug") if c in d["tables"]["server_machines"]]
        d["serverMachines"] = [dict(zip(cols, r)) for r in db.execute(f"SELECT {', '.join(cols)} FROM server_machines ORDER BY rowid")]
    db.close()
    out[name] = d
json.dump(out, sys.stdout, indent=2)
print()
