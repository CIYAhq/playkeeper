package machinelink

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

// State is how a machine is doing, as the dashboard sees it.
type State string

const (
	StateConnected State = "connected"
	StateOffline   State = "offline"
	// StateWaiting is a machine that joined but has not connected yet.
	StateWaiting State = "waiting"
	StateRemoved State = "removed"
)

// Problem is something wrong to show the admin, in plain words. Code and
// Params are for the UI's translations; Message and Hint are the English
// text.
type Problem struct {
	Code    string            `json:"code"`
	Params  map[string]string `json:"params,omitempty"`
	Message string            `json:"message"`
	Hint    string            `json:"hint,omitempty"`
}

// Codes of problems in a Status.
const (
	ProblemOffline        = "machine_offline"
	ProblemNeverConnected = "machine_never_connected"
	ProblemSlow           = "link_slow"
	ProblemClockSkew      = "clock_skew"
	ProblemVersion        = "version_mismatch"
	ProblemUnstable       = "link_unstable"
	ProblemCloned         = "machine_cloned"
)

const (
	recentWindow      = 10 * time.Minute
	unstableConnects  = 5
	cloneReplacements = 3
	slowRTT           = time.Second
	maxSkew           = 2 * time.Minute
)

// Status is what the dashboard shows about a machine.
type Status struct {
	MachineID   string    `json:"machineId"`
	Name        string    `json:"name"`
	Fingerprint string    `json:"fingerprint"`
	State       State     `json:"state"`
	ConnectedAt time.Time `json:"connectedAt,omitzero"`
	// LastSeen is the last time the machine answered: its last heartbeat
	// while connected, or when it went away.
	LastSeen time.Time `json:"lastSeen,omitzero"`
	// RTT is the last heartbeat's round trip, or 0 when not known. The
	// JSON has it as rttMs.
	RTT      time.Duration `json:"-"`
	Version  string        `json:"version,omitempty"`
	Address  string        `json:"address,omitempty"`
	Problems []Problem     `json:"problems"`
}

func (s Status) MarshalJSON() ([]byte, error) {
	type plain Status
	var rtt *float64
	if s.RTT > 0 {
		ms := math.Round(float64(s.RTT)/float64(100*time.Microsecond)) / 10
		rtt = &ms
	}
	return json.Marshal(struct {
		plain
		RTTMs *float64 `json:"rttMs,omitempty"`
	}{plain(s), rtt})
}

// machineStats is what the hub remembers about a machine between its
// connections, while the dashboard runs.
type machineStats struct {
	name     string
	lastSeen time.Time
	connects []time.Time
	// clones are connections that replaced a live one from another
	// process: two computers with the same key take turns.
	clones []time.Time
}

// pushRecent appends t and forgets what is older than recentWindow; it
// never keeps more than 32 times.
func pushRecent(list []time.Time, t time.Time) []time.Time {
	list = append(list, t)
	i := 0
	for i < len(list) && t.Sub(list[i]) > recentWindow {
		i++
	}
	if len(list)-i > 32 {
		i = len(list) - 32
	}
	return append(list[:0], list[i:]...)
}

func countRecent(list []time.Time, now time.Time) int {
	n := 0
	for _, t := range list {
		if now.Sub(t) <= recentWindow {
			n++
		}
	}
	return n
}

// Status lists every machine, removed ones included, with what is wrong
// with each.
func (h *Hub) Status(ctx context.Context) ([]Status, error) {
	machines, err := h.store.Machines(ctx)
	if err != nil {
		return nil, err
	}
	now := h.now()
	out := make([]Status, 0, len(machines))
	for _, m := range machines {
		out = append(out, h.statusOf(m, now))
	}
	return out, nil
}

// MachineStatus is the status of one machine.
func (h *Hub) MachineStatus(ctx context.Context, id string) (Status, error) {
	m, found, err := h.store.Machine(ctx, id)
	if err != nil {
		return Status{}, err
	}
	if !found {
		return Status{}, ErrNotFound
	}
	return h.statusOf(m, h.now()), nil
}

