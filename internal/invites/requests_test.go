package invites

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/CIYAhq/playkeeper/internal/mojang"
)

var pixelPia = mojang.Profile{ID: "3c1e8f2a-7b4d-4e6f-9a0b-1c2d3e4f5a6b", Name: "PixelPia"}

const piaAddress = "203.0.113.7"

// asked returns an invite named "School friends" that waits for a yes, and
// PixelPia's request made with it.
func asked(t *testing.T) (Invite, JoinRequest) {
	t.Helper()
	inv, code := newPlayer(t, PlayerSpec{Label: "School friends", Approval: AfterYes})
	lookup := newLookup()
	lookup.profiles["pixelpia"] = pixelPia
	r, err := RedeemPlayer(context.Background(), lookup, inv, code, owner, "pixelpia", t0)
	if err != nil {
		t.Fatal(err)
	}
	req, err := NewJoinRequest(r, piaAddress, Pending{}, t0)
	if err != nil {
		t.Fatal(err)
	}
	return inv, req
}

func TestNewJoinRequest(t *testing.T) {
	inv, req := asked(t)
	want := JoinRequest{ID: req.ID, InviteID: inv.ID, ServerID: serverID, PlayerName: "PixelPia", PlayerUUID: pixelPia.ID,
		Address: piaAddress, State: RequestPending, CreatedAt: stamp(t0)}
	if req != want || !ValidID(req.ID) {
		t.Errorf("got  %+v\nwant %+v", req, want)
	}
	if _, again := asked(t); again.ID == req.ID {
		t.Error("two requests got the same id")
	}
	b, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	wantJSON := `{"id":"` + req.ID + `","inviteId":"` + inv.ID + `","serverId":"` + serverID +
		`","playerName":"PixelPia","playerUuid":"` + pixelPia.ID + `","state":"pending","createdAt":"2026-09-25T15:04:51.123Z"}`
	if string(b) != wantJSON {
		t.Errorf("JSON %s\nwant %s; the address stays out of answers", b, wantJSON)
	}
}

func TestNewJoinRequestNeedsAWait(t *testing.T) {
	r := Redemption{InviteID: "abcdefghij", ServerID: serverID, Profile: pixelPia}
	if _, err := NewJoinRequest(r, piaAddress, Pending{}, t0); err == nil || CodeOf(err) != "" {
		t.Errorf("a redemption that lets the player in right away made a request (%v)", err)
	}
	r.Wait = true
	if _, err := NewJoinRequest(r, "", Pending{}, t0); err == nil || CodeOf(err) != "" {
		t.Errorf("a request without an address was made (%v)", err)
	}
}

func TestNewJoinRequestLimits(t *testing.T) {
	r := Redemption{InviteID: "abcdefghij", ServerID: serverID, Profile: pixelPia, Wait: true}
	for _, tc := range []struct {
		name    string
		pending Pending
		scope   string
		msg     string
	}{
		{"none pending", Pending{}, "", ""},
		{"just under both limits", Pending{Invite: MaxPendingPerInvite - 1, Address: MaxPendingPerAddress - 1}, "", ""},
		{"invite full", Pending{Invite: MaxPendingPerInvite}, "invite", "Too many people are waiting to join with this link."},
		{"address full", Pending{Address: MaxPendingPerAddress}, "address", "Too many people on your network are waiting to join."},
		{"both full", Pending{Invite: MaxPendingPerInvite + 3, Address: MaxPendingPerAddress + 3}, "invite", "Too many people are waiting to join with this link."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewJoinRequest(r, piaAddress, tc.pending, t0)
			if tc.scope == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			e := wantCode(t, err, CodeRequestsFull)
			if e.Status != 429 || e.Params["scope"] != tc.scope || e.Msg != tc.msg {
				t.Errorf("%+v", e)
			}
		})
	}
}

func TestApprove(t *testing.T) {
	inv, req := asked(t)
	later := t0.Add(5 * time.Minute)
	done, grant, err := Approve(req, moderator, later)
	if err != nil {
		t.Fatal(err)
	}
	want := req
	want.State, want.DecidedAt, want.DecidedBy, want.Address = RequestApproved, stamp(later), moderator.UserID, ""
	if done != want {
		t.Errorf("got  %+v\nwant %+v", done, want)
	}
	if grant != (PlayerGrant{InviteID: inv.ID, ServerID: serverID, Profile: pixelPia, RequestID: req.ID}) {
		t.Errorf("grant %+v", grant)
	}
	b, _ := json.Marshal(done)
	if !strings.HasSuffix(string(b), `"state":"approved","createdAt":"2026-09-25T15:04:51.123Z","decidedAt":"2026-09-25T15:09:51.123Z","decidedBy":3}`) {
		t.Errorf("JSON %s", b)
	}

	_, _, err = Approve(done, owner, later)
	if e := wantCode(t, err, CodeRequestDecided); e.Status != 409 {
		t.Errorf("status %d", e.Status)
	}
	_, err = Decline(done, owner, later)
	wantCode(t, err, CodeRequestDecided)
}

