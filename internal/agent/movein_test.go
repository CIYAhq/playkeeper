package agent

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/CIYAhq/playkeeper/internal/api"
)

// moveInBody is the move-in the dashboard sends for a server it moves here
// as id: what the server had where it was, sc.
func moveInBody(id, slug string, sc *api.ServerConfig, start bool) map[string]any {
	return map[string]any{"serverId": id, "name": "Survival", "slug": slug, "memoryMB": sc.MemoryMB, "playStyle": sc.PlayStyle, "start": start,
		"createdAt": sc.CreatedAt, "eulaAcceptedAt": sc.EULAAcceptedAt, "eulaAcceptedBy": sc.EULAAcceptedBy, "creatorNames": sc.CreatorNames, "actor": "playkeeper"}
}

// movingIn makes Survival, a server with a world, and stages a backup of it
// as the dashboard stages one it moves here: an upload for a new server
// against the account's disk limit. It returns what Survival has and the
// upload's id.
func (e *agentEnv) movingIn(style string) (*api.ServerConfig, serverRow, []byte, string) {
	e.t.Helper()
	e.createWith(map[string]any{"name": "Survival", "playStyle": style, "operators": []string{"Steve_Builds"}})
	had, err := e.srv().serverConfig()
	if err != nil {
		e.t.Fatal(err)
	}
	row, err := e.srv().row()
	if err != nil {
		e.t.Fatal(err)
	}
	_, raw := e.compressibleBackup()
	e.setLimits(map[string]any{"id": "account-7", "limitBytes": 1 << 30, "servers": []string{}})
	return had, row, raw, e.stageMoveIn(raw)
}

// stageMoveIn uploads the backup raw for a new server against account-7 and
// returns the upload's id.
func (e *agentEnv) stageMoveIn(raw []byte) string {
	e.t.Helper()
	code, out := e.uploadTo("/v1/restore/upload?diskLimit=account-7", raw)
	if code != 200 {
		e.t.Fatalf("staging the backup: %d %v", code, out)
	}
	return out["id"].(string)
}

func (e *agentEnv) moveIn(rid string, body map[string]any) (int, map[string]any) {
	e.t.Helper()
	return e.callWhenFree("POST", "/v1/restore/"+rid+"/move-in", body)
}

// containerRuns reports whether server id's container runs.
func (e *agentEnv) containerRuns(id string) bool {
	e.fd.mu.Lock()
	defer e.fd.mu.Unlock()
	c := e.fd.byName[containerPrefix+id]
	return c != nil && c.running
}

// A server moved in from another machine keeps the id, name and slug it had
// there, its memory, creation time and play style, and the Minecraft EULA
// acceptance of whoever accepted it there, with its backup's world. It
// counts against the disk limit its upload named, and one that didn't run
// there stays stopped: nothing starts it to check its world. An id a server
// here has is refused, and so is one whose files are still here.
func TestAServerMovedInKeepsWhatItHadWhereItWas(t *testing.T) {
	e := newAgentEnv(t)
	had, _, _, rid := e.movingIn("friends")
	id := e.sid
	body := moveInBody(id, "our-survival", had, false)
	if code, out := e.moveIn(rid, body); code != http.StatusConflict {
		t.Fatalf("moving in a server whose id a server here has: %d %v", code, out)
	}
	code, out := e.callWhenFree("POST", e.sp("/delete"), map[string]any{"confirm": "Survival", "actor": "admin"})
	if code != http.StatusAccepted {
		t.Fatalf("delete: %d %v", code, out)
	}
	if op := e.waitOp(out["id"].(string)); op.Status != api.OpSucceeded {
		t.Fatalf("delete: %+v", op)
	}
	left := filepath.Join(e.cfg.DataDir, "servers", id)
	if err := os.MkdirAll(filepath.Join(left, "data"), 0o755); err != nil {
		t.Fatal(err)
	}
	if code, out := e.moveIn(rid, body); code != http.StatusConflict {
		t.Fatalf("moving in a server whose files are still here: %d %v", code, out)
	}
	if err := os.RemoveAll(left); err != nil {
		t.Fatal(err)
	}
	code, out = e.moveIn(rid, body)
	if code != http.StatusAccepted || out["serverId"] != id {
		t.Fatalf("move-in: %d %v", code, out)
	}
	if op := e.waitOp(out["id"].(string)); op.Status != api.OpSucceeded {
		t.Fatalf("move-in: %+v", op)
	}
	s := e.a.serverByID(id)
	if s == nil {
		t.Fatalf("no server %s after its move-in", id)
	}
	sc, err := s.serverConfig()
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.row()
	if err != nil {
		t.Fatal(err)
	}
	switch {
	case got.Name != "Survival" || got.Slug != "our-survival":
		t.Errorf("moved in as %q at %q, want Survival at our-survival", got.Name, got.Slug)
	case sc.MemoryMB != had.MemoryMB || sc.PlayStyle != "friends" || !sc.CreatedAt.Equal(had.CreatedAt):
		t.Errorf("moved in with %d MB, style %q, made %s; it had %d MB, friends, %s", sc.MemoryMB, sc.PlayStyle, sc.CreatedAt, had.MemoryMB, had.CreatedAt)
	case sc.EULAAcceptedBy != had.EULAAcceptedBy || !sc.EULAAcceptedAt.Equal(had.EULAAcceptedAt):
		t.Errorf("the EULA accepted by %q at %s, want %q at %s", sc.EULAAcceptedBy, sc.EULAAcceptedAt, had.EULAAcceptedBy, had.EULAAcceptedAt)
	case had.CreatorNames != "Steve_Builds" || sc.CreatorNames != had.CreatorNames:
		t.Errorf("moved in made by %q; it was made by %q", sc.CreatorNames, had.CreatorNames)
	}
	e.sid = id
	if _, err := os.Stat(filepath.Join(e.dataDir(), "world", "filler.dat")); err != nil {
		t.Errorf("the backup's world isn't in place: %v", err)
	}
	if s.desired() != api.DesiredStopped || e.containerRuns(id) {
		t.Errorf("a server that didn't run where it was is %s here, running %v", s.desired(), e.containerRuns(id))
	}
	if l := e.a.diskLimitOf(id); l == nil || l.ID != "account-7" {
		t.Errorf("the server moved in counts against %+v", l)
	}
	if n := e.countRows(`SELECT COUNT(*) FROM audit WHERE action = 'server.moved_in' AND server_id = ? AND actor = 'playkeeper' AND result = 'started'`, id); n != 1 {
		t.Errorf("%d audit entries for the move-in", n)
	}
}

