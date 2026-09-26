package panel

import (
	"archive/zip"
	"bytes"
	"cmp"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/CIYAhq/playkeeper/internal/addons"
	"github.com/CIYAhq/playkeeper/internal/api"
	"github.com/CIYAhq/playkeeper/internal/modpacks/mrpack"
	"github.com/CIYAhq/playkeeper/internal/modpacks/share"
)

const friendsToken = "Rk9yZnJpZW5kc09ubHkxMj"

// friendsAgent is an agent whose server Cobblemon shares its pack at
// friendsToken while on is set.
type friendsAgent struct {
	mu   sync.Mutex
	on   bool
	down bool
	link api.PackLink
	sh   share.Share
	icon []byte
}

func newFriendsAgent(t *testing.T) *friendsAgent {
	t.Helper()
	f := &friendsAgent{on: true, sh: cobblemonShare(), icon: []byte("\x89PNG emblem")}
	raw, err := json.Marshal(f.sh)
	if err != nil {
		t.Fatal(err)
	}
	f.link = api.PackLink{Server: "k3v9q2m7xw", Slug: "cobblemon", GamePort: 25566, HasIcon: true, Share: raw}
	return f
}

func (f *friendsAgent) set(on, down bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.on, f.down = on, down
}

func (f *friendsAgent) handler(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	switch {
	case f.down:
		w.WriteHeader(http.StatusServiceUnavailable)
		io.WriteString(w, `{"error":"Playkeeper can't make this pack right now.","code":"upstream_unavailable"}`)
		return
	case r.URL.Path == "/v1/packs/"+friendsToken && f.on:
		json.NewEncoder(w).Encode(f.link)
		return
	case r.URL.Path == "/v1/servers/"+f.link.Server+"/icon":
		w.Header().Set("Content-Type", "image/png")
		w.Write(f.icon)
		return
	}
	w.WriteHeader(http.StatusNotFound)
	io.WriteString(w, `{"error":"Pack not found.","code":"not_found"}`)
}

// cobblemonShare is the design's Cobblemon: a pack, three mods friends get,
// one they get themselves and spark, which only the server runs.
func cobblemonShare() share.Share {
	need, optional, serverOnly := share.Required, share.Optional, share.ServerOnly
	both := &mrpack.Env{Client: mrpack.Required, Server: mrpack.Required}
	file := func(name, sum string) mrpack.File {
		return mrpack.File{Path: "mods/" + name + ".jar", Hashes: mrpack.Hashes{SHA1: strings.Repeat(sum, 40), SHA512: strings.Repeat(sum, 128)}, Env: both,
			Downloads: []string{"https://cdn.modrinth.com/data/" + name + "/versions/1/" + name + ".jar"}, FileSize: 1000}
	}
	return share.Share{
		Key: "setup", Server: "Cobblemon", Type: "fabric", MinecraftVersion: "26.1.2", LoaderVersion: "0.17.2",
		Pack:   &share.Pack{Name: "Cobblemon Modpack", Version: "26.1.2-5", Source: addons.Modrinth, Page: "https://modrinth.com/modpack/cobblemon-fabric", Need: need, Label: need.Label()},
		Notice: share.Text{Key: "share.notice.pack_one", Params: map[string]string{"mod": "Waystones"}, Text: "Friends need the pack plus Waystones"},
		Mods: []share.Mod{
			{Name: "Waystones", Version: "21.1.4", Path: "mods/waystones.jar", From: share.FromUser, Source: addons.Modrinth, Project: "LOpKHB2A", OnServer: true, Need: need, Label: need.Label(), InFile: true},
			{Name: "Balm", Version: "21.0.20", Path: "mods/balm.jar", From: share.FromUser, Source: addons.Modrinth, Project: "MBAkmtvl", DependencyOf: "LOpKHB2A", OnServer: true, Need: need, Label: need.Label(), InFile: true},
			{Name: "Chunky", Version: "1.4.40", Path: "mods/chunky.jar", From: share.FromUser, Source: addons.Modrinth, Project: "fALzjamp", OnServer: true, Need: optional, Label: optional.Label(), InFile: true},
			{Name: "spark", Version: "1.10.124", Path: "mods/spark.jar", From: share.FromUser, Source: addons.Modrinth, Project: "l6YH9Als", OnServer: true, Need: serverOnly, Label: serverOnly.Label()},
			{Name: "Emote Wheel", Path: "mods/emote-wheel.jar", From: share.FromUser, OnServer: true, Need: need, Label: need.Label(), ByHand: true},
		},
		Yourself: []share.Yourself{{Name: "Emote Wheel", Path: "mods/emote-wheel.jar", Page: "https://www.curseforge.com/minecraft/mc-mods/emote-wheel", Need: need,
			Reason: share.Text{Key: "share.yourself.curseforge", Params: map[string]string{"name": "Emote Wheel", "folder": "mods"}, Text: "Emote Wheel comes from CurseForge."}}},
		Index: mrpack.Index{FormatVersion: 1, Game: "minecraft", VersionID: "26.1.2-5", Name: "Cobblemon",
			Files:        []mrpack.File{file("waystones", "a"), file("balm", "b"), file("chunky", "c")},
			Dependencies: map[string]string{"minecraft": "26.1.2", "fabric-loader": "0.17.2"}},
	}
}

