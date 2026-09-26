package twofactor

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/CIYAhq/playkeeper/internal/qrcode"
	"github.com/CIYAhq/playkeeper/internal/totp"
)

const account = "alice@play.example.com"

// enrolled is a Factor turned on at t0, with its recovery codes.
func enrolled(t *testing.T) (Factor, []string) {
	t.Helper()
	f, _, err := Begin(Factor{}, true, account, t0, newRand(2))
	if err != nil {
		t.Fatal(err)
	}
	f, codes, err := Confirm(f, totp.Code(f.Secret, t0), t0, newRand(3))
	if err != nil {
		t.Fatal(err)
	}
	return f, codes
}

// wrongCode is six digits the app does not show within the window around at.
func wrongCode(t *testing.T, f Factor, at time.Time) string {
	t.Helper()
	for _, c := range []string{"000000", "111111", "222222", "333333"} {
		if _, err := totp.Verify(f.Secret, c, at, 0); err != nil {
			return c
		}
	}
	t.Fatal("no wrong code found")
	return ""
}

// checkRevision checks that Revision went up by one exactly when something
// else changed, which callers rely on to know when to store.
func checkRevision(t *testing.T, before, after Factor) {
	t.Helper()
	b, a := before, after
	b.Revision, a.Revision = 0, 0
	same := reflect.DeepEqual(a, b)
	if same && after.Revision != before.Revision {
		t.Errorf("Revision went from %d to %d without a change", before.Revision, after.Revision)
	}
	if !same && after.Revision != before.Revision+1 {
		t.Errorf("the Factor changed but Revision went from %d to %d", before.Revision, after.Revision)
	}
}

func signIn(t *testing.T, f Factor, code string, at time.Time) (Factor, Result, error) {
	t.Helper()
	next, res, err := SignIn(f, code, at)
	checkRevision(t, f, next)
	return next, res, err
}

func kinds(ns []Notice) []NoticeKind {
	out := []NoticeKind{}
	for _, n := range ns {
		out = append(out, n.Kind)
	}
	return out
}

func TestTurningOnShowsTheSecretOnlyUntilConfirmed(t *testing.T) {
	if _, _, err := Begin(Factor{}, false, account, t0, newRand(2)); KindOf(err) != KindPasswordWrong {
		t.Fatalf("Begin without the password: %v", err)
	}
	f, setup, err := Begin(Factor{}, true, account, t0, newRand(2))
	if err != nil {
		t.Fatal(err)
	}
	checkRevision(t, Factor{}, f)
	if st := f.Status(t0); st != (Status{State: StatePending, SetupExpiresAt: t0.Add(SetupTTL)}) {
		t.Errorf("status while setting up: %+v", st)
	}
	uri, err := totp.KeyURI(f.Secret, "Playkeeper", account)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(uri, "otpauth://totp/Playkeeper:alice%40play.example.com?secret=") {
		t.Errorf("URI = %s", uri)
	}
	qr, err := qrcode.Encode(uri)
	if err != nil {
		t.Fatal(err)
	}
	want := Setup{QRCodeSVG: qr.SVG(), ManualKey: f.Secret.Grouped(), URI: uri, Issuer: "Playkeeper", Account: account, ExpiresAt: t0.Add(SetupTTL)}
	if setup != want {
		t.Errorf("setup: key %q, URI %q, issuer %q, account %q, expires %v, QR matches %v",
			setup.ManualKey, setup.URI, setup.Issuer, setup.Account, setup.ExpiresAt, setup.QRCodeSVG == want.QRCodeSVG)
	}

	if again, err := f.Setup(account, t0.Add(SetupTTL-time.Second)); err != nil || again != setup {
		t.Errorf("the same QR code and key must show until the setup expires: %v", err)
	}
	if _, err := f.Setup(account, t0.Add(SetupTTL)); KindOf(err) != KindSetupExpired {
		t.Errorf("after SetupTTL: %v", err)
	}
	if st := f.Status(t0.Add(SetupTTL)); st.State != StateOff {
		t.Errorf("an expired setup is off: %+v", st)
	}

	at := t0.Add(time.Minute)
	restarted, _, err := Begin(f, true, account, at, newRand(4))
	if err != nil || restarted.Secret.Base32() == f.Secret.Base32() {
		t.Fatalf("starting again must make a new secret: %v", err)
	}
	checkRevision(t, f, restarted)
	on, _, err := Confirm(restarted, totp.Code(restarted.Secret, at), at, newRand(3))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := on.Setup(account, at); KindOf(err) != KindOn {
		t.Errorf("once on, the secret must not be shown again: %v", err)
	}
	if next, _, err := Begin(on, true, account, at, newRand(5)); KindOf(err) != KindOn || !reflect.DeepEqual(next, on) {
		t.Errorf("Begin while on: %v", err)
	}
	if _, err := (Factor{}).Setup(account, t0); KindOf(err) != KindNoSetup {
		t.Errorf("Setup with nothing started: %v", err)
	}
	if next, _, err := Begin(Factor{}, true, "bad:name", t0, newRand(2)); err == nil || !reflect.DeepEqual(next, Factor{}) {
		t.Errorf("an account name with a colon must be refused: %v", err)
	}
}

