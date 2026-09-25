package invites

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/CIYAhq/playkeeper/internal/mojang"
)

var _ ProfileLookup = (*mojang.Client)(nil)

var (
	notch = mojang.Profile{ID: "069a79f4-44e9-4726-a5be-fca90e38aaf5", Name: "Notch"}
	jeb   = mojang.Profile{ID: "853c80ef-3c37-49fd-aa49-938b674adae6", Name: "jeb_"}
)

var survival = Server{Name: " Survival ", Address: "survival.alex.playkeeper.io", Version: "26.1.2", Online: true, Playing: 3}

type fakeLookup struct {
	mu       sync.Mutex
	profiles map[string]mojang.Profile
	err      error
	calls    []string
}

func (f *fakeLookup) Lookup(_ context.Context, name string) (mojang.Profile, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, name)
	if f.err != nil {
		return mojang.Profile{}, f.err
	}
	if p, ok := f.profiles[strings.ToLower(name)]; ok {
		return p, nil
	}
	return mojang.Profile{}, &mojang.Error{Err: mojang.ErrNotFound}
}

func newLookup() *fakeLookup {
	return &fakeLookup{profiles: map[string]mojang.Profile{
		"notch":     notch,
		"pipdemo42": {ID: "5f1d3c2a-9b8e-4d7c-ae6f-0b1a2c3d4e5f", Name: "PipDemo42", Demo: true},
		"oldtimer":  {ID: "0badc0de-0000-4000-8000-000000000001", Name: "OldTimer", Legacy: true},
	}}
}

func TestPreviewPlayer(t *testing.T) {
	inv, code := newPlayer(t, PlayerSpec{Label: "Secret plans"})
	p, err := PreviewPlayer(inv, code, owner, survival, t0)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(p)
	if string(b) != `{"kind":"player","inviter":"siya","server":"Survival","version":"26.1.2","online":true,"playing":3,"approval":"right_away"}` {
		t.Errorf("preview %s", b)
	}

	for _, tc := range []struct {
		name    string
		srv     func(*Server)
		online  bool
		playing int
	}{
		{"offline", func(s *Server) { s.Online = false }, false, 0},
		{"offline with a stale count", func(s *Server) { s.Online = false; s.Playing = 4 }, false, 0},
		{"online and empty", func(s *Server) { s.Playing = 0 }, true, 0},
		{"a negative count", func(s *Server) { s.Playing = -2 }, true, 0},
	} {
		srv := survival
		tc.srv(&srv)
		p, err := PreviewPlayer(inv, code, owner, srv, t0)
		if err != nil || p.Online != tc.online || p.Playing != tc.playing {
			t.Errorf("%s: %+v, %v", tc.name, p, err)
		}
	}

	waiting, waitingCode := newPlayer(t, PlayerSpec{Approval: AfterYes})
	for name, row := range map[string]Invite{
		"after you say yes":  waiting,
		"no approval stored": func() Invite { i := waiting; i.Approval = ""; return i }(),
		"approval in capitals": func() Invite {
			i := waiting
			i.Approval = "RIGHT_AWAY"
			return i
		}(),
	} {
		if p, err := PreviewPlayer(row, waitingCode, owner, survival, t0); err != nil || p.Approval != AfterYes {
			t.Errorf("%s: %+v, %v", name, p, err)
		}
	}
}

