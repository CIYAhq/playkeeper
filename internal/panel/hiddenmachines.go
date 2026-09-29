package panel

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/CIYAhq/playkeeper/internal/agentclient"
	"github.com/CIYAhq/playkeeper/internal/api"
	"github.com/CIYAhq/playkeeper/internal/machinelink"
)

// Creators and customers never see the machines (the fleet plan): not which
// machine their servers run on, how many there are, or their names,
// addresses or sizes (see access.hidesMachines). They see their servers and
// their plan. Their machine list has only what their pages need to reach
// those servers, with no name, details or events; a path naming any other
// machine is not found; their catalog's memory is their plan's; and a
// machine that can't be reached is their server in the dashboard's
// refusals.

// blindWriter answers an account that doesn't see the machines, so the
// dashboard's refusals name none (see agentFailure).
type blindWriter struct{ http.ResponseWriter }

func (w *blindWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (w *blindWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// blind reports whether w answers an account that doesn't see the machines.
func blind(w http.ResponseWriter) bool {
	for {
		switch x := w.(type) {
		case *blindWriter:
			return true
		case interface{ Unwrap() http.ResponseWriter }:
			w = x.Unwrap()
		default:
			return false
		}
	}
}

// blindFailure is body, the answer to a request to a machine that failed,
// for an account that doesn't see the machines. A machine's own refusal is
// about the server and stays as it is. Anything else would name a machine,
// say there are several, or tell how to fix one: a link that's down, a
// removed machine, two machines that list the same server, an agent that
// doesn't answer. Those become the same few words, with their code kept for
// the dashboard.
func blindFailure(err error, body api.Error) api.Error {
	var ae *agentclient.Error
	if errors.As(err, &ae) {
		return body
	}
	return api.Error{Error: unreachableText, Code: body.Code, Hint: "It's probably still running. Try again in a few minutes."}
}

const unreachableText = "Your server can't be reached right now."

// shownMachines are the machines a, who doesn't see the machines, may still
// reach, by id: the one their servers go on, and each that runs a server of
// theirs.
func (s *Server) shownMachines(ctx context.Context, a access) map[string]bool {
	out := map[string]bool{}
	if home, ok, err := s.homeMachine(ctx, a.UserID); err == nil && ok {
		out[home] = true
	}
	for _, id := range a.Servers.Servers {
		if m, err := s.machineForServer(id); err == nil {
			out[m.ID] = true
		}
	}
	return out
}

// machineShown reports whether a, who doesn't see the machines, may reach
// machine id (see shownMachines).
func (s *Server) machineShown(ctx context.Context, a access, id string) bool {
	return s.shownMachines(ctx, a)[id]
}

// hiddenMachine is a machine in the list of an account that doesn't see
// the machines. It has the fields of the dashboard's MachineView that its
// pages read, and no name.
type hiddenMachine struct {
	ID        string      `json:"id"`
	ProjectID string      `json:"projectId"`
	Name      string      `json:"name"`
	Kind      string      `json:"kind"`
	Live      *hiddenLive `json:"live,omitempty"`
	Error     *api.Error  `json:"error,omitempty"`
	Link      *hiddenLink `json:"link,omitempty"`
}

// hiddenLink is a joined machine's link as an account that doesn't see the
// machines needs it: whether it's connected, since when it's away, and the
// address its servers join at when they have no name.
type hiddenLink struct {
	MachineID string                `json:"machineId"`
	State     machinelink.State     `json:"state"`
	LastSeen  time.Time             `json:"lastSeen,omitzero"`
	Address   string                `json:"address,omitempty"`
	Problems  []machinelink.Problem `json:"problems"`
}

// hiddenLive is what an account that doesn't see the machines learns about
// the dashboard: the Playkeeper it runs and the update it installs, so its
// pages wait out an update and then load the new version. Every machine in
// its list that answers says so, whichever machine it is.
type hiddenLive struct {
	AgentVersion     string         `json:"agentVersion"`
	UpdateInstalling string         `json:"updateInstalling,omitempty"`
	Operation        *api.Operation `json:"operation,omitempty"`
}

// hiddenMachines is the machine list of a, who doesn't see the machines:
// those in shownMachines, and no other, not even the dashboard's own. Each
// says only whether its servers can be reached, the Playkeeper the
// dashboard runs, and a joined machine the IP its servers' join addresses
// carry anyway.
func (s *Server) hiddenMachines(ctx context.Context, a access, list []machine, links map[string]*machinelink.Status) []hiddenMachine {
	shown := s.shownMachines(ctx, a)
	var keep []machine
	for _, m := range list {
		if m.Kind == localKind || shown[m.ID] {
			keep = append(keep, m)
		}
	}
	views := make([]hiddenMachine, len(keep))
	var wg sync.WaitGroup
	for i, m := range keep {
		wg.Go(func() { views[i] = hiddenView(ctx, m, links[m.ID]) })
	}
	wg.Wait()
	var dashboard *hiddenLive
	for _, v := range views {
		if v.Kind == localKind {
			dashboard = v.Live
		}
	}
	out := []hiddenMachine{}
	for _, v := range views {
		if !shown[v.ID] {
			continue
		}
		if v.Error == nil {
			v.Live = dashboard
		}
		out = append(out, v)
	}
	return out
}

// planFigures puts creator or customer a's plan where a status of one of
// their servers, sv, shows the machine's: how much more memory a crashed
// server could get, and the disk their servers have free and in all.
// Those are what they may use (see capCatalog and disklimits.go); the
// machine's are none of theirs to see. usedMB is the memory their own
// servers take.
func (s *Server) planFigures(a access, sv map[string]any, usedMB int) {
	room := max(a.Allowance.MemoryMB-usedMB, 0)
	var memoryMB float64
	if cfg, ok := sv["config"].(map[string]any); ok {
		memoryMB, _ = cfg["memoryMB"].(float64)
	}
	for _, key := range []string{"crash", "recoveredCrash"} {
		c, ok := sv[key].(map[string]any)
		if !ok {
			continue
		}
		if mb, ok := c["roomMB"].(float64); ok && int(mb) > room {
			c["roomMB"] = room
		}
		// A raise past what the plan has left is none they can take.
		if fixes, ok := c["fixes"].([]any); ok {
			kept := []any{}
			for _, f := range fixes {
				fix, _ := f.(map[string]any)
				params, _ := fix["params"].(map[string]any)
				if to, ok := params["to_mb"].(float64); ok && fix["kind"] == "raise_memory" && int(to-memoryMB) > room {
					continue
				}
				kept = append(kept, f)
			}
			c["fixes"] = kept
		}
	}
	if r, ok := sv["resources"].(map[string]any); ok {
		if machineFree, ok := r["diskFreeBytes"].(float64); ok {
			r["diskFreeBytes"], r["diskTotalBytes"] = s.planDisk(a, int64(machineFree))
		}
	}
}

// planDisk is the disk creator or customer a's servers have free and in
// all: their plan's, within what the machine has free.
func (s *Server) planDisk(a access, machineFree int64) (free, total int64) {
	total = a.Allowance.DiskBytes()
	free = total
	if used := s.diskUsed(a.UserID); used != nil {
		free = max(total-*used, 0)
	}
	return min(free, machineFree), total
}

// ownMemory is the memory creator a's own servers take, of the statuses
// servers.
func (s *Server) ownMemory(a access, servers []map[string]any) int {
	owned, _ := s.creatorServers(a.UserID)
	used := 0
	for _, sv := range servers {
		if id, _ := sv["id"].(string); slices.Contains(owned, id) {
			if cfg, ok := sv["config"].(map[string]any); ok {
				mb, _ := cfg["memoryMB"].(float64)
				used += int(mb)
			}
		}
	}
	return used
}

// hServer is one server's status as its machine says, with a creator's or
// customer's plan's figures (see planFigures).
func (s *Server) hServer(w http.ResponseWriter, r *http.Request, sess *session) {
	if !sess.Access.hidesMachines() {
		s.serverProxy(http.MethodGet, "/v1/servers/{id}")(w, r, sess)
		return
	}
	m, ok := s.target(w, r)
	if !ok {
		return
	}
	var sv map[string]any
	if _, err := m.agent.Do(asActor(r.Context(), sess.User.Username), http.MethodGet, agentPath("/v1/servers/{id}", r), nil, nil, &sv); err != nil {
		s.agentFailure(w, err)
		return
	}
	use, err := s.allowanceUse(r.Context(), sess.Access, m, "")
	if err != nil {
		s.listFailure(w, err)
		return
	}
	s.planFigures(sess.Access, sv, use.memoryMB)
	writeJSON(w, http.StatusOK, sv)
}

func hiddenView(ctx context.Context, m machine, link *machinelink.Status) hiddenMachine {
	v := hiddenMachine{ID: m.ID, ProjectID: m.ProjectID, Kind: m.Kind}
	if link != nil {
		v.Link = &hiddenLink{MachineID: m.ID, State: link.State, LastSeen: link.LastSeen, Address: link.Address, Problems: []machinelink.Problem{}}
	}
	ctx, cancel := context.WithTimeout(ctx, machineTimeout)
	defer cancel()
	var live api.Machine
	if _, err := m.agent.Do(ctx, http.MethodGet, "/v1/machine", nil, nil, &live); err != nil {
		_, body := failureOf(err)
		failed := blindFailure(err, body)
		v.Error = &failed
		return v
	}
	if m.Kind == localKind {
		v.Live = &hiddenLive{AgentVersion: live.AgentVersion, UpdateInstalling: live.UpdateInstalling}
		if op := live.Operation; op != nil && op.Kind == "update" {
			v.Live.Operation = &api.Operation{ID: op.ID, Kind: op.Kind, Status: op.Status, Phase: op.Phase, StartedAt: op.StartedAt}
		}
	}
	return v
}