func TestConfirmNeedsACorrectAppCodeInTime(t *testing.T) {
	f, _, err := Begin(Factor{}, true, account, t0, newRand(2))
	if err != nil {
		t.Fatal(err)
	}
	at := t0.Add(2 * time.Minute)
	for _, tc := range []struct {
		code string
		kind Kind
	}{
		{wrongCode(t, f, at), KindCodeWrong},
		{"12345", KindAppCodeMalformed},
		{"abcd-efgh-ijkm-npqr", KindAppCodeMalformed},
		{"", KindAppCodeMalformed},
	} {
		next, codes, err := Confirm(f, tc.code, at, newRand(3))
		if KindOf(err) != tc.kind || codes != nil {
			t.Errorf("Confirm(%q) = %v, want %s", tc.code, err, tc.kind)
		}
		if !reflect.DeepEqual(next, f) {
			t.Errorf("a refused code during setup must change nothing, not even a count: %+v", next)
		}
	}
	code := totp.Code(f.Secret, at)
	if next, _, err := Confirm(f, code, at, bytes.NewReader(nil)); err == nil || !reflect.DeepEqual(next, f) {
		t.Errorf("a failing random source must leave setup unfinished: %v", err)
	}
	on, codes, err := Confirm(f, code, at, newRand(3))
	if err != nil {
		t.Fatal(err)
	}
	checkRevision(t, f, on)
	if !on.On() || !on.ConfirmedAt.Equal(at) || on.LastStep != totp.Step(at) || len(codes) != RecoveryCodeCount || on.Recovery.Remaining() != RecoveryCodeCount {
		t.Fatalf("after Confirm: on %v, step %d, %d codes, %d stored", on.On(), on.LastStep, len(codes), on.Recovery.Remaining())
	}
	if _, _, err := SignIn(on, code, at); KindOf(err) != KindCodeReused {
		t.Errorf("the code that finished setup must not also sign in: %v", err)
	}
	next := at.Add(totp.Period)
	if _, _, err := Confirm(on, totp.Code(on.Secret, next), next, newRand(3)); KindOf(err) != KindOn {
		t.Errorf("Confirm twice: %v", err)
	}
	late := t0.Add(SetupTTL)
	if _, _, err := Confirm(f, totp.Code(f.Secret, late), late, newRand(3)); KindOf(err) != KindSetupExpired {
		t.Errorf("Confirm after SetupTTL: %v", err)
	}
	if _, _, err := Confirm(Factor{}, "123456", t0, newRand(3)); KindOf(err) != KindNoSetup {
		t.Errorf("Confirm with nothing started: %v", err)
	}
}

