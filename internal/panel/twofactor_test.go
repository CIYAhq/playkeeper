package panel

import (
	"net/http"
	"strings"
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

// Two requests that load the same factor cannot both store it, so a code
// cannot be used twice by racing.
func TestAFactorChangeIsStoredOnlyOnce(t *testing.T) {
	e := newEnv(t)
	cookie, csrf := e.setup(t)
	_, codes := turnOn(t, e, cookie, csrf)
	f, ok, err := e.srv.loadFactor(1)
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	first, _, err := twofactor.SignIn(f, codes[0], e.clock.now())
	if err != nil {
		t.Fatal(err)
	}
	second, _, _ := twofactor.SignIn(f, codes[0], e.clock.now())
	if stored, err := e.srv.storeFactor(1, f, true, first); !stored || err != nil {
		t.Fatalf("first store: %v %v", stored, err)
	}
	if stored, err := e.srv.storeFactor(1, f, true, second); stored || err != nil {
		t.Fatalf("second store from the same revision: %v %v", stored, err)
	}
	back, _, _ := e.srv.loadFactor(1)
	if back.Recovery.Remaining() != 9 || back.Revision != first.Revision || !back.ConfirmedAt.Equal(f.ConfirmedAt) || back.Secret.Base32() != f.Secret.Base32() {
		t.Fatalf("stored factor: %+v", back)
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
