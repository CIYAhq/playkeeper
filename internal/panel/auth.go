package panel

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"golang.org/x/crypto/argon2"

	"github.com/CIYAhq/playkeeper/internal/invites"
)

var panelMigrations = []string{
	`
CREATE TABLE users (
  id                  INTEGER PRIMARY KEY AUTOINCREMENT,
  username            TEXT NOT NULL UNIQUE,
  password_hash       TEXT NOT NULL,
  created_at          INTEGER NOT NULL,
  password_changed_at INTEGER NOT NULL
);
CREATE TABLE sessions (
  id_hash    TEXT PRIMARY KEY,
  user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  csrf       TEXT NOT NULL,
  created_at INTEGER NOT NULL,
  last_seen  INTEGER NOT NULL,
  expires_at INTEGER NOT NULL
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
`,
	// 0.3.0: projects, machines and roles (decision 0004). Existing accounts
	// are owners of the install.
	`
ALTER TABLE users ADD COLUMN role TEXT NOT NULL DEFAULT 'owner';
CREATE TABLE projects (
  id         TEXT PRIMARY KEY,
  name       TEXT NOT NULL,
  created_at INTEGER NOT NULL
);
CREATE TABLE project_members (
  project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  role       TEXT NOT NULL,
  created_at INTEGER NOT NULL,
  PRIMARY KEY (project_id, user_id)
);
CREATE TABLE machines (
  id         TEXT PRIMARY KEY,
  project_id TEXT NOT NULL REFERENCES projects(id),
  name       TEXT NOT NULL DEFAULT '',
  kind       TEXT NOT NULL,
  endpoint   TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL
);
CREATE TABLE user_prefs (
  user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  key     TEXT NOT NULL,
  value   TEXT NOT NULL,
  PRIMARY KEY (user_id, key)
);
CREATE TABLE player_heads (
  name       TEXT PRIMARY KEY,
  status     TEXT NOT NULL,
  png        BLOB,
  fetched_at INTEGER NOT NULL
);
`,
	// 0.4.0, wave 2: two-factor sign-in. Each user's authenticator app, with
	// the session that started a setup not yet confirmed; and sign-ins that
	// passed the password but not yet the second step, kept apart from
	// sessions so they can never be taken for one.
	`
CREATE TABLE user_factors (
  user_id             INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  kind                TEXT NOT NULL DEFAULT 'totp',
  secret              TEXT NOT NULL,
  created_at          INTEGER NOT NULL,
  setup_session       TEXT NOT NULL DEFAULT '',
  confirmed_at        INTEGER,
  last_step           INTEGER NOT NULL DEFAULT 0,
  last_used_at        INTEGER,
  recovery_hashes     TEXT NOT NULL DEFAULT '[]',
  recovery_created_at INTEGER,
  failures            INTEGER NOT NULL DEFAULT 0,
  recovery_failures   INTEGER NOT NULL DEFAULT 0,
  locked_until        INTEGER,
  revision            INTEGER NOT NULL,
  PRIMARY KEY (user_id, kind)
);
CREATE TABLE pending_logins (
  id_hash    TEXT PRIMARY KEY,
  user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  created_at INTEGER NOT NULL,
  expires_at INTEGER NOT NULL,
  attempts   INTEGER NOT NULL DEFAULT 0
);
`,
	// Wave 5: invite links for friends and team members, join requests, how
	// players got in, and the servers each team member can use. The servers
	// column fails closed: only the owner starts with all servers. One owner
	// per install, and usernames are unique in any capitalisation.
	// admin_factor is the confirmed_at of the two-factor setup an owner or
	// admin confirmed Admin rights with; factor_seen is the one the
	// dashboard last saw, to notice when it's turned on or off.
	`
CREATE TABLE invites (
  id          TEXT    PRIMARY KEY,
  kind        TEXT    NOT NULL CHECK (kind IN ('player', 'member')),
  code_hash   TEXT    NOT NULL UNIQUE,
  code        TEXT    NOT NULL DEFAULT '',
  project_id  TEXT    NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  server_id   TEXT    NOT NULL DEFAULT '',
  role        TEXT    NOT NULL DEFAULT '',
  servers     TEXT    NOT NULL DEFAULT '',
  approval    TEXT    NOT NULL DEFAULT '',
  label       TEXT    NOT NULL DEFAULT '',
  created_by  INTEGER NOT NULL,
  created_at  INTEGER NOT NULL,
  expires_at  INTEGER NOT NULL,
  max_uses    INTEGER NOT NULL CHECK (max_uses BETWEEN 0 AND 100),
  uses        INTEGER NOT NULL DEFAULT 0,
  revoked_at  INTEGER NOT NULL DEFAULT 0,
  CHECK (kind = 'player' OR (max_uses = 1 AND expires_at > 0))
);
CREATE INDEX invites_server ON invites(server_id, created_at);
CREATE INDEX invites_project ON invites(project_id, kind, created_at);
CREATE TABLE join_requests (
  id          TEXT    PRIMARY KEY,
  invite_id   TEXT    NOT NULL REFERENCES invites(id) ON DELETE CASCADE,
  server_id   TEXT    NOT NULL,
  player_uuid TEXT    NOT NULL,
  player_name TEXT    NOT NULL,
  address     TEXT    NOT NULL DEFAULT '',
  state       TEXT    NOT NULL CHECK (state IN ('pending', 'approved', 'declined')),
  created_at  INTEGER NOT NULL,
  decided_at  INTEGER NOT NULL DEFAULT 0,
  decided_by  INTEGER NOT NULL DEFAULT 0
);
CREATE UNIQUE INDEX join_requests_one_pending ON join_requests(server_id, player_uuid) WHERE state = 'pending';
CREATE INDEX join_requests_server ON join_requests(server_id, state, created_at);
CREATE INDEX join_requests_invite ON join_requests(invite_id, state);
CREATE INDEX join_requests_address ON join_requests(address) WHERE state = 'pending';
CREATE TABLE player_origins (
  server_id   TEXT    NOT NULL,
  player_uuid TEXT    NOT NULL,
  player_name TEXT    NOT NULL,
  invite_id   TEXT    NOT NULL,
  request_id  TEXT    NOT NULL DEFAULT '',
  joined_at   INTEGER NOT NULL,
  PRIMARY KEY (server_id, player_uuid)
);
ALTER TABLE project_members ADD COLUMN servers TEXT NOT NULL DEFAULT '';
UPDATE project_members SET servers = '*' WHERE user_id IN (SELECT id FROM users WHERE role = 'owner');
ALTER TABLE project_members ADD COLUMN admin_factor INTEGER NOT NULL DEFAULT 0;
ALTER TABLE project_members ADD COLUMN factor_seen INTEGER NOT NULL DEFAULT 0;
CREATE UNIQUE INDEX users_username_nocase ON users(username COLLATE NOCASE);
CREATE UNIQUE INDEX users_one_owner ON users(role) WHERE role = 'owner';
`,
	// Machines that joined over a machine link (kind 'remote'), their join
	// codes (a keyed hash only, never the code), what happened to each, and
	// which machine runs each server with its last known status and the
	// joined machine that also lists it, if any.
	`
ALTER TABLE machines ADD COLUMN public_key BLOB;
ALTER TABLE machines ADD COLUMN joined_from TEXT NOT NULL DEFAULT '';
ALTER TABLE machines ADD COLUMN created_by TEXT NOT NULL DEFAULT '';
ALTER TABLE machines ADD COLUMN version TEXT NOT NULL DEFAULT '';
ALTER TABLE machines ADD COLUMN last_seen INTEGER NOT NULL DEFAULT 0;
ALTER TABLE machines ADD COLUMN last_addr TEXT NOT NULL DEFAULT '';
ALTER TABLE machines ADD COLUMN revoked_at INTEGER NOT NULL DEFAULT 0;
ALTER TABLE machines ADD COLUMN revoked_by TEXT NOT NULL DEFAULT '';
CREATE UNIQUE INDEX machines_public_key ON machines(public_key) WHERE public_key IS NOT NULL;
CREATE TABLE machine_join_codes (
  id         TEXT PRIMARY KEY,
  hash       BLOB NOT NULL,
  created_at INTEGER NOT NULL,
  expires_at INTEGER NOT NULL,
  created_by TEXT NOT NULL DEFAULT '',
  used_at    INTEGER NOT NULL DEFAULT 0,
  machine_id TEXT NOT NULL DEFAULT '',
  name       TEXT NOT NULL DEFAULT '',
  dials      TEXT NOT NULL DEFAULT ''
);
CREATE TABLE machine_events (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  machine_id TEXT NOT NULL,
  ts         INTEGER NOT NULL,
  kind       TEXT NOT NULL,
  actor      TEXT NOT NULL DEFAULT '',
  address    TEXT NOT NULL DEFAULT '',
  code       TEXT NOT NULL DEFAULT ''
);
CREATE INDEX machine_events_machine ON machine_events(machine_id, id);
CREATE TABLE server_machines (
  server_id   TEXT PRIMARY KEY,
  machine_id  TEXT NOT NULL,
  status      TEXT NOT NULL DEFAULT '',
  seen_at     INTEGER NOT NULL DEFAULT 0,
  disputed_by TEXT NOT NULL DEFAULT ''
);
`,
	// API tokens for AI agents (a hash only, never the token), with the
	// role and servers each was made for ('*' is every server), and what
	// agents did with them. Revoked tokens stay, so their names still show
	// in the activity log.
	`
CREATE TABLE api_tokens (
  id           TEXT PRIMARY KEY,
  user_id      INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  name         TEXT NOT NULL,
  token_hash   TEXT NOT NULL UNIQUE,
  role         TEXT NOT NULL,
  servers      TEXT NOT NULL,
  created_at   INTEGER NOT NULL,
  expires_at   INTEGER NOT NULL,
  last_used_at INTEGER NOT NULL DEFAULT 0,
  revoked_at   INTEGER NOT NULL DEFAULT 0,
  revoked_by   TEXT NOT NULL DEFAULT ''
);
CREATE INDEX api_tokens_user ON api_tokens(user_id);
CREATE TABLE agent_activity (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  token_id    TEXT NOT NULL REFERENCES api_tokens(id) ON DELETE CASCADE,
  ts          INTEGER NOT NULL,
  tool        TEXT NOT NULL,
  server_id   TEXT NOT NULL DEFAULT '',
  server_name TEXT NOT NULL DEFAULT '',
  count       INTEGER NOT NULL DEFAULT 1
);
CREATE INDEX agent_activity_token ON agent_activity(token_id, id);
`,
	// What the dashboard remembers about itself, such as the version it last
	// ran.
	`
CREATE TABLE panel_meta (
  key   TEXT PRIMARY KEY,
  value TEXT NOT NULL
);
`,
	// Joins refused lately for their code, with the network each came from,
	// so that a pause after too many outlasts a restart.
	`
CREATE TABLE machine_join_failures (
  at      INTEGER NOT NULL,
  network TEXT NOT NULL
);
`,
	// The role each API token's account held when the token was made (owner,
	// or its project role): a token stops working once its account holds a
	// lower one or leaves the team.
	`
ALTER TABLE api_tokens ADD COLUMN account_role TEXT NOT NULL DEFAULT '';
UPDATE api_tokens SET account_role = COALESCE((
  SELECT CASE WHEN u.role = 'owner' THEN 'owner'
    ELSE (SELECT pm.role FROM project_members pm WHERE pm.user_id = u.id ORDER BY pm.created_at LIMIT 1) END
  FROM users u WHERE u.id = api_tokens.user_id), '');
`,
	// The friends' pack pages and shared maps the dashboard serves, by a
	// hash of each link's token: the server it was made for, and the
	// machine that made it, which alone answers for it.
	`
CREATE TABLE public_links (
  kind       TEXT NOT NULL CHECK (kind IN ('pack', 'map')),
  token_hash TEXT NOT NULL,
  server_id  TEXT NOT NULL,
  machine_id TEXT NOT NULL,
  created_at INTEGER NOT NULL,
  PRIMARY KEY (kind, token_hash)
);
`,
	// The slug the dashboard shows for each server, which stays once shown
	// (see stableSlugs).
	`
ALTER TABLE server_machines ADD COLUMN slug TEXT NOT NULL DEFAULT '';
`,
	// A creator's allowance (the managed beta): on the invite and on the
	// member it made, the servers they may create and the memory between
	// them (see invites.Allowance). Zero for everyone else.
	`
ALTER TABLE invites ADD COLUMN allowance_servers INTEGER NOT NULL DEFAULT 0;
ALTER TABLE invites ADD COLUMN allowance_memory_mb INTEGER NOT NULL DEFAULT 0;
ALTER TABLE project_members ADD COLUMN allowance_servers INTEGER NOT NULL DEFAULT 0;
ALTER TABLE project_members ADD COLUMN allowance_memory_mb INTEGER NOT NULL DEFAULT 0;
`,
	// The servers each creator created, which count against their allowance
	// and which alone they may delete.
	`
CREATE TABLE creator_servers (
  server_id  TEXT    PRIMARY KEY,
  user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  created_at INTEGER NOT NULL
);
CREATE INDEX creator_servers_user ON creator_servers(user_id);
`,
	// Sell on Whop: the Whop account this dashboard sells servers for, with
	// its API key (one row), and the plans its store sells with what each
	// lets a buyer create, from the plan's metadata on Whop ('store') or
	// set here ('owner').
	`
CREATE TABLE whop_account (
  id           INTEGER PRIMARY KEY CHECK (id = 1),
  account_id   TEXT    NOT NULL,
  title        TEXT    NOT NULL DEFAULT '',
  route        TEXT    NOT NULL DEFAULT '',
  api_key      TEXT    NOT NULL,
  connected_by TEXT    NOT NULL,
  connected_at INTEGER NOT NULL,
  synced_at    INTEGER NOT NULL DEFAULT 0,
  problem      TEXT    NOT NULL DEFAULT ''
);
CREATE TABLE whop_plans (
  plan_id             TEXT    PRIMARY KEY,
  product_id          TEXT    NOT NULL,
  product_title       TEXT    NOT NULL DEFAULT '',
  title               TEXT    NOT NULL DEFAULT '',
  price               TEXT    NOT NULL DEFAULT '',
  visibility          TEXT    NOT NULL DEFAULT '',
  trial_days          INTEGER NOT NULL DEFAULT 0,
  allowance_servers   INTEGER NOT NULL DEFAULT 0,
  allowance_memory_mb INTEGER NOT NULL DEFAULT 0,
  allowance_from      TEXT    NOT NULL DEFAULT '' CHECK (allowance_from IN ('', 'store', 'owner')),
  position            INTEGER NOT NULL DEFAULT 0
);
`,
	// Sell on Whop's customers: the webhook Whop sends membership events to,
	// with its signing secret; each plan's disk, when its metadata says one;
	// each membership of the store's plans as the dashboard last heard of
	// it, and whether its cancellation was reminded; each customer, with the
	// plan last given to the hosting core, whether their plans' end paused
	// them, what went wrong and the support chat with them; the messages for
	// customers, waiting to go or sent; and the deliveries already handled,
	// so a retried one counts once.
	`
ALTER TABLE whop_account ADD COLUMN webhook_id     TEXT    NOT NULL DEFAULT '';
ALTER TABLE whop_account ADD COLUMN webhook_url    TEXT    NOT NULL DEFAULT '';
ALTER TABLE whop_account ADD COLUMN webhook_secret TEXT    NOT NULL DEFAULT '';
ALTER TABLE whop_account ADD COLUMN polled_at      INTEGER NOT NULL DEFAULT 0;
ALTER TABLE whop_plans ADD COLUMN disk_gb INTEGER NOT NULL DEFAULT 0;
CREATE TABLE whop_memberships (
  membership_id        TEXT    PRIMARY KEY,
  whop_user_id         TEXT    NOT NULL,
  plan_id              TEXT    NOT NULL,
  status               TEXT    NOT NULL,
  cancel_at_period_end INTEGER NOT NULL DEFAULT 0,
  period_end           INTEGER NOT NULL DEFAULT 0,
  stale                INTEGER NOT NULL DEFAULT 0,
  told_cancel          INTEGER NOT NULL DEFAULT 0,
  updated_at           INTEGER NOT NULL
);
CREATE INDEX whop_memberships_user ON whop_memberships(whop_user_id);
CREATE TABLE whop_customers (
  whop_user_id TEXT    PRIMARY KEY,
  handle       TEXT    NOT NULL DEFAULT '',
  applied      TEXT    NOT NULL DEFAULT '',
  paused       INTEGER NOT NULL DEFAULT 0,
  attempts     INTEGER NOT NULL DEFAULT 0,
  next_try_at  INTEGER NOT NULL DEFAULT 0,
  problem      TEXT    NOT NULL DEFAULT '',
  channel_id   TEXT    NOT NULL DEFAULT '',
  updated_at   INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE whop_messages (
  id           INTEGER PRIMARY KEY AUTOINCREMENT,
  whop_user_id TEXT    NOT NULL,
  kind         TEXT    NOT NULL DEFAULT '',
  text         TEXT    NOT NULL,
  created_at   INTEGER NOT NULL,
  attempts     INTEGER NOT NULL DEFAULT 0,
  next_try_at  INTEGER NOT NULL DEFAULT 0,
  sent_at      INTEGER NOT NULL DEFAULT 0,
  problem      TEXT    NOT NULL DEFAULT ''
);
CREATE INDEX whop_messages_waiting ON whop_messages(sent_at, id);
CREATE TABLE whop_deliveries (
  id          TEXT    PRIMARY KEY,
  received_at INTEGER NOT NULL
);
CREATE INDEX whop_deliveries_received ON whop_deliveries(received_at);
`,
	// Sign in with Whop: the Whop app customers sign in through, with its
	// secret when it has one, and each sign-in on its way through Whop, by
	// the hash of its state, with its PKCE verifier.
	`
ALTER TABLE whop_account ADD COLUMN oauth_client_id     TEXT NOT NULL DEFAULT '';
ALTER TABLE whop_account ADD COLUMN oauth_client_secret TEXT NOT NULL DEFAULT '';
CREATE TABLE whop_signins (
  state_hash TEXT    PRIMARY KEY,
  verifier   TEXT    NOT NULL,
  created_at INTEGER NOT NULL
);
`,
	// Hetzner stock: the owner's read-only Hetzner API token and the server
	// type they watch (one row), with what the last checks found: each
	// location's stock as JSON, the last problem, and whether Hetzner
	// refused the token, which stops the checks until it's replaced.
	`
CREATE TABLE hetzner_watch (
  id          INTEGER PRIMARY KEY CHECK (id = 1),
  token       TEXT    NOT NULL,
  server_type TEXT    NOT NULL,
  set_by      TEXT    NOT NULL,
  set_at      INTEGER NOT NULL,
  checked_at  INTEGER NOT NULL DEFAULT 0,
  problem     TEXT    NOT NULL DEFAULT '',
  refused     INTEGER NOT NULL DEFAULT 0,
  failures    INTEGER NOT NULL DEFAULT 0,
  places      TEXT    NOT NULL DEFAULT '[]'
);
`,
	// Placement: each customer's home machine, where their plan's memory is
	// set aside and their servers run, or '' while they wait for room. A
	// creator without a row, as an owner's invite makes, lives on the
	// dashboard's own machine.
	`
CREATE TABLE customer_homes (
  user_id    INTEGER PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
  machine_id TEXT    NOT NULL DEFAULT '',
  placed_at  INTEGER NOT NULL
);
`,
	// Selling only what fits: each plan's stock on Whop as last read or
	// set, whether it's unlimited, and whether the plan is free; when each
	// customer was last given their plan; how many more of each plan the
	// machines can take, as last said, and when; and the stock last set on
	// Whop, with how many of the plan's memberships the dashboard knew of
	// then (-1 before one was set).
	`
ALTER TABLE whop_plans ADD COLUMN stock           INTEGER NOT NULL DEFAULT 0;
ALTER TABLE whop_plans ADD COLUMN unlimited_stock INTEGER NOT NULL DEFAULT 1;
ALTER TABLE whop_plans ADD COLUMN free            INTEGER NOT NULL DEFAULT 0;
ALTER TABLE whop_customers ADD COLUMN applied_at INTEGER NOT NULL DEFAULT 0;
CREATE TABLE whop_stock (
  plan_id TEXT    PRIMARY KEY,
  want    INTEGER NOT NULL,
  set_at  INTEGER NOT NULL,
  written INTEGER NOT NULL DEFAULT -1,
  known   INTEGER NOT NULL DEFAULT -1
);
`,
	// Disk limits: the disk a creator's or customer's servers may take
	// between them, or 0 for the default from their memory (see
	// invites.Allowance).
	`
ALTER TABLE invites ADD COLUMN allowance_disk_gb INTEGER NOT NULL DEFAULT 0;
ALTER TABLE project_members ADD COLUMN allowance_disk_gb INTEGER NOT NULL DEFAULT 0;
`,
	// The hosting core's customers: the account a billing provider's
	// customer has, found only by the provider and its id for them, never
	// by name, with the handle they had there and where the account stands
	// (see customers.go).
	`
CREATE TABLE customers (
  user_id    INTEGER PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
  provider   TEXT    NOT NULL,
  subject    TEXT    NOT NULL,
  handle     TEXT    NOT NULL DEFAULT '',
  plan_id    TEXT    NOT NULL DEFAULT '',
  state      TEXT    NOT NULL DEFAULT 'active',
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL,
  UNIQUE(provider, subject)
);
`,
	// The ready server's messages: when each customer was told their server
	// is ready to start, and when, while they waited for room, that it's
	// being set up (see readyserver.go).
	`
ALTER TABLE customers ADD COLUMN told_ready   INTEGER NOT NULL DEFAULT 0;
ALTER TABLE customers ADD COLUMN told_waiting INTEGER NOT NULL DEFAULT 0;
`,
	// One seller per business: the address this dashboard last marked the
	// store's products with, and the dashboard that sells for the store
	// instead of this one, with when it took the store over from this one (0
	// while this one's own takeover isn't done). This one doesn't sell until
	// the owner takes the store over.
	`
ALTER TABLE whop_account ADD COLUMN marked_as     TEXT    NOT NULL DEFAULT '';
ALTER TABLE whop_account ADD COLUMN taken_over_by TEXT    NOT NULL DEFAULT '';
ALTER TABLE whop_account ADD COLUMN taken_over_at INTEGER NOT NULL DEFAULT 0;
`,
	// Pausing a customer whose plan ended: when, why, and when their
	// servers are deleted unless they renew (see customers.go).
	`
ALTER TABLE customers ADD COLUMN paused_at    INTEGER NOT NULL DEFAULT 0;
ALTER TABLE customers ADD COLUMN delete_after INTEGER NOT NULL DEFAULT 0;
ALTER TABLE customers ADD COLUMN pause_reason TEXT    NOT NULL DEFAULT '';
`,
	// A paused customer's servers deleted once the grace period ended: when,
	// and the machine keeping their final backups (see deletion.go).
	`
ALTER TABLE customers ADD COLUMN servers_deleted_at    INTEGER NOT NULL DEFAULT 0;
ALTER TABLE customers ADD COLUMN final_backups_machine TEXT    NOT NULL DEFAULT '';
`,
	// Confirming joined machines: when and by whom the owner confirmed a
	// joined machine is theirs, so it takes customers, or 0 and '' (see
	// machinecustomers.go).
	`
ALTER TABLE machines ADD COLUMN customers_at INTEGER NOT NULL DEFAULT 0;
ALTER TABLE machines ADD COLUMN customers_by TEXT    NOT NULL DEFAULT '';
`,
	// Confirming joined machines by themselves: when the owner last stopped
	// a joined machine taking customers, after which the Hetzner token
	// doesn't confirm it again, or 0 (see autoconfirm.go).
	`
ALTER TABLE machines ADD COLUMN customers_stopped_at INTEGER NOT NULL DEFAULT 0;
`,
	// Moving customers: each customer the owner moves to another machine,
	// who asked and when, and why it stopped, if it did; each server being
	// moved, from and to which machine, whether it ran, and the operation
	// making its copy there; each copy of a server a move left on a machine,
	// how many days its final backup is kept there, and when it went (0
	// until it did); and each server a failed move left stopped though it
	// ran, until it starts again (see moves.go).
	`
CREATE TABLE customer_moves (
  user_id    INTEGER PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
  to_machine TEXT    NOT NULL,
  started_at INTEGER NOT NULL,
  started_by TEXT    NOT NULL,
  error      TEXT    NOT NULL DEFAULT ''
);
CREATE TABLE server_moves (
  server_id    TEXT    PRIMARY KEY,
  user_id      INTEGER NOT NULL,
  from_machine TEXT    NOT NULL,
  to_machine   TEXT    NOT NULL,
  ran          INTEGER NOT NULL DEFAULT 0,
  made_by      TEXT    NOT NULL DEFAULT ''
);
CREATE TABLE left_copies (
  server_id  TEXT    NOT NULL,
  machine_id TEXT    NOT NULL,
  user_id    INTEGER NOT NULL,
  keep_days  INTEGER NOT NULL DEFAULT 0,
  left_at    INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY(server_id, machine_id)
);
CREATE TABLE move_restarts (
  server_id  TEXT    PRIMARY KEY,
  machine_id TEXT    NOT NULL,
  user_id    INTEGER NOT NULL
);
`,
	// Customers per store: the store each billing provider's customer bought
	// from, such as a Whop business, which with the provider and its id for
	// them finds their account, so someone who buys from two stores has two
	// accounts; and the store a sign-in with Whop is for, when it names one.
	// The Whop customers so far bought from the store the dashboard sells
	// for; with none connected, the next store connected takes them on (see
	// adoptWhopCustomers).
	`
CREATE TABLE customers_by_store (
  user_id               INTEGER PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
  provider              TEXT    NOT NULL,
  store                 TEXT    NOT NULL DEFAULT '',
  subject               TEXT    NOT NULL,
  handle                TEXT    NOT NULL DEFAULT '',
  plan_id               TEXT    NOT NULL DEFAULT '',
  state                 TEXT    NOT NULL DEFAULT 'active',
  created_at            INTEGER NOT NULL,
  updated_at            INTEGER NOT NULL,
  told_ready            INTEGER NOT NULL DEFAULT 0,
  told_waiting          INTEGER NOT NULL DEFAULT 0,
  paused_at             INTEGER NOT NULL DEFAULT 0,
  delete_after          INTEGER NOT NULL DEFAULT 0,
  pause_reason          TEXT    NOT NULL DEFAULT '',
  servers_deleted_at    INTEGER NOT NULL DEFAULT 0,
  final_backups_machine TEXT    NOT NULL DEFAULT '',
  UNIQUE(provider, store, subject)
);
INSERT INTO customers_by_store(user_id, provider, store, subject, handle, plan_id, state, created_at, updated_at, told_ready, told_waiting,
  paused_at, delete_after, pause_reason, servers_deleted_at, final_backups_machine)
SELECT user_id, provider, CASE WHEN provider = 'whop' THEN COALESCE((SELECT account_id FROM whop_account WHERE id = 1), '') ELSE '' END,
  subject, handle, plan_id, state, created_at, updated_at, told_ready, told_waiting, paused_at, delete_after, pause_reason, servers_deleted_at, final_backups_machine
FROM customers;
DROP TABLE customers;
ALTER TABLE customers_by_store RENAME TO customers;
ALTER TABLE whop_signins ADD COLUMN store_id TEXT NOT NULL DEFAULT '';
`,
	// Stores: the Whop businesses the dashboard sells for, by business id,
	// each reached with its own API key (the one key store, which Settings ›
	// Sell on Whop manages) or with the Playkeeper Cloud app's key (an app
	// store), with what its reconciler keeps; the Playkeeper Cloud app on the
	// dashboard, whose client id and secret sign customers in and whose key
	// acts on the app stores; and each plan, membership, customer, message
	// and stock kept for its store. The store the dashboard sold for becomes
	// the key store, and what no store sells for goes (see whop_stores.go).
	`
CREATE TABLE whop_stores (
  store_id       TEXT    PRIMARY KEY,
  via            TEXT    NOT NULL CHECK (via IN ('key', 'app')),
  title          TEXT    NOT NULL DEFAULT '',
  route          TEXT    NOT NULL DEFAULT '',
  api_key        TEXT    NOT NULL DEFAULT '',
  connected_by   TEXT    NOT NULL DEFAULT '',
  connected_at   INTEGER NOT NULL,
  synced_at      INTEGER NOT NULL DEFAULT 0,
  problem        TEXT    NOT NULL DEFAULT '',
  webhook_id     TEXT    NOT NULL DEFAULT '',
  webhook_url    TEXT    NOT NULL DEFAULT '',
  webhook_secret TEXT    NOT NULL DEFAULT '',
  polled_at      INTEGER NOT NULL DEFAULT 0,
  marked_as      TEXT    NOT NULL DEFAULT '',
  taken_over_by  TEXT    NOT NULL DEFAULT '',
  taken_over_at  INTEGER NOT NULL DEFAULT 0
);
CREATE UNIQUE INDEX whop_stores_one_key ON whop_stores(via) WHERE via = 'key';
INSERT INTO whop_stores(store_id, via, title, route, api_key, connected_by, connected_at, synced_at, problem, webhook_id, webhook_url, webhook_secret,
  polled_at, marked_as, taken_over_by, taken_over_at)
SELECT account_id, 'key', title, route, api_key, connected_by, connected_at, synced_at, problem, webhook_id, webhook_url, webhook_secret,
  polled_at, marked_as, taken_over_by, taken_over_at FROM whop_account;
CREATE TABLE whop_app (
  id            INTEGER PRIMARY KEY CHECK (id = 1),
  client_id     TEXT    NOT NULL DEFAULT '',
  client_secret TEXT    NOT NULL DEFAULT '',
  api_key       TEXT    NOT NULL DEFAULT ''
);
INSERT INTO whop_app(id, client_id, client_secret) SELECT 1, oauth_client_id, oauth_client_secret FROM whop_account;
ALTER TABLE whop_plans       ADD COLUMN store_id TEXT NOT NULL DEFAULT '';
ALTER TABLE whop_memberships ADD COLUMN store_id TEXT NOT NULL DEFAULT '';
ALTER TABLE whop_messages    ADD COLUMN store_id TEXT NOT NULL DEFAULT '';
ALTER TABLE whop_stock       ADD COLUMN store_id TEXT NOT NULL DEFAULT '';
UPDATE whop_plans       SET store_id = COALESCE((SELECT account_id FROM whop_account), '');
UPDATE whop_memberships SET store_id = COALESCE((SELECT account_id FROM whop_account), '');
UPDATE whop_messages    SET store_id = COALESCE((SELECT account_id FROM whop_account), '');
UPDATE whop_stock       SET store_id = COALESCE((SELECT account_id FROM whop_account), '');
CREATE TABLE whop_customers_by_store (
  store_id     TEXT    NOT NULL,
  whop_user_id TEXT    NOT NULL,
  handle       TEXT    NOT NULL DEFAULT '',
  applied      TEXT    NOT NULL DEFAULT '',
  paused       INTEGER NOT NULL DEFAULT 0,
  attempts     INTEGER NOT NULL DEFAULT 0,
  next_try_at  INTEGER NOT NULL DEFAULT 0,
  problem      TEXT    NOT NULL DEFAULT '',
  channel_id   TEXT    NOT NULL DEFAULT '',
  updated_at   INTEGER NOT NULL DEFAULT 0,
  applied_at   INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY(store_id, whop_user_id)
);
INSERT INTO whop_customers_by_store(store_id, whop_user_id, handle, applied, paused, attempts, next_try_at, problem, channel_id, updated_at, applied_at)
SELECT COALESCE((SELECT account_id FROM whop_account), ''), whop_user_id, handle, applied, paused, attempts, next_try_at, problem, channel_id, updated_at, applied_at
FROM whop_customers;
DROP TABLE whop_customers;
ALTER TABLE whop_customers_by_store RENAME TO whop_customers;
DELETE FROM whop_plans       WHERE store_id = '';
DELETE FROM whop_memberships WHERE store_id = '';
DELETE FROM whop_messages    WHERE store_id = '';
DELETE FROM whop_stock       WHERE store_id = '';
DELETE FROM whop_customers   WHERE store_id = '';
DROP INDEX whop_memberships_user;
CREATE INDEX whop_memberships_store_user ON whop_memberships(store_id, whop_user_id);
CREATE INDEX whop_plans_store ON whop_plans(store_id, position);
CREATE INDEX whop_messages_store ON whop_messages(store_id, sent_at, id);
DROP TABLE whop_account;
`,
	// The Playkeeper Cloud app's webhook (see whop_app.go): the secret of
	// the one the owner made on Whop, and when they pasted it.
	`
ALTER TABLE whop_app ADD COLUMN webhook_secret TEXT    NOT NULL DEFAULT '';
ALTER TABLE whop_app ADD COLUMN hooked_at      INTEGER NOT NULL DEFAULT 0;
`,
	// Suspensions (see suspension.go): when each customer's account was
	// suspended and why, and whether the owner suspended it on its own, with
	// its store, or both; and when each store was suspended, and why. An
	// account suspended before is the owner's own suspension.
	`
ALTER TABLE customers   ADD COLUMN suspended_at    INTEGER NOT NULL DEFAULT 0;
ALTER TABLE customers   ADD COLUMN suspend_reason  TEXT    NOT NULL DEFAULT '';
ALTER TABLE customers   ADD COLUMN suspended_self  INTEGER NOT NULL DEFAULT 0;
ALTER TABLE customers   ADD COLUMN suspended_store INTEGER NOT NULL DEFAULT 0;
UPDATE customers SET suspended_self = 1 WHERE state = 'suspended';
ALTER TABLE whop_stores ADD COLUMN suspended_at    INTEGER NOT NULL DEFAULT 0;
ALTER TABLE whop_stores ADD COLUMN suspend_reason  TEXT    NOT NULL DEFAULT '';
`,
	// A store that left (see leaving.go): when the Whop side found it gone,
	// and why.
	`
ALTER TABLE whop_stores ADD COLUMN left_at  INTEGER NOT NULL DEFAULT 0;
ALTER TABLE whop_stores ADD COLUMN left_why TEXT    NOT NULL DEFAULT '';
`,
	// Closed stores (see closing.go): each reason a store is closed for,
	// whose it is and in what words, so each opens it for its own reason
	// alone.
	`
CREATE TABLE whop_store_closures (
  store_id  TEXT    NOT NULL,
  closed_by TEXT    NOT NULL,
  why       TEXT    NOT NULL,
  closed_at INTEGER NOT NULL,
  PRIMARY KEY(store_id, closed_by)
);
`,
	// The redirect URI each sign-in with Whop left with, which trading its
	// code names again: the dashboard's address, or its address at the
	// panel's port while Whop lists only that one (see signInRedirect).
	`
ALTER TABLE whop_signins ADD COLUMN redirect_uri TEXT NOT NULL DEFAULT '';
`,
	// When the server whose copy a move left on a machine stopped being that
	// copy, as its requests went where it moved, or 0 for the copy a failed
	// move made, which never was the server (see adoptLeftCopy).
	`
ALTER TABLE left_copies ADD COLUMN switched_at INTEGER NOT NULL DEFAULT 0;
`,
}

