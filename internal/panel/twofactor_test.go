package panel

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/CIYAhq/playkeeper/internal/totp"
	"github.com/CIYAhq/playkeeper/internal/twofactor"
)

var xrw = map[string]string{"X-Requested-With": "playkeeper"}

func pendingHeaders(pending string) map[string]string {
	return map[string]string{"X-Requested-With": "playkeeper", "Cookie": pendingCookieName + "=" + pending}
}

// wrongCode is six digits that no step near at accepts.
func wrongCode(s totp.Secret, at time.Time) string {
	near := map[string]bool{}
	for d := -2; d <= 2; d++ {
		near[totp.Code(s, at.Add(time.Duration(d)*totp.Period))] = true
	}
	for _, c := range []string{"000000", "111111", "222222"} {
		if !near[c] {
			return c
		}
	}
	panic("unreachable")
}

// turnOn sets up two-factor sign-in for admin and returns the secret and
// the recovery codes.
func turnOn(t *testing.T, e *env, cookie, csrf string) (totp.Secret, []string) {
	t.Helper()
	r := e.do(t, "POST", "/api/auth/2fa/setup", `{"password":"correct horse battery"}`, auth(cookie, csrf))
	if r.status != 200 {
		t.Fatalf("setup: %d %v", r.status, r.body)
	}
	if !strings.HasPrefix(r.body["qrCodeSvg"].(string), "<svg") || !strings.HasPrefix(r.body["uri"].(string), "otpauth://totp/Playkeeper:admin%40127.0.0.1?") {
		t.Fatalf("setup answer: %v", r.body)
	}
	if r.header.Get("Cache-Control") != "no-store" {
		t.Fatalf("setup answer may be cached: %q", r.header.Get("Cache-Control"))
	}
	secret, err := totp.ParseSecret(r.body["manualKey"].(string))
	if err != nil {
		t.Fatal(err)
	}
	r = e.do(t, "POST", "/api/auth/2fa/confirm", `{"code":"`+totp.Code(secret, e.clock.now())+`"}`, auth(cookie, csrf))
	if r.status != 200 {
		t.Fatalf("confirm: %d %v", r.status, r.body)
	}
	var codes []string
	for _, c := range r.body["recoveryCodes"].([]any) {
		codes = append(codes, c.(string))
	}
	if len(codes) != twofactor.RecoveryCodeCount || len(codes[0]) != 19 {
		t.Fatalf("recovery codes: %v", codes)
	}
	e.clock.add(time.Minute)
	return secret, codes
}

