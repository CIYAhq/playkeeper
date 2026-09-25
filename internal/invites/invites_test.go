package invites

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"
)

var t0 = time.Date(2026, 9, 25, 15, 4, 51, 123456789, time.UTC)

const (
	serverID  = "k3q9zt7mwa"
	projectID = "p8vx2hc4ne"
)

func tokenOf(t *testing.T, c Created) string {
	t.Helper()
	_, token, ok := strings.Cut(c.Path, "#t=")
	if !ok || !WellFormed(token) {
		t.Fatalf("path %q has no token", c.Path)
	}
	return token
}

func newPlayer(t *testing.T, spec PlayerSpec) (Invite, string) {
	t.Helper()
	if spec.ServerID == "" {
		spec.ServerID = serverID
	}
	if spec.ProjectID == "" {
		spec.ProjectID = projectID
	}
	c, err := NewPlayer(spec, 1, t0)
	if err != nil {
		t.Fatal(err)
	}
	return c.Invite, tokenOf(t, c)
}

func wantCode(t *testing.T, err error, code string) *Error {
	t.Helper()
	var e *Error
	if !errors.As(err, &e) || e.Code != code {
		t.Fatalf("error = %v (code %q), want code %q", err, CodeOf(err), code)
	}
	return e
}

func TestNewTokenEncodingAndEntropy(t *testing.T) {
	var set, clear [TokenBytes]byte
	seen := map[string]bool{}
	for range 64 {
		token := NewToken()
		if len(token) != 43 || !WellFormed(token) {
			t.Fatalf("token %q is not 43 URL-safe characters", token)
		}
		raw, err := base64.RawURLEncoding.Strict().DecodeString(token)
		if err != nil || len(raw) != TokenBytes {
			t.Fatalf("token %q does not decode to %d bytes: %v", token, TokenBytes, err)
		}
		if seen[token] {
			t.Fatal("a token repeated")
		}
		seen[token] = true
		for i, b := range raw {
			set[i] |= b
			clear[i] |= ^b
		}
	}
	// Across 64 tokens every one of the 256 bits must have been both 1 and
	// 0; a random bit stays put with probability 2^-63.
	for i := range set {
		if set[i] != 0xff || clear[i] != 0xff {
			t.Fatalf("byte %d has bits that never change: ever set %08b, ever clear %08b", i, set[i], clear[i])
		}
	}
}

func TestHashToken(t *testing.T) {
	if got := HashToken("abc"); got != "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad" {
		t.Errorf("HashToken(abc) = %s, want the SHA-256 in hex", got)
	}
	token := NewToken()
	if h := HashToken(token); len(h) != 64 || h != strings.ToLower(h) || strings.Contains(h, token) {
		t.Errorf("HashToken = %q", h)
	}
}

func TestCreatedStoresOnlyTheHash(t *testing.T) {
	c, err := NewPlayer(PlayerSpec{ServerID: serverID, ProjectID: projectID, Label: "Discord friends"}, 7, t0)
	if err != nil {
		t.Fatal(err)
	}
	token := tokenOf(t, c)
	if c.Path != "/join#t="+token {
		t.Errorf("Path = %q", c.Path)
	}
	if c.Invite.TokenHash != HashToken(token) {
		t.Error("the stored hash is not the token's")
	}
	sent, err := json.Marshal(c.Invite)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(sent), c.Invite.TokenHash) {
		t.Error("the token hash should not go to the UI")
	}
	var logged bytes.Buffer
	slog.New(slog.NewJSONHandler(&logged, nil)).Info("created", "invite", c)
	slog.New(slog.NewTextHandler(&logged, nil)).Info("created", "invite", c)
	for _, s := range []string{
		fmt.Sprintf("%+v", c.Invite), fmt.Sprintf("%#v", c.Invite), string(sent),
		fmt.Sprint(c), fmt.Sprintf("%+v", c), fmt.Sprintf("%#v", c), fmt.Sprintf("%v", &c), fmt.Sprintf("%s", c),
		logged.String(),
	} {
		if strings.Contains(s, token) {
			t.Errorf("the token leaked into %q", s)
		}
	}
}

