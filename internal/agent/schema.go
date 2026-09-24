package agent

// migrations for agent.db. Append only; never edit a released migration.
var migrations = []string{
	`
CREATE TABLE kv (
  key   TEXT PRIMARY KEY,
  value TEXT NOT NULL
);
CREATE TABLE operations (
  id          TEXT PRIMARY KEY,
  kind        TEXT NOT NULL,
  status      TEXT NOT NULL,
  phase       TEXT NOT NULL DEFAULT '',
  actor       TEXT NOT NULL,
  started_at  INTEGER NOT NULL,
  finished_at INTEGER,
  error       TEXT NOT NULL DEFAULT '',
  hint        TEXT NOT NULL DEFAULT '',
  detail      TEXT NOT NULL DEFAULT '{}'
);
CREATE INDEX operations_started ON operations(started_at);
CREATE TABLE samples (
  ts             INTEGER PRIMARY KEY,
  state          TEXT NOT NULL,
  players_online INTEGER,
  players_max    INTEGER,
  cpu_pct        REAL,
  mem_bytes      INTEGER,
  mem_limit      INTEGER,
  disk_free      INTEGER
);
CREATE TABLE events (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  ts          INTEGER NOT NULL,
  kind        TEXT NOT NULL,
  player      TEXT,
  uuid        TEXT,
  source      TEXT NOT NULL,
  detail      TEXT NOT NULL DEFAULT '',
  dedup_key   TEXT NOT NULL UNIQUE,
  ingested_at INTEGER NOT NULL
);
CREATE INDEX events_ts ON events(ts);
CREATE TABLE sessions (
  id              INTEGER PRIMARY KEY AUTOINCREMENT,
  player          TEXT NOT NULL,
  uuid            TEXT,
  start_ts        INTEGER NOT NULL,
  end_ts          INTEGER,
  end_reason      TEXT NOT NULL DEFAULT '',
  start_uncertain INTEGER NOT NULL DEFAULT 0,
  end_uncertain   INTEGER NOT NULL DEFAULT 0,
  source          TEXT NOT NULL
);
CREATE INDEX sessions_start ON sessions(start_ts);
CREATE INDEX sessions_open ON sessions(end_ts) WHERE end_ts IS NULL;
CREATE TABLE backups (
  id                TEXT PRIMARY KEY,
  kind              TEXT NOT NULL,
  created_at        INTEGER NOT NULL,
  file_name         TEXT NOT NULL,
  size_bytes        INTEGER NOT NULL,
  sha256            TEXT NOT NULL,
  manifest          TEXT NOT NULL,
  verified          INTEGER,
  verified_at       INTEGER,
  verify_error      TEXT NOT NULL DEFAULT '',
  downtime_ms       INTEGER NOT NULL DEFAULT 0,
  created_by        TEXT NOT NULL,
  downloaded_at     INTEGER,
  note              TEXT NOT NULL DEFAULT ''
);
CREATE TABLE audit (
  id     INTEGER PRIMARY KEY AUTOINCREMENT,
  ts     INTEGER NOT NULL,
  actor  TEXT NOT NULL,
  action TEXT NOT NULL,
  target TEXT NOT NULL DEFAULT '',
  result TEXT NOT NULL,
  detail TEXT NOT NULL DEFAULT ''
);
CREATE INDEX audit_ts ON audit(ts);
`,
}