const (
	cookieName = "__Host-playkeeper"
	// argon2id parameters (RFC 9106 second recommended option, lower memory).
	argonTime    = 2
	argonMemory  = 64 * 1024
	argonThreads = 2
	argonKeyLen  = 32
)

// hashSem bounds concurrent password hashing so login floods cannot exhaust
// memory on a small VPS.
var hashSem = make(chan struct{}, 2)

func hashPassword(pw string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	hashSem <- struct{}{}
	key := argon2.IDKey([]byte(pw), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	<-hashSem
	return fmt.Sprintf("$argon2id$v=19$m=%d,t=%d,p=%d$%s$%s", argonMemory, argonTime, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key)), nil
}

func checkPassword(encoded, pw string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false
	}
	var m uint32
	var t uint32
	var p uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &m, &t, &p); err != nil {
		return false
	}
	salt, err1 := base64.RawStdEncoding.DecodeString(parts[4])
	want, err2 := base64.RawStdEncoding.DecodeString(parts[5])
	if err1 != nil || err2 != nil || m > 1<<20 || t > 10 || p == 0 {
		return false
	}
	hashSem <- struct{}{}
	got := argon2.IDKey([]byte(pw), salt, t, m, p, uint32(len(want)))
	<-hashSem
	return subtle.ConstantTimeCompare(got, want) == 1
}

