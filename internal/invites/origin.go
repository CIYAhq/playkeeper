package invites

import "time"

// Origin is how a player got onto a server's whitelist: one row of the
// player_origins table, one per player and server. The caller inserts it
// once the agent has actually added the player, replacing any older row (a
// player who was on the list already keeps how they first got in), and
// deletes it when the player is taken off the list.
type Origin struct {
	ServerID   string `json:"serverId"`
	PlayerUUID string `json:"playerUuid"`
	InviteID   string `json:"inviteId"`
	// RequestID is the join request that was approved, if the invite
	// waited for a yes.
	RequestID string    `json:"requestId,omitempty"`
	JoinedAt  time.Time `json:"joinedAt"`
}

// Origin is the record to keep once the agent has added g's player.
func (g PlayerGrant) Origin(now time.Time) Origin {
	return Origin{ServerID: g.ServerID, PlayerUUID: g.Profile.ID, InviteID: g.InviteID, RequestID: g.RequestID, JoinedAt: stamp(now)}
}

// Note says how the player got in, for their allowlist row and profile:
// "Joined with the Discord crew link". inv is the invite o names, if it
// still exists. A link for one person reads "Joined with their own link",
// and one without a name, or one that is gone, "Joined with an invite
// link".
func (o Origin) Note(inv Invite) Step {
	if inv.Kind == KindPlayer && inv.ID == o.InviteID {
		if inv.MaxUses == 1 {
			return Step{Key: "invite.origin.ownLink", Text: "Joined with their own link"}
		}
		if inv.Label != "" {
			return Step{Key: "invite.origin.link", Params: map[string]string{"link": inv.Label}, Text: "Joined with " + theLink(inv.Label)}
		}
	}
	return Step{Key: "invite.origin.invite", Text: "Joined with an invite link"}
}