func TestPreviewPlayerRefusals(t *testing.T) {
	inv, code := newPlayer(t, PlayerSpec{MaxUses: 2})
	used := inv
	used.Uses = 2
	c, err := NewPlayer(PlayerSpec{ServerID: serverID, ProjectID: projectID}, moderator, t0)
	if err != nil {
		t.Fatal(err)
	}
	modInv, modCode := c.Invite, codeOf(t, c)
	member, memberCode := memberInvite(t, owner, RoleViewer, AllServers())
	for _, tc := range []struct {
		name    string
		inv     Invite
		code    string
		creator Account
		now     time.Time
		want    string
	}{
		{"works for the owner's invite", inv, code, owner, t0, ""},
		{"works for a moderator's invite", modInv, modCode, moderator, t0, ""},
		{"wrong code", inv, NewCode(), owner, t0, CodeNotWorking},
		{"co-admin invite used as a friend invite", member, memberCode, owner, t0, CodeNotWorking},
		{"revoked", Revoke(inv, t0), code, owner, t0, CodeNotWorking},
		{"creator was deleted", modInv, modCode, Account{}, t0, CodeNotWorking},
		{"creator is now a viewer", modInv, modCode, with(moderator, func(a *Account) { a.ProjectRole = RoleViewer }), t0, CodeNotWorking},
		{"creator left the project", modInv, modCode, with(moderator, func(a *Account) { a.ProjectRole = "" }), t0, CodeNotWorking},
		{"creator lost the server", modInv, modCode, with(moderator, func(a *Account) { a.Servers = OnlyServers(otherServer) }), t0, CodeNotWorking},
		{"someone else passed as creator", inv, code, admin, t0, CodeNotWorking},
		{"creator was deleted after it expired", modInv, modCode, Account{}, modInv.ExpiresAt, CodeNotWorking},
		{"creator was deleted after it was used up", func() Invite { i := modInv; i.Uses = i.MaxUses; return i }(), modCode, Account{}, t0, CodeNotWorking},
		{"expired", inv, code, owner, inv.ExpiresAt, CodeExpired},
		{"used up", used, code, owner, t0, CodeUsedUp},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := PreviewPlayer(tc.inv, tc.code, tc.creator, survival, tc.now)
			if tc.want == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			e := wantCode(t, err, tc.want)
			if tc.want == CodeNotWorking && (e.Msg != NotFound().Msg || e.Params != nil) {
				t.Errorf("refusal %+v gives away more than an unknown link would", e)
			}
		})
	}
}

// A link that ran out says so as the design does, naming who to ask.
func TestRunOut(t *testing.T) {
	inv, code := newPlayer(t, PlayerSpec{})
	used := inv
	used.Uses = 5
	hint := "Ask siya for a new link. If you're already on the list, just join with the address you have."

	_, err := PreviewPlayer(used, code, owner, survival, t0)
	e := wantCode(t, err, CodeUsedUp)
	if e.Msg != "This invite was for 5 friends, and they've all joined." || e.Hint != hint || e.Status != 410 ||
		e.Params["inviter"] != "siya" || e.Params["maxUses"] != "5" || len(e.Params) != 2 {
		t.Errorf("used up: %+v", e)
	}

	one, oneCode := newPlayer(t, PlayerSpec{MaxUses: 1})
	one.Uses = 1
	_, err = PreviewPlayer(one, oneCode, owner, survival, t0)
	if e := wantCode(t, err, CodeUsedUp); e.Msg != "This invite was for one friend, and they've joined." || e.Params["maxUses"] != "1" {
		t.Errorf("used up, one friend: %+v", e)
	}

	_, err = PreviewPlayer(inv, code, owner, survival, inv.ExpiresAt)
	e = wantCode(t, err, CodeExpired)
	if e.Msg != "This invite link has expired." || e.Hint != hint || e.Status != 410 ||
		e.Params["inviter"] != "siya" || e.Params["expiredAt"] != "2026-10-02T15:04:51Z" {
		t.Errorf("expired: %+v", e)
	}

	nameless := with(owner, func(a *Account) { a.Name = "" })
	_, err = PreviewPlayer(used, code, nameless, survival, t0)
	if e := wantCode(t, err, CodeUsedUp); e.Hint != "Ask the person who sent it for a new link. If you're already on the list, just join with the address you have." {
		t.Errorf("no name for the inviter: %+v", e)
	}

	lookup := newLookup()
	_, err = LookupPlayer(context.Background(), lookup, used, code, owner, "Notch", t0)
	if e := wantCode(t, err, CodeUsedUp); e.Hint != hint {
		t.Errorf("lookup: %+v", e)
	}
	_, err = RedeemPlayer(context.Background(), lookup, inv, code, owner, "Notch", inv.ExpiresAt)
	if e := wantCode(t, err, CodeExpired); e.Hint != hint {
		t.Errorf("redeem: %+v", e)
	}
	if len(lookup.calls) != 0 {
		t.Errorf("a link that ran out cost %d lookups", len(lookup.calls))
	}
}