// dummyHash equalises timing for unknown usernames.
var dummyHash, _ = hashPassword("playkeeper-timing-equaliser")

// The account rules live in the invites package, so the join page and the
// panel can't drift apart.
func validUsername(u string) error { return invites.ValidUsername(u) }

func validPassword(pw, username string) error { return invites.ValidPassword(pw, username) }

// RandomPassword returns a readable 20-character password for CLI resets.
func RandomPassword() string {
	raw := make([]byte, 13)
	if _, err := rand.Read(raw); err != nil {
		panic(err)
	}
	return base32NoPad(raw)[:20]
}

func randomToken(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

func tokenHash(t string) string {
	h := sha256.Sum256([]byte(t))
	return hex.EncodeToString(h[:])
}

type user struct {
	ID       int64
	Username string
	// Role is the account's role on the install: owner, or member (only what
	// its project memberships grant).
	Role string
}

type session struct {
	IDHash    string
	User      user
	CSRF      string
	CreatedAt time.Time
	LastSeen  time.Time
	ExpiresAt time.Time
	// Access is what the account may do, read by guard for each request.
	Access access
}

func (s *Server) userCount() (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&n)
	return n, err
}

var errSetupDone = errors.New("an admin account already exists")

// createFirstAdmin inserts the account only if there is none, in a single
// statement, so setups racing with the same code cannot create two admins.
func (s *Server) createFirstAdmin(username, password string) (user, error) {
	h, err := hashPassword(password)
	if err != nil {
		return user{}, err
	}
	now := s.now().UnixMilli()
	res, err := s.db.Exec(`INSERT INTO users(username, password_hash, created_at, password_changed_at, role)
		SELECT ?, ?, ?, ?, ? WHERE NOT EXISTS (SELECT 1 FROM users)`, username, h, now, now, roleOwner)
	if err != nil {
		return user{}, err
	}
	if n, err := res.RowsAffected(); err != nil || n != 1 {
		return user{}, errSetupDone
	}
	id, _ := res.LastInsertId()
	s.ensureOwnerMember(id)
	return user{ID: id, Username: username, Role: roleOwner}, nil
}