func TestTwoFactorSignInNeedsACodeAfterThePassword(t *testing.T) {
	e := newEnv(t)
	cookie, csrf := e.setup(t)
	other := e.do(t, "POST", "/api/auth/login", `{"username":"admin","password":"correct horse battery"}`, xrw)
	if r := e.do(t, "POST", "/api/auth/2fa/setup", `{"password":"wrong password!"}`, auth(cookie, csrf)); r.status != http.StatusForbidden || r.body["code"] != "password_wrong" {
		t.Fatalf("setup with a wrong password: %d %v", r.status, r.body)
	}
	secret, codes := turnOn(t, e, cookie, csrf)
	if r := e.do(t, "GET", "/api/auth/me", "", auth(other.cookie, "")); r.status != http.StatusUnauthorized {
		t.Fatalf("turning two-factor on must sign out other sessions: %d", r.status)
	}
	if r := e.do(t, "GET", "/api/auth/me", "", auth(cookie, "")); r.status != 200 {
		t.Fatalf("turning two-factor on signed out the session that did it: %d", r.status)
	}
	if r := e.do(t, "GET", "/api/auth/2fa", "", auth(cookie, "")); r.body["state"] != "on" || r.body["recoveryCodesLeft"] != float64(10) {
		t.Fatalf("status: %v", r.body)
	}

	login := e.do(t, "POST", "/api/auth/login", `{"username":"admin","password":"correct horse battery"}`, xrw)
	if login.status != 200 || login.cookie != "" || login.pending == "" || login.body["csrfToken"] != nil || login.body["version"] != nil {
		t.Fatalf("a correct password alone must not sign in: %d cookie=%q %v", login.status, login.cookie, login.body)
	}
	if ch := login.body["secondFactor"].(map[string]any); len(ch["methods"].([]any)) != 2 {
		t.Fatalf("challenge: %v", ch)
	}
	if r := e.do(t, "GET", "/api/auth/me", "", map[string]string{"Cookie": cookieName + "=" + login.pending}); r.status != http.StatusUnauthorized {
		t.Fatalf("the second-step token works as a session: %d", r.status)
	}

	r := e.do(t, "POST", "/api/auth/second-factor", `{"code":"`+wrongCode(secret, e.clock.now())+`"}`, pendingHeaders(login.pending))
	if r.status != http.StatusUnauthorized || r.body["code"] != "code_wrong" || r.cookie != "" {
		t.Fatalf("wrong code: %d %v", r.status, r.body)
	}
	if r := e.do(t, "POST", "/api/auth/second-factor", `{"code":"12"}`, pendingHeaders(login.pending)); r.status != http.StatusBadRequest || r.body["code"] != "code_malformed" {
		t.Fatalf("malformed code: %d %v", r.status, r.body)
	}
	r = e.do(t, "POST", "/api/auth/second-factor", `{"code":"`+totp.Code(secret, e.clock.now())+`"}`, pendingHeaders(login.pending))
	if r.status != 200 || r.cookie == "" || r.body["csrfToken"] == nil || !r.pendingCleared {
		t.Fatalf("right code: %d %v", r.status, r.body)
	}
	notices := r.body["notices"].([]any)
	if len(notices) != 1 || notices[0].(map[string]any)["kind"] != "failed_attempts" || notices[0].(map[string]any)["count"] != float64(1) {
		t.Fatalf("notices: %v", notices)
	}
	if r := e.do(t, "GET", "/api/auth/me", "", auth(r.cookie, "")); r.status != 200 {
		t.Fatalf("session after the second step: %d", r.status)
	}
	if r := e.do(t, "POST", "/api/auth/second-factor", `{"code":"`+codes[0]+`"}`, pendingHeaders(login.pending)); r.status != http.StatusUnauthorized {
		t.Fatalf("the second-step cookie works twice: %d", r.status)
	}

	e.clock.add(15 * time.Minute)
	login = e.do(t, "POST", "/api/auth/login", `{"username":"admin","password":"correct horse battery"}`, xrw)
	r = e.do(t, "POST", "/api/auth/second-factor", `{"code":"`+strings.ToUpper(codes[3])+`"}`, pendingHeaders(login.pending))
	if r.status != 200 {
		t.Fatalf("recovery code: %d %v", r.status, r.body)
	}
	if n := r.body["notices"].([]any); len(n) != 1 || n[0].(map[string]any)["kind"] != "recovery_code_used" || n[0].(map[string]any)["count"] != float64(9) {
		t.Fatalf("recovery notices: %v", n)
	}
	login = e.do(t, "POST", "/api/auth/login", `{"username":"admin","password":"correct horse battery"}`, xrw)
	if r := e.do(t, "POST", "/api/auth/second-factor", `{"code":"`+codes[3]+`"}`, pendingHeaders(login.pending)); r.status != http.StatusUnauthorized || r.body["code"] != "recovery_code_wrong" {
		t.Fatalf("a used recovery code works again: %d %v", r.status, r.body)
	}
}

func TestSecondStepExpiresAndCanBeCancelled(t *testing.T) {
	e := newEnv(t)
	cookie, csrf := e.setup(t)
	secret, _ := turnOn(t, e, cookie, csrf)
	login := e.do(t, "POST", "/api/auth/login", `{"username":"admin","password":"correct horse battery"}`, xrw)
	e.clock.add(pendingTTL)
	if r := e.do(t, "POST", "/api/auth/second-factor", `{"code":"`+totp.Code(secret, e.clock.now())+`"}`, pendingHeaders(login.pending)); r.status != http.StatusUnauthorized || !r.pendingCleared {
		t.Fatalf("second step after it expired: %d %v", r.status, r.body)
	}
	login = e.do(t, "POST", "/api/auth/login", `{"username":"admin","password":"correct horse battery"}`, xrw)
	if r := e.do(t, "POST", "/api/auth/second-factor/cancel", ``, pendingHeaders(login.pending)); r.status != http.StatusNoContent || !r.pendingCleared {
		t.Fatalf("cancel: %d", r.status)
	}
	if r := e.do(t, "POST", "/api/auth/second-factor", `{"code":"`+totp.Code(secret, e.clock.now())+`"}`, pendingHeaders(login.pending)); r.status != http.StatusUnauthorized {
		t.Fatalf("second step after cancel: %d", r.status)
	}

	login = e.do(t, "POST", "/api/auth/login", `{"username":"admin","password":"correct horse battery"}`, xrw)
	if r := e.do(t, "POST", "/api/auth/password", `{"currentPassword":"correct horse battery","newPassword":"another good password"}`, auth(cookie, csrf)); r.status != http.StatusNoContent {
		t.Fatalf("change password: %d %v", r.status, r.body)
	}
	if r := e.do(t, "POST", "/api/auth/second-factor", `{"code":"`+totp.Code(secret, e.clock.now())+`"}`, pendingHeaders(login.pending)); r.status != http.StatusUnauthorized {
		t.Fatalf("a sign-in with the old password passes its second step after the password changed: %d", r.status)
	}
	login = e.do(t, "POST", "/api/auth/login", `{"username":"admin","password":"another good password"}`, xrw)
	if r := e.do(t, "POST", "/api/auth/logout-all", "", auth(cookie, csrf)); r.status != http.StatusNoContent {
		t.Fatalf("sign out everywhere: %d", r.status)
	}
	if r := e.do(t, "POST", "/api/auth/second-factor", `{"code":"`+totp.Code(secret, e.clock.now())+`"}`, pendingHeaders(login.pending)); r.status != http.StatusUnauthorized {
		t.Fatalf("a sign-in passes its second step after signing out everywhere: %d", r.status)
	}
}