func TestNewPlayerDefaults(t *testing.T) {
	c, err := NewPlayer(PlayerSpec{ServerID: serverID, ProjectID: projectID, Label: "  Discord friends  "}, 7, t0)
	if err != nil {
		t.Fatal(err)
	}
	inv := c.Invite
	created := t0.Truncate(time.Millisecond)
	want := Invite{ID: inv.ID, Kind: KindPlayer, TokenHash: inv.TokenHash, ProjectID: projectID, ServerID: serverID,
		Label: "Discord friends", CreatedBy: 7, CreatedAt: created, ExpiresAt: created.Add(DefaultPlayerTTL), MaxUses: DefaultPlayerUses}
	if inv != want {
		t.Fatalf("got  %+v\nwant %+v", inv, want)
	}
	if !ValidID(inv.ID) {
		t.Errorf("id %q is not valid", inv.ID)
	}
	if inv.StatusAt(t0) != StatusActive || inv.UsesLeft() != DefaultPlayerUses || inv.Actor() != "invite:"+inv.ID {
		t.Errorf("status %s, %d uses left, actor %s", inv.StatusAt(t0), inv.UsesLeft(), inv.Actor())
	}
}

func TestNewPlayerOptions(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*PlayerSpec)
		field  string
	}{
		{"shortest expiry", func(s *PlayerSpec) { s.TTL = MinTTL }, ""},
		{"longest expiry", func(s *PlayerSpec) { s.TTL = MaxPlayerTTL }, ""},
		{"expiry too short", func(s *PlayerSpec) { s.TTL = MinTTL - time.Second }, "expiry"},
		{"expiry too long", func(s *PlayerSpec) { s.TTL = MaxPlayerTTL + time.Second }, "expiry"},
		{"negative expiry", func(s *PlayerSpec) { s.TTL = -time.Hour }, "expiry"},
		{"one use", func(s *PlayerSpec) { s.MaxUses = 1 }, ""},
		{"most uses", func(s *PlayerSpec) { s.MaxUses = MaxPlayerUses }, ""},
		{"too many uses", func(s *PlayerSpec) { s.MaxUses = MaxPlayerUses + 1 }, "uses"},
		{"negative uses", func(s *PlayerSpec) { s.MaxUses = -1 }, "uses"},
		{"longest label", func(s *PlayerSpec) { s.Label = strings.Repeat("é", MaxLabelRunes) }, ""},
		{"label too long", func(s *PlayerSpec) { s.Label = strings.Repeat("é", MaxLabelRunes+1) }, "label"},
		{"label on two lines", func(s *PlayerSpec) { s.Label = "Discord\nfriends" }, "label"},
		{"label with a control character", func(s *PlayerSpec) { s.Label = "Discord\x00" }, "label"},
		{"label not UTF-8", func(s *PlayerSpec) { s.Label = "Discord \xff" }, "label"},
		{"no server", func(s *PlayerSpec) { s.ServerID = "" }, "server"},
		{"server id too short", func(s *PlayerSpec) { s.ServerID = "k3q9zt7mw" }, "server"},
		{"server id upper case", func(s *PlayerSpec) { s.ServerID = "K3Q9ZT7MWA" }, "server"},
		{"server id with a slash", func(s *PlayerSpec) { s.ServerID = "../../etc/" }, "server"},
		{"no project", func(s *PlayerSpec) { s.ProjectID = "" }, "project"},
		{"project id with a 1", func(s *PlayerSpec) { s.ProjectID = "p8vx2hc4n1" }, "project"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spec := PlayerSpec{ServerID: serverID, ProjectID: projectID}
			tc.change(&spec)
			c, err := NewPlayer(spec, 1, t0)
			if tc.field == "" {
				if err != nil {
					t.Fatal(err)
				}
				if ttl := c.Invite.ExpiresAt.Sub(c.Invite.CreatedAt); spec.TTL != 0 && ttl != spec.TTL {
					t.Errorf("lasts %v, want %v", ttl, spec.TTL)
				}
				return
			}
			e := wantCode(t, err, CodeBadOptions)
			if e.Params["field"] != tc.field || e.Status != 400 || !strings.HasSuffix(e.Msg, ".") {
				t.Errorf("got %+v, want field %s", e, tc.field)
			}
		})
	}
}

