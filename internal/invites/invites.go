// Package invites makes invite links. A friend opens a player invite to add
// their Minecraft account to one server's whitelist; a future co-admin
// opens a member invite to create a Playkeeper account with a role in one
// project.
//
// A link carries a random token that exists only when the invite is
// created. The panel stores the token's SHA-256 and looks invites up by it,
// so a copy of the database can't be turned back into working links. The
// token sits in the link's fragment (after "#"), which browsers never send,
// so it stays out of request logs and Referer headers; the public pages
// post it in a request body.
//
// Everything here is pure: functions take the stored Invite and the time,
// and return structs for the caller to store. The caller's database counts
// uses with one conditional update (see RecordUse), so two people can't
// both take an invite's last use.
package invites

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
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
	DefaultPlayerTTL  = 7 * 24 * time.Hour
	MaxPlayerTTL      = 30 * 24 * time.Hour
	DefaultPlayerUses = 10
	MaxPlayerUses     = 100
	// A member invite hands out access to run servers, so it is short-lived
	// and works once.
	DefaultMemberTTL = 2 * 24 * time.Hour
	MaxMemberTTL     = 7 * 24 * time.Hour
	MinTTL           = 10 * time.Minute
	MaxLabelRunes    = 64
)

// Paths of the public pages. The token follows "#t=" in the link.
const (
	JoinPath   = "/join"
	AcceptPath = "/accept"
)

// TokenBytes is the randomness in a token: 256 bits, written as 43
// URL-safe base64 characters.
const TokenBytes = 32

const tokenLen = (TokenBytes*8 + 5) / 6

// Invite is one invite as the panel stores it: one row of its invites
// table. Times are UTC with millisecond precision, like the panel's
// columns; a zero RevokedAt means not revoked.
type Invite struct {
	// ID names the invite in management routes and audit trails. It is not
	// secret and opens nothing.
	ID   string `json:"id"`
	Kind Kind   `json:"kind"`
	// TokenHash is HashToken of the link's token.
	TokenHash string `json:"-"`
	// ProjectID is the project a member joins, or the project of the server
	// a player joins.
	ProjectID string `json:"projectId"`
	// ServerID is the server whose whitelist a player invite adds to.
	ServerID string `json:"serverId,omitempty"`
	// Role is the project role a member invite gives.
	Role string `json:"role,omitempty"`
	// Label is the creator's note to tell links apart ("Discord friends").
	// Public pages don't show it.
	Label     string    `json:"label,omitempty"`
	CreatedBy int64     `json:"createdBy"`
	CreatedAt time.Time `json:"createdAt"`
	ExpiresAt time.Time `json:"expiresAt"`
	MaxUses   int       `json:"maxUses"`
	Uses      int       `json:"uses"`
	RevokedAt time.Time `json:"revokedAt,omitzero"`
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
	case inv.Uses >= inv.MaxUses:
		return StatusUsedUp
	case !now.Before(inv.ExpiresAt):
		return StatusExpired
	}
	return StatusActive
}

// UsesLeft is how many more times inv can be used, ignoring expiry.
func (inv Invite) UsesLeft() int { return max(0, inv.MaxUses-inv.Uses) }

// Actor names the invite in audit trails, as "invite:<id>".
func (inv Invite) Actor() string { return "invite:" + inv.ID }

// Summary is an invite as the Players tab and the team page list it.
type Summary struct {
	Invite
	Status   Status `json:"status"`
	UsesLeft int    `json:"usesLeft"`
}

// Summarize returns inv with its status at now.
func (inv Invite) Summarize(now time.Time) Summary {
	return Summary{Invite: inv, Status: inv.StatusAt(now), UsesLeft: inv.UsesLeft()}
}

// Public is what a public page may show about an invite that works:
// nothing about who created it, its label, or its uses.
type Public struct {
	Kind      Kind      `json:"kind"`
	Role      string    `json:"role,omitempty"`
	ExpiresAt time.Time `json:"expiresAt"`
}

func (inv Invite) public() Public {
	return Public{Kind: inv.Kind, Role: inv.Role, ExpiresAt: inv.ExpiresAt}
}

// NewToken returns a fresh link token.
func NewToken() string {
	b := make([]byte, TokenBytes)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// HashToken is what is stored for a token, and what invites are looked up
// by: its SHA-256 in lower-case hex.
func HashToken(token string) string {
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:])
}

