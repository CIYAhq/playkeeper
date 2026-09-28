package panel

import (
	"encoding/json"
	"io"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/CIYAhq/playkeeper/internal/invites"
)

// usageAgent answers the usage stats routes of one machine, keeping what
// the switch set.
type usageAgent struct {
	mu        sync.Mutex
	on        bool
	canChange bool
	puts      []map[string]any
}

func (u *usageAgent) state() string {
	u.mu.Lock()
	defer u.mu.Unlock()
	reason := "default"
	if len(u.puts) > 0 {
		reason = "settings"
	}
	if !u.canChange {
		reason = "install"
	}
	b, _ := json.Marshal(map[string]any{"on": u.on, "reason": reason, "canChange": u.canChange, "service": "https://stats.playkeeper.io",
		"report": map[string]any{"id": "00112233445566778899aabbccddeeff", "version": "0.4.4", "os": "ubuntu", "osVersion": "24.04",
			"arch": "amd64", "source": "playkeeper.io", "kind": "dashboard", "address": "ip", "servers": 1, "running": 1}})
	return string(b)
}

func (u *usageAgent) get(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	io.WriteString(w, u.state())
}

func (u *usageAgent) put(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	json.NewDecoder(r.Body).Decode(&body)
	u.apply(w, body)
}

// apply is put with the body read already, as the dashboard's fake agent
// reads it before it answers.
func (u *usageAgent) apply(w http.ResponseWriter, body map[string]any) {
	u.mu.Lock()
	u.puts = append(u.puts, body)
	allowed := u.canChange
	if allowed {
		u.on = body["on"] == true
	}
	u.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if !allowed {
		w.WriteHeader(http.StatusConflict)
		io.WriteString(w, `{"error":"Usage stats were turned off when Playkeeper was installed, which the switch can't change.","code":"conflict"}`)
		return
	}
	io.WriteString(w, u.state())
}

func (u *usageAgent) seen() []map[string]any {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]map[string]any(nil), u.puts...)
}

func (e *env) usageLocally(u *usageAgent) {
	e.agent.mu.Lock()
	e.agent.answers["GET /v1/usage-stats"] = u.get
	e.agent.answers["PUT /v1/usage-stats"] = func(w http.ResponseWriter, r *http.Request) {
		e.agent.mu.Lock()
		raw := e.agent.lastBody["PUT /v1/usage-stats"]
		e.agent.mu.Unlock()
		var body map[string]any
		json.Unmarshal([]byte(raw), &body)
		u.apply(w, body)
	}
	e.agent.mu.Unlock()
}

func (u *usageAgent) serveOn(ra *remoteAgent) {
	ra.handle("GET /v1/usage-stats", u.get)
	ra.handle("PUT /v1/usage-stats", u.put)
}

// One switch sets usage stats on the dashboard's machine and on each joined
// machine, as the signed-in account; a machine where root chose keeps its
// choice and says so.
func TestTheSwitchSetsUsageStatsOnEveryMachine(t *testing.T) {
	e := newEnvConfig(t, withDomain, nil)
	cookie, csrf := e.setup(t)
	local := &usageAgent{on: true, canChange: true}
	e.usageLocally(local)
	joined, stuck := &usageAgent{on: true, canChange: true}, &usageAgent{on: false}
	ra, rb := newRemoteAgent(), newRemoteAgent()
	joined.serveOn(ra)
	stuck.serveOn(rb)
	mid, _ := e.joined(t, cookie, csrf, ra)
	e.joined(t, cookie, csrf, rb)

	r := e.do(t, "PUT", "/api/usage-stats", `{"on":false}`, auth(cookie, csrf))
	if r.status != http.StatusOK || r.body["on"] != false {
		t.Fatalf("the switch: %d %v", r.status, r.body)
	}
	if p := local.seen(); len(p) != 1 || p[0]["on"] != false || p[0]["actor"] != "admin" {
		t.Errorf("the dashboard's machine got %v", p)
	}
	if p := joined.seen(); len(p) != 1 || p[0]["on"] != false || p[0]["actor"] != "admin" {
		t.Errorf("the joined machine got %v", p)
	}
	if actor, _ := ra.saw("PUT /v1/usage-stats"); actor != "admin" {
		t.Errorf("the joined machine's link carried the actor %q", actor)
	}
	machines, _ := r.body["machines"].([]any)
	if len(machines) != 2 {
		t.Fatalf("the answer lists %d joined machines: %v", len(machines), r.body)
	}
	for _, m := range machines {
		m := m.(map[string]any)
		stats, _ := m["stats"].(map[string]any)
		if stats == nil || stats["on"] != false {
			t.Errorf("a joined machine after the switch: %v", m)
		}
		if m["id"] == mid && stats["reason"] != "settings" {
			t.Errorf("the joined machine that took the switch: %v", m)
		}
	}

	// A machine where root chose off keeps it when the switch turns them on.
	r = e.do(t, "PUT", "/api/usage-stats", `{"on":true}`, auth(cookie, csrf))
	if r.status != http.StatusOK || r.body["on"] != true {
		t.Fatalf("turning them on: %d %v", r.status, r.body)
	}
	for _, m := range r.body["machines"].([]any) {
		m := m.(map[string]any)
		stats := m["stats"].(map[string]any)
		if m["id"] == mid && stats["on"] != true || m["id"] != mid && (stats["on"] != false || stats["canChange"] != false) {
			t.Errorf("after turning them on: %v", m)
		}
	}

	for _, body := range []string{`{}`, `{"on":"yes"}`, `{"on":true,"service":"https://elsewhere.example"}`, `{"on":true}{"on":false}`} {
		if r := e.do(t, "PUT", "/api/usage-stats", body, auth(cookie, csrf)); r.status != http.StatusBadRequest {
			t.Errorf("%s: %d %v", body, r.status, r.body)
		}
	}
	if n := len(local.seen()); n != 2 {
		t.Errorf("the dashboard's machine got %d switches, want 2", n)
	}
}