func TestSignInWithAnAppCodeOnce(t *testing.T) {
	f, _ := enrolled(t)
	at := t0.Add(time.Hour)
	code := totp.Code(f.Secret, at)
	next, res, err := signIn(t, f, code, at)
	if err != nil || res.Method != MethodAppCode || len(res.Notices) != 0 {
		t.Fatalf("SignIn = %+v, %v", res, err)
	}
	if next.LastStep != totp.Step(at) || !next.LastUsedAt.Equal(at) {
		t.Errorf("step %d, last used %v", next.LastStep, next.LastUsedAt)
	}
	if b, _ := json.Marshal(res); string(b) != `{"method":"app_code","notices":[]}` {
		t.Errorf("JSON = %s", b)
	}
	again, _, err := signIn(t, next, " "+code[:3]+" "+code[3:], at.Add(5*time.Second))
	if KindOf(err) != KindCodeReused || again.Failures != 0 {
		t.Errorf("a reused code is refused but not counted: %v, %d failures", err, again.Failures)
	}

	later := at.Add(10 * time.Minute)
	if _, _, err := signIn(t, next, totp.Code(f.Secret, later.Add(-30*time.Second)), later); err != nil {
		t.Errorf("a phone 30 seconds slow must work: %v", err)
	}
	if _, _, err := signIn(t, next, totp.Code(f.Secret, later.Add(30*time.Second)), later); err != nil {
		t.Errorf("a phone 30 seconds fast must work: %v", err)
	}
	slow, _, err := signIn(t, next, totp.Code(f.Secret, later.Add(-60*time.Second)), later)
	if KindOf(err) != KindCodeWrong || slow.Failures != 1 {
		t.Errorf("a phone a minute slow gives a wrong code, which counts: %v, %d failures", err, slow.Failures)
	}
	if _, res, err := signIn(t, slow, totp.Code(f.Secret, later), later); err != nil || !reflect.DeepEqual(res.Notices, []Notice{newNotice(NoticeFailedAttempts, 1)}) {
		t.Errorf("the sign-in after one wrong code must mention it: %+v, %v", res.Notices, err)
	}
}

func TestWrongCodesLockAppCodesForLongerEachTime(t *testing.T) {
	f, _ := enrolled(t)
	at := t0.Add(time.Hour)
	locks := []time.Duration{0, 0, 0, 0, time.Minute, 2 * time.Minute, 4 * time.Minute, 8 * time.Minute, 16 * time.Minute, 16 * time.Minute}
	for i, lock := range locks {
		var err error
		f, _, err = signIn(t, f, wrongCode(t, f, at), at)
		if f.Failures != i+1 {
			t.Fatalf("after wrong code %d, Failures = %d", i+1, f.Failures)
		}
		var e *Error
		if !errors.As(err, &e) {
			t.Fatalf("wrong code %d: %v", i+1, err)
		}
		if lock == 0 {
			if e.Kind != KindCodeWrong || !f.LockedUntil.IsZero() {
				t.Fatalf("wrong code %d must just be wrong: %s, locked until %v", i+1, e.Kind, f.LockedUntil)
			}
			continue
		}
		if e.Kind != KindAppCodesLocked || e.RetryAfter != lock || !f.LockedUntil.Equal(at.Add(lock)) {
			t.Fatalf("wrong code %d must lock app codes for %v: %s for %v", i+1, lock, e.Kind, e.RetryAfter)
		}
		last := at.Add(lock - time.Second)
		still, _, err := signIn(t, f, totp.Code(f.Secret, last), last)
		if !errors.As(err, &e) || e.Kind != KindAppCodesLocked || e.RetryAfter != time.Second || still.Failures != f.Failures {
			t.Fatalf("while locked, even the right code is refused unchecked and not counted: %v, %d failures", err, still.Failures)
		}
		at = at.Add(lock)
	}
	next, res, err := signIn(t, f, totp.Code(f.Secret, at), at)
	if err != nil {
		t.Fatalf("the right code once the lock is over: %v", err)
	}
	if next.Failures != 0 || !next.LockedUntil.IsZero() {
		t.Errorf("a correct code must reset the count: %d, %v", next.Failures, next.LockedUntil)
	}
	if want := []Notice{newNotice(NoticeFailedAttempts, len(locks))}; !reflect.DeepEqual(res.Notices, want) {
		t.Errorf("notices = %+v, want %+v", res.Notices, want)
	}
	if !strings.Contains(res.Notices[0].Text, "10 times") {
		t.Errorf("text = %q", res.Notices[0].Text)
	}
}

