package agent

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/CIYAhq/playkeeper/internal/api"
	"github.com/CIYAhq/playkeeper/internal/docker"
	"github.com/CIYAhq/playkeeper/internal/minecraft"
)

// Server layouts. v1 is the single server of 0.1.0 and 0.2.0, kept exactly as
// it was so a migrated server keeps running untouched; v2 is every server
// created since.
const (
	layoutV1 = "v1"
	layoutV2 = "v2"

	legacyContainerName = "playkeeper-minecraft"
	containerPrefix     = "playkeeper-mc-"
	labelServer         = "io.playkeeper.server"
)

var reServerID = regexp.MustCompile(`^[a-z2-9]{10}$`)

// server is one Minecraft server this agent runs: its persisted identity and
// its runtime state. It embeds the agent for the database, Docker and the
// clock; everything below is this server's own.
type server struct {
	*Agent
	id       string
	layout   string
	gamePort int

	// ctx ends when the agent stops or the server is deleted; loops counts
	// the goroutines that run until then.
	ctx    context.Context
	cancel context.CancelFunc
	loops  sync.WaitGroup

	console *ring

	opLock chan struct{}
	opMu   sync.Mutex
	op     *api.Operation

	mu              sync.Mutex
	runPhase        api.Phase
	runPhaseDetail  string
	runStartedAt    time.Time
	sawStopping     bool
	lastError       string
	lastErrorHint   string
	refusal         *api.FileRefusal
	reachable       bool
	reachableAt     time.Time
	players         *api.PlayerSnapshot
	resources       *api.Resources
	prevCPU         *docker.Stats
	crashes         []time.Time
	crashed         bool
	handledExit     map[string]time.Time
	exitSeen        map[string]seenExit
	intentional     map[string]bool
	followEnded     map[string]time.Time
	listMissing     map[string]int
	listExtra       map[string]int
	uuids           map[string]string
	nextAutoRestart time.Time
	worldBytes      int64
	worldAt         time.Time

	rconMu sync.Mutex
	rcon   *minecraft.RCON
	rconIP string
}

func (a *Agent) newServerHandle(id, layout string, port int) *server {
	s := &server{
		Agent: a, id: id, layout: layout, gamePort: port,
		console:     newRing(consoleCapacity),
		opLock:      make(chan struct{}, 1),
		handledExit: map[string]time.Time{},
		exitSeen:    map[string]seenExit{},
		intentional: map[string]bool{},
		followEnded: map[string]time.Time{},
		listMissing: map[string]int{},
		listExtra:   map[string]int{},
		uuids:       map[string]string{},
	}
	s.ctx, s.cancel = context.WithCancel(a.ctx)
	return s
}

// Paths and names. The v1 layout keeps 0.2.0's paths and container name.

func (s *server) dir() string {
	if s.layout == layoutV1 {
		return filepath.Dir(s.cfg.ServerDataDir())
	}
	return filepath.Join(s.cfg.DataDir, "servers", s.id)
}

func (s *server) dataDir() string         { return filepath.Join(s.dir(), "data") }
func (s *server) containerSecret() string { return filepath.Join(s.dir(), "rcon_password") }

func (s *server) agentSecret() string {
	if s.layout == layoutV1 {
		return s.cfg.RCONSecretPath()
	}
	return filepath.Join(s.cfg.AgentDir(), "servers", s.id, "rcon.secret")
}

func (s *server) containerName() string {
	if s.layout == layoutV1 {
		return legacyContainerName
	}
	return containerPrefix + s.id
}

// labels identify the server's containers. A v1 server keeps 0.2.0's labels
// so its container definition, and so its spec hash, does not change.
func (s *server) labels() map[string]string {
	l := map[string]string{labelManaged: "true", labelInstall: s.cfg.InstallID}
	if s.layout != layoutV1 {
		l[labelServer] = s.id
	}
	return l
}

// Registry.

// serverList returns the servers in display order.
func (a *Agent) serverList() []*server {
	rows, err := a.db.Query(`SELECT id FROM servers ORDER BY position, created_at, id`)
	if err != nil {
		return nil
	}
	var ids []string
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	rows.Close()
	a.srvMu.Lock()
	defer a.srvMu.Unlock()
	out := make([]*server, 0, len(ids))
	for _, id := range ids {
		if s := a.servers[id]; s != nil {
			out = append(out, s)
		}
	}
	return out
}

