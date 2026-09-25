package invites

import (
	"bytes"
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"testing"
	"time"
)

var t0 = time.Date(2026, 9, 25, 15, 4, 51, 123456789, time.UTC)

const (
	serverID  = "k3q9zt7mwa"
	projectID = "p8vx2hc4ne"
)

func codeOf(t *testing.T, c Created) string {
	t.Helper()
	code, ok := CodeFromPath(c.Path)
	if !ok {
		t.Fatalf("path %q has no code", c.Path)
	}
	return code
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
	return c.Invite, codeOf(t, c)
}

func wantCode(t *testing.T, err error, code string) *Error {
	t.Helper()
	var e *Error
	if !errors.As(err, &e) || e.Code != code {
		t.Fatalf("error = %v (code %q), want code %q", err, CodeOf(err), code)
	}
	return e
}

// leaks lists the ways an invite could end up in a log or an answer where
// its code must not.
func leaks(t *testing.T, code string, values ...any) {
	t.Helper()
	var logged bytes.Buffer
	for _, v := range values {
		slog.New(slog.NewJSONHandler(&logged, nil)).Info("x", "v", v)
		slog.New(slog.NewTextHandler(&logged, nil)).Info("x", "v", v)
		for _, s := range []string{fmt.Sprint(v), fmt.Sprintf("%v", v), fmt.Sprintf("%+v", v), fmt.Sprintf("%#v", v), fmt.Sprintf("%s", v), fmt.Sprintf("%q", v), fmt.Sprintf("%x", v), fmt.Sprintf("%d", v)} {
			if strings.Contains(s, code) || strings.Contains(s, HashCode(code)) {
				t.Errorf("the code leaked into %q", s)
			}
		}
	}
	if strings.Contains(logged.String(), code) || strings.Contains(logged.String(), HashCode(code)) {
		t.Errorf("the code leaked into the log: %s", logged.String())
	}
}

func TestFriendInviteKeepsItsCode(t *testing.T) {
	c, err := NewPlayer(PlayerSpec{ServerID: serverID, ProjectID: projectID, Label: "Discord crew"}, 7, t0)
	if err != nil {
		t.Fatal(err)
	}
	code := codeOf(t, c)
	if c.Path != "/join/"+code || c.Invite.CodeHash != HashCode(code) {
		t.Errorf("path %q, hash %q", c.Path, c.Invite.CodeHash)
	}
	if c.Invite.Code != code || c.Invite.Path() != c.Path {
		t.Error("a friend invite should keep its code, so the link can be copied again")
	}
	sent, err := json.Marshal(c.Invite)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(sent), code) || strings.Contains(string(sent), c.Invite.CodeHash) {
		t.Errorf("the invite's JSON holds its code: %s", sent)
	}
	s := c.Invite.Summarize(t0)
	listed, _ := json.Marshal(s)
	if s.Path != c.Path || !strings.Contains(string(listed), `"path":"/join/`+code+`"`) {
		t.Errorf("the management list should carry the link: %s", listed)
	}
	inv := c.Invite
	leaks(t, code, c, &c, inv, &inv, s, &s)
}

func TestNewPlayerDefaults(t *testing.T) {
	c, err := NewPlayer(PlayerSpec{ServerID: serverID, ProjectID: projectID, Label: "  Discord crew  "}, 7, t0)
	if err != nil {
		t.Fatal(err)
	}
	inv := c.Invite
	created := t0.Truncate(time.Millisecond)
	want := Invite{ID: inv.ID, Kind: KindPlayer, CodeHash: inv.CodeHash, Code: codeOf(t, c), ProjectID: projectID, ServerID: serverID, Approval: RightAway,
		Label: "Discord crew", CreatedBy: 7, CreatedAt: created, ExpiresAt: created.Add(7 * 24 * time.Hour), MaxUses: 5}
	if inv != want {
		t.Fatalf("got  %#v\nwant %#v", inv.Summarize(t0), want.Summarize(t0))
	}
	if !ValidID(inv.ID) {
		t.Errorf("id %q is not valid", inv.ID)
	}
	if left, limited := inv.UsesLeft(); inv.StatusAt(t0) != StatusActive || left != 5 || !limited || inv.Actor() != "invite:"+inv.ID {
		t.Errorf("status %s, %d uses left (%v), actor %s", inv.StatusAt(t0), left, limited, inv.Actor())
	}
}

