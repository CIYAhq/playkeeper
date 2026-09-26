package invites

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"
)

func TestOrigin(t *testing.T) {
	inv, code := newPlayer(t, PlayerSpec{Label: "Discord crew"})
	r, err := RedeemPlayer(context.Background(), newLookup(), inv, code, owner, "notch", t0)
	if err != nil {
		t.Fatal(err)
	}
	g, _ := r.Grant()
	later := t0.Add(time.Second)
	o := g.Origin(later)
	if o != (Origin{ServerID: serverID, PlayerUUID: notch.ID, InviteID: inv.ID, JoinedAt: stamp(later)}) {
		t.Errorf("got %+v", o)
	}
	b, _ := json.Marshal(o)
	if string(b) != `{"serverId":"`+serverID+`","playerUuid":"`+notch.ID+`","inviteId":"`+inv.ID+`","joinedAt":"2026-09-25T15:04:52.123Z"}` {
		t.Errorf("JSON %s", b)
	}

	_, req := asked(t)
	_, approved, err := Approve(req, owner, later)
	if err != nil {
		t.Fatal(err)
	}
	if o := approved.Origin(later); o != (Origin{ServerID: serverID, PlayerUUID: pixelPia.ID, InviteID: req.InviteID, RequestID: req.ID, JoinedAt: stamp(later)}) {
		t.Errorf("after a yes: %+v", o)
	}
}

func TestOriginNote(t *testing.T) {
	crew, _ := newPlayer(t, PlayerSpec{Label: "Discord crew"})
	own, _ := newPlayer(t, PlayerSpec{Label: "For Lenn0x", MaxUses: 1})
	family, _ := newPlayer(t, PlayerSpec{Label: "Family link", Unlimited: true, Expiry: ExpiryUntilTurnedOff})
	unnamed, _ := newPlayer(t, PlayerSpec{})
	member, _ := memberInvite(t, owner, RoleModerator, AllServers())
	named := Step{Key: "invite.origin.link", Params: map[string]string{"link": "Discord crew"}, Text: "Joined with the Discord crew link"}
	anyInvite := Step{Key: "invite.origin.invite", Text: "Joined with an invite link"}
	for _, tc := range []struct {
		name   string
		inv    Invite
		origin string
		want   Step
	}{
		{"named link", crew, crew.ID, named},
		{"turned off since", Revoke(crew, t0), crew.ID, named},
		{"link for one person", own, own.ID, Step{Key: "invite.origin.ownLink", Text: "Joined with their own link"}},
		{"name ending in link", family, family.ID, Step{Key: "invite.origin.link", Params: map[string]string{"link": "Family link"}, Text: "Joined with the Family link"}},
		{"link without a name", unnamed, unnamed.ID, anyInvite},
		{"another invite", crew, own.ID, anyInvite},
		{"invite deleted", Invite{}, crew.ID, anyInvite},
		{"team invite", member, member.ID, anyInvite},
	} {
		if got := (Origin{InviteID: tc.origin}).Note(tc.inv); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: got %+v, want %+v", tc.name, got, tc.want)
		}
	}
}