func TestAppCodesBlockedAfterAHundredWrongCodes(t *testing.T) {
	f, codes := enrolled(t)
	start := t0.Add(time.Hour)
	at := start
	var err error
	for f.Failures < BlockAfter {
		if at.Before(f.LockedUntil) {
			at = f.LockedUntil
		}
		f, _, err = signIn(t, f, wrongCode(t, f, at), at)
	}
	if KindOf(err) != KindAppCodesBlocked || !f.LockedUntil.IsZero() {
		t.Fatalf("wrong code %d: %v, locked until %v", BlockAfter, err, f.LockedUntil)
	}
	// 1 + 2 + 4 + 8 minutes, then 16 for each of the remaining 91 locks.
	if spent := at.Sub(start); spent != 1471*time.Minute {
		t.Errorf("guessing as fast as allowed took %v to reach the block", spent)
	}

	day := at.Add(48 * time.Hour)
	if still, _, err := signIn(t, f, totp.Code(f.Secret, day), day); KindOf(err) != KindAppCodesBlocked || still.Failures != BlockAfter {
		t.Fatalf("app codes stay blocked, without counting: %v", err)
	}
	if c := f.Challenge(day); !reflect.DeepEqual(c, Challenge{Methods: []Method{MethodRecoveryCode}, AppCodesBlocked: true}) {
		t.Errorf("challenge = %+v", c)
	}
	if st := f.Status(day); !st.AppCodesBlocked || !st.AppCodesLockedUntil.IsZero() {
		t.Errorf("status = %+v", st)
	}
	next, res, err := signIn(t, f, codes[0], day)
	if err != nil || res.Method != MethodRecoveryCode {
		t.Fatalf("a recovery code must work while app codes are blocked: %v", err)
	}
	if got, want := kinds(res.Notices), []NoticeKind{NoticeFailedAttempts, NoticeRecoveryCodeUsed}; !reflect.DeepEqual(got, want) || res.Notices[0].Count != BlockAfter {
		t.Errorf("notices = %+v", res.Notices)
	}
	later := day.Add(time.Minute)
	if _, _, err := signIn(t, next, totp.Code(f.Secret, later), later); err != nil {
		t.Errorf("a recovery code unblocks app codes: %v", err)
	}
}

func TestRecoveryCodesSignInOnceEvenWhileLocked(t *testing.T) {
	f, codes := enrolled(t)
	at := t0.Add(time.Hour)
	for range LockAfter {
		f, _, _ = signIn(t, f, wrongCode(t, f, at), at)
	}
	if c := f.Challenge(at); !reflect.DeepEqual(c, Challenge{Methods: []Method{MethodRecoveryCode}, AppCodesLockedUntil: at.Add(time.Minute)}) {
		t.Fatalf("challenge while locked = %+v", c)
	}
	typed := strings.ToUpper(strings.ReplaceAll(codes[3], "-", " "))
	next, res, err := signIn(t, f, typed, at)
	if err != nil || res.Method != MethodRecoveryCode {
		t.Fatalf("SignIn(%q) = %v", typed, err)
	}
	if next.Failures != 0 || !next.LockedUntil.IsZero() || next.Recovery.Remaining() != RecoveryCodeCount-1 || !next.LastUsedAt.Equal(at) {
		t.Errorf("after a recovery code: %d failures, locked until %v, %d left", next.Failures, next.LockedUntil, next.Recovery.Remaining())
	}
	if next.LastStep != f.LastStep {
		t.Error("a recovery code must not move the app code step")
	}
	again, _, err := signIn(t, next, codes[3], at)
	if KindOf(err) != KindRecoveryCodeWrong || again.RecoveryFailures != 1 || again.Failures != 0 {
		t.Errorf("a used recovery code is refused and counted on its own: %v, %d recovery failures, %d failures", err, again.RecoveryFailures, again.Failures)
	}
	madeUp, _, err := signIn(t, next, "abcd-efgh-ijkm-npqr", at)
	if KindOf(err) != KindRecoveryCodeWrong || madeUp.RecoveryFailures != 1 {
		t.Errorf("a made-up recovery code is refused and counted: %v", err)
	}
}

