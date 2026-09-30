package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/CIYAhq/playkeeper/internal/api"
	"github.com/CIYAhq/playkeeper/internal/webservers"
)

// Ports 443 and 80 for the public page, and from 0.4.11 for the dashboard
// on the standard HTTPS port (dashboardport.go). The panel runs without
// CAP_NET_BIND_SERVICE, so the agent, which has it for Let's Encrypt's
// checks, opens the ports and passes the listening sockets to the panel
// over its own socket (SCM_RIGHTS); from then on only the panel serves
// them. The agent takes a port only while nothing else would want it:
//
//   - not in the first minutes after the machine starts, so the programs
//     that start with it get their ports first;
//   - not while a Docker container publishes it or names it in its
//     settings, running or not;
//   - not while a web server that listens on these ports by default is set
//     to start with the machine, even if it isn't running now;
//   - and not while another program listens on it. A port found busy isn't
//     tried again until the agent restarts or the owner asks, so a web
//     server restarting just as Playkeeper looks never loses its port.

// pagePortsPath is the route that hands the ports over.
const pagePortsPath = "/v1/public-page/ports"

// pageSettle is how long after the machine starts the agent leaves ports
// 443 and 80 to the programs that start with it.
const pageSettle = 3 * time.Minute

// maxPageContainers bounds the stopped containers whose settings are read.
const maxPageContainers = 200

// pagePorts remembers the ports found busy, and serializes handing ports
// over.
type pagePorts struct {
	hand sync.Mutex

	mu   sync.Mutex
	busy map[string]api.PagePort
}

// takePagePorts opens the page's ports the panel asks for that are free,
// and says what each is. The files are the listening sockets of the open
// ones, HTTPS first; the caller closes them. A port not asked for is off:
// the panel holds it already, or doesn't want it.
func (a *Agent) takePagePorts(ctx context.Context, want api.PagePortsRequest) (api.PublicPagePorts, []*os.File) {
	addrs := [2]string{a.opts.PageHTTPSAddr, a.opts.PageHTTPAddr}
	asked := [2]bool{want.HTTPS, want.HTTP}
	out := [2]api.PagePort{{Port: addrPort(addrs[0]), State: api.PortOff}, {Port: addrPort(addrs[1]), State: api.PortOff}}
	result := func() api.PublicPagePorts {
		if asked[0] {
			a.noteDashboard443(out[0])
		}
		return api.PublicPagePorts{HTTPS: out[0], HTTP: out[1]}
	}
	if st := a.publicPageState(); !st.On && !st.Dashboard || !want.HTTPS && !want.HTTP {
		return api.PublicPagePorts{HTTPS: out[0], HTTP: out[1]}, nil
	}
	if a.opts.Uptime() < pageSettle {
		for i := range out {
			if asked[i] {
				out[i].State = api.PortWaiting
			}
		}
		return result(), nil
	}
	claims := a.portClaims(ctx, out[0].Port, out[1].Port)
	var files []*os.File
	for i, addr := range addrs {
		if !asked[i] {
			continue
		}
		var f *os.File
		out[i], f = a.takePagePort(addr, out[i].Port, claims)
		if f != nil {
			files = append(files, f)
		}
	}
	return result(), files
}