func TestIDs(t *testing.T) {
	seen := map[string]bool{}
	for range 200 {
		id := newID()
		if !ValidID(id) || !validRef(id) || seen[id] {
			t.Fatalf("id %q is invalid or repeated", id)
		}
		seen[id] = true
	}
	for _, id := range []string{"", "abcdefghi", "abcdefghijk", "abcdefghil", "abcdefghio", "abcdefgh01", "ABCDEFGHIJ", "abcdefgh/j"} {
		if ValidID(id) {
			t.Errorf("ValidID(%q) = true", id)
		}
	}
}

func TestCheck(t *testing.T) {
	inv, token := newPlayer(t, PlayerSpec{MaxUses: 2})
	first, last := byte('_'), byte('A')
	if token[0] == '_' {
		first = '-'
	}
	if token[42] == 'A' {
		last = 'B'
	}
	with := func(f func(*Invite)) Invite {
		c := inv
		f(&c)
		return c
	}
	for _, tc := range []struct {
		name  string
		inv   Invite
		token string
		kind  Kind
		now   time.Time
		code  string
	}{
		{"works", inv, token, KindPlayer, t0, ""},
		{"works until the last moment", inv, token, KindPlayer, inv.ExpiresAt.Add(-time.Millisecond), ""},
		{"works with one use left", with(func(i *Invite) { i.Uses = 1 }), token, KindPlayer, t0, ""},
		{"stored hash in upper case", with(func(i *Invite) { i.TokenHash = strings.ToUpper(i.TokenHash) }), token, KindPlayer, t0, ""},
		{"no token", inv, "", KindPlayer, t0, CodeNotWorking},
		{"token cut short", inv, token[:42], KindPlayer, t0, CodeNotWorking},
		{"token too long", inv, token + "A", KindPlayer, t0, CodeNotWorking},
		{"token with other characters", inv, strings.Repeat("+", 43), KindPlayer, t0, CodeNotWorking},
		{"another token", inv, NewToken(), KindPlayer, t0, CodeNotWorking},
		{"last character changed", inv, token[:42] + string(last), KindPlayer, t0, CodeNotWorking},
		{"first character changed", inv, string(first) + token[1:], KindPlayer, t0, CodeNotWorking},
		{"stored hash corrupted", with(func(i *Invite) { i.TokenHash = "zz" + i.TokenHash[2:] }), token, KindPlayer, t0, CodeNotWorking},
		{"stored hash cut short", with(func(i *Invite) { i.TokenHash = i.TokenHash[:62] }), token, KindPlayer, t0, CodeNotWorking},
		{"no stored hash", with(func(i *Invite) { i.TokenHash = "" }), token, KindPlayer, t0, CodeNotWorking},
		{"wrong kind", inv, token, KindMember, t0, CodeNotWorking},
		{"revoked", Revoke(inv, t0), token, KindPlayer, t0, CodeNotWorking},
		{"revoked after expiring", Revoke(inv, inv.ExpiresAt), token, KindPlayer, inv.ExpiresAt.Add(time.Hour), CodeNotWorking},
		{"expired", inv, token, KindPlayer, inv.ExpiresAt, CodeExpired},
		{"used up", with(func(i *Invite) { i.Uses = 2 }), token, KindPlayer, t0, CodeUsedUp},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := Check(tc.inv, tc.token, tc.kind, tc.now)
			if tc.code == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			e := wantCode(t, err, tc.code)
			if tc.token != "" && strings.Contains(e.Reason+e.Msg+e.Hint, tc.token) {
				t.Error("the refusal repeats the token")
			}
		})
	}
}

