package agent

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/CIYAhq/playkeeper/internal/docker"
)

// fakeDocker implements the subset of the Docker Engine API the agent uses and
// simulates the Minecraft container's log output.
type fakeDocker struct {
	t            *testing.T
	mu           sync.Mutex
	images       map[string]bool
	pulls        int
	networks     map[string]map[string]string
	byName       map[string]*fakeContainer
	byID         map[string]*fakeContainer
	nextID       int
	calls        []string
	startErr     string
	failedStarts int
	jarContent   []byte
	bootDelay    time.Duration
	replayAll    bool
	logDelay     time.Duration // before answering each logs request
	bootExit     int           // when set, the server exits with it while starting
	failBoots    int           // the next failBoots servers to start exit with code 1
	holdImages   bool          // image inspects wait until the caller gives up
	down         string        // requests whose path starts with it fail, as when Docker stops answering
	versionDown  bool          // version requests fail too, as when the daemon itself stops answering
	stopDelay    time.Duration // before a container stop takes effect
	setupHangs   bool          // setup containers end their log streams but keep running
	// bootFailsOn names a Minecraft version whose server rewrites the world's
	// level.dat, as an upgrade would, then exits while starting.
	bootFailsOn string
	// hangsAfterFailing makes servers log that a mod failed and the server
	// failed to start, then keep running, as Forge does.
	hangsAfterFailing bool
	// target names the container addLog, crash and the like act on when
	// there is more than one server.
	target string
	// started and stopped, when set, hear of a server container starting
	// or stopping cleanly, as a plugin would.
	started, stopped func(c *fakeContainer)
	// others are containers Playkeeper didn't make, as Docker lists them
	// after the agent's own.
	others []fakeListed
	// beforeLogs, when set, runs once with fd.mu held just before a log
	// read without a tail (the follower's) is answered.
	beforeLogs func(c *fakeContainer)
}

// fakeListed is a container in Docker's list that the fake doesn't run.
type fakeListed struct {
	name    string
	labels  map[string]string
	running bool
	ports   []fakePort
}

type fakePort struct {
	public int
	proto  string
}

// listedPorts is what Docker's list shows of published ports: none for a
// container that isn't running.
func listedPorts(running bool, ports []fakePort) []map[string]any {
	out := []map[string]any{}
	for _, p := range ports {
		if running {
			out = append(out, map[string]any{"IP": "0.0.0.0", "PrivatePort": 25565, "PublicPort": p.public, "Type": p.proto})
		}
	}
	return out
}

type fakeLine struct {
	ts   time.Time
	text string
}

type fakeContainer struct {
	id, name string
	cfg      docker.ContainerConfig
	running  bool
	exitCode int
	oom      bool
	started  time.Time
	finished time.Time
	logs     []fakeLine
	wake     chan struct{}
	rotated  chan struct{}
}

func startFakeDocker(t *testing.T, sock string) *fakeDocker {
	t.Helper()
	fd := &fakeDocker{t: t, images: map[string]bool{}, networks: map[string]map[string]string{}, byName: map[string]*fakeContainer{}, byID: map[string]*fakeContainer{}, jarContent: []byte("fake paper jar"), bootDelay: 30 * time.Millisecond}
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(fd.serve)}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	return fd
}

func (fd *fakeDocker) log(c *fakeContainer, text string) {
	c.logs = append(c.logs, fakeLine{ts: time.Now().UTC(), text: text})
	close(c.wake)
	c.wake = make(chan struct{})
}

// server is the Minecraft server container the helpers below act on: the
// one named by target, or else the only server container there is.
func (fd *fakeDocker) server() *fakeContainer {
	if fd.target != "" {
		return fd.byName[fd.target]
	}
	var found *fakeContainer
	for name, c := range fd.byName {
		if !strings.HasSuffix(name, "-setup") {
			if found != nil {
				fd.t.Fatalf("more than one server container; set fd.target")
			}
			found = c
		}
	}
	if found == nil {
		fd.t.Fatalf("no server container")
	}
	return found
}

