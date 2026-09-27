package invites

import (
	"context"
	"errors"
	"fmt"
	"net"
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

// Server is what the join page says about the server a friend invite is
// for. It holds a count of the players online and never their names, so
// none can reach the page.
type Server struct {
	Name string
	// Address is what friends type in Minecraft, as JoinAddress writes it:
	// the server's address name if it has one, else the panel's host and
	// the game port.
	Address string
	// Version is the Minecraft version friends need, e.g. "1.21.11".
	Version string
	Online  bool
	Playing int
}

// PlayerPage is what the join page shows for a friend invite that works:
// "siya invited you to Survival", the version and how many are playing.
type PlayerPage struct {
	Kind     Kind     `json:"kind"`
	Inviter  string   `json:"inviter"`
	Server   string   `json:"server"`
	Version  string   `json:"version,omitempty"`
	Online   bool     `json:"online"`
	Playing  int      `json:"playing"`
	Approval Approval `json:"approval"`
}

// PreviewPlayer checks a friend invite for the join page and returns what
// the page may show. creator is the invite's creator as it stands now: the
// invite stops working when they can no longer let players into the
// server.
func PreviewPlayer(inv Invite, code string, creator Account, srv Server, now time.Time) (PlayerPage, error) {
	if err := checkPlayer(inv, code, creator, now); err != nil {
		return PlayerPage{}, err
	}
	p := PlayerPage{Kind: KindPlayer, Inviter: creator.Name, Server: strings.TrimSpace(srv.Name), Version: srv.Version,
		Online: srv.Online, Approval: RightAway}
	if srv.Online {
		p.Playing = max(0, srv.Playing)
	}
	if inv.waits() {
		p.Approval = AfterYes
	}
	return p, nil
}

// waits reports whether inv lets people in only after a yes. Anything but
// RightAway waits, so an unreadable row can't let people in unasked.
func (inv Invite) waits() bool { return inv.Approval != RightAway }

// Candidate is the account a typed name belongs to, for the join page's
// "Is this you?". The panel draws the face from its own cache by UUID.
type Candidate struct {
	Name string `json:"name"`
	UUID string `json:"uuid"`
}

// LookupPlayer checks a friend invite and looks a typed name up with
// Mojang, for the preview before the friend confirms. Like RedeemPlayer it
// checks the invite before any lookup and refuses accounts that can't
// join. It changes nothing.
func LookupPlayer(ctx context.Context, lookup ProfileLookup, inv Invite, code string, creator Account, name string, now time.Time) (Candidate, error) {
	p, err := findPlayer(ctx, lookup, inv, code, creator, name, now)
	if err != nil {
		return Candidate{}, err
	}
	return Candidate{Name: p.Name, UUID: p.ID}, nil
}

// Redemption is a friend whose name checked out with a friend invite.
type Redemption struct {
	InviteID string
	ServerID string
	// Profile has the account's name as Mojang spells it, and its UUID.
	Profile mojang.Profile
	// Wait is set when the invite lets people in only after a yes: make a
	// join request (NewJoinRequest) instead of adding the player.
	Wait bool
}

// PlayerGrant is a friend to add to a server's whitelist.
type PlayerGrant struct {
	InviteID string
	ServerID string
	Profile  mojang.Profile
	// RequestID is the join request that was approved, if the invite
	// waited for a yes.
	RequestID string
}

// Grant is the friend to add now. ok is false when the redemption has to
// wait for a yes.
func (r Redemption) Grant() (g PlayerGrant, ok bool) {
	if r.Wait {
		return PlayerGrant{}, false
	}
	return PlayerGrant{InviteID: r.InviteID, ServerID: r.ServerID, Profile: r.Profile}, true
}

// RedeemPlayer checks a friend invite and looks the friend's name up with
// Mojang. The invite is checked first, so a link that doesn't work costs no
// lookup. For a Grant, the caller then counts the use (see RecordUse; a
// friend who already joined with this invite shouldn't use it up twice),
// asks the agent to add the player and keeps the grant's Origin. Otherwise
// it makes a join request.
func RedeemPlayer(ctx context.Context, lookup ProfileLookup, inv Invite, code string, creator Account, name string, now time.Time) (Redemption, error) {
	p, err := findPlayer(ctx, lookup, inv, code, creator, name, now)
	if err != nil {
		return Redemption{}, err
	}
	return Redemption{InviteID: inv.ID, ServerID: inv.ServerID, Profile: p, Wait: inv.waits()}, nil
}

// checkPlayer checks a friend invite and its creator as they stand now. A
// creator who can no longer let players in reads like a revoked link, ahead
// of expiry and use.
func checkPlayer(inv Invite, code string, creator Account, now time.Time) error {
	err := Check(inv, code, KindPlayer, now)
	switch {
	case CodeOf(err) == CodeNotWorking:
		return err
	case creator.UserID != inv.CreatedBy || CanLetPlayersIn(creator, inv.ServerID) != nil:
		return notWorking("the invite's creator can no longer let players in")
	case CodeOf(err) == CodeExpired:
		return runOutExpired(inv, creator.Name)
	case CodeOf(err) == CodeUsedUp:
		return runOutUsedUp(inv, creator.Name)
	}
	return err
}

func findPlayer(ctx context.Context, lookup ProfileLookup, inv Invite, code string, creator Account, name string, now time.Time) (mojang.Profile, error) {
	if err := checkPlayer(inv, code, creator, now); err != nil {
		return mojang.Profile{}, err
	}
	if !minecraft.ValidPlayerName(name) {
		return mojang.Profile{}, playerName()
	}
	p, err := lookup.Lookup(ctx, name)
	if err != nil {
		return mojang.Profile{}, lookupError(err, name)
	}
	if id, ok := mojang.NormalizeUUID(p.ID); !ok || id != p.ID || !minecraft.ValidPlayerName(p.Name) {
		return mojang.Profile{}, mojangDown(errors.New("the profile lookup answered with an invalid name or UUID"))
	}
	switch {
	case p.Demo:
		return mojang.Profile{}, playerDemo(p.Name)
	case p.Legacy:
		return mojang.Profile{}, playerLegacy(p.Name)
	}
	return p, nil
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

// JoinInfo is what the join page shows once a friend is on the whitelist,
// or has asked to be: the address to copy and the steps.
type JoinInfo struct {
	Player  string `json:"player"`
	Server  string `json:"server"`
	Address string `json:"address"`
	Version string `json:"version,omitempty"`
	// Waiting is set when the friend asked to join and nobody has said yes
	// yet.
	Waiting bool   `json:"waiting,omitempty"`
	Steps   []Step `json:"steps"`
}

// Step is one line the UI shows (an instruction, or a line of a notice), as
// a stable key with parameters for translation, and in English.
type Step struct {
	Key    string            `json:"key"`
	Params map[string]string `json:"params,omitempty"`
	Text   string            `json:"text"`
}

// Join returns what the join page shows after p was added to s.
func Join(s Server, p mojang.Profile) (JoinInfo, error) {
	return joinInfo(s, p, nil)
}

// Wait returns what the join page shows after p asked to join s through an
// invite that waits for a yes from inviter: the same steps, after one to
// wait.
func Wait(s Server, p mojang.Profile, inviter string) (JoinInfo, error) {
	wait := Step{Key: "invite.join.wait", Params: map[string]string{"inviter": inviter}, Text: fmt.Sprintf("Wait for %s to let you in.", inviter)}
	if inviter == "" {
		wait = Step{Key: "invite.join.waitAnyone", Text: "Wait for the person who sent the link to let you in."}
	}
	return joinInfo(s, p, &wait)
}

func joinInfo(s Server, p mojang.Profile, wait *Step) (JoinInfo, error) {
	if !validAddress(s.Address) {
		return JoinInfo{}, fmt.Errorf("%q is not an address players can join", s.Address)
	}
	open := Step{Key: "invite.join.open", Params: map[string]string{"version": s.Version},
		Text: fmt.Sprintf("Open Minecraft: Java Edition %s.", s.Version)}
	if s.Version == "" {
		open = Step{Key: "invite.join.openAnyVersion", Text: "Open Minecraft: Java Edition."}
	}
	steps := []Step{
		open,
		{Key: "invite.join.addServer", Text: "Pick Multiplayer, then Add Server."},
		{Key: "invite.join.paste", Text: "Paste the address and press Done, then Join."},
	}
	info := JoinInfo{Player: p.Name, Server: strings.TrimSpace(s.Name), Address: s.Address, Version: s.Version, Steps: steps}
	if wait != nil {
		info.Waiting = true
		info.Steps = append([]Step{*wait}, steps...)
	}
	return info, nil
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

// validAddress accepts an address exactly as JoinAddress writes it.
func validAddress(a string) bool {
	host, port := a, "25565"
	if h, p, err := net.SplitHostPort(a); err == nil {
		host, port = h, p
	} else if strings.HasPrefix(a, "[") && strings.HasSuffix(a, "]") {
		host = a[1 : len(a)-1]
	}
	n, err := strconv.Atoi(port)
	if err != nil {
		return false
	}
	want, err := JoinAddress(host, n)
	return err == nil && want == a
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