func (h *Hub) statusOf(m Machine, now time.Time) Status {
	st := Status{MachineID: m.ID, Name: m.Name, Fingerprint: m.Fingerprint(), LastSeen: m.LastSeen,
		Version: m.Version, Address: m.LastAddr, Problems: []Problem{}}
	h.mu.Lock()
	s := h.sessions[m.ID]
	var stats machineStats
	if p := h.stats[m.ID]; p != nil {
		stats = *p
		stats.connects = append([]time.Time(nil), p.connects...)
		stats.clones = append([]time.Time(nil), p.clones...)
	}
	revoked := h.revoked[m.ID]
	h.mu.Unlock()
	if m.Removed() || revoked {
		st.State = StateRemoved
		return st
	}
	var skew time.Duration
	pinged := false
	if s != nil && !s.isDone() {
		st.State = StateConnected
		st.ConnectedAt = s.connectedAt
		st.Address = s.addr
		s.mu.Lock()
		st.LastSeen, st.RTT, skew, pinged = s.lastSeen, s.rtt, s.skew, s.pinged
		if s.version != "" {
			st.Version = s.version
		}
		s.mu.Unlock()
	} else {
		if stats.lastSeen.After(st.LastSeen) {
			st.LastSeen = stats.lastSeen
		}
		st.State = StateOffline
		if st.LastSeen.IsZero() {
			st.State = StateWaiting
		}
	}
	name := st.Name
	switch st.State {
	case StateOffline:
		if away := now.Sub(st.LastSeen); away >= h.opts.OfflineAfter {
			st.Problems = append(st.Problems, problemOffline(name, st.LastSeen, away))
		}
	case StateWaiting:
		if away := now.Sub(m.JoinedAt); away >= h.opts.OfflineAfter {
			st.Problems = append(st.Problems, problemNeverConnected(name, away))
		}
	case StateConnected:
		if st.RTT >= slowRTT {
			st.Problems = append(st.Problems, problemSlow(name, st.RTT))
		}
		if pinged && (skew >= maxSkew || skew <= -maxSkew) {
			st.Problems = append(st.Problems, problemSkew(name, skew))
		}
	}
	if p, ok := problemVersion(name, h.opts.Version, st.Version); ok {
		st.Problems = append(st.Problems, p)
	}
	if n := countRecent(stats.clones, now); n >= cloneReplacements {
		st.Problems = append(st.Problems, problemCloned(name, n))
	} else if n := countRecent(stats.connects, now); n >= unstableConnects {
		st.Problems = append(st.Problems, problemUnstable(name, n))
	}
	return st
}

func problemOffline(name string, since time.Time, away time.Duration) Problem {
	return Problem{Code: ProblemOffline,
		Params:  map[string]string{"name": name, "since": since.UTC().Format(time.RFC3339), "duration": humanDuration(away), "minutes": strconv.Itoa(int(away / time.Minute))},
		Message: name + " hasn't called in for " + humanDuration(away) + ".",
		Hint:    "Check that it is on and online. On it, sudo playkeeper status shows what its link to the dashboard is doing."}
}

func problemNeverConnected(name string, away time.Duration) Problem {
	return Problem{Code: ProblemNeverConnected,
		Params:  map[string]string{"name": name, "duration": humanDuration(away)},
		Message: name + " joined " + humanDuration(away) + " ago but hasn't connected since.",
		Hint:    "On it, sudo playkeeper status shows why. If it was never set up, remove it here."}
}

func problemSlow(name string, rtt time.Duration) Problem {
	return Problem{Code: ProblemSlow,
		Params:  map[string]string{"name": name, "rttMs": strconv.FormatInt(rtt.Milliseconds(), 10)},
		Message: fmt.Sprintf("The connection to %s is slow: a round trip takes %.1f seconds.", name, rtt.Seconds()),
		Hint:    "Pages about its servers may load slowly. The cause is the network between the two machines."}
}