// The server's address under the machine's name, when the agent knows it,
// replaces the host the page was opened at.
func TestFriendsPackPageGivesTheServersNamedAddress(t *testing.T) {
	f := newFriendsAgent(t)
	f.link.JoinAddress = "cobblemon.alex.playkeeper.io"
	e := newEnvAgent(t, f.handler, &syncBuffer{})
	r, body := get(t, e.ts.Client(), "GET", e.ts.URL+share.PathPrefix+friendsToken+"/page", nil)
	var p share.Page
	if r.StatusCode != http.StatusOK || json.Unmarshal([]byte(body), &p) != nil || p.Address != "cobblemon.alex.playkeeper.io" {
		t.Fatalf("page data: %d %s", r.StatusCode, body)
	}
}

// A joined machine's servers join at its IP and port: its pack page gives the
// IP the machine last called in from, not the host the page was opened at nor
// a name the machine reports, since names stay with the dashboard's machine.
func TestAJoinedMachinesPackPageGivesItsIPAndPort(t *testing.T) {
	e := newEnvConfig(t, withDomain, nil)
	cookie, csrf := e.setup(t)
	e.replyStatus("GET", "/v1/packs/"+friendsToken, http.StatusNotFound, `{"error":"Pack not found.","code":"not_found"}`)
	f := newFriendsAgent(t)
	f.link.JoinAddress = "cobblemon.home.playkeeper.io"
	e.joined(t, cookie, csrf, http.HandlerFunc(f.handler))
	req, _ := http.NewRequest("GET", e.ts.URL+share.PathPrefix+friendsToken+"/page", nil)
	req.Host = "panel.example.com"
	r, err := e.ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	body, _ := io.ReadAll(r.Body)
	var p share.Page
	if r.StatusCode != http.StatusOK || json.Unmarshal(body, &p) != nil || p.Address != "127.0.0.1:25566" {
		t.Fatalf("a joined machine's pack page: %d %s", r.StatusCode, body)
	}
}

// packMachine is a joined machine's agent in the friends' pack tests. It runs
// one server and shares its pack at token when the dashboard turns sharing
// on. A greedy one opens every link with its own pack; one that is down
// can't answer about any.
type packMachine struct {
	mu     sync.Mutex
	server string
	token  string
	link   api.PackLink
	name   string
	greedy bool
	down   bool
	asked  map[string]int
}

func newPackMachine(t *testing.T, server, name, token string) *packMachine {
	t.Helper()
	sh := cobblemonShare()
	sh.Server = name
	raw, err := json.Marshal(sh)
	if err != nil {
		t.Fatal(err)
	}
	return &packMachine{server: server, token: token, name: name, asked: map[string]int{},
		link: api.PackLink{Server: server, Slug: strings.ToLower(name), GamePort: 25566, HasIcon: true, Share: raw}}
}

func (p *packMachine) set(f func(p *packMachine)) {
	p.mu.Lock()
	defer p.mu.Unlock()
	f(p)
}

func (p *packMachine) askedFor(token string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.asked[token]
}