// authenticate verifies credentials with constant work for unknown users.
func (s *Server) authenticate(username, password string) (user, bool) {
	var u user
	var h string
	err := s.db.QueryRow(`SELECT id, username, role, password_hash FROM users WHERE username = ?`, username).Scan(&u.ID, &u.Username, &u.Role, &h)
	if err != nil {
		checkPassword(dummyHash, password)
		return user{}, false
	}
	if h == "" || s.isCustomer(u.ID) {
		// A customer signs in through their billing provider alone, even
		// if a password was ever set for them.
		checkPassword(dummyHash, password)
		return u, false
	}
	return u, checkPassword(h, password)
}

func (s *Server) newSession(u user) (token string, sess session, err error) {
	return s.newSessionIn(context.Background(), s.db, u)
}

// newSessionIn is newSession through q, which may hold a transaction.
func (s *Server) newSessionIn(ctx context.Context, q querier, u user) (token string, sess session, err error) {
	token = randomToken(32)
	now := s.now()
	sess = session{IDHash: tokenHash(token), User: u, CSRF: randomToken(32), CreatedAt: now, LastSeen: now, ExpiresAt: now.Add(s.opts.AbsoluteTimeout)}
	_, err = q.ExecContext(ctx, `INSERT INTO sessions(id_hash, user_id, csrf, created_at, last_seen, expires_at) VALUES(?,?,?,?,?,?)`,
		sess.IDHash, u.ID, sess.CSRF, now.UnixMilli(), now.UnixMilli(), sess.ExpiresAt.UnixMilli())
	return token, sess, err
}

