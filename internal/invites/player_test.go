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

func TestRedeemPlayer(t *testing.T) {
	inv, code := newPlayer(t, PlayerSpec{})
	lookup := newLookup()
	got, err := RedeemPlayer(context.Background(), lookup, inv, code, "nOTCH", t0)
	if err != nil {
		t.Fatal(err)
	}
	if got != (PlayerGrant{InviteID: inv.ID, ServerID: serverID, Profile: notch}) {
		t.Errorf("got %+v", got)
	}
	if len(lookup.calls) != 1 || lookup.calls[0] != "nOTCH" {
		t.Errorf("lookups %v", lookup.calls)
	}
}

func TestRedeemPlayerRefusals(t *testing.T) {
	inv, code := newPlayer(t, PlayerSpec{})
	member, memberCode := memberInvite(t, owner, RoleViewer)
	used := inv
	used.Uses = used.MaxUses
	for _, tc := range []struct {
		name      string
		inv       Invite
		link      string
		player    string
		lookupErr error
		code      string
		lookups   int
	}{
		{"wrong code", inv, NewCode(), "Notch", nil, CodeNotWorking, 0},
		{"co-admin invite used as a friend invite", member, memberCode, "Notch", nil, CodeNotWorking, 0},
		{"revoked", Revoke(inv, t0), code, "Notch", nil, CodeNotWorking, 0},
		{"used up", used, code, "Notch", nil, CodeUsedUp, 0},
		{"expired", inv, code, "Notch", nil, CodeExpired, 0},
		{"no name", inv, code, "", nil, CodePlayerName, 0},
		{"name too short", inv, code, "ab", nil, CodePlayerName, 0},
		{"name too long", inv, code, "abcdefghijklmnopq", nil, CodePlayerName, 0},
		{"name with a space", inv, code, "Not ch", nil, CodePlayerName, 0},
		{"name with a slash", inv, code, "../Notch", nil, CodePlayerName, 0},
		{"console command in the name", inv, code, "Notch;op", nil, CodePlayerName, 0},
		{"unknown name", inv, code, "Nobody_here", nil, CodePlayerUnknown, 1},
		{"demo account", inv, code, "pipdemo42", nil, CodePlayerDemo, 1},
		{"legacy account", inv, code, "OldTimer", nil, CodePlayerLegacy, 1},
		{"Mojang rate limited", inv, code, "Notch", &mojang.Error{Err: mojang.ErrRateLimited, RetryAfter: 42 * time.Second}, CodeMojangBusy, 1},
		{"Mojang unavailable", inv, code, "Notch", &mojang.Error{Err: mojang.ErrUnavailable, Detail: "HTTP 500"}, CodeMojangDown, 1},
		{"lookup canceled", inv, code, "Notch", context.Canceled, CodeMojangDown, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lookup := newLookup()
			lookup.err = tc.lookupErr
			now := t0
			if tc.code == CodeExpired {
				now = inv.ExpiresAt
			}
			_, err := RedeemPlayer(context.Background(), lookup, tc.inv, tc.link, tc.player, now)
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
		_, err := RedeemPlayer(context.Background(), lookup, inv, code, name, t0)
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
	got, err := RedeemPlayer(context.Background(), client, inv, code, "NOTCH", t0)
	if err != nil || got.Profile != notch {
		t.Fatalf("got %+v, %v", got, err)
	}
	_, err = RedeemPlayer(context.Background(), client, inv, code, "Nobody_here", t0)
	wantCode(t, err, CodePlayerUnknown)
}

func TestJoin(t *testing.T) {
	info, err := Join(Server{Name: " Pip's world ", Host: "203.0.113.10", Port: 25566, Version: "1.21.11"}, notch)
	if err != nil {
		t.Fatal(err)
	}
	if info.Player != "Notch" || info.Server != "Pip's world" || info.Address != "203.0.113.10:25566" || info.Version != "1.21.11" {
		t.Errorf("info %+v", info)
	}
	want := []Step{
		{Key: "invite.join.open", Params: map[string]string{"version": "1.21.11", "player": "Notch"},
			Text: "Start Minecraft: Java Edition 1.21.11, signed in as Notch."},
		{Key: "invite.join.addServer", Text: "Choose Multiplayer, then Add Server."},
		{Key: "invite.join.enterAddress", Params: map[string]string{"server": "Pip's world", "address": "203.0.113.10:25566"},
			Text: "Enter Pip's world as the Server Name and 203.0.113.10:25566 as the Server Address, then choose Done."},
		{Key: "invite.join.connect", Params: map[string]string{"server": "Pip's world"},
			Text: "Select Pip's world in the list and choose Join Server."},
	}
	got, _ := json.Marshal(info.Steps)
	wantJSON, _ := json.Marshal(want)
	if string(got) != string(wantJSON) {
		t.Errorf("steps\n got %s\nwant %s", got, wantJSON)
	}

	info, err = Join(Server{Name: "Pip's world", Host: "mc.example.org", Port: 25565}, jeb)
	if err != nil {
		t.Fatal(err)
	}
	if info.Address != "mc.example.org" || info.Steps[0].Key != "invite.join.openAnyVersion" || info.Steps[0].Text != "Start Minecraft: Java Edition, signed in as jeb_." {
		t.Errorf("info %+v", info)
	}

	if _, err := Join(Server{Name: "Pip's world", Host: "mc.example.org/phish", Port: 25565}, jeb); err == nil {
		t.Error("a host with a path was accepted")
	}
}

// The cases of joinAddress in web/src/lib/lib.test.ts, and the addresses
// it must refuse.
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