// A page must not be able to tell a link that never existed from one that
// was revoked, mistyped, or meant for the other page.
func TestRefusalsLookAlike(t *testing.T) {
	inv, token := newPlayer(t, PlayerSpec{})
	refusals := map[string]error{
		"unknown":    NotFound(),
		"malformed":  Check(inv, "nope", KindPlayer, t0),
		"mismatch":   Check(inv, NewToken(), KindPlayer, t0),
		"wrong kind": Check(inv, token, KindMember, t0),
		"revoked":    Check(Revoke(inv, t0), token, KindPlayer, t0),
	}
	want := NotFound()
	reasons := map[string]bool{}
	for name, err := range refusals {
		e := wantCode(t, err, CodeNotWorking)
		if e.Msg != want.Msg || e.Hint != want.Hint || e.Status != want.Status || e.Params != nil || e.RetryAfter != 0 {
			t.Errorf("%s reads differently: %+v", name, e)
		}
		if e.Msg != "This invite link doesn't work any more." {
			t.Errorf("message = %q", e.Msg)
		}
		if e.Reason == "" || reasons[e.Reason] {
			t.Errorf("%s: reason %q is empty or shared", name, e.Reason)
		}
		reasons[e.Reason] = true
	}
}

func TestRecordUse(t *testing.T) {
	inv, _ := newPlayer(t, PlayerSpec{MaxUses: 3})
	var err error
	for i := 1; i <= 3; i++ {
		if inv, err = RecordUse(inv, t0); err != nil || inv.Uses != i {
			t.Fatalf("use %d: %v, uses = %d", i, err, inv.Uses)
		}
	}
	after, err := RecordUse(inv, t0)
	wantCode(t, err, CodeUsedUp)
	if after.Uses != 3 || inv.StatusAt(t0) != StatusUsedUp || inv.UsesLeft() != 0 {
		t.Errorf("uses = %d, status %s, %d left", after.Uses, inv.StatusAt(t0), inv.UsesLeft())
	}

	fresh, _ := newPlayer(t, PlayerSpec{})
	_, err = RecordUse(fresh, fresh.ExpiresAt)
	wantCode(t, err, CodeExpired)
	_, err = RecordUse(Revoke(fresh, t0), t0)
	wantCode(t, err, CodeNotWorking)
}

func TestMemberInviteWorksOnce(t *testing.T) {
	c, err := NewMember(MemberSpec{ProjectID: projectID, Role: RoleAdmin}, Inviter{UserID: 1, InstallRole: InstallOwner}, t0)
	if err != nil {
		t.Fatal(err)
	}
	inv, err := RecordUse(c.Invite, t0)
	if err != nil {
		t.Fatal(err)
	}
	_, err = RecordUse(inv, t0)
	if e := wantCode(t, err, CodeUsedUp); e.Msg != "This invite has already been used." {
		t.Errorf("message = %q", e.Msg)
	}
}

func TestRevoke(t *testing.T) {
	inv, token := newPlayer(t, PlayerSpec{})
	revoked := Revoke(inv, t0.Add(time.Hour))
	if !revoked.RevokedAt.Equal(t0.Add(time.Hour).Truncate(time.Millisecond)) || revoked.StatusAt(t0.Add(2*time.Hour)) != StatusRevoked {
		t.Fatalf("RevokedAt = %v, status %s", revoked.RevokedAt, revoked.StatusAt(t0))
	}
	if again := Revoke(revoked, t0.Add(5*time.Hour)); !again.RevokedAt.Equal(revoked.RevokedAt) {
		t.Error("revoking again moved the time")
	}
	if !inv.RevokedAt.IsZero() {
		t.Error("Revoke changed its argument")
	}
	wantCode(t, Check(revoked, token, KindPlayer, t0), CodeNotWorking)
}