func (e *env) count(t *testing.T, query string, args ...any) int {
	t.Helper()
	var n int
	if err := e.srv.db.QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// A sign-in that passed only the password is kept apart from sessions: its
// token is no session in either cookie, and it starts none.
func TestPendingSignInIsNeverASession(t *testing.T) {
	e := newEnv(t)
	cookie, csrf := e.setup(t)
	secret, _ := turnOn(t, e, cookie, csrf)
	sessions := e.count(t, `SELECT COUNT(*) FROM sessions`)
	login := e.do(t, "POST", "/api/auth/login", `{"username":"admin","password":"correct horse battery"}`, xrw)
	if login.status != 200 || login.pending == "" {
		t.Fatalf("login: %d %v", login.status, login.body)
	}
	if n := e.count(t, `SELECT COUNT(*) FROM sessions`); n != sessions {
		t.Fatalf("a correct password alone started a session: %d sessions, want %d", n, sessions)
	}
	for _, path := range []string{"/api/auth/me", "/api/auth/2fa", "/api/servers"} {
		if r := e.do(t, "GET", path, "", map[string]string{"Cookie": cookieName + "=" + login.pending}); r.status != http.StatusUnauthorized {
			t.Fatalf("the second-step token works as a session cookie for %s: %d", path, r.status)
		}
	}
	if r := e.do(t, "POST", "/api/auth/second-factor", `{"code":"`+totp.Code(secret, e.clock.now())+`"}`, pendingHeaders(cookie)); r.status != http.StatusUnauthorized {
		t.Fatalf("a session token works as a second-step cookie: %d", r.status)
	}
	if r := e.do(t, "POST", "/api/auth/second-factor", `{"code":"`+totp.Code(secret, e.clock.now())+`"}`, pendingHeaders(login.pending)); r.status != 200 || r.cookie == "" {
		t.Fatalf("second step: %d %v", r.status, r.body)
	}
	if n := e.count(t, `SELECT COUNT(*) FROM sessions`); n != sessions+1 {
		t.Fatalf("the second step started %d sessions", n-sessions)
	}
	if n := e.count(t, `SELECT COUNT(*) FROM pending_logins`); n != 0 {
		t.Fatalf("%d pending sign-ins left after the second step", n)
	}
}

// One correct password lets the second step try a limited number of codes,
// even from addresses the per-address limit does not stop.
func TestOnePasswordBuysTenCodes(t *testing.T) {
	e := newEnv(t)
	cookie, csrf := e.setup(t)
	secret, _ := turnOn(t, e, cookie, csrf)
	e.srv.loginIP = newLimiter(1000, time.Minute, e.clock.now)
	login := e.do(t, "POST", "/api/auth/login", `{"username":"admin","password":"correct horse battery"}`, xrw)
	for i := range pendingAttempts {
		if r := e.do(t, "POST", "/api/auth/second-factor", `{"code":"abcd-efgh-jkmn-pqrs"}`, pendingHeaders(login.pending)); r.status != http.StatusUnauthorized || r.body["code"] != "recovery_code_wrong" {
			t.Fatalf("wrong code %d: %d %v", i+1, r.status, r.body)
		}
	}
	r := e.do(t, "POST", "/api/auth/second-factor", `{"code":"`+totp.Code(secret, e.clock.now())+`"}`, pendingHeaders(login.pending))
	if r.status != http.StatusUnauthorized || r.body["code"] != "unauthorized" || r.cookie != "" || !r.pendingCleared {
		t.Fatalf("a right code after %d tries: %d %v", pendingAttempts, r.status, r.body)
	}
	if n := e.count(t, `SELECT COUNT(*) FROM audit WHERE action = 'login.second_factor' AND result = 'refused'`); n != 1 {
		t.Fatalf("running out of tries audited %d times", n)
	}
	login = e.do(t, "POST", "/api/auth/login", `{"username":"admin","password":"correct horse battery"}`, xrw)
	if r := e.do(t, "POST", "/api/auth/second-factor", `{"code":"`+totp.Code(secret, e.clock.now())+`"}`, pendingHeaders(login.pending)); r.status != 200 {
		t.Fatalf("the password again, then the right code: %d %v", r.status, r.body)
	}
}

// Wrong recovery codes are stored and reported, but on their own count, so
// they cannot lock the owner's app codes.
func TestWrongRecoveryCodesAreCountedWithoutLockingAppCodes(t *testing.T) {
	e := newEnv(t)
	cookie, csrf := e.setup(t)
	secret, _ := turnOn(t, e, cookie, csrf)
	login := e.do(t, "POST", "/api/auth/login", `{"username":"admin","password":"correct horse battery"}`, xrw)
	const wrong = twofactor.LockAfter + 1
	for i := range wrong {
		if r := e.do(t, "POST", "/api/auth/second-factor", `{"code":"abcd-efgh-jkmn-pqrs"}`, pendingHeaders(login.pending)); r.status != http.StatusUnauthorized || r.body["code"] != "recovery_code_wrong" {
			t.Fatalf("wrong recovery code %d: %d %v", i+1, r.status, r.body)
		}
	}
	if n := e.count(t, `SELECT COUNT(*) FROM audit WHERE detail = ?`, fmt.Sprintf("recovery_code_wrong, %d in a row", wrong)); n != 1 {
		t.Fatalf("the audit does not count wrong recovery codes in a row")
	}
	login = e.do(t, "POST", "/api/auth/login", `{"username":"admin","password":"correct horse battery"}`, xrw)
	if ch := login.body["secondFactor"].(map[string]any); len(ch["methods"].([]any)) != 2 || ch["appCodesLockedUntil"] != nil {
		t.Fatalf("challenge after wrong recovery codes: %v", ch)
	}
	r := e.do(t, "POST", "/api/auth/second-factor", `{"code":"`+totp.Code(secret, e.clock.now())+`"}`, pendingHeaders(login.pending))
	if r.status != 200 {
		t.Fatalf("the owner's app code after %d wrong recovery codes: %d %v", wrong, r.status, r.body)
	}
	if n := r.body["notices"].([]any); len(n) != 1 || n[0].(map[string]any)["kind"] != "failed_attempts" || n[0].(map[string]any)["count"] != float64(wrong) {
		t.Fatalf("notices: %v", n)
	}
}

// A setup not yet confirmed can be shown, confirmed or cancelled only by the
// session that started it, so a stolen session cannot read its secret.
func TestAnUnfinishedSetupBelongsToTheSessionThatStartedIt(t *testing.T) {
	e := newEnv(t)
	cookie, csrf := e.setup(t)
	other := e.do(t, "POST", "/api/auth/login", `{"username":"admin","password":"correct horse battery"}`, xrw)
	otherCSRF := other.body["csrfToken"].(string)
	started := e.do(t, "POST", "/api/auth/2fa/setup", `{"password":"correct horse battery"}`, auth(cookie, csrf))
	if started.status != 200 {
		t.Fatalf("setup: %d %v", started.status, started.body)
	}
	secret, err := totp.ParseSecret(started.body["manualKey"].(string))
	if err != nil {
		t.Fatal(err)
	}
	if r := e.do(t, "GET", "/api/auth/2fa/setup", "", auth(other.cookie, "")); r.status != http.StatusConflict || r.body["code"] != "setup_missing" || r.body["manualKey"] != nil {
		t.Fatalf("another session reads the setup: %d %v", r.status, r.body)
	}
	if r := e.do(t, "GET", "/api/auth/2fa", "", auth(other.cookie, "")); r.body["state"] != "off" {
		t.Fatalf("status for another session: %v", r.body)
	}
	if r := e.do(t, "POST", "/api/auth/2fa/confirm", `{"code":"`+totp.Code(secret, e.clock.now())+`"}`, auth(other.cookie, otherCSRF)); r.status != http.StatusConflict || r.body["code"] != "setup_missing" {
		t.Fatalf("another session confirms the setup: %d %v", r.status, r.body)
	}
	if r := e.do(t, "DELETE", "/api/auth/2fa/setup", "", auth(other.cookie, otherCSRF)); r.status != http.StatusNoContent {
		t.Fatalf("cancel from another session: %d", r.status)
	}
	if r := e.do(t, "GET", "/api/auth/2fa/setup", "", auth(cookie, "")); r.status != 200 || r.body["manualKey"] != started.body["manualKey"] {
		t.Fatalf("another session cancelled the setup: %d %v", r.status, r.body)
	}

	replaced := e.do(t, "POST", "/api/auth/2fa/setup", `{"password":"correct horse battery"}`, auth(other.cookie, otherCSRF))
	if replaced.status != 200 || replaced.body["manualKey"] == started.body["manualKey"] {
		t.Fatalf("starting again with the password from another session: %d %v", replaced.status, replaced.body)
	}
	if r := e.do(t, "POST", "/api/auth/2fa/confirm", `{"code":"`+totp.Code(secret, e.clock.now())+`"}`, auth(cookie, csrf)); r.status != http.StatusConflict || r.body["code"] != "setup_missing" {
		t.Fatalf("the replaced setup still confirms: %d %v", r.status, r.body)
	}
	if r := e.do(t, "GET", "/api/auth/2fa", "", auth(other.cookie, "")); r.body["state"] != "pending" {
		t.Fatalf("status for the session that started again: %v", r.body)
	}
}

// Five wrong codes pause app codes for a minute, doubling up to 16; recovery
// codes keep working, and 100 wrong codes block app codes.
func TestWrongCodesPauseThenBlockAppCodes(t *testing.T) {
	e := newEnv(t)
	cookie, csrf := e.setup(t)
	secret, codes := turnOn(t, e, cookie, csrf)
	login := e.do(t, "POST", "/api/auth/login", `{"username":"admin","password":"correct horse battery"}`, xrw)
	var r resp
	for i := 0; i < twofactor.LockAfter; i++ {
		r = e.do(t, "POST", "/api/auth/second-factor", `{"code":"`+wrongCode(secret, e.clock.now())+`"}`, pendingHeaders(login.pending))
	}
	if r.status != http.StatusTooManyRequests || r.body["code"] != "app_codes_locked" || r.header.Get("Retry-After") != "60" {
		t.Fatalf("fifth wrong code: %d %v Retry-After=%q", r.status, r.body, r.header.Get("Retry-After"))
	}
	if r := e.do(t, "POST", "/api/auth/second-factor", `{"code":"`+totp.Code(secret, e.clock.now())+`"}`, pendingHeaders(login.pending)); r.status != http.StatusTooManyRequests {
		t.Fatalf("a right app code while paused: %d %v", r.status, r.body)
	}
	if r := e.do(t, "POST", "/api/auth/second-factor", `{"code":"`+codes[0]+`"}`, pendingHeaders(login.pending)); r.status != 200 {
		t.Fatalf("a recovery code while app codes are paused: %d %v", r.status, r.body)
	}

	if _, err := e.srv.db.Exec(`UPDATE user_factors SET failures = ?`, twofactor.BlockAfter-1); err != nil {
		t.Fatal(err)
	}
	e.clock.add(20 * time.Minute)
	login = e.do(t, "POST", "/api/auth/login", `{"username":"admin","password":"correct horse battery"}`, xrw)
	r = e.do(t, "POST", "/api/auth/second-factor", `{"code":"`+wrongCode(secret, e.clock.now())+`"}`, pendingHeaders(login.pending))
	if r.status != http.StatusForbidden || r.body["code"] != "app_codes_blocked" || !strings.Contains(r.body["hint"].(string), "sudo playkeeper reset-2fa") {
		t.Fatalf("the hundredth wrong code: %d %v", r.status, r.body)
	}
	login = e.do(t, "POST", "/api/auth/login", `{"username":"admin","password":"correct horse battery"}`, xrw)
	if ch := login.body["secondFactor"].(map[string]any); ch["appCodesBlocked"] != true || len(ch["methods"].([]any)) != 1 {
		t.Fatalf("challenge while blocked: %v", ch)
	}
}

// The second step spends the same per-address budget as the password.
func TestSecondStepIsRateLimitedPerAddress(t *testing.T) {
	e := newEnv(t)
	cookie, csrf := e.setup(t)
	turnOn(t, e, cookie, csrf)
	login := e.do(t, "POST", "/api/auth/login", `{"username":"admin","password":"correct horse battery"}`, xrw)
	limited := false
	for i := 0; i < 15 && !limited; i++ {
		r := e.do(t, "POST", "/api/auth/second-factor", `{"code":"12"}`, pendingHeaders(login.pending))
		limited = r.status == http.StatusTooManyRequests && r.body["code"] == "rate_limited"
	}
	if !limited {
		t.Fatal("second-step attempts were never rate limited for the address")
	}
}

func TestTurningOffAndNewCodesNeedPasswordAndCode(t *testing.T) {
	e := newEnv(t)
	cookie, csrf := e.setup(t)
	secret, codes := turnOn(t, e, cookie, csrf)
	if r := e.do(t, "POST", "/api/auth/2fa/recovery-codes", `{"password":"wrong password!","code":"`+totp.Code(secret, e.clock.now())+`"}`, auth(cookie, csrf)); r.status != http.StatusForbidden || r.body["code"] != "password_wrong" {
		t.Fatalf("new codes with a wrong password: %d %v", r.status, r.body)
	}
	if r := e.do(t, "POST", "/api/auth/2fa/recovery-codes", `{"password":"correct horse battery","code":"`+wrongCode(secret, e.clock.now())+`"}`, auth(cookie, csrf)); r.status != http.StatusForbidden || r.body["code"] != "code_wrong" {
		t.Fatalf("new codes with a wrong code: %d %v", r.status, r.body)
	}
	r := e.do(t, "POST", "/api/auth/2fa/recovery-codes", `{"password":"correct horse battery","code":"`+codes[1]+`"}`, auth(cookie, csrf))
	if r.status != 200 || len(r.body["recoveryCodes"].([]any)) != 10 {
		t.Fatalf("new codes: %d %v", r.status, r.body)
	}
	fresh := r.body["recoveryCodes"].([]any)[0].(string)
	if r := e.do(t, "POST", "/api/auth/2fa/disable", `{"password":"correct horse battery","code":"`+codes[2]+`"}`, auth(cookie, csrf)); r.status != http.StatusForbidden || r.body["code"] != "recovery_code_wrong" {
		t.Fatalf("old recovery codes still work after new ones: %d %v", r.status, r.body)
	}
	if r := e.do(t, "POST", "/api/auth/2fa/disable", `{"password":"correct horse battery","code":"`+fresh+`"}`, auth(cookie, csrf)); r.status != http.StatusNoContent {
		t.Fatalf("turn off: %d %v", r.status, r.body)
	}
	if r := e.do(t, "GET", "/api/auth/me", "", auth(cookie, "")); r.status != 200 {
		t.Fatalf("turning off must keep the session: %d", r.status)
	}
	if r := e.do(t, "POST", "/api/auth/2fa/disable", `{"password":"correct horse battery","code":"`+fresh+`"}`, auth(cookie, csrf)); r.status != http.StatusConflict || r.body["code"] != "two_factor_off" {
		t.Fatalf("turning off twice: %d %v", r.status, r.body)
	}
	if r := e.do(t, "POST", "/api/auth/login", `{"username":"admin","password":"correct horse battery"}`, xrw); r.status != 200 || r.cookie == "" {
		t.Fatalf("sign-in after turning off: %d %v", r.status, r.body)
	}
}

func TestSetupCanBeShownAgainAndCancelled(t *testing.T) {
	e := newEnv(t)
	cookie, csrf := e.setup(t)
	if r := e.do(t, "GET", "/api/auth/2fa/setup", "", auth(cookie, "")); r.status != http.StatusConflict || r.body["code"] != "setup_missing" {
		t.Fatalf("setup before starting: %d %v", r.status, r.body)
	}
	started := e.do(t, "POST", "/api/auth/2fa/setup", `{"password":"correct horse battery"}`, auth(cookie, csrf))
	if r := e.do(t, "GET", "/api/auth/2fa/setup", "", auth(cookie, "")); r.status != 200 || r.body["manualKey"] != started.body["manualKey"] {
		t.Fatalf("setup shown again: %d %v", r.status, r.body)
	}
	if r := e.do(t, "GET", "/api/auth/2fa", "", auth(cookie, "")); r.body["state"] != "pending" {
		t.Fatalf("status while setting up: %v", r.body)
	}
	if r := e.do(t, "POST", "/api/auth/2fa/confirm", `{"code":"12345"}`, auth(cookie, csrf)); r.status != http.StatusBadRequest || r.body["code"] != "app_code_malformed" {
		t.Fatalf("confirm with five digits: %d %v", r.status, r.body)
	}
	if r := e.do(t, "DELETE", "/api/auth/2fa/setup", "", auth(cookie, csrf)); r.status != http.StatusNoContent {
		t.Fatalf("cancel setup: %d", r.status)
	}
	if r := e.do(t, "GET", "/api/auth/2fa", "", auth(cookie, "")); r.body["state"] != "off" {
		t.Fatalf("status after cancel: %v", r.body)
	}
	if r := e.do(t, "POST", "/api/auth/login", `{"username":"admin","password":"correct horse battery"}`, xrw); r.cookie == "" {
		t.Fatalf("an unfinished setup asked for a code: %v", r.body)
	}
}

// A factor is stored only over the revision it was loaded at, so a code
// cannot be used twice even by a write outside changeFactor's transaction.
func TestAFactorChangeIsStoredOnlyOnce(t *testing.T) {
	e := newEnv(t)
	cookie, csrf := e.setup(t)
	_, codes := turnOn(t, e, cookie, csrf)
	ctx := context.Background()
	f, _, ok, err := loadFactor(ctx, e.srv.db, 1)
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	first, _, err := twofactor.SignIn(f, codes[0], e.clock.now())
	if err != nil {
		t.Fatal(err)
	}
	second, _, _ := twofactor.SignIn(f, codes[0], e.clock.now())
	if err := storeFactor(ctx, e.srv.db, 1, f.Revision, true, first, ""); err != nil {
		t.Fatalf("first store: %v", err)
	}
	if err := storeFactor(ctx, e.srv.db, 1, f.Revision, true, second, ""); !errors.Is(err, errFactorRace) {
		t.Fatalf("second store from the same revision: %v", err)
	}
	back, _, _, _ := loadFactor(ctx, e.srv.db, 1)
	if back.Recovery.Remaining() != 9 || back.Revision != first.Revision || !back.ConfirmedAt.Equal(f.ConfirmedAt) || back.Secret.Base32() != f.Secret.Base32() {
		t.Fatalf("stored factor: %+v", back)
	}
}

// barrier holds each caller until n have arrived or wait has passed, so
// steps that can run side by side do.
type barrier struct {
	n       int32
	arrived atomic.Int32
	all     chan struct{}
	wait    time.Duration
}

func newBarrier(n int, wait time.Duration) *barrier {
	return &barrier{n: int32(n), all: make(chan struct{}), wait: wait}
}

func (b *barrier) arrive() {
	if b.arrived.Add(1) == b.n {
		close(b.all)
	}
	select {
	case <-b.all:
	case <-time.After(b.wait):
	}
}

// Concurrent second steps take turns on the factor, so every wrong code is
// counted, whether or not the requests could run side by side.
func TestConcurrentWrongCodesAreAllCounted(t *testing.T) {
	e := newEnv(t)
	cookie, csrf := e.setup(t)
	secret, _ := turnOn(t, e, cookie, csrf)
	now := e.clock.now()
	send := func(n int, code string) []error {
		b := newBarrier(n, 50*time.Millisecond)
		errs := make([]error, n)
		var wg sync.WaitGroup
		for i := range n {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, errs[i] = e.srv.changeFactor(1, "", func(f twofactor.Factor, _ bool) (twofactor.Factor, error) {
					b.arrive()
					next, _, err := twofactor.SignIn(f, code, now)
					return next, err
				})
			}()
		}
		wg.Wait()
		return errs
	}
	for _, err := range send(twofactor.LockAfter, wrongCode(secret, now)) {
		if k := twofactor.KindOf(err); k != twofactor.KindCodeWrong && k != twofactor.KindAppCodesLocked {
			t.Fatalf("a concurrent wrong app code: %v", err)
		}
	}
	const recoveryTries = 12
	for _, err := range send(recoveryTries, "abcd-efgh-jkmn-pqrs") {
		if twofactor.KindOf(err) != twofactor.KindRecoveryCodeWrong {
			t.Fatalf("a concurrent wrong recovery code: %v", err)
		}
	}
	f, _, _, err := loadFactor(context.Background(), e.srv.db, 1)
	if err != nil {
		t.Fatal(err)
	}
	if f.Failures != twofactor.LockAfter || f.RecoveryFailures != recoveryTries || !now.Before(f.LockedUntil) {
		t.Fatalf("after %d concurrent wrong app codes and %d wrong recovery codes: %d and %d counted, locked until %v",
			twofactor.LockAfter, recoveryTries, f.Failures, f.RecoveryFailures, f.LockedUntil)
	}

	login := e.do(t, "POST", "/api/auth/login", `{"username":"admin","password":"correct horse battery"}`, xrw)
	const n = 4
	statuses := make([]int, n)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			statuses[i] = e.do(t, "POST", "/api/auth/second-factor", `{"code":"abcd-efgh-jkmn-pqrs"}`, pendingHeaders(login.pending)).status
		}()
	}
	close(start)
	wg.Wait()
	for i, st := range statuses {
		if st != http.StatusUnauthorized {
			t.Fatalf("concurrent wrong recovery code %d: %d", i, st)
		}
	}
	if f, _, _, _ := loadFactor(context.Background(), e.srv.db, 1); f.RecoveryFailures != recoveryTries+n {
		t.Fatalf("%d concurrent wrong recovery codes through the dashboard: %d counted in all, want %d", n, f.RecoveryFailures, recoveryTries+n)
	}
}