func TestLookupPlayer(t *testing.T) {
	inv, code := newPlayer(t, PlayerSpec{})
	lookup := newLookup()
	c, err := LookupPlayer(context.Background(), lookup, inv, code, owner, "nOTCH", t0)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(c)
	if c != (Candidate{Name: "Notch", UUID: notch.ID}) || string(b) != `{"name":"Notch","uuid":"069a79f4-44e9-4726-a5be-fca90e38aaf5"}` {
		t.Errorf("candidate %s", b)
	}
	if len(lookup.calls) != 1 || lookup.calls[0] != "nOTCH" {
		t.Errorf("lookups %v", lookup.calls)
	}

	for _, tc := range []struct {
		name    string
		player  string
		code    string
		lookups int
	}{
		{"unknown name", "Nobody_here", CodePlayerUnknown, 1},
		{"demo account", "pipdemo42", CodePlayerDemo, 1},
		{"legacy account", "OldTimer", CodePlayerLegacy, 1},
		{"name too short", "ab", CodePlayerName, 0},
		{"name half typed with a space", "Not ", CodePlayerName, 0},
	} {
		lookup := newLookup()
		_, err := LookupPlayer(context.Background(), lookup, inv, code, owner, tc.player, t0)
		if CodeOf(err) != tc.code || len(lookup.calls) != tc.lookups {
			t.Errorf("%s: %v after %d lookups", tc.name, err, len(lookup.calls))
		}
	}

	lookup = newLookup()
	_, err = LookupPlayer(context.Background(), lookup, inv, NewCode(), owner, "Notch", t0)
	wantCode(t, err, CodeNotWorking)
	_, err = LookupPlayer(context.Background(), lookup, inv, code, Account{}, "Notch", t0)
	wantCode(t, err, CodeNotWorking)
	if len(lookup.calls) != 0 {
		t.Errorf("a link that doesn't work cost %d lookups", len(lookup.calls))
	}
}

// Whatever a lookup answers, only a valid name and UUID go on.
func TestLookupAnswerChecked(t *testing.T) {
	inv, code := newPlayer(t, PlayerSpec{})
	for name, p := range map[string]mojang.Profile{
		"UUID not a UUID":     {ID: "not-a-uuid", Name: "Notch"},
		"UUID in capitals":    {ID: strings.ToUpper(notch.ID), Name: "Notch"},
		"UUID without dashes": {ID: strings.ReplaceAll(notch.ID, "-", ""), Name: "Notch"},
		"no UUID":             {Name: "Notch"},
		"name with a space":   {ID: notch.ID, Name: "Not ch"},
		"console command":     {ID: notch.ID, Name: "Notch;op"},
		"no name":             {ID: notch.ID},
	} {
		lookup := newLookup()
		lookup.profiles["notch"] = p
		_, err := LookupPlayer(context.Background(), lookup, inv, code, owner, "Notch", t0)
		e := wantCode(t, err, CodeMojangDown)
		if strings.Contains(e.Msg+e.Hint, p.Name) && p.Name != "" {
			t.Errorf("%s: the refusal repeats the answer: %+v", name, e)
		}
		if _, err := RedeemPlayer(context.Background(), lookup, inv, code, owner, "Notch", t0); CodeOf(err) != CodeMojangDown {
			t.Errorf("%s: redeem gave %v", name, err)
		}
	}
}

