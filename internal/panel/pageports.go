package panel

import (
	"context"
	"crypto/tls"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/CIYAhq/playkeeper/internal/api"
	"github.com/CIYAhq/playkeeper/internal/certs"
)

// The public page's ports, which also carry the dashboard while Serve the
// dashboard on the standard HTTPS port is on (dashboard443.go). The panel
// can't open ports below 1024, so it asks the agent, which opens 443 and 80
// only when nothing else wants them and passes the listening sockets over
// its socket (internal/agent, pageports.go). The keeper holds them while a
// server is on the page and the machine has an address, or the dashboard
// wants them, and closes them as soon as that stops, so the owner gets the
// ports back by turning the page and the switch off.

const (
	// pageFirstLook is how soon after starting the keeper first looks.
	pageFirstLook = 2 * time.Second
	// pageLookEvery is how often it looks again.
	pageLookEvery = time.Minute
	// pageWaitAgain is how long a port that is claimed, busy or refused
	// waits before it is asked for again; one the machine's start holds
	// back is asked for at every look.
	pageWaitAgain = 15 * time.Minute
	// maxPageConns bounds the open connections on each of the page's ports.
	maxPageConns = 1024
)

// pageListener is one of the page's ports being served.
type pageListener struct {
	srv  *http.Server
	port int
}

// runPage keeps the page's ports until ctx ends.
func (s *Server) runPage(ctx context.Context) {
	t := time.NewTimer(pageFirstLook)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			s.closePagePorts(api.PortOff)
			return
		case <-t.C:
		case <-s.page.kick:
			if !t.Stop() {
				select {
				case <-t.C:
				default:
				}
			}
		}
		s.lookAtPage(ctx)
		t.Reset(pageLookEvery)
	}
}

// lookAtPage opens or closes the page's ports to match what the agent
// says: held while a server is on the page and the machine has an address,
// or while the dashboard answers the machine's name on port 443.
func (s *Server) lookAtPage(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var st api.PublicPageState
	if _, err := s.agent.Do(ctx, http.MethodGet, "/v1/public-page/state", nil, nil, &st); err != nil {
		// Without the agent the page can't show anything new, but what it
		// holds stays: the agent is back in a moment after an update.
		return
	}
	p := s.page
	p.mu.Lock()
	p.host, p.hosts = st.Host, st.Hosts
	p.on, p.dashboard, p.reached = st.On, st.Dashboard, st.Dashboard && st.Reached
	p.mu.Unlock()
	if !st.On && !st.Dashboard {
		s.closePagePorts(api.PortOff)
		return
	}
	want := s.pagePortsWanted()
	if !want.HTTPS && !want.HTTP {
		return
	}
	ports, files, err := s.agent.PublicPagePorts(ctx, want)
	if err != nil {
		s.log.Warn("could not get ports 443 and 80 for the public page", "err", err)
		return
	}
	s.servePagePorts(want, ports, files)
}

// pagePortsWanted are the ports the keeper asks the agent for now: those it
// doesn't hold, once their wait is over, and never the dashboard's own.
func (s *Server) pagePortsWanted() api.PagePortsRequest {
	p := s.page
	p.mu.Lock()
	defer p.mu.Unlock()
	now := s.now()
	ask := func(i int, port api.PagePort) bool {
		return p.held[i] == nil && !now.Before(p.next[i]) && port.Port != s.cfg.PanelPort
	}
	return api.PagePortsRequest{HTTPS: ask(0, p.ports.HTTPS), HTTP: ask(1, p.ports.HTTP)}
}

// servePagePorts starts serving the sockets the agent opened for want,
// HTTPS first, and notes what each port asked for is. The agent calls a
// port it wasn't asked for off, so what the keeper knows of that one, like
// its holder and its wait, stands.
func (s *Server) servePagePorts(want api.PagePortsRequest, ports api.PublicPagePorts, files []*os.File) {
	p := s.page
	p.mu.Lock()
	defer p.mu.Unlock()
	now := s.now()
	asked := [2]bool{want.HTTPS, want.HTTP}
	for i, port := range []api.PagePort{ports.HTTPS, ports.HTTP} {
		slot := &p.ports.HTTPS
		if i == 1 {
			slot = &p.ports.HTTP
		}
		if p.held[i] != nil || !asked[i] {
			continue
		}
		if port.State != api.PortOpen {
			*slot = port
			p.next[i] = now.Add(pageWaitAgain)
			if port.State == api.PortWaiting || port.State == api.PortOff {
				p.next[i] = time.Time{}
			}
			continue
		}
		if len(files) == 0 {
			*slot = api.PagePort{Port: port.Port, State: api.PortBusy}
			continue
		}
		f := files[0]
		files = files[1:]
		l, err := s.servePagePort(f, port.Port, i == 0)
		if err != nil {
			s.log.Warn("could not serve the public page on a port the agent opened", "port", port.Port, "err", err)
			*slot = api.PagePort{Port: port.Port, State: api.PortBusy}
			continue
		}
		p.held[i] = l
		*slot = port
		s.log.Info("the public page answers", "port", port.Port)
	}
	for _, f := range files {
		f.Close()
	}
}