var errNoSession = errors.New("no valid session")

// lookupSession resolves a cookie token. Expired (idle or absolute) sessions
// are deleted and rejected. Any database error rejects the request.
func (s *Server) lookupSession(token string) (session, error) {
	if token == "" || len(token) > 128 {
		return session{}, errNoSession
	}
	var sess session
	var created, lastSeen, expires int64
	err := s.db.QueryRow(`SELECT s.id_hash, s.csrf, s.created_at, s.last_seen, s.expires_at, u.id, u.username, u.role
		FROM sessions s JOIN users u ON u.id = s.user_id WHERE s.id_hash = ?`, tokenHash(token)).
		Scan(&sess.IDHash, &sess.CSRF, &created, &lastSeen, &expires, &sess.User.ID, &sess.User.Username, &sess.User.Role)
	if err != nil {
		return session{}, errNoSession
	}
	sess.CreatedAt, sess.LastSeen, sess.ExpiresAt = time.UnixMilli(created), time.UnixMilli(lastSeen), time.UnixMilli(expires)
	now := s.now()
	if !now.Before(sess.ExpiresAt) || now.Sub(sess.LastSeen) >= s.opts.IdleTimeout {
		_, _ = s.db.Exec(`DELETE FROM sessions WHERE id_hash = ?`, sess.IDHash)
		return session{}, errNoSession
	}
	if now.Sub(sess.LastSeen) > time.Minute {
		if _, err := s.db.Exec(`UPDATE sessions SET last_seen = ? WHERE id_hash = ?`, now.UnixMilli(), sess.IDHash); err != nil {
			return session{}, errNoSession
		}
	}
	return sess, nil
}