// Wrong recovery codes are counted, but on their own: someone who has the
// password must not be able to lock or block the owner's app codes with them.
func TestWrongRecoveryCodesNeverLockAppCodes(t *testing.T) {
	f, _ := enrolled(t)
	at := t0.Add(time.Hour)
	var err error
	for range BlockAfter + 5 {
		f, _, err = signIn(t, f, "abcd-efgh-ijkm-npqr", at)
		if KindOf(err) != KindRecoveryCodeWrong {
			t.Fatalf("a wrong recovery code: %v", err)
		}
	}
	if f.RecoveryFailures != BlockAfter+5 || f.Failures != 0 || !f.LockedUntil.IsZero() {
		t.Fatalf("after %d wrong recovery codes: %d recovery failures, %d failures, locked until %v", BlockAfter+5, f.RecoveryFailures, f.Failures, f.LockedUntil)
	}
	if c := f.Challenge(at); !reflect.DeepEqual(c.Methods, []Method{MethodAppCode, MethodRecoveryCode}) || c.AppCodesBlocked {
		t.Fatalf("challenge after wrong recovery codes = %+v", c)
	}
	f, _, _ = signIn(t, f, wrongCode(t, f, at), at)
	next, res, err := signIn(t, f, totp.Code(f.Secret, at), at)
	if err != nil {
		t.Fatalf("the owner's app code after wrong recovery codes: %v", err)
	}
	if len(res.Notices) != 1 || res.Notices[0].Kind != NoticeFailedAttempts || res.Notices[0].Count != BlockAfter+6 {
		t.Errorf("the notice must count wrong codes of both kinds: %+v", res.Notices)
	}
	if next.Failures != 0 || next.RecoveryFailures != 0 {
		t.Errorf("a correct code resets both counts: %d, %d", next.Failures, next.RecoveryFailures)
	}
}

func TestNoticesAsRecoveryCodesRunOut(t *testing.T) {
	f, codes := enrolled(t)
	at := t0.Add(time.Hour)
	used, low, none := NoticeRecoveryCodeUsed, NoticeRecoveryCodesLow, NoticeNoRecoveryCodes
	want := [][]NoticeKind{
		{used}, {used}, {used}, {used}, {used}, {used}, // 9 to 4 left
		{used, low}, {used, low}, {used, low}, // 3 to 1 left
		{used, none},
	}
	for i, code := range codes {
		var res Result
		var err error
		f, res, err = signIn(t, f, code, at)
		if err != nil {
			t.Fatalf("code %d: %v", i, err)
		}
		left := RecoveryCodeCount - 1 - i
		if got := kinds(res.Notices); !reflect.DeepEqual(got, want[i]) {
			t.Errorf("with %d left, notices = %v, want %v", left, got, want[i])
		}
		for _, n := range res.Notices {
			if n.Kind != none && n.Count != left {
				t.Errorf("%s says %d left, want %d", n.Kind, n.Count, left)
			}
		}
	}
	at = at.Add(time.Minute)
	_, res, err := signIn(t, f, totp.Code(f.Secret, at), at)
	if err != nil || !reflect.DeepEqual(kinds(res.Notices), []NoticeKind{none}) {
		t.Errorf("every sign-in must say no recovery codes are left: %+v, %v", res.Notices, err)
	}
	if c := f.Challenge(at); !reflect.DeepEqual(c.Methods, []Method{MethodAppCode}) {
		t.Errorf("methods with no recovery codes left = %v", c.Methods)
	}
	for _, tc := range []struct {
		n    Notice
		text string
	}{
		{newNotice(NoticeRecoveryCodesLow, 1), "Only 1 recovery code is left. Make new ones on the Account page."},
		{newNotice(NoticeRecoveryCodesLow, 3), "Only 3 recovery codes are left. Make new ones on the Account page."},
		{newNotice(NoticeFailedAttempts, 1), "A wrong code was entered once since your last sign-in. If that was not you, change your password: someone knows it."},
	} {
		if tc.n.Text != tc.text {
			t.Errorf("%s(%d) = %q", tc.n.Kind, tc.n.Count, tc.n.Text)
		}
	}
	for _, k := range []NoticeKind{NoticeFailedAttempts, NoticeRecoveryCodeUsed, NoticeRecoveryCodesLow, NoticeNoRecoveryCodes} {
		if newNotice(k, 2).Text == "" {
			t.Errorf("%s has no text", k)
		}
	}
}

