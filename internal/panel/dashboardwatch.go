package panel

import (
	"context"
	"errors"
	"net/url"
	"time"

	"github.com/CIYAhq/playkeeper/internal/agentclient"
	"github.com/CIYAhq/playkeeper/internal/machinelink"
)

// Joined machines' watch on the dashboard (the fleet plan's section 5; see
// internal/agent's dashboardwatch.go). Each joined machine the owner or the
// Hetzner token confirmed gets the dashboard's Discord webhook over its
// link, and posts with it only that it can't reach the dashboard and that
// it reaches it again. A machine that isn't confirmed, and every machine
// while Discord isn't connected, is told to clear it.

// watchSyncEvery is how often the dashboard checks each joined machine has
// the webhook it should, so one that was offline catches up.
var watchSyncEvery = time.Minute

// kickWatch has the joined machines given the webhook they should have now.
func (s *Server) kickWatch() {
	select {
	case s.watchKick <- struct{}{}:
	default:
	}
}

// watchReconnected has a machine whose link came back told again.
func (s *Server) watchReconnected(id string) {
	s.watchMu.Lock()
	delete(s.watchSent, id)
	s.watchMu.Unlock()
	s.kickWatch()
}

// runWatchSync gives the joined machines the webhook they should have every
// watchSyncEvery and whenever kickWatch asks, until ctx ends.
func (s *Server) runWatchSync(ctx context.Context) {
	t := time.NewTicker(watchSyncEvery)
	defer t.Stop()
	for {
		s.syncWatch(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-s.watchKick:
		}
	}
}

// syncWatch gives each joined machine the webhook it should have: the
// dashboard's while Discord is connected and the machine is confirmed, and
// none otherwise. A machine is told only when that changes, and again once
// its link comes back (watchReconnected). One that isn't connected is told
// at a later sync; one that refuses, as an older agent without the watch
// does, isn't asked again until its link comes back.
func (s *Server) syncWatch(ctx context.Context) {
	local, err := s.localMachine()
	if err != nil {
		return
	}
	var hook struct {
		WebhookURL string `json:"webhookUrl"`
	}
	if _, err := local.agent.Do(ctx, "GET", "/v1/discord/webhook", nil, nil, &hook); err != nil {
		s.log.Warn("the joined machines' watch on the dashboard can't read its Discord webhook", "err", err)
		return
	}
	ms, err := s.machines()
	if err != nil {
		s.log.Error("list the machines for their watch on the dashboard", "err", err)
		return
	}
	for _, m := range ms {
		if m.Kind != remoteKind {
			continue
		}
		want := ""
		if !m.customersAt.IsZero() {
			want = hook.WebhookURL
		}
		s.watchMu.Lock()
		last, told := s.watchSent[m.ID]
		s.watchMu.Unlock()
		if told && last == want {
			continue
		}
		actx := asActor(ctx, placementActor)
		if want != "" {
			_, err = m.agent.Do(actx, "PUT", "/v1/dashboard-watch", nil, map[string]string{"actor": placementActor, "webhookUrl": want}, nil)
		} else {
			_, err = m.agent.Do(actx, "DELETE", "/v1/dashboard-watch", url.Values{"actor": {placementActor}}, nil, nil)
		}
		var refused *agentclient.Error
		switch {
		case machinelink.CodeOf(err) != "":
			continue
		case errors.As(err, &refused) && refused.Status < 500:
			s.log.Warn("a joined machine refused the dashboard's watch", "machine", m.Name, "status", refused.Status, "code", refused.Body.Code)
		case err != nil:
			s.log.Warn("a joined machine didn't take the dashboard's watch", "machine", m.Name, "err", err)
			continue
		}
		s.watchMu.Lock()
		s.watchSent[m.ID] = want
		s.watchMu.Unlock()
	}
}
