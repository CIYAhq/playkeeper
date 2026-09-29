package panel

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/CIYAhq/playkeeper/internal/api"
	"github.com/CIYAhq/playkeeper/internal/invites"
)

// diskLimitsAgent keeps the disk limits the dashboard sends the machine, and
// answers that each limit's servers take used.
type diskLimitsAgent struct {
	mu   sync.Mutex
	sent []api.DiskLimitsRequest
}

func newDiskLimitsAgent(e *env, used int64) *diskLimitsAgent {
	d := &diskLimitsAgent{}
	e.agent.mu.Lock()
	defer e.agent.mu.Unlock()
	e.agent.answers["PUT /v1/disk-limits"] = func(w http.ResponseWriter, _ *http.Request) {
		e.agent.mu.Lock()
		raw := e.agent.lastBody["PUT /v1/disk-limits"]
		e.agent.mu.Unlock()
		var req api.DiskLimitsRequest
		json.Unmarshal([]byte(raw), &req)
		d.mu.Lock()
		d.sent = append(d.sent, req)
		d.mu.Unlock()
		json.NewEncoder(w).Encode(req.Limits)
	}
	e.agent.answers["GET /v1/disk-limits"] = func(w http.ResponseWriter, _ *http.Request) {
		limits := slices.Clone(d.last().Limits)
		for i := range limits {
			limits[i].UsedBytes = used
		}
		json.NewEncoder(w).Encode(limits)
	}
	return d
}

func (d *diskLimitsAgent) last() api.DiskLimitsRequest {
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.sent) == 0 {
		return api.DiskLimitsRequest{}
	}
	return d.sent[len(d.sent)-1]
}

// kicked says whether something asked for the limits to be sent now, and
// takes the ask.
func kicked(e *env) bool {
	select {
	case <-e.srv.diskKick:
		return true
	default:
		return false
	}
}

// The disk a creator invite sets goes with it to the account it makes, and
// on into the limit its servers get; one that sets none gets the default.
func TestACreatorInvitesDiskGoesWithIt(t *testing.T) {
	e := newJoinEnv(t)
	own := owner(t, e.env)
	r := e.do(t, "POST", "/api/team/invites", `{"role":"admin","servers":{},"label":"alex","allowance":{"servers":1,"memoryMB":4096,"diskGB":20}}`, own.auth())
	code, _ := strings.CutPrefix(r.body["path"].(string), invites.JoinPath+"/")
	if r.status != http.StatusCreated || !invites.WellFormed(code) {
		t.Fatalf("a creator invite with its disk: %d %v", r.status, r.body)
	}
	var team teamBody
	e.get(t, "/api/team", own.cookie, &team)
	if len(team.Invites) != 1 || team.Invites[0].Allowance != (invites.Allowance{Servers: 1, MemoryMB: 4096, DiskGB: 20}) {
		t.Fatalf("the invite on the Team page: %+v", team.Invites)
	}
	if r := e.public(t, "accept", codeBody(code, "username", "alex", "password", "member password 1")); r.status != http.StatusOK {
		t.Fatalf("accept: %d %v", r.status, r.body)
	}
	var u user
	if err := e.srv.db.QueryRow(`SELECT id, username, role FROM users WHERE username = 'alex'`).Scan(&u.ID, &u.Username, &u.Role); err != nil {
		t.Fatal(err)
	}
	if a, err := e.srv.access(u); err != nil || a.Allowance != (invites.Allowance{Servers: 1, MemoryMB: 4096, DiskGB: 20}) {
		t.Fatalf("alex's allowance: %+v, %v", a.Allowance, err)
	}
	all, err := e.srv.diskAllowances(context.Background())
	if err != nil || all[u.ID].DiskBytes() != 20<<30 {
		t.Fatalf("alex's disk: %+v, %v", all, err)
	}
}

// The machine gets a disk limit for each creator's servers: their
// allowance's disk, the default from their memory or what the allowance
// sets, while the owner's servers get none. Creating a server sends the
// limits at once, what the servers take shows on the Team page, and a
// removed creator's limit goes.
func TestEachCreatorsServersGetTheirAllowancesDisk(t *testing.T) {
	e := newJoinEnv(t)
	own := owner(t, e.env)
	newCreatorAgent(e.env, "cafebabe23", "deadbeef45")
	disk := newDiskLimitsAgent(e.env, 7<<30)
	alex := addCreator(t, e.env, "alex", invites.Allowance{Servers: 1, MemoryMB: 4096})
	sam := addCreator(t, e.env, "sam", invites.Allowance{Servers: 1, MemoryMB: 2048})
	if _, err := e.srv.db.Exec(`UPDATE project_members SET allowance_disk_gb = 50 WHERE user_id = ?`, sam.id); err != nil {
		t.Fatal(err)
	}
	mid := machineID(t, e.env)
	kicked(e.env)
	for _, c := range []struct {
		who  member
		body string
	}{{alex, `{"name":"alex","acceptEula":true,"memoryMB":4096}`}, {sam, `{"name":"sam","acceptEula":true,"memoryMB":2048}`}} {
		if r := e.do(t, "POST", "/api/machines/"+mid+"/servers", c.body, c.who.auth()); r.status != http.StatusOK {
			t.Fatalf("creating %s: %d %v", c.body, r.status, r.body)
		}
		if !kicked(e.env) {
			t.Fatal("a creator's new server didn't send the limits at once")
		}
	}

	e.srv.syncDiskLimits(context.Background())
	id := func(m member) string { return diskLimitPrefix + strconv.FormatInt(m.id, 10) }
	want := []api.DiskLimit{{ID: id(alex), LimitBytes: 30 << 30, Servers: []string{"cafebabe23"}}, {ID: id(sam), LimitBytes: 50 << 30, Servers: []string{"deadbeef45"}}}
	sent := disk.last()
	got := slices.Clone(sent.Limits)
	slices.SortFunc(got, func(a, b api.DiskLimit) int { return int(a.LimitBytes>>30 - b.LimitBytes>>30) })
	if sent.Actor != placementActor || len(got) != 2 || got[0].ID != want[0].ID || got[0].LimitBytes != want[0].LimitBytes || !slices.Equal(got[0].Servers, want[0].Servers) ||
		got[1].ID != want[1].ID || got[1].LimitBytes != want[1].LimitBytes || !slices.Equal(got[1].Servers, want[1].Servers) {
		t.Fatalf("the limits sent: %+v", sent)
	}

	var team struct {
		Members []struct {
			Username      string `json:"username"`
			DiskUsedBytes *int64 `json:"diskUsedBytes"`
		} `json:"members"`
	}
	e.get(t, "/api/team", own.cookie, &team)
	for _, m := range team.Members {
		creator := m.Username == "alex" || m.Username == "sam"
		if creator != (m.DiskUsedBytes != nil) || creator && *m.DiskUsedBytes != 7<<30 {
			t.Fatalf("%s's disk use on the Team page: %v", m.Username, m.DiskUsedBytes)
		}
	}

	if r := e.do(t, "DELETE", sam.path(), "", own.auth()); r.status != http.StatusNoContent {
		t.Fatalf("removing sam: %d %v", r.status, r.body)
	}
	if !kicked(e.env) {
		t.Fatal("removing a creator didn't send the limits at once")
	}
	e.srv.syncDiskLimits(context.Background())
	if sent := disk.last(); len(sent.Limits) != 1 || sent.Limits[0].ID != id(alex) {
		t.Fatalf("the limits once sam was removed: %+v", sent)
	}
}