func (p *packMachine) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	defer p.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	gone := func() {
		w.WriteHeader(http.StatusNotFound)
		io.WriteString(w, `{"error":"Pack not found.","code":"not_found"}`)
	}
	if token, ok := strings.CutPrefix(r.URL.Path, "/v1/packs/"); ok {
		p.asked[token]++
		switch {
		case p.down:
			w.WriteHeader(http.StatusServiceUnavailable)
			io.WriteString(w, `{"error":"Playkeeper can't make this pack right now.","code":"upstream_unavailable"}`)
		case p.greedy || token == p.token:
			json.NewEncoder(w).Encode(p.link)
		default:
			gone()
		}
		return
	}
	switch r.Method + " " + r.URL.Path {
	case "GET /v1/servers":
		fmt.Fprintf(w, `[{"id":%q,"name":%q,"phase":"online"}]`, p.server, p.name)
	case "POST /v1/servers/" + p.server + "/mods/share":
		json.NewEncoder(w).Encode(api.PackShare{Public: true, Token: p.token, File: p.link.Slug + ".mrpack", LoaderName: "Fabric", Share: p.link.Share})
	case "GET /v1/servers/" + p.server + "/icon":
		w.Header().Set("Content-Type", "image/png")
		io.WriteString(w, "\x89PNG "+p.name)
	default:
		gone()
	}
}

// A friends' pack link opens only on the machine it was made on: the
// dashboard records each link as it's made and asks nobody else about it,
// so an earlier-joined machine that opens every link can't answer for a
// later one's. A link it has no record of opens only where every machine
// answers and exactly one has it.
func TestAFriendsPackLinkOpensOnlyOnTheMachineThatMadeIt(t *testing.T) {
	const (
		laterToken  = "LaterMachinesPackLink1"
		legacyToken = "LinkFromBeforeRecords1"
	)
	e := newEnvConfig(t, withDomain, nil)
	cookie, csrf := e.setup(t)
	e.reply("GET", "/v1/servers", `[]`)
	survival, err := json.Marshal(newPackMachine(t, sampleServer, "Survival", legacyToken).link)
	if err != nil {
		t.Fatal(err)
	}
	e.reply("GET", "/v1/packs/"+legacyToken, string(survival))
	e.replyStatus("GET", "/v1/packs/"+laterToken, http.StatusNotFound, `{"error":"Pack not found.","code":"not_found"}`)
	earlier := newPackMachine(t, "evilserver", "Evil", "")
	earlier.greedy = true
	e.joined(t, cookie, csrf, earlier)
	later := newPackMachine(t, "k3v9q2m7xw", "Cobblemon", laterToken)
	laterID, laterLink := e.joined(t, cookie, csrf, later)
	e.get(t, "/api/servers", cookie, nil)
	if r := e.do(t, "POST", "/api/servers/k3v9q2m7xw/mods/share", `{"public":true}`, auth(cookie, csrf)); r.status != http.StatusOK {
		t.Fatalf("sharing the later machine's pack: %d %v", r.status, r.body)
	}
	// The earlier machine names the later one's link when its own pack is
	// shared: the link stays with the later machine's server.
	earlier.set(func(p *packMachine) { p.token = laterToken })
	if r := e.do(t, "POST", "/api/servers/evilserver/mods/share", `{"public":true}`, auth(cookie, csrf)); r.status != http.StatusOK {
		t.Fatalf("sharing the earlier machine's pack: %d %v", r.status, r.body)
	}
	earlier.set(func(p *packMachine) { p.token = "" })
	if !strings.Contains(e.logs.String(), "a machine named another server's link as its own") {
		t.Fatalf("the takeover isn't logged:\n%s", e.logs.String())
	}

	page := func(token string) (int, share.Page) {
		t.Helper()
		var p share.Page
		r, body := get(t, e.ts.Client(), "GET", e.ts.URL+share.PathPrefix+token+"/page", nil)
		if r.StatusCode == http.StatusOK && json.Unmarshal([]byte(body), &p) != nil {
			t.Fatalf("page data: %s", body)
		}
		return r.StatusCode, p
	}
	for _, tc := range []struct {
		name    string
		token   string
		greedy  bool   // the earlier machine opens every link
		down    bool   // the later machine's agent can't answer
		server  string // the server the later machine says its link opens, when not its own
		offline bool   // the later machine's link is down
		want    string // whose pack opens, or "" for the 404 of a link nobody has
		log     string
	}{
		{name: "the later machine's link opens its own pack", token: laterToken, greedy: true, want: "Cobblemon"},
		{name: "a link from before records opens where only one machine has it", token: legacyToken, want: "Survival"},
		{name: "a link from before records that two machines open opens on neither", token: legacyToken, greedy: true, log: "more than one machine opens a friends' pack link"},
		{name: "a link from before records is gone while a machine can't answer", token: legacyToken, down: true},
		{name: "the later machine's link is gone when it answers for another server", token: laterToken, greedy: true, server: "evilserver", log: "a machine answered a friends' pack link for another server"},
		{name: "the later machine's link is gone while the machine is offline", token: laterToken, greedy: true, offline: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			earlier.set(func(p *packMachine) { p.greedy = tc.greedy })
			later.set(func(p *packMachine) {
				p.down = tc.down
				p.link.Server = cmp.Or(tc.server, p.server)
			})
			if tc.offline {
				laterLink.stop()
				eventually(t, "the later machine is offline", func() bool { return linkState(e.machineView(t, cookie, laterID)) == "offline" })
			}
			code, p := page(tc.token)
			switch {
			case tc.want == "" && code != http.StatusNotFound:
				t.Fatalf("got %d, a page for %q, want the 404 of a link nobody has", code, p.Server)
			case tc.want != "" && (code != http.StatusOK || p.Server != tc.want):
				t.Fatalf("got %d, a page for %q, want %s's", code, p.Server, tc.want)
			}
			if n := earlier.askedFor(laterToken); n != 0 {
				t.Fatalf("the earlier machine was asked %d times about the later machine's link", n)
			}
			if tc.log != "" && !strings.Contains(e.logs.String(), tc.log) {
				t.Fatalf("the log doesn't say %q:\n%s", tc.log, e.logs.String())
			}
			if tc.want != "Cobblemon" {
				return
			}
			if p.Address != "127.0.0.1:25566" {
				t.Errorf("friends join at %q, want the later machine's IP and the server's port", p.Address)
			}
			sh := cobblemonShare()
			want, _ := sh.File()
			if r, body := get(t, e.ts.Client(), "GET", e.ts.URL+p.Download.URL, nil); r.StatusCode != http.StatusOK || body != string(want) {
				t.Errorf("the file: %d, %d bytes", r.StatusCode, len(body))
			}
			if r, body := get(t, e.ts.Client(), "GET", e.ts.URL+share.PathPrefix+tc.token+"/icon", nil); r.StatusCode != http.StatusOK || body != "\x89PNG Cobblemon" {
				t.Errorf("the emblem: %d %q", r.StatusCode, body)
			}
		})
	}
}