func (s *Server) deleteSession(idHash string) {
	_, _ = s.db.Exec(`DELETE FROM sessions WHERE id_hash = ?`, idHash)
}

// deleteUserSessions signs the user out everywhere, including sign-ins
// waiting for their second step.
func (s *Server) deleteUserSessions(userID int64) {
	_, _ = s.db.Exec(`DELETE FROM sessions WHERE user_id = ?`, userID)
	_, _ = s.db.Exec(`DELETE FROM pending_logins WHERE user_id = ?`, userID)
}

// pruneAuditEvery is how many audit rows are written between prunes.
const pruneAuditEvery = 1000

func (s *Server) audit(actor, action, target, result, detail string) {
	s.writeAudit(auditRow{at: s.now(), actor: actor, action: action, target: target, result: result, detail: detail})
}

func (s *Server) writeAudit(r auditRow) {
	if _, err := s.db.Exec(`INSERT INTO audit(ts, actor, action, target, result, detail) VALUES(?,?,?,?,?,?)`,
		r.at.UnixMilli(), r.actor, r.action, r.target, r.result, r.detail); err != nil {
		s.log.Error("audit write failed", "err", err)
		return
	}
	if s.audits.Add(1)%pruneAuditEvery == 0 {
		s.pruneAudit()
	}
}