func TestMalformedInputIsRefusedWithoutCounting(t *testing.T) {
	f, _ := enrolled(t)
	at := t0.Add(time.Hour)
	for _, in := range []string{"", "12345", "1234567", "12345a", "abc", "abcd-efgh-ijkm", strings.Repeat("1", 1000), "\uff11\uff12\uff13\uff14\uff15\uff16"} {
		if next, _, err := signIn(t, f, in, at); KindOf(err) != KindCodeMalformed || next.Revision != f.Revision {
			t.Errorf("SignIn(%.20q) = %v", in, err)
		}
	}
}

func TestSignInNeedsTwoFactorOn(t *testing.T) {
	pending, _, err := Begin(Factor{}, true, account, t0, newRand(2))
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range []Factor{{}, pending} {
		if _, _, err := SignIn(f, totp.Code(pending.Secret, t0), t0); KindOf(err) != KindOff {
			t.Errorf("SignIn before two-factor sign-in is on: %v", err)
		}
	}
}

func TestTurningOffNeedsThePasswordAndACode(t *testing.T) {
	f, codes := enrolled(t)
	at := t0.Add(time.Hour)
	code := totp.Code(f.Secret, at)
	if next, err := Disable(f, false, code, at); KindOf(err) != KindPasswordWrong || !reflect.DeepEqual(next, f) {
		t.Fatalf("without the password: %v", err)
	}
	wrong, err := Disable(f, true, wrongCode(t, f, at), at)
	if KindOf(err) != KindCodeWrong || wrong.Failures != 1 {
		t.Fatalf("a wrong code counts when turning off too: %v, %d failures", err, wrong.Failures)
	}
	checkRevision(t, f, wrong)
	off, err := Disable(wrong, true, code, at)
	if err != nil || !reflect.DeepEqual(off, Factor{}) {
		t.Fatalf("with the password and a code: %v", err)
	}
	if _, err := Disable(off, true, code, at); KindOf(err) != KindOff {
		t.Errorf("turning off twice: %v", err)
	}

	locked := f
	for range LockAfter {
		locked, _, _ = signIn(t, locked, wrongCode(t, locked, at), at)
	}
	if _, err := Disable(locked, true, code, at); KindOf(err) != KindAppCodesLocked {
		t.Errorf("app codes are locked for turning off too: %v", err)
	}
	if off, err := Disable(locked, true, codes[0], at); err != nil || off.On() {
		t.Errorf("a recovery code turns it off even while app codes are locked: %v", err)
	}

	pending, _, err := Begin(Factor{}, true, account, at, newRand(9))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Disable(pending, true, totp.Code(pending.Secret, at), at); KindOf(err) != KindOff {
		t.Errorf("an unfinished setup is not on: %v", err)
	}
}