func TestRedeemPlayer(t *testing.T) {
	inv, code := newPlayer(t, PlayerSpec{})
	lookup := newLookup()
	r, err := RedeemPlayer(context.Background(), lookup, inv, code, owner, "nOTCH", t0)
	if err != nil {
		t.Fatal(err)
	}
	if r != (Redemption{InviteID: inv.ID, ServerID: serverID, Profile: notch}) {
		t.Errorf("got %+v", r)
	}
	if g, ok := r.Grant(); !ok || g != (PlayerGrant{InviteID: inv.ID, ServerID: serverID, Profile: notch}) {
		t.Errorf("grant %+v, %v", g, ok)
	}
	if len(lookup.calls) != 1 || lookup.calls[0] != "nOTCH" {
		t.Errorf("lookups %v", lookup.calls)
	}

	waiting, waitingCode := newPlayer(t, PlayerSpec{Approval: AfterYes})
	unreadable := waiting
	unreadable.Approval = "later"
	for name, row := range map[string]Invite{"after you say yes": waiting, "unreadable approval": unreadable} {
		r, err := RedeemPlayer(context.Background(), newLookup(), row, waitingCode, owner, "Notch", t0)
		if err != nil || !r.Wait || r.Profile != notch {
			t.Fatalf("%s: %+v, %v", name, r, err)
		}
		if g, ok := r.Grant(); ok || g != (PlayerGrant{}) {
			t.Errorf("%s: a redemption that waits gave a grant %+v", name, g)
		}
	}
}

func TestRedeemPlayerRefusals(t *testing.T) {
	inv, code := newPlayer(t, PlayerSpec{})
	member, memberCode := memberInvite(t, owner, RoleViewer, AllServers())
	used := inv
	used.Uses = used.MaxUses
	for _, tc := range []struct {
		name      string
		inv       Invite
		link      string
		creator   Account
		player    string
		lookupErr error
		code      string
		lookups   int
	}{
		{"wrong code", inv, NewCode(), owner, "Notch", nil, CodeNotWorking, 0},
		{"co-admin invite used as a friend invite", member, memberCode, owner, "Notch", nil, CodeNotWorking, 0},
		{"revoked", Revoke(inv, t0), code, owner, "Notch", nil, CodeNotWorking, 0},
		{"creator can't let players in", inv, code, Account{}, "Notch", nil, CodeNotWorking, 0},
		{"used up", used, code, owner, "Notch", nil, CodeUsedUp, 0},
		{"expired", inv, code, owner, "Notch", nil, CodeExpired, 0},
		{"no name", inv, code, owner, "", nil, CodePlayerName, 0},
		{"name too short", inv, code, owner, "ab", nil, CodePlayerName, 0},
		{"name too long", inv, code, owner, "abcdefghijklmnopq", nil, CodePlayerName, 0},
		{"name with a space", inv, code, owner, "Not ch", nil, CodePlayerName, 0},
		{"name with a slash", inv, code, owner, "../Notch", nil, CodePlayerName, 0},
		{"console command in the name", inv, code, owner, "Notch;op", nil, CodePlayerName, 0},
		{"unknown name", inv, code, owner, "Nobody_here", nil, CodePlayerUnknown, 1},
		{"demo account", inv, code, owner, "pipdemo42", nil, CodePlayerDemo, 1},
		{"legacy account", inv, code, owner, "OldTimer", nil, CodePlayerLegacy, 1},
		{"Mojang rate limited", inv, code, owner, "Notch", &mojang.Error{Err: mojang.ErrRateLimited, RetryAfter: 42 * time.Second}, CodeMojangBusy, 1},
		{"Mojang unavailable", inv, code, owner, "Notch", &mojang.Error{Err: mojang.ErrUnavailable, Detail: "HTTP 500"}, CodeMojangDown, 1},
		{"lookup canceled", inv, code, owner, "Notch", context.Canceled, CodeMojangDown, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lookup := newLookup()
			lookup.err = tc.lookupErr
			now := t0
			if tc.code == CodeExpired {
				now = inv.ExpiresAt
			}
			_, err := RedeemPlayer(context.Background(), lookup, tc.inv, tc.link, tc.creator, tc.player, now)
			wantCode(t, err, tc.code)
			if len(lookup.calls) != tc.lookups {
				t.Errorf("%d lookups, want %d", len(lookup.calls), tc.lookups)
			}
		})
	}
}