// pruneAudit drops audit rows older than auditMaxAge and all but the newest
// maxAudit, and API tokens that stopped working longer ago than that, whose
// names the log no longer needs.
func (s *Server) pruneAudit() {
	cutoff := s.now().Add(-s.auditMaxAge).UnixMilli()
	if _, err := s.db.Exec(`DELETE FROM audit WHERE ts < ?`, cutoff); err != nil {
		s.log.Error("audit prune failed", "err", err)
	}
	if _, err := s.db.Exec(`DELETE FROM audit WHERE id <= (SELECT id FROM audit ORDER BY id DESC LIMIT 1 OFFSET ?)`, s.maxAudit); err != nil {
		s.log.Error("audit prune failed", "err", err)
	}
	if _, err := s.db.Exec(`DELETE FROM api_tokens WHERE (revoked_at != 0 AND revoked_at < ?) OR expires_at < ?`, cutoff, cutoff); err != nil {
		s.log.Error("prune API tokens", "err", err)
	}
}

// SetupToken is the one-time first-run secret written by the installer. Only
// its SHA-256 is stored on disk.
type SetupToken struct {
	Hash      string    `json:"hash"`
	ExpiresAt time.Time `json:"expiresAt"`
}

// NewSetupToken writes a fresh token hash to path and returns the token.
func NewSetupToken(path string, ttl time.Duration, now time.Time) (string, error) {
	raw := make([]byte, 15)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	tok := strings.ToLower(base32NoPad(raw))
	grouped := tok[0:6] + "-" + tok[6:12] + "-" + tok[12:18] + "-" + tok[18:24]
	b, _ := json.Marshal(SetupToken{Hash: tokenHash(grouped), ExpiresAt: now.Add(ttl).UTC()})
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return "", err
	}
	return grouped, os.Rename(tmp, path)
}