// A server that ran where it was starts once it's moved in, its world
// checked by that start as any restore's is. A name or slug a server here
// has gets a number, as a new server's would.
func TestAServerMovedInThatRanThereStartsHere(t *testing.T) {
	e := newAgentEnv(t)
	had, row, _, rid := e.movingIn("")
	code, out := e.moveIn(rid, moveInBody("mvdserver2", row.Slug, had, true))
	if code != http.StatusAccepted {
		t.Fatalf("move-in: %d %v", code, out)
	}
	if op := e.waitOp(out["id"].(string)); op.Status != api.OpSucceeded {
		t.Fatalf("move-in: %+v", op)
	}
	e.sid = "mvdserver2"
	e.waitFor("the server moved in online", func() bool { return e.status().Phase == api.PhaseOnline })
	got, err := e.srv().row()
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "Survival 2" || got.Slug == row.Slug {
		t.Errorf("moved in as %q at %q beside Survival at %q", got.Name, got.Slug, row.Slug)
	}
}

// A customer's server moved in keeps its name beside a server of the same
// name that isn't theirs, since names are unique for each account: a number
// after it would tell them another has that name. The slug another server
// has gets a few random letters after it.
func TestACustomersServerMovedInKeepsItsNameBesideAnothers(t *testing.T) {
	e := newAgentEnv(t)
	had, row, _, rid := e.movingIn("")
	body := moveInBody("mvdserver3", row.Slug, had, false)
	body["account"] = "account-7"
	code, out := e.moveIn(rid, body)
	if code != http.StatusAccepted {
		t.Fatalf("move-in: %d %v", code, out)
	}
	if op := e.waitOp(out["id"].(string)); op.Status != api.OpSucceeded {
		t.Fatalf("move-in: %+v", op)
	}
	e.sid = "mvdserver3"
	got, err := e.srv().row()
	if err != nil {
		t.Fatal(err)
	}
	if account := e.a.accountOf(e.sid); got.Name != "Survival" || !reRandomSlug(row.Slug).MatchString(got.Slug) || account != "account-7" {
		t.Errorf("moved in as %q at %q in %q, beside the machine's own Survival at %q", got.Name, got.Slug, account, row.Slug)
	}
}