func TestRedeemPlayerMessages(t *testing.T) {
	inv, code := newPlayer(t, PlayerSpec{})
	redeem := func(name string, lookupErr error) *Error {
		t.Helper()
		lookup := newLookup()
		lookup.err = lookupErr
		_, err := RedeemPlayer(context.Background(), lookup, inv, code, owner, name, t0)
		var e *Error
		if !errors.As(err, &e) {
			t.Fatalf("error %v is not an *Error", err)
		}
		return e
	}

	e := redeem("Nobody_here", nil)
	if e.Msg != "No Minecraft: Java Edition account is called Nobody_here." || e.Params["name"] != "Nobody_here" || e.Status != 422 {
		t.Errorf("unknown player: %+v", e)
	}
	e = redeem("pipdemo42", nil)
	if e.Params["name"] != "PipDemo42" || !strings.HasPrefix(e.Msg, "PipDemo42 hasn't bought") {
		t.Errorf("demo account: %+v", e)
	}

	e = redeem("Notch", &mojang.Error{Err: mojang.ErrRateLimited, RetryAfter: 42 * time.Second})
	if e.RetryAfter != 42*time.Second || e.Params["seconds"] != "42" || e.Status != 503 {
		t.Errorf("rate limited: %+v", e)
	}
	if e.Hint != "Try again in a minute." {
		t.Errorf("hint %q", e.Hint)
	}
	e = redeem("Notch", &mojang.Error{Err: mojang.ErrRateLimited})
	if e.RetryAfter != time.Minute {
		t.Errorf("rate limited without a wait: RetryAfter %v", e.RetryAfter)
	}
	e = redeem("Notch", &mojang.Error{Err: mojang.ErrRateLimited, RetryAfter: 10 * time.Minute})
	if e.Hint != "Try again in a few minutes." || e.Params["seconds"] != "600" {
		t.Errorf("long wait: %+v", e)
	}

	cause := &mojang.Error{Err: mojang.ErrUnavailable, Detail: "HTTP 500"}
	e = redeem("Notch", cause)
	if !errors.Is(e, mojang.ErrUnavailable) || e.Reason != cause.Error() || strings.Contains(e.Msg, "500") {
		t.Errorf("unavailable: %+v; the detail belongs in Reason, not the message", e)
	}
}

// The package's lookup fits the real client, answering like Mojang does.
func TestRedeemPlayerWithMojangClient(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.EqualFold(r.URL.Path, "/minecraft/profile/lookup/name/notch") {
			w.Write([]byte("{\n  \"id\" : \"069a79f444e94726a5befca90e38aaf5\",\n  \"name\" : \"Notch\"\n}"))
			return
		}
		name := strings.TrimPrefix(r.URL.Path, "/minecraft/profile/lookup/name/")
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte("{\n  \"path\" : \"" + r.URL.Path + "\",\n  \"errorMessage\" : \"Couldn't find any profile with name " + name + "\"\n}"))
	}))
	defer srv.Close()
	client, err := mojang.NewClient(mojang.Options{BaseURL: srv.URL, HTTP: srv.Client(), Now: func() time.Time { return t0 }})
	if err != nil {
		t.Fatal(err)
	}
	inv, code := newPlayer(t, PlayerSpec{})
	got, err := RedeemPlayer(context.Background(), client, inv, code, owner, "NOTCH", t0)
	if err != nil || got.Profile != notch {
		t.Fatalf("got %+v, %v", got, err)
	}
	c, err := LookupPlayer(context.Background(), client, inv, code, owner, "notch", t0)
	if err != nil || c != (Candidate{Name: "Notch", UUID: notch.ID}) {
		t.Fatalf("candidate %+v, %v", c, err)
	}
	_, err = RedeemPlayer(context.Background(), client, inv, code, owner, "Nobody_here", t0)
	wantCode(t, err, CodePlayerUnknown)
}

var joinSteps = []Step{
	{Key: "invite.join.open", Params: map[string]string{"version": "26.1.2"}, Text: "Open Minecraft: Java Edition 26.1.2."},
	{Key: "invite.join.addServer", Text: "Pick Multiplayer, then Add Server."},
	{Key: "invite.join.paste", Text: "Paste the address and press Done, then Join."},
}

func sameSteps(t *testing.T, got, want []Step) {
	t.Helper()
	g, _ := json.Marshal(got)
	w, _ := json.Marshal(want)
	if string(g) != string(w) {
		t.Errorf("steps\n got %s\nwant %s", g, w)
	}
}