// Viewers see them; only those who manage the machine change them.
func TestOnlyThoseWhoManageTheMachineUseTheSwitch(t *testing.T) {
	e := newEnvConfig(t, withDomain, nil)
	cookie, _ := e.setup(t)
	local := &usageAgent{on: true, canChange: true}
	e.usageLocally(local)
	for _, m := range []member{addMember(t, e, "friend", invites.RoleModerator, "*"), addMember(t, e, "watcher", invites.RoleViewer, "*")} {
		if r := e.do(t, "GET", "/api/usage-stats", "", m.auth()); r.status != http.StatusOK || r.body["on"] != true {
			t.Errorf("GET: %d %v", r.status, r.body)
		}
		if r := e.do(t, "PUT", "/api/usage-stats", `{"on":false}`, m.auth()); r.status != http.StatusForbidden {
			t.Errorf("PUT: %d %v", r.status, r.body)
		}
	}
	if r := e.do(t, "PUT", "/api/usage-stats", `{"on":false}`, auth(cookie, "")); r.status != http.StatusForbidden {
		t.Errorf("without the CSRF token: %d %v", r.status, r.body)
	}
	if p := local.seen(); len(p) != 0 {
		t.Errorf("a refused switch reached the agent: %v", p)
	}
}

// A machine that was away when usage stats were turned off gets them off
// when it connects; one that connects while they're on keeps its own.
func TestAMachineThatWasAwayIsTurnedOffWhenItConnects(t *testing.T) {
	e := newEnvConfig(t, withDomain, nil)
	cookie, csrf := e.setup(t)
	local := &usageAgent{on: true, canChange: true}
	e.usageLocally(local)
	early := &usageAgent{on: true, canChange: true}
	ra := newRemoteAgent()
	early.serveOn(ra)
	e.joined(t, cookie, csrf, ra)
	if r := e.do(t, "PUT", "/api/usage-stats", `{"on":false}`, auth(cookie, csrf)); r.status != http.StatusOK {
		t.Fatalf("%d %v", r.status, r.body)
	}
	late := &usageAgent{on: true, canChange: true}
	rb := newRemoteAgent()
	late.serveOn(rb)
	e.joined(t, cookie, csrf, rb)
	eventually(t, "the machine that connected is turned off", func() bool {
		p := late.seen()
		return len(p) == 1 && p[0]["on"] == false && p[0]["actor"] == "playkeeper"
	})

	e2 := newEnvConfig(t, withDomain, nil)
	cookie2, csrf2 := e2.setup(t)
	e2.usageLocally(&usageAgent{on: true, canChange: true})
	keeps := &usageAgent{on: true, canChange: true}
	rc := newRemoteAgent()
	keeps.serveOn(rc)
	e2.joined(t, cookie2, csrf2, rc)
	if r := e2.do(t, "GET", "/api/usage-stats", "", auth(cookie2, "")); r.status != http.StatusOK {
		t.Fatalf("%d %v", r.status, r.body)
	}
	time.Sleep(200 * time.Millisecond)
	if p := keeps.seen(); len(p) != 0 {
		t.Errorf("a machine that connected while usage stats were on was changed: %v", p)
	}
}