// takePagePort opens one of the page's ports, unless something claims it
// or it was found busy before.
func (a *Agent) takePagePort(addr string, port int, claims map[int]string) (api.PagePort, *os.File) {
	a.pagePorts.mu.Lock()
	defer a.pagePorts.mu.Unlock()
	if p, ok := a.pagePorts.busy[addr]; ok {
		return p, nil
	}
	if holder, ok := claims[port]; ok {
		return api.PagePort{Port: port, State: api.PortClaimed, Holder: holder}, nil
	}
	var ln net.Listener
	var err error
	listen := func() { ln, err = net.Listen("tcp", addr) }
	// The agent's own HTTP-01 checks listen on port 80 for a few seconds;
	// the port is asked for again after them, never counted as busy.
	if port == addrPort(a.opts.HTTP01Addr) {
		if !a.http01.Idle(listen) {
			return api.PagePort{Port: port, State: api.PortWaiting}, nil
		}
	} else {
		listen()
	}
	if err != nil {
		p := api.PagePort{Port: port, State: api.PortBusy}
		if errors.Is(err, syscall.EACCES) || errors.Is(err, syscall.EPERM) {
			p.State = api.PortDenied
			return p, nil
		}
		if name, _, ok := a.opts.PortHolder(port); ok {
			p.Holder = name
		}
		if a.pagePorts.busy == nil {
			a.pagePorts.busy = map[string]api.PagePort{}
		}
		a.pagePorts.busy[addr] = p
		a.log.Info("the public page leaves a port that is in use alone", "port", port, "holder", p.Holder, "err", err)
		return p, nil
	}
	defer ln.Close()
	f, err := ln.(*net.TCPListener).File()
	if err != nil {
		a.log.Warn("could not hand over a port for the public page", "port", port, "err", err)
		return api.PagePort{Port: port, State: api.PortBusy}, nil
	}
	return api.PagePort{Port: port, State: api.PortOpen}, f
}

// portClaims names what claims each of ports besides a program listening
// on it now: a Docker container that publishes it or names it in its
// settings, or a web server set to start with the machine.
func (a *Agent) portClaims(ctx context.Context, ports ...int) map[int]string {
	out := map[int]string{}
	claim := func(port int, by string) {
		if _, ok := out[port]; !ok && slices.Contains(ports, port) {
			out[port] = by
		}
	}
	list, err := a.docker.ContainerList(ctx, true)
	if err != nil {
		a.log.Warn("could not ask Docker which containers use the public page's ports", "err", err)
	}
	inspected := 0
	for _, c := range list {
		if c.Labels[labelManaged] == "true" {
			continue
		}
		name := containerLabel(c.Names)
		for _, p := range c.Ports {
			if p.Type == "tcp" {
				claim(p.PublicPort, name)
			}
		}
		if c.State == "running" || inspected >= maxPageContainers {
			continue
		}
		inspected++
		j, err := a.docker.ContainerInspect(ctx, c.ID)
		if err != nil {
			continue
		}
		for spec, binds := range j.HostConfig.PortBindings {
			if !strings.HasSuffix(spec, "/tcp") {
				continue
			}
			for _, b := range binds {
				if p, err := strconv.Atoi(b.HostPort); err == nil {
					claim(p, name)
				}
			}
		}
	}
	for _, unit := range webservers.Units {
		if a.enabledService(unit) {
			for _, p := range ports {
				claim(p, unit)
			}
		}
	}
	return out
}

// containerLabel is a container's name as the dashboard may show it, or
// "Docker" when it has none Docker would allow.
func containerLabel(names []string) string {
	for _, n := range names {
		if n = strings.TrimPrefix(n, "/"); reContainerName.MatchString(n) {
			return n
		}
	}
	return "Docker"
}

// enabledService reports whether systemd starts the service unit with the
// machine: a link to it in a .wants folder.
func (a *Agent) enabledService(unit string) bool {
	return webservers.Enabled(a.opts.SystemdDir, unit)
}

// addrPort is the port of a listen address like ":443".
func addrPort(addr string) int {
	_, p, _ := net.SplitHostPort(addr)
	n, _ := strconv.Atoi(p)
	return n
}

// uptime is how long the machine has been up, from /proc/uptime; long
// ago when that can't be read.
func uptime() time.Duration {
	b, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return 24 * time.Hour
	}
	first, _, _ := strings.Cut(strings.TrimSpace(string(b)), " ")
	secs, err := strconv.ParseFloat(first, 64)
	if err != nil {
		return 24 * time.Hour
	}
	return time.Duration(secs * float64(time.Second))
}

// hPublicPagePorts hands the panel the page's ports that are free: the
// JSON of what each port is, with the listening sockets of the open ones
// passed along with it.
func (a *Agent) hPublicPagePorts(w http.ResponseWriter, r *http.Request) {
	// Only the agent's own socket can carry the sockets, so a request that
	// came another way opens nothing.
	if _, ok := r.Context().Value(connKey{}).(*net.UnixConn); !ok {
		writeErr(w, http.StatusBadRequest, api.CodeInvalid, "Only the agent's own socket can carry the public page's ports.", "")
		return
	}
	var want api.PagePortsRequest
	if err := decode(r, &want); err != nil {
		writeError(w, err)
		return
	}
	a.pagePorts.hand.Lock()
	defer a.pagePorts.hand.Unlock()
	ports, files := a.takePagePorts(r.Context(), want)
	if err := writeWithFiles(w, ports, files); err != nil {
		a.log.Warn("could not hand the public page's ports to the panel", "err", err)
	}
}