// A move-in takes only what the dashboard sends: an id of a server's shape,
// the EULA acceptance the server had, a play style Playkeeper knows, memory
// that fits, and an upload for a new server whose world fits the disk limit
// it names. A restore applied as the dashboard's pages apply one can't name
// its server's id.
func TestAMoveInTakesOnlyWhatTheDashboardSends(t *testing.T) {
	e := newAgentEnv(t)
	had, row, raw, rid := e.movingIn("")
	for name, change := range map[string]func(map[string]any){
		"an id of another shape":            func(b map[string]any) { b["serverId"] = "../escaped" },
		"a slug of another shape":           func(b map[string]any) { b["slug"] = "Survival World" },
		"no EULA acceptance":                func(b map[string]any) { delete(b, "eulaAcceptedAt") },
		"nobody accepting the EULA":         func(b map[string]any) { b["eulaAcceptedBy"] = "" },
		"an unknown play style":             func(b map[string]any) { b["playStyle"] = "chaos" },
		"no memory":                         func(b map[string]any) { b["memoryMB"] = 0 },
		"memory that isn't one of the fits": func(b map[string]any) { b["memoryMB"] = 1000 },
		"no actor":                          func(b map[string]any) { delete(b, "actor") },
		"a creator Minecraft can't have":    func(b map[string]any) { b["creatorNames"] = "Steve_Builds bad;name" },
	} {
		body := moveInBody("mvdserver2", row.Slug, had, false)
		change(body)
		if code, out := e.moveIn(rid, body); code != http.StatusBadRequest || e.a.serverByID("mvdserver2") != nil {
			t.Errorf("a move-in with %s: %d %v", name, code, out)
		}
	}
	code, into := e.uploadTo(e.sp("/restore/upload"), raw)
	if code != 200 {
		t.Fatalf("an upload into Survival: %d %v", code, into)
	}
	if code, out := e.moveIn(into["id"].(string), moveInBody("mvdserver2", row.Slug, had, false)); code != http.StatusBadRequest || e.a.serverByID("mvdserver2") != nil {
		t.Errorf("moving in an upload into Survival: %d %v", code, out)
	}
	apply := map[string]any{"confirm": "restore", "acceptEula": true, "actor": "alex", "serverId": "mvdserver2"}
	if code, out := e.callWhenFree("POST", "/v1/restore/"+rid+"/apply", apply); code != http.StatusBadRequest || e.a.serverByID("mvdserver2") != nil {
		t.Errorf("a restore applied naming its server's id: %d %v", code, out)
	}
	e.setLimits(map[string]any{"id": "account-7", "limitBytes": 1 << 10, "servers": []string{}})
	if code, out := e.moveIn(rid, moveInBody("mvdserver2", row.Slug, had, false)); code != http.StatusInsufficientStorage || codeOf(out) != api.CodeDiskLimit || e.a.serverByID("mvdserver2") != nil {
		t.Errorf("a server moved in past its disk limit: %d %v", code, out)
	}
	e.setLimits(map[string]any{"id": "account-7", "limitBytes": 1 << 30, "servers": []string{}})
	if code, out := e.moveIn(rid, moveInBody("mvdserver2", row.Slug, had, false)); code != http.StatusAccepted {
		t.Errorf("the upload refused all that, moved in: %d %v", code, out)
	}
}

// A server moved in that didn't run stays stopped when the agent stops in
// the middle of its move-in, before its restore's journal is written or
// after: the next agent process finishes it without starting it.
func TestAMoveInFinishedAfterARestartStaysStopped(t *testing.T) {
	for _, step := range []string{"journal", "checking"} {
		t.Run(step, func(t *testing.T) {
			e := newAgentEnv(t)
			had, row, _, rid := e.movingIn("")
			stage := e.a.stageDir(rid)
			reached := make(chan struct{})
			setRestoreStep(t, func(_ context.Context, at string) {
				if at != step {
					return
				}
				// A process that dies tidies nothing up: before its journal,
				// the restore would delete its stage on the way out.
				if step == "journal" {
					if err := os.Rename(stage, stage+".dead"); err != nil {
						t.Error(err)
					}
				}
				close(reached)
				runtime.Goexit()
			})
			code, out := e.moveIn(rid, moveInBody("mvdserver2", row.Slug, had, false))
			if code != http.StatusAccepted {
				t.Fatalf("move-in: %d %v", code, out)
			}
			waitClosed(t, reached, "the move-in's "+step+" step")
			e.stop()
			if step == "journal" {
				if err := os.Rename(stage+".dead", stage); err != nil {
					t.Fatal(err)
				}
			}
			restoreStep = func(context.Context, string) {}
			e.start()
			op := e.waitOp(out["id"].(string))
			if op.Status != api.OpSucceeded || op.Detail["resumedAfterRestart"] != true {
				t.Fatalf("the next agent process must finish the move-in: %+v", op)
			}
			s := e.a.serverByID("mvdserver2")
			if s == nil {
				t.Fatal("the move-in finished after a restart left no server")
			}
			if s.desired() != api.DesiredStopped || e.containerRuns("mvdserver2") {
				t.Fatalf("the move-in finished after a restart left the server %s, running %v", s.desired(), e.containerRuns("mvdserver2"))
			}
		})
	}
}
