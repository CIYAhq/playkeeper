package agent

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/CIYAhq/playkeeper/internal/api"
	"github.com/CIYAhq/playkeeper/internal/modpacks/share"
)

// fabricForShare makes the test server a Fabric server, as the share reads
// it: the type and the loader it runs.
func (e *agentEnv) fabricForShare() {
	e.t.Helper()
	s := e.srv()
	sc, err := s.serverConfig()
	if err != nil || sc == nil {
		e.t.Fatalf("config: %v", err)
	}
	sc.Type = "fabric"
	sc.Software = &api.SoftwarePin{Type: "fabric", MinecraftVersion: sc.MinecraftVersion, FabricLoader: "0.17.2"}
	if err := s.saveServerConfig(*sc); err != nil {
		e.t.Fatal(err)
	}
	if _, err := e.a.db.Exec(`UPDATE servers SET type = 'fabric' WHERE id = ?`, e.sid); err != nil {
		e.t.Fatal(err)
	}
}

func (e *agentEnv) packShare(method string, body any) api.PackShare {
	e.t.Helper()
	code, out := e.call(method, e.sp("/mods/share"), body)
	if code != http.StatusOK {
		e.t.Fatalf("%s share: %d %v", method, code, out)
	}
	b, _ := json.Marshal(out)
	var ps api.PackShare
	if err := json.Unmarshal(b, &ps); err != nil {
		e.t.Fatal(err)
	}
	return ps
}

func (e *agentEnv) sharePublic(on bool) api.PackShare {
	e.t.Helper()
	return e.packShare("POST", map[string]any{"public": on, "actor": "admin"})
}

// rawAnswer is an agent answer as the panel receives it.
func (e *agentEnv) rawAnswer(path string) (int, string) {
	e.t.Helper()
	resp, err := http.Get(e.ts.URL + path)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func TestPackShareLinkIsMadeWhenSharedAndReplacedAfterward(t *testing.T) {
	e := newAgentEnv(t)
	e.withSources()
	e.create()
	e.fabricForShare()

	ps := e.packShare("GET", nil)
	slug := e.status().Slug
	if ps.Public || ps.Token != "" || ps.File != slug+".mrpack" || ps.Size <= 0 || ps.LoaderName != "Fabric" || len(ps.Share) == 0 {
		t.Fatalf("before sharing: %+v", ps)
	}
	if strings.Contains(string(ps.Share), `"index"`) {
		t.Errorf("the dashboard got the file's index: %s", ps.Share)
	}

	on := e.sharePublic(true)
	if !on.Public || !share.ValidToken(on.Token) || strings.Contains(on.Token, slug) {
		t.Fatalf("shared: %+v", on)
	}
	if again := e.sharePublic(true); again.Token != on.Token {
		t.Errorf("sharing what is shared changed the link: %q, then %q", on.Token, again.Token)
	}
	var link api.PackLink
	e.decode("GET", "/v1/packs/"+on.Token, &link)
	if link.Server != e.sid || link.Slug != slug || link.GamePort != e.srv().gamePort || !strings.Contains(string(link.Share), `"index"`) {
		t.Fatalf("link: %+v", link)
	}

	if off := e.sharePublic(false); off.Public || off.Token != "" {
		t.Fatalf("stopped sharing: %+v", off)
	}
	if code, _ := e.rawAnswer("/v1/packs/" + on.Token); code != http.StatusNotFound {
		t.Errorf("the link after sharing stopped: %d", code)
	}
	var stored string
	e.a.db.QueryRow(`SELECT packs_token FROM servers WHERE id = ?`, e.sid).Scan(&stored)
	if stored != "" {
		t.Errorf("the old token is still stored: %q", stored)
	}

	next := e.sharePublic(true)
	if !share.ValidToken(next.Token) || next.Token == on.Token {
		t.Fatalf("shared again: %q, before %q", next.Token, on.Token)
	}
	if code, _ := e.rawAnswer("/v1/packs/" + on.Token); code != http.StatusNotFound {
		t.Errorf("the first link after sharing again: %d", code)
	}
	if code, _ := e.rawAnswer("/v1/packs/" + next.Token); code != http.StatusOK {
		t.Errorf("the new link: %d", code)
	}
	if n := e.countRows(`SELECT COUNT(*) FROM audit WHERE action = 'packs.shared' AND actor = 'admin'`); n != 2 {
		t.Errorf("%d audit rows for sharing, want 2", n)
	}
	if n := e.countRows(`SELECT COUNT(*) FROM audit WHERE action = 'packs.unshared' AND actor = 'admin'`); n != 1 {
		t.Errorf("%d audit rows for stopping, want 1", n)
	}
}

func TestPackLinkAnswersAlikeWhateverTheReason(t *testing.T) {
	e := newAgentEnv(t)
	e.withSources()
	e.create()
	e.fabricForShare()
	old := e.sharePublic(true).Token
	e.sharePublic(false)
	tok := e.sharePublic(true).Token
	if code, _ := e.rawAnswer("/v1/packs/" + tok); code != http.StatusOK {
		t.Fatalf("the link: %d", code)
	}

	unknownCode, unknown := e.rawAnswer("/v1/packs/" + strings.Repeat("A", share.TokenLen))
	if unknownCode != http.StatusNotFound || strings.Contains(unknown, e.sid) {
		t.Fatalf("an unknown token: %d %s", unknownCode, unknown)
	}
	same := func(what, path string) {
		t.Helper()
		code, body := e.rawAnswer(path)
		if code != unknownCode || body != unknown {
			t.Errorf("%s: %d %s, want the unknown token's %d %s", what, code, body, unknownCode, unknown)
		}
	}
	same("the server's slug", "/v1/packs/"+e.status().Slug)
	same("an old token", "/v1/packs/"+old)
	same("a token of the wrong shape", "/v1/packs/"+tok[:share.TokenLen-1]+"-")

	e.sharePublic(false)
	same("sharing off", "/v1/packs/"+tok)
	tok = e.sharePublic(true).Token

	if _, err := e.a.db.Exec(`UPDATE servers SET type = 'paper' WHERE id = ?`, e.sid); err != nil {
		t.Fatal(err)
	}
	same("a server that no longer runs mods", "/v1/packs/"+tok)
	if code, out := e.call("POST", e.sp("/mods/share"), map[string]any{"public": true, "actor": "admin"}); code != http.StatusConflict {
		t.Errorf("sharing a Paper server: %d %v", code, out)
	}
	if _, err := e.a.db.Exec(`UPDATE servers SET type = 'fabric' WHERE id = ?`, e.sid); err != nil {
		t.Fatal(err)
	}

	code, out := e.call("POST", e.sp("/stop"), map[string]any{"actor": "admin"})
	if code != http.StatusAccepted {
		t.Fatalf("stop: %d %v", code, out)
	}
	e.waitOp(out["id"].(string))
	same("a stopped server", "/v1/packs/"+tok)

	// Signed-in users still download the file while the page is off.
	e.sharePublic(false)
	if code, body := e.rawAnswer(e.sp("/mods/share.mrpack")); code != http.StatusOK || !strings.HasPrefix(body, "PK") {
		t.Errorf("signed-in download with sharing off: %d", code)
	}
}