func (a *Agent) serverByID(id string) *server {
	a.srvMu.Lock()
	defer a.srvMu.Unlock()
	return a.servers[id]
}

// loadServers creates a runtime handle for every server in the database.
func (a *Agent) loadServers() error {
	rows, err := a.db.Query(`SELECT id, layout, game_port FROM servers`)
	if err != nil {
		return err
	}
	defer rows.Close()
	a.srvMu.Lock()
	defer a.srvMu.Unlock()
	for rows.Next() {
		var id, layout string
		var port int
		if err := rows.Scan(&id, &layout, &port); err != nil {
			return err
		}
		a.servers[id] = a.newServerHandle(id, layout, port)
	}
	return rows.Err()
}

// startServerLoops runs the follower, collector and reconciler of a server.
func (s *server) startLoops() {
	for _, fn := range []func(context.Context){s.followLoop, s.sampleLoop, s.reconcileLoop} {
		fn := fn
		s.wg.Add(1)
		s.loops.Add(1)
		go func() {
			defer s.wg.Done()
			defer s.loops.Done()
			fn(s.ctx)
		}()
	}
}

func newServerID() string {
	const alphabet = "abcdefghijkmnpqrstuvwxyz23456789"
	b := make([]byte, 10)
	_, _ = rand.Read(b)
	for i := range b {
		b[i] = alphabet[int(b[i])%len(alphabet)]
	}
	return string(b)
}

// validName checks a server name: 1 to 32 printable characters.
func validName(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", errInvalid("Give the server a name.")
	}
	if utf8.RuneCountInString(s) > 32 {
		return "", errInvalid("Server names can be at most 32 characters.")
	}
	for _, r := range s {
		if !unicode.IsPrint(r) || r == '§' {
			return "", errInvalid("Server names may not contain control or formatting characters.")
		}
	}
	return s, nil
}