// servePagePort serves the page on the listening socket f, and on port 443
// the dashboard too, for the machine's name while it's wanted there.
func (s *Server) servePagePort(f *os.File, port int, secure bool) (*pageListener, error) {
	ln, err := net.FileListener(f)
	f.Close()
	if err != nil {
		return nil, err
	}
	ln = limitListener(ln, maxPageConns)
	srv := &http.Server{
		Handler:           s.securityHeaders(s.logPageRequests(s.pageHandler(false))),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       time.Minute,
		MaxHeaderBytes:    16 << 10,
		ErrorLog:          slog.NewLogLogger(s.log.Handler(), slog.LevelDebug),
	}
	serve := func() error { return srv.Serve(ln) }
	if secure {
		// The dashboard may answer here, so the port has the panel's own
		// port's limits, HTTP/2 included.
		srv.Handler, srv.IdleTimeout, srv.MaxHeaderBytes = s.httpsHandler(), 2*time.Minute, 0
		srv.TLSConfig = &tls.Config{MinVersion: tls.VersionTLS12, GetCertificate: s.pageCertificate}
		serve = func() error { return srv.ServeTLS(ln, "", "") }
	}
	go func() {
		if err := serve(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			s.log.Warn("the public page stopped answering", "port", port, "err", err)
		}
	}()
	return &pageListener{srv: srv, port: port}, nil
}

// pageCertificate serves only a publicly trusted certificate the agent got
// for the name asked for, never the self-signed one: a browser that can't
// get one gives up on port 443 and tries port 80, where the page answers.
func (s *Server) pageCertificate(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
	if s.pageCerts == nil {
		return nil, errors.New("no certificates")
	}
	return s.pageCerts.GetCertificate(hello)
}

// pageRedirect is where plain HTTP on port 80 goes: HTTPS, while port 443
// is served and a certificate for the name asked for, the machine's or a
// server's own address, is in place.
func (s *Server) pageRedirect(r *http.Request) (string, bool) {
	p := s.page
	p.mu.Lock()
	https := p.held[0]
	port := p.ports.HTTPS.Port
	p.mu.Unlock()
	host := hostName(r.Host)
	if https == nil || host == "" || s.pageCerts == nil {
		return "", false
	}
	if _, err := s.pageCerts.GetCertificate(&tls.ClientHelloInfo{ServerName: host}); err != nil {
		return "", false
	}
	if port != 443 && port != 0 {
		host = net.JoinHostPort(host, strconv.Itoa(port))
	}
	return "https://" + host + r.URL.RequestURI(), true
}

// closePagePorts stops serving the page and gives the ports back.
func (s *Server) closePagePorts(state string) {
	p := s.page
	p.mu.Lock()
	held := p.held
	p.held = [2]*pageListener{}
	p.next = [2]time.Time{}
	p.ports.HTTPS.State, p.ports.HTTPS.Holder = state, ""
	p.ports.HTTP.State, p.ports.HTTP.Holder = state, ""
	p.mu.Unlock()
	for _, l := range held {
		if l == nil {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		l.srv.Shutdown(ctx)
		cancel()
		s.log.Info("the public page stopped answering", "port", l.port)
	}
}

// portsNow is what each of the page's ports is, for the dashboard.
func (p *pageSite) portsNow() api.PublicPagePorts {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.ports
}

// retryNow lets the next look ask for every port the keeper doesn't hold.
func (p *pageSite) retryNow() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.next = [2]time.Time{}
}

// logPageRequests logs the page's failures without their paths or
// addresses: only the page group's route prefix.
func (s *Server) logPageRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, status: 200}
		next.ServeHTTP(sw, r)
		if sw.status >= 500 {
			s.log.Info("request", "method", r.Method, "path", s.page.group.logPath(r.URL.Path), "status", sw.status, "ms", time.Since(start).Milliseconds())
		}
	})
}

// limitListener accepts at most n connections at once; the next waits
// until one closes.
func limitListener(ln net.Listener, n int) net.Listener {
	return &limitedListener{Listener: ln, slots: make(chan struct{}, n), done: make(chan struct{})}
}

type limitedListener struct {
	net.Listener
	slots chan struct{}
	once  sync.Once
	done  chan struct{}
}

func (l *limitedListener) Accept() (net.Conn, error) {
	select {
	case l.slots <- struct{}{}:
	case <-l.done:
		return nil, net.ErrClosed
	}
	c, err := l.Listener.Accept()
	if err != nil {
		<-l.slots
		return nil, err
	}
	return &limitedConn{Conn: c, release: sync.OnceFunc(func() { <-l.slots })}, nil
}

func (l *limitedListener) Close() error {
	l.once.Do(func() { close(l.done) })
	return l.Listener.Close()
}

type limitedConn struct {
	net.Conn
	release func()
}

func (c *limitedConn) Close() error {
	err := c.Conn.Close()
	c.release()
	return err
}

// pageCertStore is the agent's certificates without the self-signed
// fallback, for the page's port 443.
func (s *Server) pageCertStore() *certs.Store {
	st, err := certs.NewStore(certs.StoreOptions{Dir: s.cfg.CertsDir(), Now: s.now})
	if err != nil {
		s.log.Warn("the public page can't read the certificates", "err", err)
		return nil
	}
	return st
}