func TestFriendsPackPageIsPublicAndListsOnlyWhatFriendsGet(t *testing.T) {
	f := newFriendsAgent(t)
	logs := &syncBuffer{}
	e := newEnvAgent(t, f.handler, logs)
	c := e.ts.Client()
	base := e.ts.URL + share.PathPrefix + friendsToken

	r, _ := get(t, c, "GET", base, nil)
	if r.StatusCode != http.StatusOK || !strings.HasPrefix(r.Header.Get("Content-Type"), "text/html") || r.Header.Get("Cache-Control") != "no-store" ||
		r.Header.Get("X-Robots-Tag") != "noindex, nofollow" || r.Header.Get("Referrer-Policy") != "no-referrer" {
		t.Fatalf("page: %d %v", r.StatusCode, r.Header)
	}

	r, body := get(t, c, "GET", base+"/page", nil)
	if r.StatusCode != http.StatusOK || r.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("page data: %d %s", r.StatusCode, body)
	}
	var p struct {
		share.Page
		HasIcon bool `json:"hasIcon"`
	}
	if err := json.Unmarshal([]byte(body), &p); err != nil {
		t.Fatal(err)
	}
	if p.Server != "Cobblemon" || p.LoaderName != "Fabric" || p.LoaderVersion != "0.17.2" || !p.HasIcon || p.Address != "127.0.0.1:25566" ||
		p.Download.URL != share.PathPrefix+friendsToken+"/cobblemon.mrpack" || p.Download.Name != "cobblemon.mrpack" {
		t.Fatalf("page data: %+v", p)
	}
	var names []string
	for _, m := range p.Mods {
		names = append(names, m.Name)
		if m.Name == "Balm" && m.NeededBy != "Waystones" {
			t.Errorf("Balm is needed by %q", m.NeededBy)
		}
	}
	if !slices.Equal(names, []string{"Waystones", "Balm", "Chunky", "Emote Wheel"}) || len(p.Yourself) != 1 {
		t.Errorf("mods on the page: %v, yourself %+v", names, p.Yourself)
	}
	for _, secret := range []string{"spark", "l6YH9Als", f.link.Server, `"index"`, `"key":"setup"`} {
		if strings.Contains(body, secret) {
			t.Errorf("the public page data holds %q: %s", secret, body)
		}
	}

	r, body = get(t, c, "GET", e.ts.URL+p.Download.URL, nil)
	want, _ := f.sh.File()
	if r.StatusCode != http.StatusOK || r.Header.Get("Content-Type") != share.ContentType || body != string(want) ||
		r.Header.Get("Content-Disposition") != `attachment; filename=cobblemon.mrpack` {
		t.Fatalf("file: %d %v", r.StatusCode, r.Header)
	}
	checkFriendsFile(t, []byte(body))
	if r, body := get(t, c, "HEAD", e.ts.URL+p.Download.URL, nil); r.StatusCode != http.StatusOK || body != "" {
		t.Errorf("HEAD file: %d %q", r.StatusCode, body)
	}
	if r, body := get(t, c, "GET", base+"/icon", nil); r.StatusCode != http.StatusOK || r.Header.Get("Content-Type") != "image/png" || body != string(f.icon) {
		t.Errorf("emblem: %d %v", r.StatusCode, r.Header)
	}
	if r, _ := get(t, c, "GET", base+"/other.mrpack", nil); r.StatusCode != http.StatusNotFound {
		t.Errorf("another file name: %d", r.StatusCode)
	}
	if l := logs.String(); !strings.Contains(l, share.PathPrefix+"…") || strings.Contains(l, friendsToken) {
		t.Errorf("the log shows the token:\n%s", l)
	}
}