// addLog appends a server log line to the running Minecraft container.
func (fd *fakeDocker) addLog(text string) {
	fd.mu.Lock()
	defer fd.mu.Unlock()
	fd.log(fd.server(), text)
}

// rotate simulates json-file log rotation: all but the last keep lines are
// gone, open follow streams end (as some Docker versions do on rotation) and,
// with replay, the next follow request returns the whole current file again.
func (fd *fakeDocker) rotate(keep int, replay bool) {
	fd.mu.Lock()
	defer fd.mu.Unlock()
	c := fd.server()
	if len(c.logs) > keep {
		c.logs = append([]fakeLine(nil), c.logs[len(c.logs)-keep:]...)
	}
	close(c.rotated)
	c.rotated = make(chan struct{})
	fd.replayAll = replay
}

// crash kills the server without a clean shutdown (like `kill -9 java`).
func (fd *fakeDocker) crash(code int) {
	fd.mu.Lock()
	defer fd.mu.Unlock()
	c := fd.server()
	c.running, c.exitCode, c.finished = false, code, time.Now().UTC()
	close(c.wake)
	c.wake = make(chan struct{})
}

// oomKill is the kernel killing the server at its memory limit: Docker says
// OOMKilled, exit code 137.
func (fd *fakeDocker) oomKill() {
	fd.mu.Lock()
	defer fd.mu.Unlock()
	c := fd.server()
	c.running, c.exitCode, c.finished, c.oom = false, 137, time.Now().UTC(), true
	close(c.wake)
	c.wake = make(chan struct{})
}

// externalStop stops the container the way `docker stop` or a host shutdown
// does: the server logs its clean shutdown.
func (fd *fakeDocker) externalStop() {
	fd.mu.Lock()
	defer fd.mu.Unlock()
	c := fd.server()
	fd.log(c, "[12:00:00 INFO]: Stopping server")
	c.running, c.exitCode, c.finished = false, 0, time.Now().UTC()
}

func (fd *fakeDocker) containerCount(prefix string) int {
	fd.mu.Lock()
	defer fd.mu.Unlock()
	n := 0
	for name := range fd.byName {
		if strings.HasPrefix(name, prefix) {
			n++
		}
	}
	return n
}

// containerImage is the image the container called name was made from;
// "" when there's no such container.
func (fd *fakeDocker) containerImage(name string) string {
	fd.mu.Lock()
	defer fd.mu.Unlock()
	if c := fd.byName[name]; c != nil {
		return c.cfg.Image
	}
	return ""
}

func (fd *fakeDocker) called(prefix string) int {
	fd.mu.Lock()
	defer fd.mu.Unlock()
	n := 0
	for _, c := range fd.calls {
		if strings.HasPrefix(c, prefix) {
			n++
		}
	}
	return n
}

