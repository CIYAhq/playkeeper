package invites

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"github.com/CIYAhq/playkeeper/internal/minecraft"
	"github.com/CIYAhq/playkeeper/internal/mojang"
)

// ProfileLookup finds a Minecraft: Java Edition account by name.
// *mojang.Client is one.
type ProfileLookup interface {
	Lookup(ctx context.Context, name string) (mojang.Profile, error)
}

// PlayerGrant is a friend to add to a server's whitelist.
type PlayerGrant struct {
	InviteID string
	ServerID string
	// Profile has the account's name as Mojang spells it, and its UUID.
	Profile mojang.Profile
}

// PreviewPlayer checks a player invite for the join page and returns what
// the page may show.
func PreviewPlayer(inv Invite, code string, now time.Time) (Public, error) {
	if err := Check(inv, code, KindPlayer, now); err != nil {
		return Public{}, err
	}
	return inv.public(), nil
}

// RedeemPlayer checks a player invite and looks the friend's name up with
// Mojang. The invite is checked first, so a link that doesn't work costs
// no lookup. The caller then counts the use (see RecordUse; a friend who
// already used this invite shouldn't use it up twice) and asks the agent
// to add the player.
func RedeemPlayer(ctx context.Context, lookup ProfileLookup, inv Invite, code, name string, now time.Time) (PlayerGrant, error) {
	if err := Check(inv, code, KindPlayer, now); err != nil {
		return PlayerGrant{}, err
	}
	if !minecraft.ValidPlayerName(name) {
		return PlayerGrant{}, playerName()
	}
	p, err := lookup.Lookup(ctx, name)
	if err != nil {
		return PlayerGrant{}, lookupError(err, name)
	}
	switch {
	case p.Demo:
		return PlayerGrant{}, playerDemo(p.Name)
	case p.Legacy:
		return PlayerGrant{}, playerLegacy(p.Name)
	}
	return PlayerGrant{InviteID: inv.ID, ServerID: inv.ServerID, Profile: p}, nil
}

func lookupError(err error, name string) *Error {
	switch {
	case errors.Is(err, mojang.ErrInvalidName):
		return playerName()
	case errors.Is(err, mojang.ErrNotFound):
		return playerNotFound(name)
	case errors.Is(err, mojang.ErrRateLimited):
		wait := time.Minute
		var me *mojang.Error
		if errors.As(err, &me) && me.RetryAfter > 0 {
			wait = me.RetryAfter
		}
		return mojangBusy(wait, err)
	default:
		return mojangDown(err)
	}
}

// Server is what the join page says about the server a player invite is
// for.
type Server struct {
	Name string
	// Host is the hostname or IP address friends connect to: the panel's
	// domain, or the address the friend reached the panel at.
	Host string
	Port int
	// Version is the Minecraft version friends need, e.g. "1.21.11".
	Version string
}

// JoinInfo is what the join page shows once a friend is on the whitelist.
type JoinInfo struct {
	Player  string `json:"player"`
	Server  string `json:"server"`
	Address string `json:"address"`
	Version string `json:"version,omitempty"`
	Steps   []Step `json:"steps"`
}

// Step is one instruction, as a stable key with parameters for
// translation, and in English.
type Step struct {
	Key    string            `json:"key"`
	Params map[string]string `json:"params,omitempty"`
	Text   string            `json:"text"`
}

// Join returns what the join page shows after p was added to s.
func Join(s Server, p mojang.Profile) (JoinInfo, error) {
	addr, err := JoinAddress(s.Host, s.Port)
	if err != nil {
		return JoinInfo{}, err
	}
	name := strings.TrimSpace(s.Name)
	open := Step{Key: "invite.join.open", Params: map[string]string{"version": s.Version, "player": p.Name},
		Text: fmt.Sprintf("Start Minecraft: Java Edition %s, signed in as %s.", s.Version, p.Name)}
	if s.Version == "" {
		open = Step{Key: "invite.join.openAnyVersion", Params: map[string]string{"player": p.Name},
			Text: fmt.Sprintf("Start Minecraft: Java Edition, signed in as %s.", p.Name)}
	}
	return JoinInfo{
		Player: p.Name, Server: name, Address: addr, Version: s.Version,
		Steps: []Step{
			open,
			{Key: "invite.join.addServer", Text: "Choose Multiplayer, then Add Server."},
			{Key: "invite.join.enterAddress", Params: map[string]string{"server": name, "address": addr},
				Text: fmt.Sprintf("Enter %s as the Server Name and %s as the Server Address, then choose Done.", name, addr)},
			{Key: "invite.join.connect", Params: map[string]string{"server": name},
				Text: fmt.Sprintf("Select %s in the list and choose Join Server.", name)},
		},
	}, nil
}

// JoinAddress is the address players type in Minecraft, written like the
// UI's joinAddress: the port is left out when it is Minecraft's default,
// and an IPv6 address is put in brackets.
func JoinAddress(host string, port int) (string, error) {
	if port < 1 || port > 65535 {
		return "", fmt.Errorf("%d is not a port", port)
	}
	if !validHost(host) {
		return "", fmt.Errorf("%q is not a hostname or IP address", host)
	}
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	if port == 25565 {
		return host, nil
	}
	return host + ":" + strconv.Itoa(port), nil
}

// validHost accepts an IP address without a zone, or a DNS name.
func validHost(h string) bool {
	if ip, err := netip.ParseAddr(h); err == nil {
		return ip.Zone() == ""
	}
	if h == "" || len(h) > 253 {
		return false
	}
	for _, label := range strings.Split(h, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for i := 0; i < len(label); i++ {
			c := label[i]
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-') {
				return false
			}
		}
	}
	return true
}