// Every address in an IPv6 /64 shares one sign-in budget; IPv4 addresses,
// also written as IPv4-mapped IPv6, have their own.
func TestSignInLimiterCountsIPv6By64(t *testing.T) {
	for addr, want := range map[string]string{
		"2001:db8:1:2::1":        "net:2001:db8:1:2::/64",
		"2001:db8:1:2:ffff::abc": "net:2001:db8:1:2::/64",
		"fe80::1%eth0":           "net:fe80::/64",
		"198.51.100.7":           "ip:198.51.100.7",
		"::ffff:198.51.100.7":    "ip:198.51.100.7",
		"not an address":         "ip:not an address",
	} {
		if got := limitKey(addr); got != want {
			t.Errorf("limitKey(%q) = %q, want %q", addr, got, want)
		}
	}
	e := newEnv(t)
	allowed := func(remote string) bool {
		r := httptest.NewRequest("POST", "/api/auth/login", nil)
		r.RemoteAddr = remote
		return e.srv.rateLimitIP(httptest.NewRecorder(), r)
	}
	limited := false
	for i := range 15 {
		if !allowed(fmt.Sprintf("[2001:db8:1:2::%x]:40000", i+1)) {
			limited = true
			break
		}
	}
	if !limited {
		t.Fatal("sign-ins from new addresses in one /64 were never rate limited")
	}
	if !allowed("[2001:db8:1:3::1]:40000") || !allowed("198.51.100.7:40000") {
		t.Fatal("another /64 or an IPv4 address shares the limited budget")
	}
}