func TestJoin(t *testing.T) {
	info, err := Join(survival, notch)
	if err != nil {
		t.Fatal(err)
	}
	if info.Player != "Notch" || info.Server != "Survival" || info.Address != "survival.alex.playkeeper.io" || info.Version != "26.1.2" || info.Waiting {
		t.Errorf("info %+v", info)
	}
	sameSteps(t, info.Steps, joinSteps)
	if b, _ := json.Marshal(info); strings.Contains(string(b), "waiting") {
		t.Errorf("JSON %s", b)
	}

	info, err = Join(Server{Name: "Pip's world", Address: "203.0.113.10:25566"}, jeb)
	if err != nil {
		t.Fatal(err)
	}
	if info.Address != "203.0.113.10:25566" || info.Steps[0].Key != "invite.join.openAnyVersion" || info.Steps[0].Text != "Open Minecraft: Java Edition." {
		t.Errorf("info %+v", info)
	}

	for _, addr := range []string{"", "mc.example.org/phish", "mc.example.org:25565", "2001:db8::1", "[2001:db8::1]:25565", "[mc.example.org]",
		"mc.example.org:", "mc.example.org:+25566", "mc.example.org:025566", "mc.example.org:0", "mc.example.org:65536", "[fe80::1%eth0]",
		"mc example.org", "https://mc.example.org", "mc.example.org\n"} {
		if _, err := Join(Server{Name: "Survival", Address: addr}, notch); err == nil {
			t.Errorf("address %q was accepted", addr)
		}
	}
}

func TestWait(t *testing.T) {
	info, err := Wait(survival, notch, "siya")
	if err != nil {
		t.Fatal(err)
	}
	if !info.Waiting || info.Player != "Notch" || info.Address != "survival.alex.playkeeper.io" {
		t.Errorf("info %+v", info)
	}
	wait := Step{Key: "invite.join.wait", Params: map[string]string{"inviter": "siya"}, Text: "Wait for siya to let you in."}
	sameSteps(t, info.Steps, append([]Step{wait}, joinSteps...))
	if b, _ := json.Marshal(info); !strings.Contains(string(b), `"waiting":true`) {
		t.Errorf("JSON %s", b)
	}

	info, err = Wait(survival, notch, "")
	if err != nil {
		t.Fatal(err)
	}
	sameSteps(t, info.Steps[:1], []Step{{Key: "invite.join.waitAnyone", Text: "Wait for the person who sent the link to let you in."}})
	if _, err := Wait(Server{Address: "mc.example.org/phish"}, notch, "siya"); err == nil {
		t.Error("a bad address was accepted")
	}
}

// The cases of joinAddress in web/src/lib/lib.test.ts, and the addresses
// it must refuse. Every address it writes is one the join page accepts.
func TestJoinAddress(t *testing.T) {
	for _, tc := range []struct {
		host string
		port int
		want string
	}{
		{"203.0.113.10", 25565, "203.0.113.10"},
		{"203.0.113.10", 25566, "203.0.113.10:25566"},
		{"2001:db8::1", 25565, "[2001:db8::1]"},
		{"2001:db8::1", 25570, "[2001:db8::1]:25570"},
		{"mc.example.org", 25565, "mc.example.org"},
		{"MC.Example.org", 1, "MC.Example.org:1"},
		{"minecraft", 65535, "minecraft:65535"},
		{"", 25565, ""},
		{"mc.example.org", 0, ""},
		{"mc.example.org", 65536, ""},
		{"mc example.org", 25565, ""},
		{"mc.example.org:25565", 25565, ""},
		{"[2001:db8::1]", 25565, ""},
		{"fe80::1%eth0", 25565, ""},
		{"-mc.example.org", 25565, ""},
		{"mc-.example.org", 25565, ""},
		{"mc..example.org", 25565, ""},
		{"mc.example.org.", 25565, ""},
		{strings.Repeat("a", 64) + ".example.org", 25565, ""},
		{strings.Repeat("a.", 127) + "org", 25565, ""},
		{"mc_example.org", 25565, ""},
		{"mç.example.org", 25565, ""},
	} {
		got, err := JoinAddress(tc.host, tc.port)
		if got != tc.want || (err == nil) != (tc.want != "") {
			t.Errorf("JoinAddress(%q, %d) = %q, %v; want %q", tc.host, tc.port, got, err, tc.want)
		}
		if tc.want != "" && !validAddress(tc.want) {
			t.Errorf("the join page refuses %q", tc.want)
		}
	}
}

