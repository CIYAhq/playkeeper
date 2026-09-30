package panel

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/CIYAhq/playkeeper/internal/invites"
)

const machineWithRelease = `{"hostname":"my-vps","agentVersion":"0.4.8","updateAvailable":"0.4.9","memoryTotalMB":16384}`

// can is what /api/auth/me says the account may do.
func (e *env) can(t *testing.T, m member) []string {
	t.Helper()
	var me struct {
		Access struct {
			Can []string `json:"can"`
		} `json:"access"`
	}
	if code := e.get(t, "/api/auth/me", m.cookie, &me); code != http.StatusOK {
		t.Fatalf("/api/auth/me: %d", code)
	}
	return me.Access.Can
}

// The owner and the admins of every server, who can install a release, hear
// of it: their machine list says which. Creators and customers can't update
// the machine and never hear of one: their list carries no update at all.
func TestOnlyThoseWhoCanUpdateHearOfARelease(t *testing.T) {
	e, own, core := customerEnv(t)
	e.reply("GET", "/v1/machine", machineWithRelease)
	admin := addAdmin(t, e.env, "sam", "*")
	creator := addCreator(t, e.env, "cleo", invites.Allowance{Servers: 1, MemoryMB: 4096})
	ctx := context.Background()
	if _, err := core.StartCustomer(ctx, Customer{Provider: whopProvider, Subject: "user_alex", Handle: "alex"}, starter); err != nil {
		t.Fatal(err)
	}
	info, _, _ := core.CustomerAccount(ctx, whopProvider, "user_alex")
	customer := signIn(t, e.env, info.UserID)

	for name, m := range map[string]member{"the owner": own, "an admin of every server": admin} {
		var list []map[string]any
		e.get(t, "/api/machines", m.cookie, &list)
		if len(list) != 1 {
			t.Fatalf("%s's machine list: %v", name, list)
		}
		if live, _ := list[0]["live"].(map[string]any); live["updateAvailable"] != "0.4.9" {
			t.Errorf("%s isn't told of 0.4.9: %v", name, live)
		}
		if !slices.Contains(e.can(t, m), string(actManageMachine)) {
			t.Errorf("%s may not install it", name)
		}
	}
	for name, m := range map[string]member{"a creator": creator, "a customer": customer} {
		r, body := e.raw(t, "GET", "/api/machines", "", m.auth())
		if r.StatusCode != http.StatusOK || strings.Contains(body, "updateAvailable") || strings.Contains(body, "0.4.9") {
			t.Errorf("%s hears of the release: %d %s", name, r.StatusCode, body)
		}
		if slices.Contains(e.can(t, m), string(actManageMachine)) {
			t.Errorf("%s may manage the machine", name)
		}
		for _, p := range []string{"/update", "/update/check", "/update/auto"} {
			method := map[string]string{"/update": "GET", "/update/check": "POST", "/update/auto": "PUT"}[p]
			if r := e.do(t, method, "/api/machines/"+machineID(t, e.env)+p, `{"on":false}`, m.auth()); r.status != http.StatusForbidden {
				t.Errorf("%s: %s %s answered %d", name, method, p, r.status)
			}
		}
	}
}

// However many tabs are open and however often they poll, the dashboard
// only reads what the machine's last check found: it never asks the machine
// to check.
func TestTabsOnlyReadWhatTheLastCheckFound(t *testing.T) {
	e := newEnv(t)
	own := owner(t, e)
	mid := machineID(t, e)
	e.reply("GET", "/v1/machine", machineWithRelease)
	e.reply("GET", "/v1/servers", `[]`)
	e.reply("GET", "/v1/update", `{"current":"0.4.8","supported":true,"latest":"0.4.9","available":true,"autoCheck":true}`)
	poll := func(p string) {
		req, _ := http.NewRequest("GET", e.ts.URL+p, nil)
		req.Header.Set("Cookie", cookieName+"="+own.cookie)
		resp, err := e.ts.Client().Do(req)
		if err != nil {
			t.Errorf("GET %s: %v", p, err)
			return
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("GET %s: %d", p, resp.StatusCode)
		}
	}
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() {
			for range 5 {
				for _, p := range []string{"/api/machines", "/api/machines/" + mid, "/api/machines/" + mid + "/update", "/api/servers"} {
					poll(p)
				}
			}
		})
	}
	wg.Wait()
	if n := e.hitCount("GET /v1/update"); n != 100 {
		t.Errorf("the tabs read the update %d times, want 100", n)
	}
	for _, key := range []string{"POST /v1/update/check", "PUT /v1/update/auto"} {
		if n := e.hitCount(key); n != 0 {
			t.Errorf("20 tabs polling made the machine %s %d times", key, n)
		}
	}
}

// Check for updates automatically is the owner's and the admins' of every
// server: the switch reaches the machine with who turned it, and nobody
// else's.
func TestTheAutomaticCheckSwitchIsForThoseWhoCanUpdate(t *testing.T) {
	e := newEnv(t)
	own := owner(t, e)
	mid := machineID(t, e)
	e.reply("PUT", "/v1/update/auto", `{"current":"0.4.8","supported":true,"available":false,"autoCheck":false}`)
	r := e.do(t, "PUT", "/api/machines/"+mid+"/update/auto", `{"on":false}`, own.auth())
	if r.status != http.StatusOK || r.body["autoCheck"] != false {
		t.Fatalf("the owner turns it off: %d %v", r.status, r.body)
	}
	var sent map[string]any
	json.Unmarshal([]byte(e.agentBody("PUT /v1/update/auto")), &sent)
	if sent["on"] != false || sent["actor"] != "admin" {
		t.Errorf("the machine got %v", sent)
	}
	for _, role := range []string{invites.RoleModerator, invites.RoleViewer} {
		m := addMember(t, e, role+"1", role, "*")
		if r := e.do(t, "PUT", "/api/machines/"+mid+"/update/auto", `{"on":true}`, m.auth()); r.status != http.StatusForbidden {
			t.Errorf("a %s turns it on: %d %v", role, r.status, r.body)
		}
	}
	if n := e.hitCount("PUT /v1/update/auto"); n != 1 {
		t.Errorf("the machine got the switch %d times, want once", n)
	}
}