// WellFormed reports whether s has the shape of a token, so the panel can
// refuse garbage without a database lookup.
func WellFormed(s string) bool {
	if len(s) != tokenLen {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

// tokenMatches compares the token's digest with the stored one in constant
// time. Both are 32 bytes whatever the token, so timing says nothing about
// how much of a token was right.
func tokenMatches(storedHash, token string) bool {
	want, err := hex.DecodeString(storedHash)
	if err != nil || len(want) != sha256.Size {
		return false
	}
	got := sha256.Sum256([]byte(token))
	return subtle.ConstantTimeCompare(got[:], want) == 1
}

// LinkPath is the path and fragment of an invite's link; the UI puts the
// panel's address in front.
func LinkPath(kind Kind, token string) string {
	switch kind {
	case KindPlayer:
		return JoinPath + "#t=" + token
	case KindMember:
		return AcceptPath + "#t=" + token
	default:
		return ""
	}
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

// Created is a new invite and its link. The link exists only here: the UI
// shows it once, and only Invite is stored.
type Created struct {
	Invite Invite `json:"invite"`
	// Path is LinkPath with the token.
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

// PlayerSpec is what the creator of a player invite chooses. A zero TTL or
// MaxUses takes the default.
type PlayerSpec struct {
	ServerID  string
	ProjectID string
	Label     string
	TTL       time.Duration
	MaxUses   int
}

// MemberSpec is what the creator of a member invite chooses. A zero TTL
// takes the default; a member invite always works exactly once.
type MemberSpec struct {
	ProjectID string
	Role      string
	Label     string
	TTL       time.Duration
}

// NewPlayer creates an invite that lets friends add themselves to a
// server's whitelist.
func NewPlayer(spec PlayerSpec, createdBy int64, now time.Time) (Created, error) {
	if !validRef(spec.ServerID) {
		return Created{}, badOptions("server", "The invite needs a valid server.")
	}
	uses := spec.MaxUses
	if uses == 0 {
		uses = DefaultPlayerUses
	}
	if uses < 1 || uses > MaxPlayerUses {
		return Created{}, badOptions("uses", fmt.Sprintf("A friend invite can work from 1 to %d times.", MaxPlayerUses), "max", strconv.Itoa(MaxPlayerUses))
	}
	return create(Invite{Kind: KindPlayer, ServerID: spec.ServerID, ProjectID: spec.ProjectID, MaxUses: uses},
		spec.Label, spec.TTL, DefaultPlayerTTL, MaxPlayerTTL, createdBy, now)
}

// NewMember creates an invite that lets one person create an account with
// a role in a project. The inviter must be allowed to give the role (see
// CanGrant).
func NewMember(spec MemberSpec, inviter Inviter, now time.Time) (Created, error) {
	if err := CanGrant(inviter, spec.Role); err != nil {
		return Created{}, err
	}
	return create(Invite{Kind: KindMember, ProjectID: spec.ProjectID, Role: spec.Role, MaxUses: 1},
		spec.Label, spec.TTL, DefaultMemberTTL, MaxMemberTTL, inviter.UserID, now)
}

func create(inv Invite, label string, ttl, defaultTTL, maxTTL time.Duration, createdBy int64, now time.Time) (Created, error) {
	if !validRef(inv.ProjectID) {
		return Created{}, badOptions("project", "The invite needs a valid project.")
	}
	if ttl == 0 {
		ttl = defaultTTL
	}
	if ttl < MinTTL || ttl > maxTTL {
		days := strconv.Itoa(int(maxTTL / (24 * time.Hour)))
		return Created{}, badOptions("expiry", fmt.Sprintf("An invite can last from 10 minutes to %s days.", days), "maxDays", days)
	}
	label, err := cleanLabel(label)
	if err != nil {
		return Created{}, err
	}
	now = stamp(now)
	token := NewToken()
	inv.ID = newID()
	inv.TokenHash = HashToken(token)
	inv.Label = label
	inv.CreatedBy = createdBy
	inv.CreatedAt = now
	inv.ExpiresAt = now.Add(ttl)
	return Created{Invite: inv, Path: LinkPath(inv.Kind, token)}, nil
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

// Check reports whether token opens inv as an invite of kind at now. A
// malformed or wrong token, a revoked invite and one of the other kind are
// all refused with the same public message; only Reason tells them apart.
func Check(inv Invite, token string, kind Kind, now time.Time) error {
	switch {
	case !WellFormed(token):
		return notWorking("the token is malformed")
	case !tokenMatches(inv.TokenHash, token):
		return notWorking("the token does not match")
	case inv.Kind != kind:
		return notWorking("the invite is of another kind")
	}
	return usable(inv, now)
}

func usable(inv Invite, now time.Time) error {
	switch {
	case !inv.RevokedAt.IsZero():
		return notWorking("the invite was revoked")
	case !now.Before(inv.ExpiresAt):
		return expired()
	case inv.Uses >= inv.MaxUses:
		return usedUp(inv.Kind)
	}
	return nil
}

// RecordUse returns inv with one more use, or why it can't be used. The
// database must count the use in one statement, so two people can't both
// take the last one:
//
//	UPDATE invites SET uses = uses + 1
//	WHERE id = ? AND revoked_at = 0 AND expires_at > ? AND uses < max_uses
//
// If no row changed, the invite ran out meanwhile: read it again and Check
// it for the message to show.
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