func problemSkew(name string, skew time.Duration) Problem {
	dir := "ahead of"
	if skew < 0 {
		dir, skew = "behind", -skew
	}
	return Problem{Code: ProblemClockSkew,
		Params:  map[string]string{"name": name, "duration": humanDuration(skew), "seconds": seconds(skew), "direction": strings.Fields(dir)[0]},
		Message: name + "'s clock is " + humanDuration(skew) + " " + dir + " the dashboard's.",
		Hint:    "Times in its logs and backups will look wrong. Turn on automatic time on it: sudo timedatectl set-ntp true"}
}

func problemVersion(name, local, remote string) (Problem, bool) {
	c, ok := compareMinor(remote, local)
	if !ok || c == 0 {
		return Problem{}, false
	}
	p := Problem{Code: ProblemVersion, Params: map[string]string{"name": name, "version": remote, "dashboardVersion": local},
		Message: name + " runs Playkeeper " + remote + ", and the dashboard runs " + local + "."}
	if c < 0 {
		p.Params["older"] = "machine"
		p.Hint = "Update Playkeeper on " + name + ", from its page or with sudo playkeeper self-update on it."
	} else {
		p.Params["older"] = "dashboard"
		p.Hint = "Update Playkeeper on the dashboard's machine."
	}
	return p, true
}

func problemUnstable(name string, n int) Problem {
	return Problem{Code: ProblemUnstable, Params: map[string]string{"name": name, "count": strconv.Itoa(n)},
		Message: fmt.Sprintf("The connection to %s keeps dropping: it connected %d times in the last 10 minutes.", name, n),
		Hint:    "Check that machine's network. Wi-Fi and mobile connections often cause this."}
}

func problemCloned(name string, n int) Problem {
	return Problem{Code: ProblemCloned, Params: map[string]string{"name": name, "count": strconv.Itoa(n)},
		Message: "Two computers seem to be taking turns connecting as " + name + ".",
		Hint:    "This happens when a machine's disk is copied, for example from a snapshot. Remove " + name + " here, then connect each computer with its own command."}
}

// compareMinor compares the major and minor numbers of two versions such
// as v0.3.1 or 0.3.1-rc1: -1 when a is older, 1 when newer, 0 when the
// same. ok is false if either can't be read (a development build).
func compareMinor(a, b string) (int, bool) {
	x, ok1 := minorOf(a)
	y, ok2 := minorOf(b)
	if !ok1 || !ok2 {
		return 0, false
	}
	for i := range x {
		if x[i] != y[i] {
			if x[i] < y[i] {
				return -1, true
			}
			return 1, true
		}
	}
	return 0, true
}

func minorOf(v string) ([2]int, bool) {
	parts := strings.SplitN(strings.TrimPrefix(v, "v"), ".", 3)
	var out [2]int
	if len(parts) < 2 {
		return out, false
	}
	for i := range out {
		n, err := strconv.Atoi(parts[i])
		if err != nil || n < 0 || strings.Trim(parts[i], "0123456789") != "" {
			return out, false
		}
		out[i] = n
	}
	return out, true
}

// EventKind is what happened to a machine.
type EventKind string

const (
	EventJoined       EventKind = "machine.joined"
	EventJoinRefused  EventKind = "machine.join_refused"
	EventConnected    EventKind = "machine.connected"
	EventDisconnected EventKind = "machine.disconnected"
	EventRemoved      EventKind = "machine.removed"
	EventLeft         EventKind = "machine.left"
)

// Event is something that happened to a machine, for the audit log. It
// never holds a join code or a key.
type Event struct {
	Kind      EventKind `json:"kind"`
	At        time.Time `json:"at"`
	MachineID string    `json:"machineId,omitempty"`
	Name      string    `json:"name,omitempty"`
	// Actor is the account behind it: who made the join code for a join,
	// who removed the machine, or machine:<id> when it left by itself.
	Actor   string `json:"actor,omitempty"`
	Address string `json:"address,omitempty"`
	// Code is the error code of a refused join or a disconnection.
	Code string `json:"code,omitempty"`
}
