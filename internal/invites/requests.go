package invites

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/CIYAhq/playkeeper/internal/minecraft"
	"github.com/CIYAhq/playkeeper/internal/mojang"
)

// Limits on join requests waiting for a yes, so a link that leaked can't
// bury the Players tab.
const (
	MaxPendingPerInvite  = 20
	MaxPendingPerAddress = 5
)

// RequestState is where a join request is.
type RequestState string

const (
	RequestPending  RequestState = "pending"
	RequestApproved RequestState = "approved"
	RequestDeclined RequestState = "declined"
)

// JoinRequest is a friend who asked to join through an invite that lets
// people in after a yes: one row of the join_requests table.
type JoinRequest struct {
	ID         string `json:"id"`
	InviteID   string `json:"inviteId"`
	ServerID   string `json:"serverId"`
	PlayerName string `json:"playerName"`
	PlayerUUID string `json:"playerUuid"`
	// Address is the AddressKey of the network the request came from. It is
	// kept only while the request is pending, to limit requests per
	// address, and deciding clears it.
	Address   string       `json:"-"`
	State     RequestState `json:"state"`
	CreatedAt time.Time    `json:"createdAt"`
	DecidedAt time.Time    `json:"decidedAt,omitzero"`
	DecidedBy int64        `json:"decidedBy,omitempty"`
}

// Pending is what the caller counted in join_requests just before making
// a request.
type Pending struct {
	// Invite counts the pending requests made with the invite.
	Invite int
	// Address counts the pending requests from the same address, with any
	// invite.
	Address int
}

// NewJoinRequest makes the join request for a redemption that waits for a
// yes. address is AddressKey of the friend's IP. The caller counts pending
// requests, stores this one and counts the invite's use (see RecordUse) in
// one write transaction, so a request holds a use until it is declined.
// A player who already has a pending request for the server, or is already
// on its whitelist, needs no new one: show Wait or Join again instead.
func NewJoinRequest(r Redemption, address string, pending Pending, now time.Time) (JoinRequest, error) {
	switch {
	case !r.Wait:
		return JoinRequest{}, errors.New("the invite lets people in right away, so there is nothing to ask")
	case address == "":
		return JoinRequest{}, errors.New("a join request needs the address it came from")
	case pending.Invite >= MaxPendingPerInvite:
		return JoinRequest{}, requestsFull("invite")
	case pending.Address >= MaxPendingPerAddress:
		return JoinRequest{}, requestsFull("address")
	}
	return JoinRequest{ID: newID(), InviteID: r.InviteID, ServerID: r.ServerID, PlayerName: r.Profile.Name, PlayerUUID: r.Profile.ID,
		Address: address, State: RequestPending, CreatedAt: stamp(now)}, nil
}

// Approve says yes to a pending request. by is the signed-in account that
// answers, which must be able to let players into the server (see
// CanLetPlayersIn). It returns the request as decided and the friend to
// add. The caller asks the agent to add the player, then stores the
// request only if it is still pending, so two people answering at once
// can't both decide it.
func Approve(r JoinRequest, by Account, now time.Time) (JoinRequest, PlayerGrant, error) {
	p := mojang.Profile{ID: r.PlayerUUID, Name: r.PlayerName}
	if id, ok := mojang.NormalizeUUID(p.ID); !ok || id != p.ID || !minecraft.ValidPlayerName(p.Name) {
		return JoinRequest{}, PlayerGrant{}, errors.New("the join request has no valid player name and UUID")
	}
	d, err := decide(r, by, RequestApproved, now)
	if err != nil {
		return JoinRequest{}, PlayerGrant{}, err
	}
	return d, PlayerGrant{InviteID: r.InviteID, ServerID: r.ServerID, Profile: p, RequestID: r.ID}, nil
}

// Decline says no to a pending request, with the same rules as Approve.
// The caller stores the request only if it is still pending, and gives the
// invite its use back:
//
//	UPDATE invites SET uses = uses - 1 WHERE id = :invite AND uses > 0
func Decline(r JoinRequest, by Account, now time.Time) (JoinRequest, error) {
	return decide(r, by, RequestDeclined, now)
}

func decide(r JoinRequest, by Account, state RequestState, now time.Time) (JoinRequest, error) {
	if err := CanLetPlayersIn(by, r.ServerID); err != nil {
		return JoinRequest{}, err
	}
	if r.State != RequestPending {
		return JoinRequest{}, requestDecided()
	}
	r.State = state
	r.DecidedAt = stamp(now)
	r.DecidedBy = by.UserID
	r.Address = ""
	return r, nil
}

// Notice is a join request as the Players tab or a Discord alert shows it:
// a title and the line under it.
type Notice struct {
	Title  Step `json:"title"`
	Detail Step `json:"detail"`
}

// Notice is the Players tab notice for r, "PixelPia wants to join" over
// "Asked with the School friends link". inv is the invite r was made with.
// The UI adds how long ago, and draws the face for r.PlayerUUID.
func (r JoinRequest) Notice(inv Invite) Notice {
	n := Notice{Title: wantsToJoin(r.PlayerName), Detail: Step{Key: "invite.request.askedWithInvite", Text: "Asked with an invite link"}}
	if inv.ID == r.InviteID && inv.Label != "" {
		n.Detail = Step{Key: "invite.request.askedWith", Params: map[string]string{"link": inv.Label}, Text: "Asked with " + theLink(inv.Label)}
	}
	return n
}

// Alert is the Discord alert for r on the server called server, "PixelPia
// wants to join Survival". It leaves the invite's name out: the create
// dialog promises a name only you see, and the friends the link went to
// may read the channel.
func (r JoinRequest) Alert(server string) Notice {
	n := Notice{Title: wantsToJoin(r.PlayerName), Detail: Step{Key: "invite.request.answer", Text: "Let them in or say no on the Players tab."}}
	if server = strings.TrimSpace(server); server != "" {
		n.Title = Step{Key: "invite.request.alert", Params: map[string]string{"player": r.PlayerName, "server": server},
			Text: fmt.Sprintf("%s wants to join %s", r.PlayerName, server)}
	}
	return n
}

func wantsToJoin(player string) Step {
	return Step{Key: "invite.request.title", Params: map[string]string{"player": player}, Text: player + " wants to join"}
}

// theLink names an invite by its label as the design does, "the Discord
// crew link", without doubling a label that already ends in "link".
func theLink(label string) string {
	lower := strings.ToLower(label)
	if lower == "link" || strings.HasSuffix(lower, " link") {
		return "the " + label
	}
	return "the " + label + " link"
}
