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
	// Plugins and mods Playkeeper installed, one row per add-on and server,
	// and the map pre-generation Playkeeper started last on each server:
	// ended is '' until it is 'finished' or 'cancelled', and rate (chunks a
	// second) is kept for the next task's estimates. addons_changed_at is
	// when a server's add-on files last changed, so a running server that
	// hasn't loaded the change shows it needs a restart.
	`
CREATE TABLE addons (
  server_id      TEXT NOT NULL,
  source         TEXT NOT NULL,
  project_id     TEXT NOT NULL,
  slug           TEXT NOT NULL DEFAULT '',
  name           TEXT NOT NULL,
  summary        TEXT NOT NULL DEFAULT '',
  icon_url       TEXT NOT NULL DEFAULT '',
  version_id     TEXT NOT NULL,
  version_number TEXT NOT NULL DEFAULT '',
  channel        TEXT NOT NULL DEFAULT 'release',
  published      INTEGER NOT NULL DEFAULT 0,
  file_name      TEXT NOT NULL,
  hash_algo      TEXT NOT NULL,
  hash           TEXT NOT NULL,
  size_bytes     INTEGER NOT NULL DEFAULT 0,
  dependency_of  TEXT NOT NULL DEFAULT '',
  requires       TEXT NOT NULL DEFAULT '[]',
  installed_at   INTEGER NOT NULL,
  PRIMARY KEY (server_id, source, project_id)
);
CREATE TABLE pregen (
  server_id          TEXT PRIMARY KEY,
  world              TEXT NOT NULL,
  preset             TEXT NOT NULL DEFAULT '',
  radius             INTEGER NOT NULL DEFAULT 0,
  pause_for_players  INTEGER NOT NULL DEFAULT 1,
  paused_by_user     INTEGER NOT NULL DEFAULT 0,
  paused_by_policy   INTEGER NOT NULL DEFAULT 0,
  paused_for         TEXT NOT NULL DEFAULT '',
  started_at         INTEGER NOT NULL,
  ended              TEXT NOT NULL DEFAULT '',
  ended_at           INTEGER,
  world_bytes_before INTEGER,
  world_bytes_after  INTEGER,
  chunks             INTEGER NOT NULL DEFAULT 0,
  total              INTEGER NOT NULL DEFAULT 0,
  elapsed_secs       INTEGER NOT NULL DEFAULT 0,
  rate               REAL NOT NULL DEFAULT 0
);
ALTER TABLE servers ADD COLUMN addons_changed_at INTEGER;
`,
	// 0.4.0: the dashboard's certificates, one row per name: certs.Status
	// and its Certificate. Times are Unix milliseconds.
	`
CREATE TABLE certificates (
  name         TEXT PRIMARY KEY,
  names        TEXT NOT NULL,
  source       TEXT NOT NULL,
  challenge    TEXT NOT NULL,
  file         TEXT NOT NULL DEFAULT '',
  not_before   INTEGER,
  not_after    INTEGER,
  renew_at     INTEGER,
  issuer       TEXT NOT NULL DEFAULT '',
  serial       TEXT NOT NULL DEFAULT '',
  sha256       TEXT NOT NULL DEFAULT '',
  last_attempt INTEGER,
  next_attempt INTEGER,
  failures     INTEGER NOT NULL DEFAULT 0,
  problem      TEXT NOT NULL DEFAULT ''
);
`,
	// Online backups, and the history behind lag and memory advice.
	// saving_paused_since is set while a backup may have left world saving
	// off; gc_windows holds 15-minute summaries of the JVM's GC log.
	`
ALTER TABLE backups ADD COLUMN saving_paused_ms INTEGER NOT NULL DEFAULT 0;
ALTER TABLE backups ADD COLUMN duration_ms INTEGER NOT NULL DEFAULT 0;
ALTER TABLE servers ADD COLUMN saving_paused_since INTEGER;
ALTER TABLE servers ADD COLUMN gc_cursor TEXT NOT NULL DEFAULT '';
ALTER TABLE samples ADD COLUMN tps REAL;
ALTER TABLE samples ADD COLUMN mspt REAL;
CREATE TABLE gc_windows (
  server_id           TEXT NOT NULL,
  start               INTEGER NOT NULL,
  collections         INTEGER NOT NULL,
  min_after_mb        INTEGER NOT NULL,
  max_after_mb        INTEGER NOT NULL,
  heap_mb             INTEGER NOT NULL,
  full_gcs            INTEGER NOT NULL,
  evacuation_failures INTEGER NOT NULL,
  pause_ms            REAL NOT NULL,
  max_pause_ms        REAL NOT NULL,
  PRIMARY KEY (server_id, start)
);
`,
	// wave 5: Discord alerts and the live status message, one webhook for
	// the whole machine. webhook_url is a secret: it never leaves the agent.
	`
CREATE TABLE discord (
  id                INTEGER PRIMARY KEY CHECK (id = 1),
  webhook_url       TEXT NOT NULL DEFAULT '',
  webhook_name      TEXT NOT NULL DEFAULT '',
  alerts            TEXT NOT NULL,
  live_status       INTEGER NOT NULL DEFAULT 0,
  status_message_id TEXT NOT NULL DEFAULT '',
  public_host       TEXT NOT NULL DEFAULT '',
  connected_at      INTEGER NOT NULL DEFAULT 0,
  update_alerted    TEXT NOT NULL DEFAULT ''
);
`,
	// wave 5: the Minecraft version each server's Discord update alert last
	// went out for.
	`
ALTER TABLE servers ADD COLUMN minecraft_update_alerted TEXT NOT NULL DEFAULT '';
`,
}