func TestStatusAt(t *testing.T) {
	inv, _ := newPlayer(t, PlayerSpec{MaxUses: 2})
	used := inv
	used.Uses = 2
	later := inv.ExpiresAt.Add(time.Minute)
	for _, tc := range []struct {
		inv  Invite
		now  time.Time
		want Status
	}{
		{inv, t0, StatusActive},
		{inv, later, StatusExpired},
		{used, t0, StatusUsedUp},
		{used, later, StatusUsedUp},
		{Revoke(inv, t0), t0, StatusRevoked},
		{Revoke(used, t0), later, StatusRevoked},
	} {
		if got := tc.inv.StatusAt(tc.now); got != tc.want {
			t.Errorf("status = %s, want %s", got, tc.want)
		}
	}
	s := used.Summarize(t0)
	if s.Status != StatusUsedUp || s.UsesLeft != 0 || s.ID != inv.ID {
		t.Errorf("summary %+v", s)
	}
	b, _ := json.Marshal(s)
	if !strings.Contains(string(b), `"status":"used_up"`) || strings.Contains(string(b), "revokedAt") {
		t.Errorf("summary JSON %s", b)
	}
}

func TestLinkPath(t *testing.T) {
	token := NewToken()
	for kind, want := range map[Kind]string{KindPlayer: "/join#t=" + token, KindMember: "/accept#t=" + token, "other": ""} {
		got := LinkPath(kind, token)
		if got != want {
			t.Errorf("LinkPath(%s) = %q, want %q", kind, got, want)
		}
		if path, _, _ := strings.Cut(got, "#"); strings.Contains(path, token) {
			t.Errorf("the token is in the path part of %q, which browsers send", got)
		}
	}
}

func TestPublicViewHidesTheRest(t *testing.T) {
	inv, token := newPlayer(t, PlayerSpec{Label: "Secret plans"})
	p, err := PreviewPlayer(inv, token, t0)
	if err != nil {
		t.Fatal(err)
	}
	if p != (Public{Kind: KindPlayer, ExpiresAt: inv.ExpiresAt}) {
		t.Errorf("preview %+v", p)
	}
	b, _ := json.Marshal(p)
	for _, s := range []string{"Secret plans", "createdBy", "uses", "serverId", inv.ID} {
		if strings.Contains(string(b), s) {
			t.Errorf("preview %s shows %q", b, s)
		}
	}
}

func TestMillis(t *testing.T) {
	inv, _ := newPlayer(t, PlayerSpec{})
	for _, tm := range []time.Time{inv.CreatedAt, inv.ExpiresAt, Revoke(inv, t0).RevokedAt} {
		if back := FromMillis(Millis(tm)); !back.Equal(tm) || back.Location() != time.UTC || back != tm {
			t.Errorf("%v came back as %v", tm, back)
		}
	}
	if Millis(time.Time{}) != 0 || !FromMillis(0).IsZero() {
		t.Error("the zero time should be stored as 0")
	}
}

func TestErrors(t *testing.T) {
	cause := errors.New("dial tcp: connection refused")
	all := []*Error{
		NotFound(), UsernameTaken(), expired(), usedUp(KindPlayer), usedUp(KindMember),
		badOptions("uses", "A friend invite can work from 1 to 100 times."), roleUnknown(), roleNotAllowed(RoleAdmin), roleNotAllowed(InstallOwner),
		rateLimited("address", 1200*time.Millisecond), rateLimited("invite", time.Minute),
		playerName(), playerNotFound("Nobody"), playerDemo("PipDemo42"), playerLegacy("OldTimer"),
		mojangBusy(time.Minute, cause), mojangDown(cause),
		usernameInvalid("length", "Username must be 3–32 characters."), passwordInvalid("too_short", "Password must be at least 10 characters."),
	}
	for _, e := range all {
		if e.Code == "" || e.Status < 400 || e.Error() != e.Msg || !strings.HasSuffix(e.Msg, ".") || e.Hint != "" && !strings.HasSuffix(e.Hint, ".") {
			t.Errorf("%+v is not a coded refusal in full sentences", e)
		}
	}
	wrapped := fmt.Errorf("redeeming: %w", mojangDown(cause))
	if CodeOf(wrapped) != CodeMojangDown || !errors.Is(wrapped, cause) || CodeOf(cause) != "" || CodeOf(nil) != "" {
		t.Error("codes and causes are not reachable through wrapping")
	}
	if e := rateLimited("address", 1200*time.Millisecond); e.Params["seconds"] != "2" || e.RetryAfter != 1200*time.Millisecond {
		t.Errorf("rate limit params %v", e.Params)
	}
}