// whitelist.json as a Minecraft server writes it.
const minecraftWhitelist = `[
  {
    "uuid": "853c80ef-3c37-49fd-aa49-938b674adae6",
    "name": "jeb_"
  }
]`

func TestAddToWhitelist(t *testing.T) {
	both := `[
  {
    "uuid": "853c80ef-3c37-49fd-aa49-938b674adae6",
    "name": "jeb_"
  },
  {
    "uuid": "069a79f4-44e9-4726-a5be-fca90e38aaf5",
    "name": "Notch"
  }
]`
	onlyNotch := `[
  {
    "uuid": "069a79f4-44e9-4726-a5be-fca90e38aaf5",
    "name": "Notch"
  }
]`
	for _, tc := range []struct {
		name, file, want string
		changed          bool
	}{
		{"no file yet", "", onlyNotch, true},
		{"blank file", " \n", onlyNotch, true},
		{"empty list", "[]", onlyNotch, true},
		{"another player listed", minecraftWhitelist, both, true},
		{"compact file", `[{"uuid":"853c80ef-3c37-49fd-aa49-938b674adae6","name":"jeb_"}]`, both, true},
		{"already listed", both, both, false},
		{"already listed in capitals without hyphens", `[{"uuid":"069A79F444E94726A5BEFCA90E38AAF5","name":"Notch"}]`, `[{"uuid":"069A79F444E94726A5BEFCA90E38AAF5","name":"Notch"}]`, false},
		{"listed by name only", `[{"name":"notch"}]`, onlyNotch, true},
		{"old owner of the name listed", `[{"uuid":"00000000-0000-4000-8000-000000000000","name":"Notch"}]`, `[
  {
    "uuid": "00000000-0000-4000-8000-000000000000",
    "name": "Notch"
  },
  {
    "uuid": "069a79f4-44e9-4726-a5be-fca90e38aaf5",
    "name": "Notch"
  }
]`, true},
		{"extra fields kept", `[{"uuid":"853c80ef-3c37-49fd-aa49-938b674adae6","name":"jeb_","note":"founder"}]`, `[
  {
    "uuid": "853c80ef-3c37-49fd-aa49-938b674adae6",
    "name": "jeb_",
    "note": "founder"
  },
  {
    "uuid": "069a79f4-44e9-4726-a5be-fca90e38aaf5",
    "name": "Notch"
  }
]`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, changed, err := AddToWhitelist([]byte(tc.file), notch)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tc.want || changed != tc.changed {
				t.Errorf("got %v\n%s\nwant %v\n%s", changed, got, tc.changed, tc.want)
			}
		})
	}
}

func TestAddToWhitelistRefuses(t *testing.T) {
	for name, file := range map[string]string{
		"an object":          `{"uuid":"853c80ef-3c37-49fd-aa49-938b674adae6","name":"jeb_"}`,
		"not JSON":           `jeb_`,
		"cut off":            `[{"uuid":"853c80ef-3c37-49fd`,
		"a number in it":     `[42]`,
		"a string in it":     `["jeb_"]`,
		"a numeric uuid":     `[{"uuid":42,"name":"jeb_"}]`,
		"larger than allows": "[" + strings.Repeat(" ", MaxWhitelistBytes) + "]",
	} {
		if _, _, err := AddToWhitelist([]byte(file), notch); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	for _, p := range []mojang.Profile{{ID: "not-a-uuid", Name: "Notch"}, {ID: notch.ID, Name: "Not ch"}, {}} {
		if _, _, err := AddToWhitelist(nil, p); err == nil {
			t.Errorf("profile %+v accepted", p)
		}
	}
}