func jsonOut(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func (fd *fakeDocker) serve(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	if path == "/version" {
		fd.mu.Lock()
		down := fd.versionDown
		fd.mu.Unlock()
		if down {
			jsonOut(w, 500, map[string]string{"message": "fake Docker is not answering"})
			return
		}
		jsonOut(w, 200, map[string]string{"Version": "29.0.0-fake", "ApiVersion": "1.52", "MinAPIVersion": "1.44"})
		return
	}
	path = strings.TrimPrefix(path, "/v1.52")
	fd.mu.Lock()
	fd.calls = append(fd.calls, r.Method+" "+path)
	down := fd.down != "" && strings.HasPrefix(path, fd.down)
	fd.mu.Unlock()
	if down {
		jsonOut(w, 500, map[string]string{"message": "fake Docker is not answering"})
		return
	}
	switch {
	case r.Method == "GET" && strings.HasPrefix(path, "/images/") && strings.HasSuffix(path, "/json"):
		ref := strings.TrimSuffix(strings.TrimPrefix(path, "/images/"), "/json")
		fd.mu.Lock()
		ok, hold := fd.images[ref], fd.holdImages
		fd.mu.Unlock()
		if hold {
			<-r.Context().Done()
			return
		}
		if !ok {
			jsonOut(w, 404, map[string]string{"message": "No such image"})
			return
		}
		jsonOut(w, 200, map[string]any{"Id": "sha256:img"})
	case r.Method == "POST" && path == "/images/create":
		ref := r.URL.Query().Get("fromImage") + "@" + r.URL.Query().Get("tag")
		fd.mu.Lock()
		fd.images[ref] = true
		fd.pulls++
		fd.mu.Unlock()
		jsonOut(w, 200, map[string]string{"status": "Pull complete"})
	case r.Method == "GET" && strings.HasPrefix(path, "/networks/"):
		name := strings.TrimPrefix(path, "/networks/")
		fd.mu.Lock()
		labels, ok := fd.networks[name]
		fd.mu.Unlock()
		if !ok {
			jsonOut(w, 404, map[string]string{"message": "network not found"})
			return
		}
		jsonOut(w, 200, map[string]any{"Id": "net1", "Name": name, "Labels": labels})
	case r.Method == "POST" && path == "/networks/create":
		var body struct {
			Name   string
			Labels map[string]string
		}
		json.NewDecoder(r.Body).Decode(&body)
		fd.mu.Lock()
		fd.networks[body.Name] = body.Labels
		fd.mu.Unlock()
		jsonOut(w, 201, map[string]string{"Id": "net1"})
	case r.Method == "POST" && path == "/containers/create":
		fd.create(w, r)
	case r.Method == "GET" && path == "/containers/json":
		all := r.URL.Query().Get("all") == "1"
		fd.mu.Lock()
		var list []map[string]any
		for _, c := range fd.byName {
			if !all && !c.running {
				continue
			}
			var ports []fakePort
			for key, bindings := range c.cfg.HostConfig.PortBindings {
				_, proto, _ := strings.Cut(key, "/")
				for _, b := range bindings {
					p, _ := strconv.Atoi(b.HostPort)
					ports = append(ports, fakePort{p, proto})
				}
			}
			list = append(list, map[string]any{"Id": c.id, "Names": []string{"/" + c.name}, "Image": c.cfg.Image, "State": map[bool]string{true: "running", false: "exited"}[c.running], "Labels": c.cfg.Labels, "Ports": listedPorts(c.running, ports)})
		}
		for i, o := range fd.others {
			if all || o.running {
				list = append(list, map[string]any{"Id": fmt.Sprintf("o%063d", i), "Names": []string{"/" + o.name}, "Image": "busybox", "State": map[bool]string{true: "running", false: "exited"}[o.running], "Labels": o.labels, "Ports": listedPorts(o.running, o.ports)})
			}
		}
		fd.mu.Unlock()
		jsonOut(w, 200, list)
	case strings.HasPrefix(path, "/containers/"):
		rest := strings.TrimPrefix(path, "/containers/")
		ref, action, _ := strings.Cut(rest, "/")
		fd.mu.Lock()
		c := fd.byName[ref]
		if c == nil {
			c = fd.byID[ref]
		}
		fd.mu.Unlock()
		if c == nil {
			jsonOut(w, 404, map[string]string{"message": "No such container: " + ref})
			return
		}
		fd.container(w, r, c, action)
	default:
		jsonOut(w, 404, map[string]string{"message": "fake: unsupported " + r.Method + " " + path})
	}
}

func (fd *fakeDocker) create(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("name")
	var cfg docker.ContainerConfig
	json.NewDecoder(r.Body).Decode(&cfg)
	fd.mu.Lock()
	defer fd.mu.Unlock()
	if !fd.images[cfg.Image] {
		jsonOut(w, 404, map[string]string{"message": "No such image: " + cfg.Image})
		return
	}
	if _, exists := fd.byName[name]; exists {
		jsonOut(w, 409, map[string]string{"message": "Conflict. The container name is already in use"})
		return
	}
	fd.nextID++
	c := &fakeContainer{id: fmt.Sprintf("c%063d", fd.nextID), name: name, cfg: cfg, wake: make(chan struct{}), rotated: make(chan struct{})}
	fd.byName[name], fd.byID[c.id] = c, c
	jsonOut(w, 201, map[string]string{"Id": c.id})
}

func env(cfg docker.ContainerConfig, key string) string {
	for _, e := range cfg.Env {
		if k, v, ok := strings.Cut(e, "="); ok && k == key {
			return v
		}
	}
	return ""
}

func (fd *fakeDocker) container(w http.ResponseWriter, r *http.Request, c *fakeContainer, action string) {
	switch {
	case r.Method == "GET" && action == "json":
		fd.mu.Lock()
		defer fd.mu.Unlock()
		fin := "0001-01-01T00:00:00Z"
		if !c.finished.IsZero() {
			fin = c.finished.Format(time.RFC3339Nano)
		}
		st := "0001-01-01T00:00:00Z"
		if !c.started.IsZero() {
			st = c.started.Format(time.RFC3339Nano)
		}
		jsonOut(w, 200, map[string]any{
			"Id": c.id, "Name": "/" + c.name, "Image": "sha256:img",
			"State":           map[string]any{"Status": map[bool]string{true: "running", false: "exited"}[c.running], "Running": c.running, "ExitCode": c.exitCode, "OOMKilled": c.oom, "StartedAt": st, "FinishedAt": fin},
			"Config":          map[string]any{"Image": c.cfg.Image, "Env": c.cfg.Env, "Labels": c.cfg.Labels},
			"HostConfig":      map[string]any{"Memory": c.cfg.HostConfig.Memory},
			"NetworkSettings": map[string]any{"Networks": map[string]any{networkName: map[string]string{"IPAddress": "127.0.0.1"}}},
		})
	case r.Method == "POST" && action == "start":
		fd.mu.Lock()
		if fd.startErr != "" && len(c.cfg.HostConfig.PortBindings) > 0 {
			msg := fd.startErr
			fd.failedStarts++
			fd.mu.Unlock()
			jsonOut(w, 500, map[string]string{"message": msg})
			return
		}
		if c.running {
			fd.mu.Unlock()
			w.WriteHeader(304)
			return
		}
		c.running, c.started, c.finished, c.exitCode, c.oom = true, time.Now().UTC(), time.Time{}, 0, false
		setup := env(c.cfg, "SETUP_ONLY") == "TRUE"
		fd.log(c, "[init] Running as uid=1000 gid=1000")
		fd.log(c, "[init] Resolving type given PAPER")
		started := fd.started
		fd.mu.Unlock()
		if started != nil && !setup {
			started(c)
		}
		go fd.boot(c, setup)
		w.WriteHeader(204)
	case r.Method == "POST" && action == "stop":
		fd.mu.Lock()
		delay := fd.stopDelay
		fd.mu.Unlock()
		time.Sleep(delay)
		fd.mu.Lock()
		wasRunning, stopped := c.running, fd.stopped
		if c.running {
			fd.log(c, "[12:00:00 INFO]: Stopping server")
			c.running, c.exitCode, c.finished = false, 0, time.Now().UTC()
		}
		fd.mu.Unlock()
		if wasRunning && stopped != nil {
			stopped(c)
		}
		w.WriteHeader(204)
	case r.Method == "DELETE" && action == "":
		fd.mu.Lock()
		if c.running { // like Docker's force removal: kill it and end its log streams
			c.running, c.exitCode, c.finished = false, 137, time.Now().UTC()
			close(c.wake)
			c.wake = make(chan struct{})
		}
		delete(fd.byName, c.name)
		delete(fd.byID, c.id)
		fd.mu.Unlock()
		w.WriteHeader(204)
	case r.Method == "GET" && action == "logs":
		fd.logs(w, r, c)
	case r.Method == "GET" && action == "stats":
		jsonOut(w, 200, map[string]any{
			"cpu_stats":    map[string]any{"cpu_usage": map[string]any{"total_usage": time.Now().UnixNano() / 2}, "system_cpu_usage": time.Now().UnixNano(), "online_cpus": 2},
			"memory_stats": map[string]any{"usage": 900 << 20, "limit": 1536 << 20, "stats": map[string]any{"inactive_file": 100 << 20}},
		})
	default:
		jsonOut(w, 404, map[string]string{"message": "fake: unsupported container action " + action})
	}
}

// boot simulates the image: setup-only writes the jar and exits; the server
// prints its startup lines and "Done".
func (fd *fakeDocker) boot(c *fakeContainer, setup bool) {
	fd.mu.Lock()
	delay := fd.bootDelay
	fd.mu.Unlock()
	time.Sleep(delay)
	fd.mu.Lock()
	defer fd.mu.Unlock()
	if !c.running {
		return
	}
	if setup && fd.setupHangs {
		fd.log(c, "[mc-image-helper] Downloading /data/paper.jar")
		close(c.rotated)
		return
	}
	if setup {
		for _, b := range c.cfg.HostConfig.Binds {
			if host, dst, _ := strings.Cut(b, ":"); strings.HasPrefix(dst, "/data") {
				name := fmt.Sprintf("paper-%s-%s.jar", env(c.cfg, "VERSION"), env(c.cfg, "PAPER_BUILD"))
				os.MkdirAll(host, 0o750)
				os.WriteFile(filepath.Join(host, name), fd.jarContent, 0o644)
				os.MkdirAll(filepath.Join(host, "world"), 0o750)
			}
		}
		fd.log(c, "[mc-image-helper] Downloaded /data/paper.jar")
		c.running, c.exitCode, c.finished = false, 0, time.Now().UTC()
		close(c.wake)
		c.wake = make(chan struct{})
		return
	}
	version := strings.TrimSuffix(strings.TrimPrefix(env(c.cfg, "CUSTOM_SERVER"), "/data/paper-"), ".jar")
	fd.log(c, "[12:00:00 INFO]: Starting minecraft server version "+version)
	if fd.bootFailsOn != "" && strings.HasPrefix(version, fd.bootFailsOn+"-") {
		for _, b := range c.cfg.HostConfig.Binds {
			if host, dst, _ := strings.Cut(b, ":"); dst == "/data" {
				os.WriteFile(filepath.Join(host, "world", "level.dat"), []byte("upgraded by "+version), 0o644)
			}
		}
		fd.log(c, "[12:00:00 ERROR]: Failed to upgrade the world")
		c.running, c.exitCode, c.finished = false, 1, time.Now().UTC()
		close(c.wake)
		c.wake = make(chan struct{})
		return
	}
	if fd.hangsAfterFailing {
		fd.log(c, "[12:00:00] [main/ERROR] [ne.mi.fm.DeferredWorkQueue/]: Mod 'waila' encountered an error in a deferred task:")
		fd.log(c, "java.lang.NoSuchFieldError: Class net.minecraft.world.entity.EntityType does not have member field 'net.minecraft.world.entity.EntityType AREA_EFFECT_CLOUD'")
		fd.log(c, "[12:00:00] [main/ERROR] [minecraft/Main]: Failed to start the minecraft server")
		return
	}
	exit := fd.bootExit
	if exit == 0 && fd.failBoots > 0 {
		fd.failBoots--
		exit = 1
	}
	if exit != 0 {
		fd.log(c, "[12:00:00 ERROR]: Encountered an unexpected exception")
		c.running, c.exitCode, c.finished = false, exit, time.Now().UTC()
		close(c.wake)
		c.wake = make(chan struct{})
		return
	}
	for _, b := range c.cfg.HostConfig.Binds {
		if host, dst, _ := strings.Cut(b, ":"); dst == "/data" {
			level := filepath.Join(host, "world", "level.dat")
			if _, err := os.Stat(level); err != nil {
				os.MkdirAll(filepath.Dir(level), 0o750)
				os.WriteFile(level, []byte("generated world"), 0o644)
			}
		}
	}
	fd.log(c, `[12:00:00 INFO]: Preparing level "world"`)
	fd.log(c, `[12:00:01 INFO]: Done (1.000s)! For help, type "help"`)
}

func (fd *fakeDocker) logs(w http.ResponseWriter, r *http.Request, c *fakeContainer) {
	fd.mu.Lock()
	delay := fd.logDelay
	fd.mu.Unlock()
	time.Sleep(delay)
	q := r.URL.Query()
	var since time.Time
	if s := q.Get("since"); s != "" {
		sec, frac, _ := strings.Cut(s, ".")
		si, _ := strconv.ParseInt(sec, 10, 64)
		ns, _ := strconv.ParseInt(frac, 10, 64)
		since = time.Unix(si, ns)
	}
	tail, _ := strconv.Atoi(q.Get("tail"))
	follow := q.Get("follow") == "1"
	w.Header().Set("Content-Type", "application/vnd.docker.multiplexed-stream")
	w.WriteHeader(200)
	flusher, _ := w.(http.Flusher)
	sent := 0
	fd.mu.Lock()
	if hook := fd.beforeLogs; hook != nil && tail == 0 {
		fd.beforeLogs = nil
		hook(c)
	}
	if follow && fd.replayAll {
		since, fd.replayAll = time.Time{}, false
	}
	rotated := c.rotated
	lines := c.logs
	if tail > 0 && len(lines) > tail {
		lines = lines[len(lines)-tail:]
		sent = len(c.logs) - tail
	}
	fd.mu.Unlock()
	emit := func(l fakeLine) {
		if !since.IsZero() && l.ts.Before(since) {
			return
		}
		payload := []byte(l.ts.Format(time.RFC3339Nano) + " " + l.text + "\n")
		var hdr [8]byte
		hdr[0] = 1
		binary.BigEndian.PutUint32(hdr[4:], uint32(len(payload)))
		w.Write(append(hdr[:], payload...))
	}
	for _, l := range lines {
		emit(l)
		sent++
	}
	if flusher != nil {
		flusher.Flush()
	}
	if tail > 0 {
		return
	}
	for follow {
		fd.mu.Lock()
		running := c.running
		wake := c.wake
		pending := append([]fakeLine(nil), c.logs[min(sent, len(c.logs)):]...)
		sent = len(c.logs)
		fd.mu.Unlock()
		for _, l := range pending {
			emit(l)
		}
		if flusher != nil {
			flusher.Flush()
		}
		if !running {
			return
		}
		select {
		case <-wake:
		case <-rotated:
			return
		case <-r.Context().Done():
			return
		case <-time.After(time.Second):
		}
	}
}

// Paper's replies to the tick commands.
const (
	paperTPSSmooth  = "§6TPS from last 1m, 5m, 15m: §a*20.0, §a19.95, §a19.98"
	paperMSPTSmooth = "§6Server tick times §e(§7avg§e/§7min§e/§7max§e)§6 from last 5s§7,§6 10s§7,§6 1m§e:\n§6◴ §a4.2§7/§a2.1§7/§a9.8§e, §a4.5§7/§a2.0§7/§a14.3§e, §a4.9§7/§a1.9§7/§e41.7"
	paperTPSBehind  = "§6TPS from last 1m, 5m, 15m: §e17.1, §e17.4, §a19.2"
	paperMSPTBehind = "§6Server tick times §e(§7avg§e/§7min§e/§7max§e)§6 from last 5s§7,§6 10s§7,§6 1m§e:\n§6◴ §e58.2§7/§a31.0§7/§c142.0§e, §e57.0§7/§a30.1§7/§c150.3§e, §e58.4§7/§a29.9§7/§c188.0"
)

// fakeRCON answers console commands like a Paper server.
type fakeRCON struct {
	addr string
	// accept decides whether a password is one of the servers'.
	accept   func(string) bool
	mu       sync.Mutex
	commands []string
	online   []string
	// listed are the names whitelist add was given, lower case.
	listed map[string]bool
	// savingOff holds the servers, by RCON password, whose automatic saving
	// is turned off.
	savingOff map[string]bool
	// lose, when it returns true, runs a command and then drops the
	// connection without replying, as when a reply is lost.
	lose func(cmd string) bool
	// hangUp holds commands the fake takes and then hangs up on without
	// answering, like a server stopping mid-command.
	hangUp map[string]bool
	conns  map[net.Conn]bool
	// answer, when it returns true, replaces the reply to a command.
	answer    func(cmd string) (string, bool)
	tps, mspt string
}

// dropAll hangs up every open connection, like a server restarting.
func (fr *fakeRCON) dropAll() {
	fr.mu.Lock()
	defer fr.mu.Unlock()
	for c := range fr.conns {
		c.Close()
	}
}

func (fr *fakeRCON) count(cmd string) int {
	fr.mu.Lock()
	defer fr.mu.Unlock()
	n := 0
	for _, c := range fr.commands {
		if c == cmd {
			n++
		}
	}
	return n
}

func startFakeRCON(t *testing.T, accept func(string) bool) *fakeRCON {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	fr := &fakeRCON{addr: ln.Addr().String(), accept: accept, savingOff: map[string]bool{}, hangUp: map[string]bool{}, conns: map[net.Conn]bool{}, tps: paperTPSSmooth, mspt: paperMSPTSmooth}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go fr.handle(c)
		}
	}()
	return fr
}