func TestNewRecoveryCodesReplaceTheOldOnes(t *testing.T) {
	f, old := enrolled(t)
	at := t0.Add(time.Hour)
	code := totp.Code(f.Secret, at)
	if next, codes, err := RenewRecoveryCodes(f, false, code, at, newRand(7)); KindOf(err) != KindPasswordWrong || codes != nil || !reflect.DeepEqual(next, f) {
		t.Fatalf("without the password: %v", err)
	}
	if next, _, err := RenewRecoveryCodes(f, true, code, at, bytes.NewReader(nil)); err == nil || !reflect.DeepEqual(next, f) {
		t.Fatalf("a failing random source must change nothing, not even use up the code: %v", err)
	}
	if next, codes, err := RenewRecoveryCodes(f, true, wrongCode(t, f, at), at, newRand(7)); KindOf(err) != KindCodeWrong || codes != nil || next.Failures != 1 || next.Recovery.Hashes[0] != f.Recovery.Hashes[0] {
		t.Fatalf("with a wrong code: %v", err)
	}
	next, fresh, err := RenewRecoveryCodes(f, true, code, at, newRand(7))
	if err != nil {
		t.Fatal(err)
	}
	checkRevision(t, f, next)
	if len(fresh) != RecoveryCodeCount || !next.Recovery.CreatedAt.Equal(at) || next.LastStep != totp.Step(at) {
		t.Fatalf("%d new codes, made at %v, step %d", len(fresh), next.Recovery.CreatedAt, next.LastStep)
	}
	for _, c := range old {
		if _, ok := next.Recovery.Use(c); ok {
			t.Errorf("old code %s still works", c)
		}
	}
	for _, c := range fresh {
		if _, ok := next.Recovery.Use(c); !ok {
			t.Errorf("new code %s does not work", c)
		}
	}
	if next, _, err := RenewRecoveryCodes(f, true, old[0], at, newRand(8)); err != nil || next.Recovery.Remaining() != RecoveryCodeCount {
		t.Errorf("a recovery code can make new ones, for someone who lost the phone: %v", err)
	}
	if _, _, err := RenewRecoveryCodes(Factor{}, true, code, at, newRand(8)); KindOf(err) != KindOff {
		t.Errorf("with two-factor sign-in off: %v", err)
	}
}

// Two requests that load the same Factor and send the same code both pass
// on their own; only the Revision stops the second from storing its result.
func TestOneCodeCannotSignInTwiceThroughRacingRequests(t *testing.T) {
	stored, _ := enrolled(t)
	at := t0.Add(time.Hour)
	code := totp.Code(stored.Secret, at)
	loaded := stored
	first, _, errFirst := SignIn(loaded, code, at)
	second, _, errSecond := SignIn(loaded, code, at)
	if errFirst != nil || errSecond != nil || first.Revision != second.Revision {
		t.Fatalf("%v, %v, revisions %d and %d", errFirst, errSecond, first.Revision, second.Revision)
	}
	store := func(old, next Factor) bool {
		if stored.Revision != old.Revision {
			return false
		}
		stored = next
		return true
	}
	if !store(loaded, first) || store(loaded, second) {
		t.Fatal("only the first request may store")
	}
	if _, _, err := SignIn(stored, code, at); KindOf(err) != KindCodeReused {
		t.Fatalf("the second request, retried on the stored Factor: %v", err)
	}
}

func TestStatusAndChallenge(t *testing.T) {
	if st := (Factor{}).Status(t0); st != (Status{State: StateOff}) {
		t.Errorf("status when off = %+v", st)
	}
	f, _ := enrolled(t)
	at := t0.Add(time.Hour)
	st := f.Status(at)
	if want := (Status{State: StateOn, ConfirmedAt: t0, LastUsedAt: t0, RecoveryCodesLeft: RecoveryCodeCount, RecoveryCodesMadeAt: t0}); st != want {
		t.Errorf("status = %+v, want %+v", st, want)
	}
	if b, _ := json.Marshal(st); string(b) != `{"state":"on","confirmedAt":"2026-09-25T12:00:07Z","lastUsedAt":"2026-09-25T12:00:07Z","recoveryCodesLeft":10,"recoveryCodesMadeAt":"2026-09-25T12:00:07Z","appCodesBlocked":false}` {
		t.Errorf("status JSON = %s", b)
	}
	if b, _ := json.Marshal(f.Challenge(at)); string(b) != `{"methods":["app_code","recovery_code"],"appCodesBlocked":false}` {
		t.Errorf("challenge JSON = %s", b)
	}
	for range LockAfter {
		f, _, _ = signIn(t, f, wrongCode(t, f, at), at)
	}
	if st := f.Status(at); !st.AppCodesLockedUntil.Equal(at.Add(time.Minute)) {
		t.Errorf("status while locked = %+v", st)
	}
	if st := f.Status(at.Add(time.Minute)); !st.AppCodesLockedUntil.IsZero() {
		t.Errorf("status after the lock = %+v", st)
	}
	if c := f.Challenge(at.Add(time.Minute)); !reflect.DeepEqual(c.Methods, []Method{MethodAppCode, MethodRecoveryCode}) {
		t.Errorf("challenge after the lock = %+v", c)
	}
}

