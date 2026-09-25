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
	// 0.3.0: several servers. Rows made by 0.1.0 and 0.2.0 get an empty
	// server_id here; migrateSingleServer assigns them to the migrated server.
	`
CREATE TABLE servers (
  id               TEXT PRIMARY KEY,
  name             TEXT NOT NULL,
  slug             TEXT NOT NULL UNIQUE,
  game             TEXT NOT NULL,
  type             TEXT NOT NULL,
  layout           TEXT NOT NULL,
  game_port        INTEGER NOT NULL UNIQUE,
  config           TEXT NOT NULL,
  desired          TEXT NOT NULL DEFAULT 'stopped',
  position         INTEGER NOT NULL DEFAULT 0,
  created_at       INTEGER NOT NULL,
  collecting_since INTEGER,
  log_cursor       TEXT NOT NULL DEFAULT ''
);
ALTER TABLE operations ADD COLUMN server_id TEXT NOT NULL DEFAULT '';
CREATE INDEX operations_server ON operations(server_id, started_at);
ALTER TABLE events ADD COLUMN server_id TEXT NOT NULL DEFAULT '';
CREATE INDEX events_server_ts ON events(server_id, ts);
ALTER TABLE sessions ADD COLUMN server_id TEXT NOT NULL DEFAULT '';
CREATE INDEX sessions_server_start ON sessions(server_id, start_ts);
ALTER TABLE backups ADD COLUMN server_id TEXT NOT NULL DEFAULT '';
CREATE INDEX backups_server ON backups(server_id, created_at);
ALTER TABLE audit ADD COLUMN server_id TEXT NOT NULL DEFAULT '';
CREATE TABLE samples_v2 (
  server_id      TEXT NOT NULL DEFAULT '',
  ts             INTEGER NOT NULL,
  state          TEXT NOT NULL,
  players_online INTEGER,
  players_max    INTEGER,
  cpu_pct        REAL,
  mem_bytes      INTEGER,
  mem_limit      INTEGER,
  disk_free      INTEGER,
  PRIMARY KEY (server_id, ts)
);
INSERT INTO samples_v2(server_id, ts, state, players_online, players_max, cpu_pct, mem_bytes, mem_limit, disk_free)
  SELECT '', ts, state, players_online, players_max, cpu_pct, mem_bytes, mem_limit, disk_free FROM samples;
DROP TABLE samples;
ALTER TABLE samples_v2 RENAME TO samples;
`,
	// Wave 7 (0.4.0): schedules, sleep when nobody's playing, and backup rules
	// with encrypted copies somewhere else. Off-site secrets and keys live in
	// the offsite table, never in logs or responses.
	`
CREATE TABLE schedules (
  id         TEXT PRIMARY KEY,
  server_id  TEXT NOT NULL,
  name       TEXT NOT NULL DEFAULT '',
  kind       TEXT NOT NULL,
  timing     TEXT NOT NULL,
  payload    TEXT NOT NULL DEFAULT '{}',
  enabled    INTEGER NOT NULL DEFAULT 1,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL,
  created_by TEXT NOT NULL DEFAULT '',
  updated_by TEXT NOT NULL DEFAULT '',
  last_run   TEXT NOT NULL DEFAULT ''
);
CREATE INDEX schedules_server ON schedules(server_id, created_at);
CREATE TABLE schedule_runs (
  id           INTEGER PRIMARY KEY AUTOINCREMENT,
  schedule_id  TEXT NOT NULL,
  server_id    TEXT NOT NULL,
  kind         TEXT NOT NULL,
  due          INTEGER NOT NULL,
  started_at   INTEGER,
  finished_at  INTEGER,
  result       TEXT NOT NULL,
  reason       TEXT NOT NULL DEFAULT '',
  detail       TEXT NOT NULL DEFAULT '',
  operation_id TEXT NOT NULL DEFAULT '',
  players      INTEGER,
  UNIQUE (schedule_id, due)
);
CREATE INDEX schedule_runs_server ON schedule_runs(server_id, due);
ALTER TABLE servers ADD COLUMN sleep TEXT NOT NULL DEFAULT '{}';
CREATE TABLE sleep_periods (
  id        INTEGER PRIMARY KEY AUTOINCREMENT,
  server_id TEXT NOT NULL,
  start_ts  INTEGER NOT NULL,
  end_ts    INTEGER,
  woke_by   TEXT NOT NULL DEFAULT ''
);
CREATE INDEX sleep_periods_server ON sleep_periods(server_id, start_ts);
ALTER TABLE servers ADD COLUMN backup_rules TEXT NOT NULL DEFAULT '';
CREATE TABLE offsite (
  server_id    TEXT PRIMARY KEY,
  enabled      INTEGER NOT NULL DEFAULT 0,
  config       TEXT NOT NULL DEFAULT '{}',
  secret       TEXT NOT NULL DEFAULT '',
  password     TEXT NOT NULL DEFAULT '',
  private_key  TEXT NOT NULL DEFAULT '',
  ssh_public   TEXT NOT NULL DEFAULT '',
  keys         TEXT NOT NULL DEFAULT '',
  key_saved_at INTEGER,
  updated_at   INTEGER NOT NULL
);
CREATE TABLE offsite_copies (
  server_id         TEXT NOT NULL,
  backup_id         TEXT NOT NULL,
  kind              TEXT NOT NULL,
  backup_created_at INTEGER NOT NULL,
  file_name         TEXT NOT NULL,
  size_bytes        INTEGER NOT NULL,
  minecraft_version TEXT NOT NULL DEFAULT '',
  level_name        TEXT NOT NULL DEFAULT '',
  copy              TEXT NOT NULL,
  copied_at         INTEGER NOT NULL,
  PRIMARY KEY (server_id, backup_id)
);
CREATE TABLE offsite_uploads (
  server_id    TEXT NOT NULL,
  backup_id    TEXT NOT NULL,
  state        TEXT NOT NULL DEFAULT '',
  attempts     INTEGER NOT NULL DEFAULT 0,
  next_attempt INTEGER NOT NULL DEFAULT 0,
  last_error   TEXT NOT NULL DEFAULT '',
  error_hint   TEXT NOT NULL DEFAULT '',
  error_kind   TEXT NOT NULL DEFAULT '',
  error_params TEXT NOT NULL DEFAULT '',
  created_at   INTEGER NOT NULL,
  PRIMARY KEY (server_id, backup_id)
);
`,
}