func TestResetTwoFactorFromTheCommandLine(t *testing.T) {
	e := newEnv(t)
	cookie, csrf := e.setup(t)
	turnOn(t, e, cookie, csrf)
	if _, err := e.srv.ResetTwoFactor("nobody"); err == nil || err.Error() != "no such user: nobody" {
		t.Fatalf("unknown user: %v", err)
	}
	if wasOn, err := e.srv.ResetTwoFactor("admin"); !wasOn || err != nil {
		t.Fatalf("reset: %v %v", wasOn, err)
	}
	if r := e.do(t, "GET", "/api/auth/me", "", auth(cookie, "")); r.status != http.StatusUnauthorized {
		t.Fatalf("reset must sign out every session: %d", r.status)
	}
	if wasOn, err := e.srv.ResetTwoFactor("admin"); wasOn || err != nil {
		t.Fatalf("reset when off: %v %v", wasOn, err)
	}
	if r := e.do(t, "POST", "/api/auth/login", `{"username":"admin","password":"correct horse battery"}`, xrw); r.cookie == "" {
		t.Fatalf("sign-in after reset asked for a code: %v", r.body)
	}
	var n int
	e.srv.db.QueryRow(`SELECT COUNT(*) FROM audit WHERE action = '2fa.reset' AND actor = 'root@host' AND target = 'admin'`).Scan(&n)
	if n != 1 {
		t.Fatalf("reset audited %d times", n)
	}
}