// checkFriendsFile checks that a friends' .mrpack holds names, versions and
// download references only: one index, no overrides, Modrinth's CDN, and no
// server-only mod.
func checkFriendsFile(t *testing.T, b []byte) {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		t.Fatal(err)
	}
	if len(zr.File) != 1 || zr.File[0].Name != mrpack.IndexName {
		t.Fatalf("the file holds %d entries, the first %q", len(zr.File), zr.File[0].Name)
	}
	rc, _ := zr.File[0].Open()
	defer rc.Close()
	var ix map[string]json.RawMessage
	if err := json.NewDecoder(rc).Decode(&ix); err != nil {
		t.Fatal(err)
	}
	for k := range ix {
		if !slices.Contains([]string{"formatVersion", "game", "versionId", "name", "summary", "files", "dependencies"}, k) {
			t.Errorf("the index holds %q", k)
		}
	}
	var files []map[string]json.RawMessage
	json.Unmarshal(ix["files"], &files)
	for _, f := range files {
		for k := range f {
			if !slices.Contains([]string{"path", "hashes", "env", "downloads", "fileSize"}, k) {
				t.Errorf("a file entry holds %q", k)
			}
		}
		var downloads []string
		var path string
		json.Unmarshal(f["downloads"], &downloads)
		json.Unmarshal(f["path"], &path)
		if len(downloads) == 0 || strings.Contains(path, "spark") {
			t.Errorf("file %s: %v", path, downloads)
		}
		for _, d := range downloads {
			if !strings.HasPrefix(d, "https://cdn.modrinth.com/") {
				t.Errorf("file %s downloads from %s", path, d)
			}
		}
	}
	if len(files) != 3 {
		t.Errorf("%d files, want 3", len(files))
	}
}

