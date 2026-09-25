// Package invites makes invite links. A friend opens a player invite to add
// their Minecraft account to one server's whitelist; a new team member
// opens a member invite to create a Playkeeper account with a role in one
// project, for all of its servers or some.
//
// Both kinds of link point at the panel's public page, /join/<code>. The
// code is random and the panel looks invites up by its SHA-256. A member
// invite's code exists only when the invite is created. A player invite
// keeps its code too, so the Players tab can show and copy the link again:
// it only ever adds a name to a whitelist, and it can be turned off. Codes
// are secrets, so they never appear in logs or error messages.
//
// Everything here is pure: functions take the stored Invite and the time,
// and return structs for the caller to store. The caller's database counts
// uses with one conditional update (see RecordUse), so two people can't
// both take an invite's last use.
package invites

import (
	"cmp"
	"crypto/rand"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// Kind is what an invite lets its holder do.
type Kind string

const (
	// KindPlayer adds the holder's Minecraft account to a server's whitelist.
	KindPlayer Kind = "player"
	// KindMember creates an account with a role in a project.
	KindMember Kind = "member"
)

// Limits on what an invite's creator may choose.
const (
	DefaultPlayerUses = 5
	MaxPlayerUses     = 100
	// A member invite hands out access to run servers, so it works once and
	// for a fixed time.
	MemberLifetime = 7 * 24 * time.Hour
	MaxLabelRunes  = 64
)

// Expiry is how long a friend invite works, as its creator chooses.
type Expiry string

const (
	ExpiryOneDay     Expiry = "1d"
	ExpirySevenDays  Expiry = "7d"
	ExpiryThirtyDays Expiry = "30d"
	// ExpiryUntilTurnedOff never expires. The invite still stops when it is
	// turned off or used up.
	ExpiryUntilTurnedOff Expiry = "until_turned_off"
	DefaultExpiry               = ExpirySevenDays
)

// Expiries lists the choices in the order the create dialog shows them.
func Expiries() []Expiry {
	return []Expiry{ExpiryOneDay, ExpirySevenDays, ExpiryThirtyDays, ExpiryUntilTurnedOff}
}

// lifetime is how long an invite made with e works, 0 meaning until it is
// turned off.
func (e Expiry) lifetime() (time.Duration, bool) {
	switch e {
	case ExpiryOneDay:
		return 24 * time.Hour, true
	case ExpirySevenDays:
		return 7 * 24 * time.Hour, true
	case ExpiryThirtyDays:
		return 30 * 24 * time.Hour, true
	case ExpiryUntilTurnedOff:
		return 0, true
	}
	return 0, false
}

// Approval is when a friend invite lets people in.
type Approval string

const (
	// RightAway adds a friend to the whitelist as soon as their name checks
	// out.
	RightAway Approval = "right_away"
	// AfterYes makes a join request instead, which someone who can let
	// players in approves or declines.
	AfterYes Approval = "after_yes"
)

// Invite is one invite as the panel stores it: one row of its invites
// table. Times are UTC with millisecond precision, like the panel's
// columns; a zero RevokedAt means not revoked.
type Invite struct {
	// ID names the invite in management routes and audit trails. It is not
	// secret and opens nothing.
	ID   string `json:"id"`
	Kind Kind   `json:"kind"`
	// CodeHash is HashCode of the link's code. Invites are looked up by it.
	CodeHash string `json:"-"`
	// Code is the link's code, kept for a player invite so the Players tab
	// can show and copy the link again. A member invite's code is shown
	// once and never stored, so this is empty.
	Code string `json:"-"`
	// ProjectID is the project a member joins, or the project of the server
	// a player joins.
	ProjectID string `json:"projectId"`
	// ServerID is the server whose whitelist a player invite adds to.
	ServerID string `json:"serverId,omitempty"`
	// Role is the project role a member invite gives.
	Role string `json:"role,omitempty"`
	// Servers is the scope a member invite gives with the role.
	Servers Scope `json:"servers,omitzero"`
	// Approval is when a player invite lets people in.
	Approval Approval `json:"approval,omitempty"`
	// Label is the creator's note to tell links apart ("Discord crew").
	// Public pages don't show it.
	Label     string    `json:"label,omitempty"`
	CreatedBy int64     `json:"createdBy"`
	CreatedAt time.Time `json:"createdAt"`
	// ExpiresAt is zero for a player invite that works until turned off.
	ExpiresAt time.Time `json:"expiresAt,omitzero"`
	// MaxUses is 0 for a player invite with no limit.
	MaxUses   int       `json:"maxUses"`
	Uses      int       `json:"uses"`
	RevokedAt time.Time `json:"revokedAt,omitzero"`
}

// Format keeps the code out of anything printed with fmt.
func (inv Invite) Format(f fmt.State, verb rune) {
	fmt.Fprintf(f, "{ID:%s Kind:%s Project:%s Server:%s Code:[hidden]}", inv.ID, inv.Kind, inv.ProjectID, inv.ServerID)
}

// LogValue keeps the code out of slog output.
func (inv Invite) LogValue() slog.Value {
	return slog.GroupValue(slog.String("id", inv.ID), slog.String("kind", string(inv.Kind)))
}

// Status is where an invite is in its life, for lists.
type Status string

const (
	StatusActive  Status = "active"
	StatusUsedUp  Status = "used_up"
	StatusExpired Status = "expired"
	StatusRevoked Status = "revoked"
)

// StatusAt says whether inv is usable at now, and if not, why. A revoked
// invite reads as revoked, and one used up before it expired as used up.
func (inv Invite) StatusAt(now time.Time) Status {
	switch {
	case !inv.RevokedAt.IsZero():
		return StatusRevoked
	case inv.usedUp():
		return StatusUsedUp
	case inv.expired(now):
		return StatusExpired
	}
	return StatusActive
}

// UsesLeft is how many more times inv can be used, ignoring expiry.
// limited is false for a player invite with no limit.
func (inv Invite) UsesLeft() (left int, limited bool) {
	if inv.unlimited() {
		return 0, false
	}
	return max(0, inv.limit()-inv.Uses), true
}

// Only a player invite may go without a use limit or an expiry. Any other
// row with zeros reads as used up or expired, never as unlimited.
func (inv Invite) unlimited() bool { return inv.Kind == KindPlayer && inv.MaxUses == 0 }

func (inv Invite) limit() int {
	if inv.Kind == KindMember {
		return 1
	}
	return inv.MaxUses
}

func (inv Invite) usedUp() bool { return !inv.unlimited() && inv.Uses >= inv.limit() }

func (inv Invite) expired(now time.Time) bool {
	if inv.ExpiresAt.IsZero() {
		return inv.Kind != KindPlayer
	}
	return !now.Before(inv.ExpiresAt)
}

// Actor names the invite in audit trails, as "invite:<id>".
func (inv Invite) Actor() string { return "invite:" + inv.ID }

// Path is the link of a player invite, or "" for a member invite, whose
// code isn't kept.
func (inv Invite) Path() string {
	if inv.Code == "" {
		return ""
	}
	return LinkPath(inv.Code)
}

// Summary is an invite as the Players tab and the team page list it. Only
// signed-in management routes may send it, since Path holds the code.
type Summary struct {
	Invite
	Status Status `json:"status"`
	// UsesLeft is nil for an invite with no limit.
	UsesLeft *int   `json:"usesLeft,omitempty"`
	Path     string `json:"path,omitempty"`
}

// Summarize returns inv with its status at now.
func (inv Invite) Summarize(now time.Time) Summary {
	s := Summary{Invite: inv, Status: inv.StatusAt(now), Path: inv.Path()}
	if left, limited := inv.UsesLeft(); limited {
		s.UsesLeft = &left
	}
	return s
}

// Public is what a public page may show about an invite that works:
// nothing about who created it, its label, or its uses.
type Public struct {
	Kind      Kind      `json:"kind"`
	Role      string    `json:"role,omitempty"`
	ExpiresAt time.Time `json:"expiresAt,omitzero"`
}

func (inv Invite) public() Public {
	return Public{Kind: inv.Kind, Role: inv.Role, ExpiresAt: inv.ExpiresAt}
}

const idAlphabet = "abcdefghijkmnpqrstuvwxyz23456789"

// newID returns a random 10-character id in the style of the panel's other
// ids. The alphabet has 32 symbols, which divide 256, so each byte modulo
// 32 is unbiased.
func newID() string {
	b := make([]byte, 10)
	_, _ = rand.Read(b)
	for i := range b {
		b[i] = idAlphabet[int(b[i])%len(idAlphabet)]
	}
	return string(b)
}

// ValidID reports whether id could be an invite id, for routes that take
// one.
func ValidID(id string) bool {
	if len(id) != 10 {
		return false
	}
	for i := 0; i < len(id); i++ {
		if strings.IndexByte(idAlphabet, id[i]) < 0 {
			return false
		}
	}
	return true
}

// validRef reports whether s looks like a server or project id
// (^[a-z2-9]{10}$, as the agent and panel make them).
func validRef(s string) bool {
	if len(s) != 10 {
		return false
	}
	for i := 0; i < len(s); i++ {
		if !(s[i] >= 'a' && s[i] <= 'z' || s[i] >= '2' && s[i] <= '9') {
			return false
		}
	}
	return true
}

// Created is a new invite and its link. The UI shows the link right away;
// only Invite is stored.
type Created struct {
	Invite Invite `json:"invite"`
	// Path is LinkPath with the code.
	Path string `json:"path"`
}

// Format keeps the link out of anything printed with fmt.
func (c Created) Format(f fmt.State, verb rune) {
	fmt.Fprintf(f, "{Invite:%s Path:[hidden]}", c.Invite.ID)
}

// LogValue keeps the link out of slog output.
func (c Created) LogValue() slog.Value {
	return slog.GroupValue(slog.String("invite", c.Invite.ID))
}

// PlayerSpec is what the creator of a player invite chooses. Zero values
// take the defaults: 7 days, DefaultPlayerUses friends, right away.
type PlayerSpec struct {
	ServerID  string
	ProjectID string
	Label     string
	Expiry    Expiry
	MaxUses   int
	// Unlimited lets any number of friends use the invite. It has to be
	// chosen: a zero MaxUses alone means the default.
	Unlimited bool
	Approval  Approval
}

// MemberSpec is what the creator of a member invite chooses. A member
// invite always works once, for MemberLifetime.
type MemberSpec struct {
	ProjectID string
	Role      string
	Servers   Scope
	Label     string
}

// NewPlayer creates an invite that lets friends add themselves to a
// server's whitelist. The creator must be able to let players into it (see
// CanLetPlayersIn).
func NewPlayer(spec PlayerSpec, creator Account, now time.Time) (Created, error) {
	if !validRef(spec.ServerID) {
		return Created{}, badOptions("server", "The invite needs a valid server.")
	}
	if err := CanLetPlayersIn(creator, spec.ServerID); err != nil {
		return Created{}, err
	}
	expiry := cmp.Or(spec.Expiry, DefaultExpiry)
	lifetime, ok := expiry.lifetime()
	if !ok {
		return Created{}, badOptions("expiry", "An invite link works for 1 day, 7 days, 30 days, or until you turn it off.")
	}
	uses, err := playerUses(spec)
	if err != nil {
		return Created{}, err
	}
	approval := cmp.Or(spec.Approval, RightAway)
	if approval != RightAway && approval != AfterYes {
		return Created{}, badOptions("approval", "An invite link lets people in right away or after you say yes.")
	}
	c, err := create(Invite{Kind: KindPlayer, ServerID: spec.ServerID, ProjectID: spec.ProjectID, Approval: approval, MaxUses: uses},
		spec.Label, lifetime, creator.UserID, now)
	if err != nil {
		return Created{}, err
	}
	c.Invite.Code, _ = CodeFromPath(c.Path)
	return c, nil
}

func playerUses(spec PlayerSpec) (int, error) {
	switch {
	case spec.Unlimited && spec.MaxUses != 0:
		return 0, badOptions("uses", "Choose a number of friends or no limit, not both.")
	case spec.Unlimited:
		return 0, nil
	case spec.MaxUses == 0:
		return DefaultPlayerUses, nil
	case spec.MaxUses < 1 || spec.MaxUses > MaxPlayerUses:
		return 0, badOptions("uses", fmt.Sprintf("An invite link can be for 1 to %d friends, or have no limit.", MaxPlayerUses), "max", strconv.Itoa(MaxPlayerUses))
	}
	return spec.MaxUses, nil
}

// NewMember creates an invite that lets one person create an account with
// a role in a project, for all of its servers or some. existing lists the
// project's servers now; the invite may name only those. The inviter must
// be allowed to give the role and the servers (see CanGrant).
func NewMember(spec MemberSpec, inviter Account, existing []string, now time.Time) (Created, error) {
	if err := CanGrant(inviter, spec.Role, spec.Servers); err != nil {
		return Created{}, err
	}
	if kept, _ := spec.Servers.narrow(existing); len(kept.Servers) != len(spec.Servers.Servers) {
		return Created{}, badOptions("servers", "Choose servers from this project, each once.")
	}
	return create(Invite{Kind: KindMember, ProjectID: spec.ProjectID, Role: spec.Role, Servers: spec.Servers.sorted(), MaxUses: 1},
		spec.Label, MemberLifetime, inviter.UserID, now)
}

// create fills in inv. A zero lifetime means it works until turned off.
func create(inv Invite, label string, lifetime time.Duration, createdBy int64, now time.Time) (Created, error) {
	if !validRef(inv.ProjectID) {
		return Created{}, badOptions("project", "The invite needs a valid project.")
	}
	label, err := cleanLabel(label)
	if err != nil {
		return Created{}, err
	}
	now = stamp(now)
	code := NewCode()
	inv.ID = newID()
	inv.CodeHash = HashCode(code)
	inv.Label = label
	inv.CreatedBy = createdBy
	inv.CreatedAt = now
	if lifetime > 0 {
		inv.ExpiresAt = now.Add(lifetime)
	}
	return Created{Invite: inv, Path: LinkPath(code)}, nil
}

func cleanLabel(s string) (string, error) {
	s = strings.TrimSpace(s)
	bad := !utf8.ValidString(s) || utf8.RuneCountInString(s) > MaxLabelRunes
	for _, r := range s {
		bad = bad || unicode.IsControl(r)
	}
	if bad {
		return "", badOptions("label", fmt.Sprintf("A label is at most %d characters, on one line.", MaxLabelRunes), "max", strconv.Itoa(MaxLabelRunes))
	}
	return s, nil
}

// Check reports whether code opens inv as an invite of kind at now. A
// malformed or wrong code, a revoked invite and one of the other kind are
// all refused with the same public message; only Reason tells them apart.
func Check(inv Invite, code string, kind Kind, now time.Time) error {
	switch {
	case !WellFormed(code):
		return notWorking("the code is malformed")
	case !codeMatches(inv.CodeHash, code):
		return notWorking("the code does not match")
	case inv.Kind != kind:
		return notWorking("the invite is of another kind")
	}
	return usable(inv, now)
}

func usable(inv Invite, now time.Time) error {
	switch {
	case !inv.RevokedAt.IsZero():
		return notWorking("the invite was revoked")
	case inv.expired(now):
		return expired()
	case inv.usedUp():
		return usedUp(inv.Kind)
	}
	return nil
}

// RecordUse returns inv with one more use, or why it can't be used. The
// database must count the use in one statement, so two people can't both
// take the last one:
//
//	UPDATE invites SET uses = uses + 1
//	WHERE id = :id AND revoked_at = 0
//	  AND (expires_at = 0 OR expires_at > :now)
//	  AND (max_uses = 0 OR uses < max_uses)
//
// The zeros ("until turned off", "no limit") are safe there only because
// the table's CHECK keeps them off member invites. If no row changed, the
// invite ran out meanwhile: read it again and Check it for the message to
// show.
func RecordUse(inv Invite, now time.Time) (Invite, error) {
	if err := usable(inv, now); err != nil {
		return inv, err
	}
	inv.Uses++
	return inv, nil
}

// Revoke returns inv revoked at now. Revoking twice keeps the first time.
func Revoke(inv Invite, now time.Time) Invite {
	if inv.RevokedAt.IsZero() {
		inv.RevokedAt = stamp(now)
	}
	return inv
}

func stamp(t time.Time) time.Time { return t.UTC().Truncate(time.Millisecond) }

// Millis converts a time to the unix milliseconds the panel stores, with
// the zero time as 0.
func Millis(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}

// FromMillis is the inverse of Millis.
func FromMillis(ms int64) time.Time {
	if ms == 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms).UTC()
}