func (fr *fakeRCON) setOnline(names ...string) {
	fr.mu.Lock()
	fr.online = names
	fr.mu.Unlock()
}

// letGo takes a kicked or banned player off the list of who is online.
func (fr *fakeRCON) letGo(name string) {
	fr.mu.Lock()
	fr.online = slices.DeleteFunc(slices.Clone(fr.online), func(n string) bool { return strings.EqualFold(n, name) })
	fr.mu.Unlock()
}

// save turns a server's automatic saving on or off and answers like Paper.
func (fr *fakeRCON) save(pass string, on bool) string {
	fr.mu.Lock()
	defer fr.mu.Unlock()
	wasOn := !fr.savingOff[pass]
	fr.savingOff[pass] = !on
	switch {
	case on && wasOn:
		return "Saving is already turned on"
	case on:
		return "Automatic saving is now enabled"
	case !wasOn:
		return "Saving is already turned off"
	}
	return "Automatic saving is now disabled"
}

// savingIsOff reports whether any server's automatic saving is off.
func (fr *fakeRCON) savingIsOff() bool {
	fr.mu.Lock()
	defer fr.mu.Unlock()
	for _, off := range fr.savingOff {
		if off {
			return true
		}
	}
	return false
}

func (fr *fakeRCON) handle(c net.Conn) {
	fr.mu.Lock()
	fr.conns[c] = true
	fr.mu.Unlock()
	defer func() {
		fr.mu.Lock()
		delete(fr.conns, c)
		fr.mu.Unlock()
		c.Close()
	}()
	authed := false
	pass := ""
	for {
		var hdr [4]byte
		if _, err := io.ReadFull(c, hdr[:]); err != nil {
			return
		}
		n := binary.LittleEndian.Uint32(hdr[:])
		buf := make([]byte, n)
		if _, err := io.ReadFull(c, buf); err != nil {
			return
		}
		id := int32(binary.LittleEndian.Uint32(buf))
		typ := int32(binary.LittleEndian.Uint32(buf[4:]))
		body := string(bytes.TrimRight(buf[8:], "\x00"))
		reply := func(id int32, typ int32, s string) {
			p := make([]byte, 14+len(s))
			binary.LittleEndian.PutUint32(p, uint32(10+len(s)))
			binary.LittleEndian.PutUint32(p[4:], uint32(id))
			binary.LittleEndian.PutUint32(p[8:], uint32(typ))
			copy(p[12:], s)
			c.Write(p)
		}
		switch {
		case typ == 3:
			authed = body != "" && fr.accept(body)
			if authed {
				pass = body
				reply(id, 2, "")
			} else {
				reply(-1, 2, "")
			}
		case typ == 2 && authed:
			fr.mu.Lock()
			fr.commands = append(fr.commands, body)
			online := append([]string(nil), fr.online...)
			hangUp, lose, answer, tps, mspt := fr.hangUp[body], fr.lose, fr.answer, fr.tps, fr.mspt
			fr.mu.Unlock()
			if hangUp {
				return
			}
			out, custom := "", false
			if answer != nil {
				out, custom = answer(body)
			}
			switch {
			case custom:
			case body == "list":
				out = fmt.Sprintf("There are %d of a max of 10 players online: %s", len(online), strings.Join(online, ", "))
			case body == "save-off":
				out = fr.save(pass, false)
			case body == "save-on":
				out = fr.save(pass, true)
			case strings.HasPrefix(body, "save-all"):
				out = "Saved the game"
			case body == "tps":
				out = tps
			case body == "mspt":
				out = mspt
			case strings.HasPrefix(body, "whitelist add "):
				name := strings.TrimPrefix(body, "whitelist add ")
				fr.mu.Lock()
				already := fr.listed[strings.ToLower(name)]
				if fr.listed == nil {
					fr.listed = map[string]bool{}
				}
				fr.listed[strings.ToLower(name)] = true
				fr.mu.Unlock()
				out = "Added " + name + " to the whitelist"
				if already {
					out = "Player is already whitelisted"
				}
			case strings.HasPrefix(body, "tellraw "):
				name, _, _ := strings.Cut(strings.TrimPrefix(body, "tellraw "), " ")
				if !slices.ContainsFunc(online, func(n string) bool { return strings.EqualFold(n, name) }) {
					out = "No player was found"
				}
			case strings.HasPrefix(body, "ban "):
				name, reason, _ := strings.Cut(strings.TrimPrefix(body, "ban "), " ")
				fr.letGo(name)
				out = "Banned " + name + ": " + reason
			case strings.HasPrefix(body, "op "):
				out = "Made " + strings.TrimPrefix(body, "op ") + " a server operator"
			case strings.HasPrefix(body, "kick "):
				name, reason, _ := strings.Cut(strings.TrimPrefix(body, "kick "), " ")
				out = "No player was found"
				if slices.ContainsFunc(online, func(n string) bool { return strings.EqualFold(n, name) }) {
					fr.letGo(name)
					out = "Kicked " + name + ": " + reason
				}
			case strings.HasPrefix(body, "say "), strings.HasPrefix(body, "tellraw "):
			default:
				out = "Unknown or incomplete command. See below for error\n" + body + "<--[HERE]"
			}
			if lose != nil && lose(body) {
				return
			}
			reply(id, 0, out)
		default:
			reply(id, 0, "Unknown request")
		}
	}
}

// fakeSLP answers Server List Pings with the current player count.
func startFakeSLP(t *testing.T, fr *fakeRCON) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				c.SetDeadline(time.Now().Add(2 * time.Second))
				buf := make([]byte, 512)
				c.Read(buf)
				fr.mu.Lock()
				n := len(fr.online)
				fr.mu.Unlock()
				status := fmt.Sprintf(`{"version":{"name":"Paper 26.1.2","protocol":775},"players":{"max":10,"online":%d}}`, n)
				var body bytes.Buffer
				body.WriteByte(0)
				body.Write(varint(len(status)))
				body.WriteString(status)
				out := append(varint(body.Len()), body.Bytes()...)
				c.Write(out)
			}(c)
		}
	}()
	return ln.Addr().String()
}

func varint(v int) []byte {
	var out []byte
	u := uint32(v)
	for {
		if u&^0x7F == 0 {
			return append(out, byte(u))
		}
		out = append(out, byte(u&0x7F|0x80))
		u >>= 7
	}
}
