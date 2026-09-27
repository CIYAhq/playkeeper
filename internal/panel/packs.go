package panel

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/CIYAhq/playkeeper/internal/api"
	"github.com/CIYAhq/playkeeper/internal/packs"
)

// activePacks remembers which resource packs this machine's servers offer,
// so that serving a pack doesn't ask the agent every time.
type activePacks struct {
	fetch func(context.Context) ([]string, error)
	now   func() time.Time

	mu      sync.Mutex
	sums    map[string]bool
	fetched time.Time
	tried   time.Time
	// renewing is closed when the agent has answered the request in flight,
	// and nil while none is.
	renewing chan struct{}
}

const (
	// activeFresh is how long a list of offered packs is used, and
	// activeRetry how often a request for a pack not on it may ask again,
	// as a server may have just started offering it.
	activeFresh = 10 * time.Second
	activeRetry = time.Second
)

// has reports whether a server offers the pack whose SHA-1 hash is sum.
// Only the request that renews the list, and requests for a pack missing
// from it, wait for the agent, and never while holding the lock: players
// downloading a listed pack aren't held up. While the agent can't answer,
// the last list it gave is used.
func (a *activePacks) has(sum string) bool {
	a.mu.Lock()
	now := a.now()
	known, started := a.sums[sum], false
	if (!known || now.Sub(a.fetched) >= activeFresh) && now.Sub(a.tried) >= activeRetry && a.renewing == nil {
		a.tried, a.renewing, started = now, make(chan struct{}), true
		go a.renew(now, a.renewing)
	}
	wait := a.renewing
	a.mu.Unlock()
	if wait == nil || known && !started {
		return known
	}
	<-wait
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.sums[sum]
}

// renew asks the agent which packs are offered, keeps the list if it
// answered, and closes done.
func (a *activePacks) renew(at time.Time, done chan struct{}) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	list, err := a.fetch(ctx)
	cancel()
	a.mu.Lock()
	if err == nil {
		a.sums = make(map[string]bool, len(list))
		for _, s := range list {
			a.sums[s] = true
		}
		a.fetched = at
	}
	a.renewing = nil
	a.mu.Unlock()
	close(done)
}

func (s *Server) fetchActivePacks(ctx context.Context) ([]string, error) {
	var out api.ActiveResourcePacks
	_, err := s.agent.Do(ctx, "GET", "/v1/resource-packs/active", nil, nil, &out)
	return out.SHA1, err
}

// packOrigin is where players' games download resource packs: the host and
// port the dashboard was opened at, as the panel's port answers plain HTTP
// too.
func packOrigin(r *http.Request) packs.Origin {
	host, port := r.Host, 443
	if h, p, err := net.SplitHostPort(r.Host); err == nil {
		host = h
		port, _ = strconv.Atoi(p)
	}
	return packs.Origin{Host: host, Port: port}
}

// hResourcePackUpload offers an uploaded resource pack to a server's
// players. They download it from the address the dashboard was opened at:
// their games refuse the panel's self-signed certificate, so over plain
// HTTP unless the agent finds a publicly trusted one for that host.
func (s *Server) hResourcePackUpload(w http.ResponseWriter, r *http.Request, sess *session) {
	m, ok := s.target(w, r)
	if !ok {
		return
	}
	origin := packOrigin(r)
	if _, err := origin.PackURL(strings.Repeat("0", 40)); err != nil {
		code, msg := api.CodeInvalid, "Players' games couldn't download the pack from this address."
		var pe *packs.Error
		if errors.As(err, &pe) {
			code, msg = pe.Code, pe.Msg
		}
		writeErr(w, http.StatusBadRequest, code, msg, "Open the dashboard at the address players use to join, then upload the pack again.")
		return
	}
	q := url.Values{"host": {origin.Host}, "port": {strconv.Itoa(origin.Port)}}
	if name := r.URL.Query().Get("name"); name != "" {
		q.Set("name", name)
	}
	s.relayUpload(w, r, m, agentPath("/v1/servers/{id}/resourcepack", r), q, "application/zip", sess)
}
