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
	t          *testing.T
	mu         sync.Mutex
	images     map[string]bool
	pulls      int
	networks   map[string]map[string]string
	byName     map[string]*fakeContainer
	byID       map[string]*fakeContainer
	nextID     int
	calls      []string
	startErr   string
	jarContent []byte
	bootDelay  time.Duration
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

// addLog appends a server log line to the running Minecraft container.
func (fd *fakeDocker) addLog(text string) {
	fd.mu.Lock()
	defer fd.mu.Unlock()
	c := fd.byName[containerName]
	fd.log(c, text)
}

// crash kills the server without a clean shutdown (like `kill -9 java`).
func (fd *fakeDocker) crash(code int) {
	fd.mu.Lock()
	defer fd.mu.Unlock()
	c := fd.byName[containerName]
	c.running, c.exitCode, c.finished = false, code, time.Now().UTC()
	close(c.wake)
	c.wake = make(chan struct{})
}

// externalStop stops the container the way `docker stop` or a host shutdown
// does: the server logs its clean shutdown.
func (fd *fakeDocker) externalStop() {
	fd.mu.Lock()
	defer fd.mu.Unlock()
	c := fd.byName[containerName]
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
		jsonOut(w, 200, map[string]string{"Version": "29.0.0-fake", "ApiVersion": "1.52", "MinAPIVersion": "1.44"})
		return
	}
	path = strings.TrimPrefix(path, "/v1.52")
	fd.mu.Lock()
	fd.calls = append(fd.calls, r.Method+" "+path)
	fd.mu.Unlock()
	switch {
	case r.Method == "GET" && strings.HasPrefix(path, "/images/") && strings.HasSuffix(path, "/json"):
		ref := strings.TrimSuffix(strings.TrimPrefix(path, "/images/"), "/json")
		fd.mu.Lock()
		ok := fd.images[ref]
		fd.mu.Unlock()
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
		fd.mu.Lock()
		var list []map[string]any
		for _, c := range fd.byName {
			list = append(list, map[string]any{"Id": c.id, "Names": []string{"/" + c.name}, "Image": c.cfg.Image, "State": map[bool]string{true: "running", false: "exited"}[c.running], "Labels": c.cfg.Labels})
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
	c := &fakeContainer{id: fmt.Sprintf("c%063d", fd.nextID), name: name, cfg: cfg, wake: make(chan struct{})}
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
			"Config":          map[string]any{"Image": c.cfg.Image, "Labels": c.cfg.Labels},
			"NetworkSettings": map[string]any{"Networks": map[string]any{networkName: map[string]string{"IPAddress": "127.0.0.1"}}},
		})
	case r.Method == "POST" && action == "start":
		fd.mu.Lock()
		if fd.startErr != "" && len(c.cfg.HostConfig.PortBindings) > 0 {
			msg := fd.startErr
			fd.mu.Unlock()
			jsonOut(w, 500, map[string]string{"message": msg})
			return
		}
		if c.running {
			fd.mu.Unlock()
			w.WriteHeader(304)
			return
		}
		c.running, c.started, c.finished, c.exitCode = true, time.Now().UTC(), time.Time{}, 0
		setup := env(c.cfg, "SETUP_ONLY") == "TRUE"
		fd.log(c, "[init] Running as uid=1000 gid=1000")
		fd.log(c, "[init] Resolving type given PAPER")
		fd.mu.Unlock()
		go fd.boot(c, setup)
		w.WriteHeader(204)
	case r.Method == "POST" && action == "stop":
		fd.mu.Lock()
		if c.running {
			fd.log(c, "[12:00:00 INFO]: Stopping server")
			c.running, c.exitCode, c.finished = false, 0, time.Now().UTC()
		}
		fd.mu.Unlock()
		w.WriteHeader(204)
	case r.Method == "DELETE" && action == "":
		fd.mu.Lock()
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
	time.Sleep(fd.bootDelay)
	fd.mu.Lock()
	defer fd.mu.Unlock()
	if !c.running {
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
	fd.log(c, "[12:00:00 INFO]: Starting minecraft server version "+env(c.cfg, "VERSION"))
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
		case <-r.Context().Done():
			return
		case <-time.After(time.Second):
		}
	}
}

// fakeRCON answers console commands like a Paper server.
type fakeRCON struct {
	addr     string
	password string
	mu       sync.Mutex
	commands []string
	online   []string
}

func startFakeRCON(t *testing.T, password string) *fakeRCON {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	fr := &fakeRCON{addr: ln.Addr().String(), password: password}
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

func (fr *fakeRCON) handle(c net.Conn) {
	defer c.Close()
	authed := false
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
			authed = body == fr.password
			if authed {
				reply(id, 2, "")
			} else {
				reply(-1, 2, "")
			}
		case typ == 2 && authed:
			fr.mu.Lock()
			fr.commands = append(fr.commands, body)
			online := append([]string(nil), fr.online...)
			fr.mu.Unlock()
			switch {
			case body == "list":
				reply(id, 0, fmt.Sprintf("There are %d of a max of 10 players online: %s", len(online), strings.Join(online, ", ")))
			case strings.HasPrefix(body, "save-all"):
				reply(id, 0, "Saved the game")
			case strings.HasPrefix(body, "whitelist add "):
				reply(id, 0, "Added "+strings.TrimPrefix(body, "whitelist add ")+" to the whitelist")
			default:
				reply(id, 0, "Unknown or incomplete command. See below for error\n"+body+"<--[HERE]")
			}
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
