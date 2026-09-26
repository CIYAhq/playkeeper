package agent

import (
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/CIYAhq/playkeeper/internal/api"
	"github.com/CIYAhq/playkeeper/internal/modpacks"
	"github.com/CIYAhq/playkeeper/internal/modpacks/curseforge"
)

const cfSearch = "https://api.curseforge.com/v1/mods/search"

// serveCurseForgeKey makes the fake CurseForge accept only the key good.
func serveCurseForgeKey(e *agentEnv, good string) {
	e.up.handle(cfSearch, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-api-key") != good {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"data":[],"pagination":{"index":0,"pageSize":1,"resultCount":0,"totalCount":0}}`)
	})
}

func (e *agentEnv) addonSources() api.AddonSources {
	e.t.Helper()
	code, out := e.call("GET", "/v1/addon-sources", nil)
	if code != 200 {
		e.t.Fatalf("addon sources: %d %v", code, out)
	}
	b, _ := json.Marshal(out)
	var s api.AddonSources
	if err := json.Unmarshal(b, &s); err != nil {
		e.t.Fatal(err)
	}
	return s
}

func TestCurseForgeKeyIsCheckedSavedAndRemoved(t *testing.T) {
	e := newAgentEnv(t)
	const good = "good-curseforge-key-0123456789abcdef"
	serveCurseForgeKey(e, good)
	file := e.a.curseForgeKeyFile()
	offersCurseForge := func() bool { return slices.Contains(e.a.packs().Sources(), modpacks.CurseForge) }

	if s := e.addonSources(); s.CurseForge.Key != string(curseforge.KeyNone) || s.CurseForge.Ending != "" || offersCurseForge() {
		t.Fatalf("with no key, CurseForge is not set up: %+v", s)
	}

	code, out := e.call("POST", "/v1/addon-sources/curseforge", map[string]any{"key": "made-up-key-000000000000", "actor": "admin"})
	if code != 400 || out["code"] != api.CodeKeyRefused || out["error"] != "That key didn't work. Copy it again from console.curseforge.com." {
		t.Fatalf("a key CurseForge refuses: %d %v", code, out)
	}
	if _, err := os.Lstat(file); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("a refused key must not be saved: %v", err)
	}

	asked := e.up.hitCount(cfSearch)
	if code, out := e.call("POST", "/v1/addon-sources/curseforge", map[string]any{"key": "too short", "actor": "admin"}); code != 400 || out["code"] != api.CodeKeyRefused {
		t.Fatalf("something that can't be a key: %d %v", code, out)
	}
	if e.up.hitCount(cfSearch) != asked {
		t.Error("CurseForge was asked about something that can't be a key")
	}

	code, out = e.call("POST", "/v1/addon-sources/curseforge", map[string]any{"key": " " + good + "\n", "actor": "admin"})
	if code != 200 {
		t.Fatalf("a key CurseForge accepts: %d %v", code, out)
	}
	if b, _ := json.Marshal(out); strings.Contains(string(b), good) || strings.Contains(string(b), good[:20]) {
		t.Fatalf("the answer must never carry the key: %s", b)
	}
	fi, err := os.Lstat(file)
	if err != nil || !fi.Mode().IsRegular() || fi.Mode().Perm() != 0o600 {
		t.Fatalf("the key must be kept in a file only root can read: %v %v", fi, err)
	}
	if b, _ := os.ReadFile(file); string(b) != good+"\n" {
		t.Fatalf("the key file holds %q", b)
	}
	if s := e.addonSources(); s.CurseForge.Key != string(curseforge.KeyFile) || s.CurseForge.Ending != "cdef" || s.CurseForge.Problem != "" {
		t.Fatalf("the owner's key shows by its ending: %+v", s)
	}
	if !offersCurseForge() {
		t.Fatal("modpacks must come from CurseForge too once the key is saved")
	}
	rows, err := e.a.db.Query(`SELECT action, result, detail FROM audit WHERE action LIKE 'curseforge.%'`)
	if err != nil {
		t.Fatal(err)
	}
	var audit []string
	for rows.Next() {
		var action, result, detail string
		rows.Scan(&action, &result, &detail)
		audit = append(audit, action+" "+result+" "+detail)
	}
	rows.Close()
	if !slices.Contains(audit, "curseforge.key_saved succeeded key ending cdef") || slices.ContainsFunc(audit, func(s string) bool { return strings.Contains(s, good[:10]) }) {
		t.Fatalf("the audit log names the key by its ending only: %q", audit)
	}

	if code, out := e.call("DELETE", "/v1/addon-sources/curseforge?actor=admin", nil); code != 200 {
		t.Fatalf("remove: %d %v", code, out)
	}
	if _, err := os.Lstat(file); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("removing the key deletes its file: %v", err)
	}
	if s := e.addonSources(); s.CurseForge.Key != string(curseforge.KeyNone) || offersCurseForge() {
		t.Fatalf("without the key, CurseForge is off again: %+v", s)
	}
}

func TestABuiltInCurseForgeKeyNeverShows(t *testing.T) {
	old := curseforge.BuildKey
	curseforge.BuildKey = "built-in-curseforge-key-0123456789"
	t.Cleanup(func() { curseforge.BuildKey = old })
	e := newAgentEnv(t)
	if s := e.addonSources(); s.CurseForge.Key != string(curseforge.KeyBuild) || s.CurseForge.Ending != "" {
		t.Fatalf("a release's own key is on, built in, and never shown: %+v", s)
	}
}

func TestAnUnusableCurseForgeKeyFileIsReported(t *testing.T) {
	e := newAgentEnv(t)
	if err := os.WriteFile(e.a.curseForgeKeyFile(), []byte("readable-by-all-0123456789abcdef\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	e.a.loadPacks()
	s := e.addonSources()
	if s.CurseForge.Key != string(curseforge.KeyNone) || !strings.Contains(s.CurseForge.Problem, "chmod 600") || strings.Contains(s.CurseForge.Problem, "readable-by-all") {
		t.Fatalf("a key file other users can read is not used, and the reason says how to fix it without the key: %+v", s)
	}
}