func TestFriendsPackLinksAnswerAlikeWhateverTheReason(t *testing.T) {
	f := newFriendsAgent(t)
	e := newEnvAgent(t, f.handler, io.Discard)
	c := e.ts.Client()
	type answer struct {
		code   int
		header http.Header
		body   string
	}
	shapes := map[string]func(token string) string{
		"page":      func(tok string) string { return share.PathPrefix + tok },
		"page data": func(tok string) string { return share.PathPrefix + tok + "/page" },
		"emblem":    func(tok string) string { return share.PathPrefix + tok + "/icon" },
		"file":      func(tok string) string { return share.PathPrefix + tok + "/cobblemon.mrpack" },
	}
	// The public group answers a route's failures with its own 404, so the
	// route is asked on its own too: each must answer alike without the other.
	for _, via := range []string{"panel", "route"} {
		t.Run(via, func(t *testing.T) {
			f.set(true, false)
			ask := func(method, path string) answer {
				if via == "route" {
					rec := httptest.NewRecorder()
					e.srv.friendsPacks().ServeHTTP(rec, httptest.NewRequest(method, path, nil))
					return answer{rec.Code, rec.Header(), rec.Body.String()}
				}
				r, body := get(t, c, method, e.ts.URL+path, nil)
				h := r.Header.Clone()
				h.Del("Date")
				return answer{r.StatusCode, h, body}
			}
			unknown := strings.Repeat("A", share.TokenLen)
			baseline := map[string]answer{}
			for shape, path := range shapes {
				a := ask("GET", path(unknown))
				want := http.StatusNotFound
				if shape == "page" {
					want = http.StatusOK
				}
				if a.code != want || strings.Contains(strings.ToLower(a.body), "cobblemon") {
					t.Fatalf("%s for an unknown token: %d %s", shape, a.code, a.body)
				}
				baseline[shape] = a
			}
			if got, want := ask("GET", share.PathPrefix+friendsToken), baseline["page"]; got.code != want.code || got.body != want.body || !headersEqual(got.header, want.header) {
				t.Errorf("the page for a link that works: %d %v; for an unknown one: %d %v", got.code, got.header, want.code, want.header)
			}
			same := func(what string, token string) {
				t.Helper()
				for shape, path := range shapes {
					if got := ask("GET", path(token)); got.code != baseline[shape].code || got.body != baseline[shape].body || !headersEqual(got.header, baseline[shape].header) {
						t.Errorf("%s, %s: %d %v %q; an unknown token: %d %v %q", what, shape, got.code, got.header, got.body, baseline[shape].code, baseline[shape].header, baseline[shape].body)
					}
				}
			}
			same("the server's name", "cobblemon")
			same("a token of the wrong shape", friendsToken[:share.TokenLen-1]+"-")
			same("a token in another case", strings.ToLower(friendsToken))
			f.set(false, false)
			same("sharing off, a stopped server or an old link", friendsToken)
			f.set(true, false)
			if got, want := ask("GET", share.PathPrefix+friendsToken+"/other.mrpack"), baseline["file"]; got.code != want.code || got.body != want.body {
				t.Errorf("another file name: %d %q", got.code, got.body)
			}
			if got, want := ask("POST", share.PathPrefix+friendsToken+"/page"), baseline["page data"]; got.code != want.code || got.body != want.body {
				t.Errorf("a POST: %d %q", got.code, got.body)
			}

			f.set(true, true)
			same("a machine that can't answer", friendsToken)
		})
	}
}

func headersEqual(a, b http.Header) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if !slices.Equal(v, b[k]) {
			return false
		}
	}
	return true
}

func TestFriendsPackPagesAreLimitedPerAddress(t *testing.T) {
	f := newFriendsAgent(t)
	e := newEnvAgent(t, f.handler, io.Discard)
	h := e.srv.public.handler(share.PathPrefix)
	send := func(remote string, hdr map[string]string) int {
		req := httptest.NewRequest("GET", share.PathPrefix+"not-a-token/page", nil)
		req.RemoteAddr = remote
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}
	for i := range friendsPackLimits.perMinute {
		if code := send("192.0.2.10:1000", map[string]string{"X-Forwarded-For": "198.51.100." + string(rune('0'+i%10))}); code != http.StatusNotFound {
			t.Fatalf("request %d: %d", i+1, code)
		}
	}
	if code := send("192.0.2.10:1001", map[string]string{"X-Forwarded-For": "198.51.100.99", "X-Real-IP": "198.51.100.98", "Forwarded": "for=198.51.100.97"}); code != http.StatusTooManyRequests {
		t.Errorf("one more from the same address, with forwarded headers: %d", code)
	}
	if code := send("192.0.2.11:1000", nil); code != http.StatusNotFound {
		t.Errorf("another address: %d", code)
	}
}
