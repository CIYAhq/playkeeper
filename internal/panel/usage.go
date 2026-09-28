package panel

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"sync"

	"github.com/CIYAhq/playkeeper/internal/api"
)

// Anonymous usage stats (internal/usage): each machine's agent sends its
// own heartbeat, and the switch in Settings sets them on every machine of
// the dashboard. The panel only ever passes on or off: where they go and
// what they say is up to each agent and its config.json.

// usageMachine is a joined machine's usage stats, or why they can't be read.
type usageMachine struct {
	ID    string          `json:"id"`
	Name  string          `json:"name"`
	Stats *api.UsageStats `json:"stats,omitempty"`
	Error *api.Error      `json:"error,omitempty"`
}

// usageView is the dashboard machine's usage stats, with each joined
// machine's.
type usageView struct {
	api.UsageStats
	Machines []usageMachine `json:"machines"`
}

// usageView reads every machine's usage stats; an error is the dashboard
// machine's own.
func (s *Server) usageView(ctx context.Context) (usageView, error) {
	var v usageView
	list, err := s.machines()
	if err != nil {
		return v, err
	}
	v.Machines = []usageMachine{}
	var wg sync.WaitGroup
	var mu sync.Mutex
	var localErr error
	for _, m := range list {
		wg.Go(func() {
			ctx, cancel := context.WithTimeout(ctx, machineTimeout)
			defer cancel()
			var st api.UsageStats
			_, err := m.agent.Do(ctx, "GET", "/v1/usage-stats", nil, nil, &st)
			mu.Lock()
			defer mu.Unlock()
			if m.Kind == localKind {
				v.UsageStats, localErr = st, err
				return
			}
			um := usageMachine{ID: m.ID, Name: m.Name}
			if err != nil {
				um.Error = agentErrorBody(err)
			} else {
				um.Stats = &st
			}
			v.Machines = append(v.Machines, um)
		})
	}
	wg.Wait()
	return v, localErr
}

func (s *Server) hUsageStats(w http.ResponseWriter, r *http.Request, _ *session) {
	v, err := s.usageView(r.Context())
	if err != nil {
		s.agentFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

// hUsageStatsSet turns usage stats on or off on the dashboard's machine,
// then on each joined machine that is connected. A machine where root chose
// keeps its choice, and says so in the answer.
func (s *Server) hUsageStatsSet(w http.ResponseWriter, r *http.Request, sess *session) {
	var req struct {
		On *bool `json:"on"`
	}
	dec := json.NewDecoder(io.LimitReader(r.Body, 4<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil || req.On == nil || dec.More() {
		writeErr(w, http.StatusBadRequest, api.CodeInvalid, "Send {\"on\": true} or {\"on\": false}.", "")
		return
	}
	list, err := s.machines()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, api.CodeInternal, "Database error.", "")
		return
	}
	ctx := asActor(r.Context(), sess.User.Username)
	body := map[string]any{"on": *req.On, "actor": sess.User.Username}
	for _, m := range list {
		if m.Kind != localKind {
			continue
		}
		if _, err := m.agent.Do(ctx, "PUT", "/v1/usage-stats", nil, body, nil); err != nil {
			s.agentFailure(w, err)
			return
		}
	}
	var wg sync.WaitGroup
	for _, m := range list {
		if m.Kind != remoteKind || s.hub == nil || !s.hub.Connected(m.ID) {
			continue
		}
		wg.Go(func() {
			mctx, cancel := context.WithTimeout(ctx, machineTimeout)
			defer cancel()
			_, _ = m.agent.Do(mctx, "PUT", "/v1/usage-stats", nil, body, nil)
		})
	}
	wg.Wait()
	s.hUsageStats(w, r, sess)
}

// carryUsageOff turns usage stats off on a joined machine that connects
// while the switch has them off on the dashboard's machine, as it may have
// been away when the switch turned them off. An off the dashboard's machine
// has for itself (its environment, its install) stays its own, and an on is
// never carried over by itself.
func (s *Server) carryUsageOff(machineID string) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*machineTimeout)
	defer cancel()
	list, err := s.machines()
	if err != nil {
		return
	}
	var here api.UsageStats
	for _, m := range list {
		if m.Kind == localKind {
			if _, err := m.agent.Do(ctx, "GET", "/v1/usage-stats", nil, nil, &here); err != nil {
				return
			}
		}
	}
	if here.On || here.Reason != api.UsageSettings {
		return
	}
	for _, m := range list {
		if m.ID != machineID || m.Kind != remoteKind {
			continue
		}
		var there api.UsageStats
		if _, err := m.agent.Do(ctx, "GET", "/v1/usage-stats", nil, nil, &there); err != nil || !there.On || !there.CanChange {
			return
		}
		if _, err := m.agent.Do(asActor(ctx, "playkeeper"), "PUT", "/v1/usage-stats", nil, map[string]any{"on": false, "actor": "playkeeper"}, nil); err != nil {
			s.log.Warn("could not turn usage stats off on a joined machine", "machine", machineID, "err", err)
		}
	}
}