func TestErrorsSayWhatHappenedAndWhatToDo(t *testing.T) {
	all := []Kind{
		KindCodeMalformed, KindAppCodeMalformed, KindCodeWrong, KindCodeReused, KindRecoveryCodeWrong,
		KindAppCodesLocked, KindAppCodesBlocked, KindPasswordWrong, KindNoSetup, KindSetupExpired, KindOff, KindOn,
	}
	fallback := (&Error{Kind: "new_kind"}).Error()
	seen := map[string]Kind{}
	for _, k := range all {
		e := &Error{Kind: k, RetryAfter: time.Minute}
		msg, hint := e.Error(), e.Hint()
		first, _ := utf8.DecodeRuneInString(msg)
		if msg == fallback || !unicode.IsUpper(first) || !strings.HasSuffix(msg, ".") {
			t.Errorf("%s: message %q is not its own sentence", k, msg)
		}
		if other, dup := seen[msg]; dup {
			t.Errorf("%s and %s say the same thing", k, other)
		}
		seen[msg] = k
		if hint != "" && !strings.HasSuffix(hint, ".") {
			t.Errorf("%s: hint %q", k, hint)
		}
		if KindOf(fmt.Errorf("signing in: %w", e)) != k {
			t.Errorf("KindOf does not see %s through wrapping", k)
		}
	}
	for _, tc := range []struct {
		wait time.Duration
		want string
	}{
		{time.Second, "Try again in 1 minute."},
		{time.Minute, "Try again in 1 minute."},
		{time.Minute + time.Second, "Try again in 2 minutes."},
		{16 * time.Minute, "Try again in 16 minutes."},
	} {
		if msg := (&Error{Kind: KindAppCodesLocked, RetryAfter: tc.wait}).Error(); msg != "Too many wrong codes. "+tc.want {
			t.Errorf("locked for %v: %q", tc.wait, msg)
		}
	}
	if !strings.Contains((&Error{Kind: KindAppCodesBlocked}).Hint(), "sudo playkeeper reset-2fa <username>") {
		t.Error("the blocked hint must name the reset command")
	}
	if KindOf(errors.New("disk full")) != "" || KindOf(nil) != "" {
		t.Error("KindOf of other errors must be empty")
	}
}

func TestSetupNeverPrintsTheSecret(t *testing.T) {
	f, setup, err := Begin(Factor{}, true, account, t0, newRand(2))
	if err != nil {
		t.Fatal(err)
	}
	key := f.Secret.Base32()
	var logged bytes.Buffer
	slog.New(slog.NewJSONHandler(&logged, nil)).Info("setup", "setup", setup, "factor", f)
	slog.New(slog.NewTextHandler(&logged, nil)).Info("setup", "setup", setup, "factor", f)
	for _, out := range []string{
		fmt.Sprintf("%v %+v %#v %s %q %x %d", setup, setup, setup, setup, setup, setup, setup),
		fmt.Sprintf("%v %+v %#v", f, f, f),
		fmt.Sprint(&setup, []Setup{setup}),
		logged.String(),
	} {
		if strings.Contains(out, key[:8]) || strings.Contains(out, setup.ManualKey[:9]) {
			t.Errorf("secret leaked in %.300s", out)
		}
	}
	b, err := json.Marshal(setup)
	if err != nil || !strings.Contains(string(b), `"manualKey":"`+setup.ManualKey+`"`) || !strings.Contains(string(b), "secret="+key) {
		t.Errorf("the page needs the key in JSON: %v", err)
	}
}