func TestDecline(t *testing.T) {
	_, req := asked(t)
	later := t0.Add(time.Hour)
	done, err := Decline(req, owner, later)
	if err != nil {
		t.Fatal(err)
	}
	want := req
	want.State, want.DecidedAt, want.DecidedBy, want.Address = RequestDeclined, stamp(later), owner.UserID, ""
	if done != want {
		t.Errorf("got  %+v\nwant %+v", done, want)
	}
	_, _, err = Approve(done, owner, later)
	wantCode(t, err, CodeRequestDecided)
}

func TestDecideNeedsTheRight(t *testing.T) {
	_, req := asked(t)
	for _, tc := range []struct {
		name string
		by   Account
		ok   bool
	}{
		{"owner", owner, true},
		{"admin", admin, true},
		{"admin without two-factor", with(admin, func(a *Account) { a.TwoFactor = false }), true},
		{"moderator", moderator, true},
		{"moderator of this server", with(moderator, func(a *Account) { a.Servers = OnlyServers(otherServer, serverID) }), true},
		{"moderator of another server", with(moderator, func(a *Account) { a.Servers = OnlyServers(otherServer) }), false},
		{"viewer", viewer, false},
		{"member without a role", outsider, false},
		{"signed out", Account{}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, errYes := Approve(req, tc.by, t0)
			_, errNo := Decline(req, tc.by, t0)
			if tc.ok {
				if errYes != nil || errNo != nil {
					t.Errorf("approve: %v; decline: %v", errYes, errNo)
				}
				return
			}
			wantCode(t, errYes, CodePlayersNotAllowed)
			wantCode(t, errNo, CodePlayersNotAllowed)
		})
	}

	done, _ := Decline(req, owner, t0)
	_, _, err := Approve(done, viewer, t0)
	wantCode(t, err, CodePlayersNotAllowed)
}

func TestApproveChecksTheStoredPlayer(t *testing.T) {
	_, req := asked(t)
	for name, change := range map[string]func(*JoinRequest){
		"no UUID":                     func(r *JoinRequest) { r.PlayerUUID = "" },
		"UUID without dashes":         func(r *JoinRequest) { r.PlayerUUID = strings.ReplaceAll(r.PlayerUUID, "-", "") },
		"upper-case UUID":             func(r *JoinRequest) { r.PlayerUUID = strings.ToUpper(r.PlayerUUID) },
		"no name":                     func(r *JoinRequest) { r.PlayerName = "" },
		"name with a space":           func(r *JoinRequest) { r.PlayerName = "Pixel Pia" },
		"console command in the name": func(r *JoinRequest) { r.PlayerName = "PixelPia;op" },
	} {
		bad := req
		change(&bad)
		if _, g, err := Approve(bad, owner, t0); err == nil || CodeOf(err) != "" || g != (PlayerGrant{}) {
			t.Errorf("%s: approved with %+v (%v)", name, g, err)
		}
	}
}

func TestNotice(t *testing.T) {
	inv, req := asked(t)
	n := req.Notice(inv)
	want := Notice{
		Title:  Step{Key: "invite.request.title", Params: map[string]string{"player": "PixelPia"}, Text: "PixelPia wants to join"},
		Detail: Step{Key: "invite.request.askedWith", Params: map[string]string{"link": "School friends"}, Text: "Asked with the School friends link"},
	}
	if !reflect.DeepEqual(n, want) {
		t.Errorf("got  %+v\nwant %+v", n, want)
	}
	b, _ := json.Marshal(n)
	if string(b) != `{"title":{"key":"invite.request.title","params":{"player":"PixelPia"},"text":"PixelPia wants to join"},`+
		`"detail":{"key":"invite.request.askedWith","params":{"link":"School friends"},"text":"Asked with the School friends link"}}` {
		t.Errorf("JSON %s", b)
	}

	for label, text := range map[string]string{
		"Discord crew": "Asked with the Discord crew link",
		"Discord link": "Asked with the Discord link",
		"Blink":        "Asked with the Blink link",
		"Link":         "Asked with the Link",
	} {
		named := inv
		named.Label = label
		if d := req.Notice(named).Detail; d.Text != text || d.Params["link"] != label {
			t.Errorf("name %q: %+v", label, d)
		}
	}

	unnamed, another := inv, inv
	unnamed.Label = ""
	another.ID = "zzzzzzzzzz"
	anyInvite := Step{Key: "invite.request.askedWithInvite", Text: "Asked with an invite link"}
	for name, row := range map[string]Invite{"no name": unnamed, "another invite": another, "invite deleted": {}} {
		if d := req.Notice(row).Detail; !reflect.DeepEqual(d, anyInvite) {
			t.Errorf("%s: %+v", name, d)
		}
	}
}

func TestAlert(t *testing.T) {
	_, req := asked(t)
	answer := Step{Key: "invite.request.answer", Text: "Let them in or say no on the Players tab."}
	want := Notice{
		Title:  Step{Key: "invite.request.alert", Params: map[string]string{"player": "PixelPia", "server": "Survival"}, Text: "PixelPia wants to join Survival"},
		Detail: answer,
	}
	if got := req.Alert(survival.Name); !reflect.DeepEqual(got, want) {
		t.Errorf("got  %+v\nwant %+v", got, want)
	}
	want.Title = Step{Key: "invite.request.title", Params: map[string]string{"player": "PixelPia"}, Text: "PixelPia wants to join"}
	if got := req.Alert("  "); !reflect.DeepEqual(got, want) {
		t.Errorf("without a server name: %+v", got)
	}
}