// hPublicPagePortsRetry forgets the ports found busy, so the next hand-over
// tries them again: the owner freed one.
func (a *Agent) hPublicPagePortsRetry(w http.ResponseWriter, r *http.Request) {
	actor, err := actionActor(r)
	if err != nil {
		writeError(w, err)
		return
	}
	a.pagePorts.mu.Lock()
	a.pagePorts.busy = nil
	a.pagePorts.mu.Unlock()
	a.audit(actor, "public_page.ports_retry", "public_page", "requested", "")
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// writeWithFiles answers with v as JSON and passes files along with the
// answer's first byte, which only a Unix socket carries. It takes the
// connection over, so it is the last thing a handler does. It closes the
// files, and before the connection: the panel reads the answer to its end,
// so by the time it has the files, the agent holds no copy of them.
func writeWithFiles(w http.ResponseWriter, v any, files []*os.File) error {
	closeFiles := func() {
		for _, f := range files {
			f.Close()
		}
	}
	body, err := json.Marshal(v)
	if err != nil {
		closeFiles()
		return err
	}
	conn, _, err := http.NewResponseController(w).Hijack()
	if err != nil {
		closeFiles()
		return err
	}
	defer func() {
		closeFiles()
		conn.Close()
	}()
	uc, ok := conn.(*net.UnixConn)
	if !ok {
		msg := `{"error":"Only the agent's own socket can carry the public page's ports.","code":"invalid_request"}`
		fmt.Fprintf(conn, "HTTP/1.1 400 Bad Request\r\nContent-Type: application/json\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s", len(msg), msg)
		return errors.New("the request didn't come over the agent's socket")
	}
	msg := fmt.Appendf(nil, "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: %d\r\nConnection: close\r\n\r\n", len(body))
	msg = append(msg, body...)
	var oob []byte
	if len(files) > 0 {
		fds := make([]int, len(files))
		for i, f := range files {
			fds[i] = int(f.Fd())
		}
		oob = syscall.UnixRights(fds...)
	}
	_ = uc.SetWriteDeadline(time.Now().Add(10 * time.Second))
	n, _, err := uc.WriteMsgUnix(msg, oob, nil)
	if err == nil && n < len(msg) {
		_, err = uc.Write(msg[n:])
	}
	return err
}

// pageRelaysChallenge reports whether port 80's holder answers Let's
// Encrypt's check for token with keyAuth: the public page's port 80 passes
// checks on to the agent. It asks over the loopback address.
func (a *Agent) pageRelaysChallenge(token, keyAuth string) bool {
	port := addrPort(a.opts.HTTP01Addr)
	if port == 0 {
		return false
	}
	ctx, cancel := context.WithTimeout(a.ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://127.0.0.1:"+strconv.Itoa(port)+"/.well-known/acme-challenge/"+token, nil)
	if err != nil {
		return false
	}
	if host := a.pageHost(); host != "" {
		req.Host = host
	}
	c := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := c.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
	return err == nil && resp.StatusCode == http.StatusOK && strings.TrimSpace(string(b)) == keyAuth
}

// hACMEChallenge answers a check Let's Encrypt sent to the page's port 80,
// which the panel passes on: the key authorization of a pending check.
func (a *Agent) hACMEChallenge(w http.ResponseWriter, r *http.Request) {
	keyAuth, ok := a.http01.KeyAuthorization(r.PathValue("token"))
	if !ok {
		writeErr(w, http.StatusNotFound, api.CodeNotFound, "No check is pending for that token.", "")
		return
	}
	w.Header().Set("Content-Type", "text/plain")
	w.Header().Set("Cache-Control", "no-store")
	io.WriteString(w, keyAuth)
}