// slugFor turns a name into a short, URL-safe slug.
func slugFor(name string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(name) {
		switch {
		case r >= 'a' && r <= 'z' || r >= '0' && r <= '9':
			b.WriteRune(r)
			dash = false
		case b.Len() > 0 && !dash:
			b.WriteByte('-')
			dash = true
		}
		if b.Len() >= 24 {
			break
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" || out == "new" {
		out = "server"
	}
	return out
}

// uniqueSlug and uniqueName pick a slug or name not used by another server.
func (a *Agent) uniqueSlug(base string) string {
	for i := 1; ; i++ {
		s := base
		if i > 1 {
			s = fmt.Sprintf("%s-%d", base, i)
		}
		var n int
		if a.db.QueryRow(`SELECT COUNT(*) FROM servers WHERE slug = ?`, s).Scan(&n) == nil && n == 0 {
			return s
		}
	}
}

func (a *Agent) nameTaken(name, except string) bool {
	var n int
	_ = a.db.QueryRow(`SELECT COUNT(*) FROM servers WHERE lower(name) = lower(?) AND id != ?`, name, except).Scan(&n)
	return n > 0
}

func (a *Agent) defaultName() string {
	for i := 1; ; i++ {
		n := "My server"
		if i > 1 {
			n = fmt.Sprintf("My server %d", i)
		}
		if !a.nameTaken(n, "") {
			return n
		}
	}
}

// Memory and ports.

// reservedMemoryMB is the memory every server except `except` has reserved.
func (a *Agent) reservedMemoryMB(except string) int {
	total := 0
	for _, sv := range a.serverList() {
		if sv.id == except {
			continue
		}
		if sc, _ := sv.serverConfig(); sc != nil {
			total += sc.MemoryMB
		}
	}
	return total
}

// memoryFor returns the budgets a server (existing, or new with except "")
// may choose on this host, with a suggestion.
func (a *Agent) memoryFor(except string) (options []int, recommended, max int) {
	return minecraft.MemoryOptionsFor(a.opts.HostMemoryMB(), a.reservedMemoryMB(except))
}

func (a *Agent) validMemory(mb int, except string) error {
	opts, _, max := a.memoryFor(except)
	for _, o := range opts {
		if o == mb {
			return nil
		}
	}
	if len(opts) == 0 {
		return &apiError{Status: http.StatusConflict, Code: api.CodeConflict, Msg: fmt.Sprintf("There is not enough memory left on this machine for another server: %d MB is free after the system and the other servers.", max),
			Hint: "Give another server less memory in its Settings, or delete a server you no longer need."}
	}
	return errInvalid("Memory must be one of %v MB here: that is what fits next to the system and the other servers.", opts)
}

// nextGamePort is the lowest port from the install's game port up that no
// server uses and nothing else on the host listens on.
func (a *Agent) nextGamePort() (int, error) {
	used := map[int]bool{a.cfg.PanelPort: true}
	rows, err := a.db.Query(`SELECT game_port FROM servers`)
	if err != nil {
		return 0, err
	}
	for rows.Next() {
		var p int
		if rows.Scan(&p) == nil {
			used[p] = true
		}
	}
	rows.Close()
	for p := a.cfg.GamePort; p < a.cfg.GamePort+1000 && p <= 65535; p++ {
		if !used[p] && !a.opts.PortInUse(p) {
			return p, nil
		}
	}
	return 0, errConflict("No free game port was found near "+strconv.Itoa(a.cfg.GamePort)+".", "Free a port on this machine, then try again.")
}

type newServerSpec struct {
	name     string
	typ      string
	config   api.ServerConfig
	desired  string
	actor    string
	auditMsg string
}

// addServer records a new v2 server and starts its first operation, kind,
// which runs the function first returns for the server. It holds createMu,
// so two new servers never get the same slug, port or memory and none is
// recorded while a machine-wide operation runs. The server holds its
// operation lock before anything can see it, so neither a machine-wide
// operation nor an automatic start gets in ahead of its first operation.
// With first nil, the server is only recorded.
func (a *Agent) addServer(spec newServerSpec, kind string, first func(s *server) func(ctx context.Context, h *opHandle) error) (*server, *api.Operation, error) {
	a.createMu.Lock()
	defer a.createMu.Unlock()
	if err := a.machineBusy(); err != nil {
		return nil, nil, err
	}
	name := spec.name
	if name == "" {
		name = a.defaultName()
	} else if a.nameTaken(name, "") {
		return nil, nil, errConflict(fmt.Sprintf("A server named %q already exists on this machine.", name), "Pick another name.")
	}
	if err := a.validMemory(spec.config.MemoryMB, ""); err != nil {
		return nil, nil, err
	}
	port, err := a.nextGamePort()
	if err != nil {
		return nil, nil, err
	}
	id := newServerID()
	slug := a.uniqueSlug(slugFor(name))
	cfgJSON, err := json.Marshal(spec.config)
	if err != nil {
		return nil, nil, err
	}
	now := a.now().UTC()
	var pos int
	_ = a.db.QueryRow(`SELECT COALESCE(MAX(position), 0) + 1 FROM servers`).Scan(&pos)
	if _, err := a.db.Exec(`INSERT INTO servers(id, name, slug, game, type, layout, game_port, config, desired, position, created_at, collecting_since)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, id, name, slug, api.GameMinecraftJava, spec.typ, layoutV2, port, string(cfgJSON), spec.desired, pos, now.UnixMilli(), now.UnixMilli()); err != nil {
		return nil, nil, err
	}
	s := a.newServerHandle(id, layoutV2, port)
	s.opLock <- struct{}{}
	a.srvMu.Lock()
	a.servers[id] = s
	a.srvMu.Unlock()
	var op *api.Operation
	if first != nil {
		op = s.startOp(kind, spec.actor, first(s))
	} else {
		<-s.opLock
	}
	s.startLoops()
	return s, op, nil
}

// migrateSingleServer turns the single server of an install made by 0.1.0 or
// 0.2.0 into a v1 server, in one transaction, without touching its files or
// container. It does nothing once any server is recorded.
func (a *Agent) migrateSingleServer() error {
	tx, err := a.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var n int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM servers`).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	kv := func(key string) (string, bool) {
		var v string
		err := tx.QueryRow(`SELECT value FROM kv WHERE key = ?`, key).Scan(&v)
		return v, err == nil
	}
	raw, ok := kv(kvServerConfig)
	if !ok {
		// No server was ever created; samples without a server say nothing.
		if _, err := tx.Exec(`DELETE FROM samples WHERE server_id = ''`); err != nil {
			return err
		}
		return tx.Commit()
	}
	var sc api.ServerConfig
	if err := json.Unmarshal([]byte(raw), &sc); err != nil {
		return fmt.Errorf("the server's settings cannot be read: %w", err)
	}
	desired, ok := kv(kvDesired)
	if !ok {
		desired = api.DesiredStopped
	}
	var since any
	if v, ok := kv(kvCollectingSince); ok {
		if t, err := time.Parse(time.RFC3339Nano, v); err == nil {
			since = t.UnixMilli()
		}
	}
	cursor, _ := kv(kvLogCursor)
	created := sc.CreatedAt
	if created.IsZero() {
		created = a.now()
	}
	id := newServerID()
	if _, err := tx.Exec(`INSERT INTO servers(id, name, slug, game, type, layout, game_port, config, desired, position, created_at, collecting_since, log_cursor)
		VALUES(?,?,?,?,?,?,?,?,?,0,?,?,?)`, id, "My server", "my-server", api.GameMinecraftJava, api.TypePaper, layoutV1, a.cfg.GamePort, raw, desired, created.UnixMilli(), since, cursor); err != nil {
		return err
	}
	for _, q := range []string{
		`UPDATE operations SET server_id = ? WHERE server_id = '' AND kind != 'update'`,
		`UPDATE events SET server_id = ? WHERE server_id = ''`,
		`UPDATE sessions SET server_id = ? WHERE server_id = ''`,
		`UPDATE backups SET server_id = ? WHERE server_id = ''`,
		`UPDATE samples SET server_id = ? WHERE server_id = ''`,
		`UPDATE audit SET server_id = ? WHERE server_id = '' AND action NOT LIKE 'update.%'`,
	} {
		if _, err := tx.Exec(q, id); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`DELETE FROM kv WHERE key IN (?, ?, ?, ?, 'pending_recreate')`, kvServerConfig, kvDesired, kvCollectingSince, kvLogCursor); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	a.log.Info("the existing server now runs as one of several", "server", id, "layout", layoutV1)
	return nil
}

// deleteServer removes a stopped server's container, world, backups and
// records. It runs as the server's last operation.
func (s *server) deleteServer(ctx context.Context, h *opHandle, actor string) error {
	if err := s.stopServer(ctx, h); err != nil {
		return err
	}
	h.phase("deleting")
	for _, name := range []string{s.containerName(), s.containerName() + "-setup"} {
		if err := s.docker.ContainerRemove(ctx, name, true); err != nil && !docker.IsNotFound(err) {
			return s.dockerErr(err)
		}
	}
	backups, err := s.listBackups("")
	if err != nil {
		return err
	}
	// The world moves aside before anything is deleted, so a server whose
	// files can't be moved keeps its backups.
	trash := s.dir() + ".deleting-" + s.now().UTC().Format("20060102-150405")
	if err := renameDir(s.dir(), trash); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	for _, b := range backups {
		os.Remove(s.backupPath(b.FileName))
		os.Remove(s.backupPath(b.FileName) + ".sha256")
	}
	if err := os.RemoveAll(trash); err != nil {
		s.log.Warn("could not remove a deleted server's files", "path", trash, "err", err)
	}
	if s.layout == layoutV2 {
		os.RemoveAll(filepath.Dir(s.agentSecret()))
	}
	// The server's loops end before its records go, so none of them writes a
	// row for a server that no longer exists. Nothing below uses ctx.
	s.cancel()
	s.loops.Wait()
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, q := range []string{
		`DELETE FROM backups WHERE server_id = ?`, `DELETE FROM samples WHERE server_id = ?`,
		`DELETE FROM events WHERE server_id = ?`, `DELETE FROM sessions WHERE server_id = ?`,
		`DELETE FROM servers WHERE id = ?`,
	} {
		if _, err := tx.Exec(q, s.id); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.srvMu.Lock()
	delete(s.servers, s.id)
	s.srvMu.Unlock()
	s.audit(actor, "server.deleted", s.id, "succeeded", fmt.Sprintf("%d backup(s) deleted with it", len(backups)))
	return nil
}

// serverRow is a server's persisted identity.
type serverRow struct {
	Name, Slug, Game, Type string
	CreatedAt              time.Time
}

func (s *server) row() (serverRow, error) {
	var r serverRow
	var created int64
	err := s.db.QueryRow(`SELECT name, slug, game, type, created_at FROM servers WHERE id = ?`, s.id).Scan(&r.Name, &r.Slug, &r.Game, &r.Type, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return r, errNotFound("Server")
	}
	r.CreatedAt = time.UnixMilli(created).UTC()
	return r, err
}

func (s *server) name() string {
	r, err := s.row()
	if err != nil || r.Name == "" {
		return "the server"
	}
	return r.Name
}