func TestExpiries(t *testing.T) {
	want := []Expiry{"1d", "7d", "30d", "until_turned_off"}
	if got := Expiries(); !slices.Equal(got, want) || DefaultExpiry != "7d" {
		t.Errorf("Expiries() = %v, default %s", got, DefaultExpiry)
	}
}

func TestNewPlayerOptions(t *testing.T) {
	day := 24 * time.Hour
	for _, tc := range []struct {
		name     string
		change   func(*PlayerSpec)
		field    string
		lifetime time.Duration
		uses     int
	}{
		{"1 day", func(s *PlayerSpec) { s.Expiry = ExpiryOneDay }, "", day, 5},
		{"7 days", func(s *PlayerSpec) { s.Expiry = ExpirySevenDays }, "", 7 * day, 5},
		{"30 days", func(s *PlayerSpec) { s.Expiry = ExpiryThirtyDays }, "", 30 * day, 5},
		{"until turned off", func(s *PlayerSpec) { s.Expiry = ExpiryUntilTurnedOff }, "", 0, 5},
		{"a number of days", func(s *PlayerSpec) { s.Expiry = "2d" }, "expiry", 0, 0},
		{"expiry in capitals", func(s *PlayerSpec) { s.Expiry = "7D" }, "expiry", 0, 0},
		{"expiry as hours", func(s *PlayerSpec) { s.Expiry = "168h" }, "expiry", 0, 0},
		{"one friend", func(s *PlayerSpec) { s.MaxUses = 1 }, "", 7 * day, 1},
		{"most friends", func(s *PlayerSpec) { s.MaxUses = MaxPlayerUses }, "", 7 * day, 100},
		{"too many friends", func(s *PlayerSpec) { s.MaxUses = MaxPlayerUses + 1 }, "uses", 0, 0},
		{"negative friends", func(s *PlayerSpec) { s.MaxUses = -1 }, "uses", 0, 0},
		{"no limit, chosen", func(s *PlayerSpec) { s.Unlimited = true }, "", 7 * day, 0},
		{"no limit and a number", func(s *PlayerSpec) { s.Unlimited = true; s.MaxUses = 3 }, "uses", 0, 0},
		{"no limit until turned off", func(s *PlayerSpec) { s.Unlimited = true; s.Expiry = ExpiryUntilTurnedOff }, "", 0, 0},
		{"after you say yes", func(s *PlayerSpec) { s.Approval = AfterYes }, "", 7 * day, 5},
		{"right away, chosen", func(s *PlayerSpec) { s.Approval = RightAway }, "", 7 * day, 5},
		{"made-up approval", func(s *PlayerSpec) { s.Approval = "maybe" }, "approval", 0, 0},
		{"longest label", func(s *PlayerSpec) { s.Label = strings.Repeat("é", MaxLabelRunes) }, "", 7 * day, 5},
		{"label too long", func(s *PlayerSpec) { s.Label = strings.Repeat("é", MaxLabelRunes+1) }, "label", 0, 0},
		{"label on two lines", func(s *PlayerSpec) { s.Label = "Discord\ncrew" }, "label", 0, 0},
		{"label with a control character", func(s *PlayerSpec) { s.Label = "Discord\x00" }, "label", 0, 0},
		{"label not UTF-8", func(s *PlayerSpec) { s.Label = "Discord \xff" }, "label", 0, 0},
		{"no server", func(s *PlayerSpec) { s.ServerID = "" }, "server", 0, 0},
		{"server id too short", func(s *PlayerSpec) { s.ServerID = "k3q9zt7mw" }, "server", 0, 0},
		{"server id upper case", func(s *PlayerSpec) { s.ServerID = "K3Q9ZT7MWA" }, "server", 0, 0},
		{"server id with a slash", func(s *PlayerSpec) { s.ServerID = "../../etc/" }, "server", 0, 0},
		{"no project", func(s *PlayerSpec) { s.ProjectID = "" }, "project", 0, 0},
		{"project id with a 1", func(s *PlayerSpec) { s.ProjectID = "p8vx2hc4n1" }, "project", 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spec := PlayerSpec{ServerID: serverID, ProjectID: projectID}
			tc.change(&spec)
			c, err := NewPlayer(spec, 1, t0)
			if tc.field == "" {
				if err != nil {
					t.Fatal(err)
				}
				inv := c.Invite
				if lifetime := inv.ExpiresAt.Sub(inv.CreatedAt); tc.lifetime == 0 && !inv.ExpiresAt.IsZero() || tc.lifetime != 0 && lifetime != tc.lifetime {
					t.Errorf("expires %v after it was made, want %v", lifetime, tc.lifetime)
				}
				if inv.MaxUses != tc.uses || inv.Approval != cmp.Or(spec.Approval, RightAway) {
					t.Errorf("max uses %d, approval %q", inv.MaxUses, inv.Approval)
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

// other returns a character of the code alphabet that differs from c.
func other(c byte) string {
	if c == 'A' {
		return "B"
	}
	return "A"
}

func TestCheck(t *testing.T) {
	inv, code := newPlayer(t, PlayerSpec{MaxUses: 2})
	last := CodeLen - 1
	with := func(f func(*Invite)) Invite {
		c := inv
		f(&c)
		return c
	}
	for _, tc := range []struct {
		name string
		inv  Invite
		code string
		kind Kind
		now  time.Time
		want string
	}{
		{"works", inv, code, KindPlayer, t0, ""},
		{"works until the last moment", inv, code, KindPlayer, inv.ExpiresAt.Add(-time.Millisecond), ""},
		{"works with one use left", with(func(i *Invite) { i.Uses = 1 }), code, KindPlayer, t0, ""},
		{"stored hash in upper case", with(func(i *Invite) { i.CodeHash = strings.ToUpper(i.CodeHash) }), code, KindPlayer, t0, ""},
		{"no code", inv, "", KindPlayer, t0, CodeNotWorking},
		{"code cut short", inv, code[:last], KindPlayer, t0, CodeNotWorking},
		{"code too long", inv, code + "A", KindPlayer, t0, CodeNotWorking},
		{"code with other characters", inv, strings.Repeat("-", CodeLen), KindPlayer, t0, CodeNotWorking},
		{"code in other capitals", inv, strings.ToUpper(code), KindPlayer, t0, CodeNotWorking},
		{"another code", inv, NewCode(), KindPlayer, t0, CodeNotWorking},
		{"last character changed", inv, code[:last] + other(code[last]), KindPlayer, t0, CodeNotWorking},
		{"first character changed", inv, other(code[0]) + code[1:], KindPlayer, t0, CodeNotWorking},
		{"the stored code without its hash", with(func(i *Invite) { i.CodeHash = "" }), code, KindPlayer, t0, CodeNotWorking},
		{"stored hash corrupted", with(func(i *Invite) { i.CodeHash = "zz" + i.CodeHash[2:] }), code, KindPlayer, t0, CodeNotWorking},
		{"stored hash cut short", with(func(i *Invite) { i.CodeHash = i.CodeHash[:62] }), code, KindPlayer, t0, CodeNotWorking},
		{"the hash given as the code", inv, inv.CodeHash, KindPlayer, t0, CodeNotWorking},
		{"wrong kind", inv, code, KindMember, t0, CodeNotWorking},
		{"revoked", Revoke(inv, t0), code, KindPlayer, t0, CodeNotWorking},
		{"revoked after expiring", Revoke(inv, inv.ExpiresAt), code, KindPlayer, inv.ExpiresAt.Add(time.Hour), CodeNotWorking},
		{"expired", inv, code, KindPlayer, inv.ExpiresAt, CodeExpired},
		{"used up", with(func(i *Invite) { i.Uses = 2 }), code, KindPlayer, t0, CodeUsedUp},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := Check(tc.inv, tc.code, tc.kind, tc.now)
			if tc.want == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			e := wantCode(t, err, tc.want)
			if tc.code != "" && strings.Contains(e.Reason+e.Msg+e.Hint+fmt.Sprint(e.Params), tc.code) {
				t.Error("the refusal repeats the code")
			}
		})
	}
}

// A page must not be able to tell a link that never existed from one that
// was revoked, mistyped, or meant for the other kind.
func TestRefusalsLookAlike(t *testing.T) {
	inv, code := newPlayer(t, PlayerSpec{})
	refusals := map[string]error{
		"unknown":    NotFound(),
		"malformed":  Check(inv, "nope", KindPlayer, t0),
		"mismatch":   Check(inv, NewCode(), KindPlayer, t0),
		"wrong kind": Check(inv, code, KindMember, t0),
		"revoked":    Check(Revoke(inv, t0), code, KindPlayer, t0),
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
	if left, limited := inv.UsesLeft(); after.Uses != 3 || inv.StatusAt(t0) != StatusUsedUp || left != 0 || !limited {
		t.Errorf("uses = %d, status %s, %d left", after.Uses, inv.StatusAt(t0), left)
	}

	fresh, _ := newPlayer(t, PlayerSpec{})
	_, err = RecordUse(fresh, fresh.ExpiresAt)
	wantCode(t, err, CodeExpired)
	_, err = RecordUse(Revoke(fresh, t0), t0)
	wantCode(t, err, CodeNotWorking)
}

func TestUntilTurnedOff(t *testing.T) {
	inv, code := newPlayer(t, PlayerSpec{Expiry: ExpiryUntilTurnedOff, MaxUses: 2})
	years := t0.AddDate(20, 0, 0)
	if !inv.ExpiresAt.IsZero() || inv.StatusAt(years) != StatusActive || Check(inv, code, KindPlayer, years) != nil {
		t.Fatalf("an invite without expiry stopped: expires %v, status %s", inv.ExpiresAt, inv.StatusAt(years))
	}
	b, _ := json.Marshal(inv.Summarize(years))
	if strings.Contains(string(b), "expiresAt") || !strings.Contains(string(b), `"usesLeft":2`) {
		t.Errorf("summary JSON %s", b)
	}
	var err error
	for range 2 {
		if inv, err = RecordUse(inv, years); err != nil {
			t.Fatal(err)
		}
	}
	_, err = RecordUse(inv, years)
	wantCode(t, err, CodeUsedUp)
	wantCode(t, Check(Revoke(inv, years), code, KindPlayer, years), CodeNotWorking)
}

func TestUnlimited(t *testing.T) {
	inv, code := newPlayer(t, PlayerSpec{Unlimited: true})
	var err error
	for range 1000 {
		if inv, err = RecordUse(inv, t0); err != nil {
			t.Fatal(err)
		}
	}
	if left, limited := inv.UsesLeft(); inv.Uses != 1000 || inv.StatusAt(t0) != StatusActive || limited || left != 0 {
		t.Errorf("uses %d, status %s, %d left (%v)", inv.Uses, inv.StatusAt(t0), left, limited)
	}
	s := inv.Summarize(t0)
	b, _ := json.Marshal(s)
	if s.UsesLeft != nil || strings.Contains(string(b), "usesLeft") || !strings.Contains(string(b), `"maxUses":0`) {
		t.Errorf("summary JSON %s", b)
	}
	wantCode(t, Check(inv, code, KindPlayer, inv.ExpiresAt), CodeExpired)
	wantCode(t, Check(Revoke(inv, t0), code, KindPlayer, t0), CodeNotWorking)
}

func TestMemberInviteWorksOnce(t *testing.T) {
	c, err := NewMember(MemberSpec{ProjectID: projectID, Role: RoleAdmin}, Inviter{UserID: 1, InstallRole: InstallOwner}, t0)
	if err != nil {
		t.Fatal(err)
	}
	if c.Invite.MaxUses != 1 || c.Invite.ExpiresAt.Sub(c.Invite.CreatedAt) != 7*24*time.Hour || c.Invite.Approval != "" {
		t.Errorf("invite %#v", c.Invite.Summarize(t0))
	}
	inv, err := RecordUse(c.Invite, t0)
	if err != nil {
		t.Fatal(err)
	}
	_, err = RecordUse(inv, t0)
	if e := wantCode(t, err, CodeUsedUp); e.Msg != "This invite has already been used." {
		t.Errorf("message = %q", e.Msg)
	}

	// The zeros that mean "no limit" and "until turned off" on a friend
	// invite must not open a member invite up.
	for _, maxUses := range []int{0, 5, -1} {
		row := c.Invite
		row.MaxUses = maxUses
		used, err := RecordUse(row, t0)
		if err != nil {
			t.Fatalf("max uses %d: %v", maxUses, err)
		}
		if _, err = RecordUse(used, t0); CodeOf(err) != CodeUsedUp {
			t.Errorf("max uses %d: a second use gave %v", maxUses, err)
		}
		if left, limited := row.UsesLeft(); left != 1 || !limited {
			t.Errorf("max uses %d: %d left (%v)", maxUses, left, limited)
		}
	}
	row := c.Invite
	row.ExpiresAt = time.Time{}
	if _, err := RecordUse(row, t0); CodeOf(err) != CodeExpired || row.StatusAt(t0) != StatusExpired {
		t.Errorf("a member invite without an expiry: %v, status %s", err, row.StatusAt(t0))
	}
	odd := Invite{Kind: "guest", MaxUses: 0, ExpiresAt: time.Time{}}
	if odd.StatusAt(t0) != StatusUsedUp {
		t.Errorf("a row of an unknown kind reads as %s", odd.StatusAt(t0))
	}
}

func TestRevoke(t *testing.T) {
	inv, code := newPlayer(t, PlayerSpec{})
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
	wantCode(t, Check(revoked, code, KindPlayer, t0), CodeNotWorking)
}

func TestStatusAt(t *testing.T) {
	inv, _ := newPlayer(t, PlayerSpec{MaxUses: 2})
	used := inv
	used.Uses = 2
	later := inv.ExpiresAt.Add(time.Minute)
	forever, _ := newPlayer(t, PlayerSpec{Expiry: ExpiryUntilTurnedOff, Unlimited: true})
	forever.Uses = 500
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
		{forever, later.AddDate(10, 0, 0), StatusActive},
		{Revoke(forever, t0), t0, StatusRevoked},
	} {
		if got := tc.inv.StatusAt(tc.now); got != tc.want {
			t.Errorf("status = %s, want %s", got, tc.want)
		}
	}
	s := used.Summarize(t0)
	if s.Status != StatusUsedUp || s.UsesLeft == nil || *s.UsesLeft != 0 || s.ID != inv.ID {
		t.Errorf("summary %#v", s)
	}
	b, _ := json.Marshal(s)
	for _, want := range []string{`"status":"used_up"`, `"usesLeft":0`, `"approval":"right_away"`, `"expiresAt":"`} {
		if !strings.Contains(string(b), want) || strings.Contains(string(b), "revokedAt") {
			t.Errorf("summary JSON %s lacks %s", b, want)
		}
	}
}

func TestPublicViewHidesTheRest(t *testing.T) {
	inv, code := newPlayer(t, PlayerSpec{Label: "Secret plans"})
	p, err := PreviewPlayer(inv, code, t0)
	if err != nil {
		t.Fatal(err)
	}
	if p != (Public{Kind: KindPlayer, ExpiresAt: inv.ExpiresAt}) {
		t.Errorf("preview %+v", p)
	}
	b, _ := json.Marshal(p)
	for _, s := range []string{"Secret plans", "createdBy", "uses", "serverId", inv.ID, code} {
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
		badOptions("uses", "An invite link can be for 1 to 100 friends, or have no limit."), roleUnknown(), roleNotAllowed(RoleAdmin), roleNotAllowed(InstallOwner),
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