func base32NoPad(b []byte) string {
	const alphabet = "abcdefghijkmnpqrstuvwxyz23456789"
	var out strings.Builder
	var buf uint64
	bits := 0
	for _, c := range b {
		buf = buf<<8 | uint64(c)
		bits += 8
		for bits >= 5 {
			out.WriteByte(alphabet[(buf>>(bits-5))&31])
			bits -= 5
		}
	}
	if bits > 0 {
		out.WriteByte(alphabet[(buf<<(5-bits))&31])
	}
	return out.String()
}

// checkSetupToken validates a presented token without consuming it.
func (s *Server) checkSetupToken(presented string) error {
	b, err := os.ReadFile(s.cfg.SetupTokenPath())
	if err != nil {
		return errors.New("No setup code is active. Run `sudo playkeeper setup-code` on the server to create one.")
	}
	var t SetupToken
	if err := json.Unmarshal(b, &t); err != nil {
		return errors.New("The setup code file is damaged. Run `sudo playkeeper setup-code` to create a new one.")
	}
	if !s.now().Before(t.ExpiresAt) {
		return errors.New("This setup code has expired. Run `sudo playkeeper setup-code` on the server to create a new one.")
	}
	got := tokenHash(strings.ToLower(strings.TrimSpace(presented)))
	if subtle.ConstantTimeCompare([]byte(got), []byte(t.Hash)) != 1 {
		return errors.New("That setup code is not correct.")
	}
	return nil
}

func (s *Server) consumeSetupToken() {
	os.Remove(s.cfg.SetupTokenPath())
}

var errDB = errors.New("database error")

func isNoRows(err error) bool { return errors.Is(err, sql.ErrNoRows) }