func TestTwoFactorSecretsStayOutOfTheAuditLog(t *testing.T) {
	e := newEnv(t)
	cookie, csrf := e.setup(t)
	secret, codes := turnOn(t, e, cookie, csrf)
	login := e.do(t, "POST", "/api/auth/login", `{"username":"admin","password":"correct horse battery"}`, xrw)
	app := totp.Code(secret, e.clock.now())
	e.do(t, "POST", "/api/auth/second-factor", `{"code":"`+wrongCode(secret, e.clock.now())+`"}`, pendingHeaders(login.pending))
	e.do(t, "POST", "/api/auth/second-factor", `{"code":"`+app+`"}`, pendingHeaders(login.pending))
	rows, err := e.srv.db.Query(`SELECT action || '|' || target || '|' || result || '|' || detail FROM audit`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var all []string
	for rows.Next() {
		var s string
		rows.Scan(&s)
		all = append(all, s)
	}
	joined := strings.Join(all, "\n")
	for _, secretText := range append([]string{secret.Base32(), app, login.pending}, codes...) {
		if strings.Contains(joined, secretText) {
			t.Fatalf("audit log contains a secret: %q", joined)
		}
	}
	for _, want := range []string{"2fa.setup|panel|succeeded|", "2fa.enable|panel|succeeded|other sessions signed out", "login.password|panel|succeeded|second factor needed",
		"login.second_factor|panel|failed|code_wrong, 1 in a row", "login|panel|succeeded|app_code"} {
		if !strings.Contains(joined, want) {
			t.Errorf("audit log lacks %q:\n%s", want, joined)
		}
	}
}

func TestAccountNameForTheAuthenticatorApp(t *testing.T) {
	for host, want := range map[string]string{
		"198.51.100.10:8443":        "siya@198.51.100.10",
		"alex.playkeeper.io:8443":   "siya@alex.playkeeper.io",
		"Play.Example.com":          "siya@play.example.com",
		"[2001:db8::1]:8443":        "siya",
		"":                          "siya",
		"evil\u202e.example.com:80": "siya",
	} {
		if got := accountName("siya", host); got != want {
			t.Errorf("accountName(%q) = %q, want %q", host, got, want)
		}
	}
}
