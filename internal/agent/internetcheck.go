package agent

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/CIYAhq/playkeeper/internal/api"
	"github.com/CIYAhq/playkeeper/internal/usage"
	"github.com/CIYAhq/playkeeper/internal/version"
)

// An internet check asks the stats service to connect back to the machine's
// address on a server's game port (usage.Client.Reach). That shows whether
// the provider's firewall lets friends in, which the machine can't see from
// inside. The dashboard asks for one by itself while usage stats are on, at
// most hourly; with them off, only someone's "Check from the internet"
// asks. A test install and playkeeper dev ask only a stats service their
// config names (StatsURL), never the project's own.

const (
	// internetCheckEvery is how long a check holds for the dashboard's
	// automatic ones.
	internetCheckEvery = time.Hour
	// internetCheckAgain is how soon after a check someone can ask again.
	internetCheckAgain = 15 * time.Second
	// internetCheckWait bounds a check: the service takes 10 seconds at most.
	internetCheckWait = 30 * time.Second
)

// internetCheck is the latest check of a server's game port, of port, asked
// at askedAt; the server's mu guards it.
type internetCheck struct {
	port     int
	askedAt  time.Time
	checking bool
	result   string
	at       time.Time
	problem  string
	retryAt  time.Time
}

// internetCheckable reports whether the machine may ask for internet checks:
// a test install or playkeeper dev never of the project's own service.
func (a *Agent) internetCheckable() bool {
	return a.statsService() != usage.DefaultURL || !a.cfg.Dev && !a.usageTest()
}

// internetCheckStatus is the server's internet check as the dashboard shows
// it, or nil where its game port can't be checked.
func (s *server) internetCheckStatus() *api.InternetCheck {
	port := s.gamePort
	if !s.usage.checkable() || (usage.ReachRequest{Port: port}).Check() != nil {
		return nil
	}
	on, _, _ := s.usageDecision()
	p := s.usage.provider()
	st := &api.InternetCheck{Auto: on, Provider: p.Name, StepsURL: p.StepsURL()}
	s.mu.Lock()
	c := s.check
	s.mu.Unlock()
	if c.port != port {
		return st
	}
	st.Checking, st.Problem = c.checking, c.problem
	if !c.at.IsZero() {
		at := c.at
		st.Result, st.CheckedAt = c.result, &at
	}
	if s.now().Before(c.retryAt) {
		retry := c.retryAt
		st.RetryAt = &retry
	}
	return st
}

// hInternetCheck asks for an internet check of the server's game port and
// answers the server's status, which shows the check on its way, or the
// latest one while it still holds.
func (s *server) hInternetCheck(w http.ResponseWriter, r *http.Request) {
	var req api.InternetCheckRequest
	if err := decode(r, &req); err != nil {
		writeError(w, err)
		return
	}
	if _, err := validActor(req.Actor); err != nil {
		writeError(w, err)
		return
	}
	if err := s.startInternetCheck(r.Context(), req.Auto); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, s.Status(r.Context()))
}

// startInternetCheck starts a check, unless one is on its way, the latest
// still holds (an hour for an automatic one, a few seconds for one someone
// asked for), or the stats service said to wait.
func (s *server) startInternetCheck(ctx context.Context, auto bool) error {
	if !s.usage.checkable() {
		return errConflict("This machine doesn't ask the stats service to check its ports: it's a test install, or playkeeper dev.", "")
	}
	port := s.gamePort
	if (usage.ReachRequest{Port: port}).Check() != nil {
		return errConflict(fmt.Sprintf("Port %d can't be checked from the internet: the check covers the ports Playkeeper gives servers, %d to %d.", port, usage.ReachPortMin, usage.ReachPortMax), "")
	}
	if on, _, _ := s.usageDecision(); auto && !on {
		return errConflict("Usage stats are off, so Playkeeper checks from the internet only when someone asks.", "")
	}
	notAnswering := errConflict("The server isn't answering on this machine, so there's nothing to check from the internet yet.", "Start it, then check again.")
	if !s.online(ctx) {
		return notAnswering
	}
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.reachable || !s.fresh(s.reachableAt) {
		return notAnswering
	}
	c := &s.check
	holds := internetCheckAgain
	if auto {
		holds = internetCheckEvery
	}
	if c.port == port && (c.checking || now.Sub(c.askedAt) < holds || now.Before(c.retryAt)) {
		return nil
	}
	if c.port != port {
		*c = internetCheck{port: port}
	}
	c.askedAt, c.checking, c.problem = now, true, ""
	go s.runInternetCheck(port)
	return nil
}

// runInternetCheck asks the stats service and keeps what it found, or why
// it couldn't ask.
func (s *server) runInternetCheck(port int) {
	ctx, cancel := context.WithTimeout(s.ctx, internetCheckWait)
	defer cancel()
	client := usage.Client{URL: s.cfg.StatsURL, HTTP: s.opts.UsageClient, Version: version.Version}
	ans, err := client.Reach(ctx, port)
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	c := &s.check
	if c.port != port {
		return
	}
	c.checking = false
	if err != nil {
		c.problem = internetCheckProblem(err, s.statsService())
		if se := (*usage.ServiceError)(nil); errors.As(err, &se) && se.RetryAfter > 0 {
			c.retryAt = now.Add(se.RetryAfter)
		}
		return
	}
	c.result, c.at = ans.Result, now
}

// internetCheckProblem says why a check of the stats service at service
// couldn't be made.
func internetCheckProblem(err error, service string) string {
	host := service
	if u, perr := url.Parse(service); perr == nil && u.Host != "" {
		host = u.Host
	}
	var se *usage.ServiceError
	if !errors.As(err, &se) {
		return fmt.Sprintf("Playkeeper couldn't ask the stats service at %s to check from the internet.", host)
	}
	switch se.Code {
	case "rate_limited":
		return "The stats service has checked this machine's address as often as it will for now."
	case "busy":
		return "The stats service is busy with other checks."
	case "not_public":
		return "The stats service saw this machine's requests come from an address that isn't public, so it can't check from the internet."
	}
	return fmt.Sprintf("The stats service at %s answered HTTP %d, so it couldn't check from the internet.", host, se.Status)
}
