// Package twofactor holds the rules for signing in to the dashboard with a
// second factor, an authenticator app: turning it on with a QR code, the
// second sign-in step with an app code or a recovery code, turning it off,
// and how many wrong codes are allowed. It keeps no state and touches no
// database: each step takes the user's Factor as stored and returns the
// Factor to store.
//
// Recovery codes are stored as SHA-256 hashes rather than with a slow
// password hash: each code has 80 random bits, so a leaked hash cannot be
// searched for its code, and a slow hash would only make every sign-in
// attempt costly for the server.
package twofactor

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"time"

	"github.com/CIYAhq/playkeeper/internal/qrcode"
	"github.com/CIYAhq/playkeeper/internal/totp"
)

const (
	// Issuer names the dashboard in authenticator apps.
	Issuer = "Playkeeper"
	// SetupTTL is how long a started setup waits for its first code.
	SetupTTL = 15 * time.Minute
	// LockAfter wrong codes in a row lock app codes for a minute, and each
	// further one doubles that up to 16 minutes, as the dashboard does for
	// wrong passwords. Recovery codes still work.
	LockAfter = 5
	// BlockAfter wrong codes in a row block app codes until a recovery code
	// is used or the owner resets two-factor sign-in, so someone who knows
	// the password gets at most that many guesses (NIST SP 800-63B, 5.2.2).
	BlockAfter = 100
)

// Factor is a user's authenticator app as stored in the user_factors table.
// The zero Factor means two-factor sign-in is off.
type Factor struct {
	Secret totp.Secret
	// CreatedAt is when setup started. ConfirmedAt stays zero until the
	// first correct code turns two-factor sign-in on.
	CreatedAt   time.Time
	ConfirmedAt time.Time
	// LastStep is the time step of the last app code accepted; no code from
	// that step or before works again.
	LastStep   int64
	LastUsedAt time.Time
	Recovery   RecoverySet
	// Failures counts wrong codes since the last correct one.
	Failures    int
	LockedUntil time.Time
	// Revision goes up by one with every change. Store a returned Factor
	// when its Revision differs, only if the stored Revision is still the
	// one that was loaded, so two requests cannot both use one code.
	Revision int64
}

// On reports whether two-factor sign-in is on.
func (f Factor) On() bool { return !f.ConfirmedAt.IsZero() }

func (f Factor) setupError(now time.Time) error {
	switch {
	case f.On():
		return &Error{Kind: KindOn}
	case f.Secret.IsZero():
		return &Error{Kind: KindNoSetup}
	case !now.Before(f.CreatedAt.Add(SetupTTL)):
		return &Error{Kind: KindSetupExpired}
	}
	return nil
}

// Setup is what the dashboard shows to add the secret to an app: a QR code,
// the key to type instead, and the otpauth:// link for the phone itself. It
// holds the secret, so fmt and slog print it as "[two-factor setup]"; only
// JSON shows it, for the page.
type Setup struct {
	QRCodeSVG string    `json:"qrCodeSvg"`
	ManualKey string    `json:"manualKey"`
	URI       string    `json:"uri"`
	Issuer    string    `json:"issuer"`
	Account   string    `json:"account"`
	ExpiresAt time.Time `json:"expiresAt"`
}

func (Setup) String() string               { return "[two-factor setup]" }
func (s Setup) Format(f fmt.State, _ rune) { io.WriteString(f, s.String()) }
func (s Setup) LogValue() slog.Value       { return slog.StringValue(s.String()) }

// Begin starts turning two-factor sign-in on with a new secret, replacing a
// setup that was never finished. It needs the password, so a stolen session
// cannot tie the account to someone else's phone. The user then has
// SetupTTL to Confirm a code from the app.
func Begin(f Factor, passwordOK bool, account string, now time.Time, rand io.Reader) (Factor, Setup, error) {
	if f.On() {
		return f, Setup{}, &Error{Kind: KindOn}
	}
	if !passwordOK {
		return f, Setup{}, &Error{Kind: KindPasswordWrong}
	}
	secret, err := totp.NewSecret(rand)
	if err != nil {
		return f, Setup{}, err
	}
	next := Factor{Secret: secret, CreatedAt: now, Revision: f.Revision + 1}
	setup, err := next.Setup(account, now)
	if err != nil {
		return f, Setup{}, err
	}
	return next, setup, nil
}

// Setup shows the QR code and key again while the setup waits for its first
// code. Once two-factor sign-in is on, the secret is never shown again.
func (f Factor) Setup(account string, now time.Time) (Setup, error) {
	if err := f.setupError(now); err != nil {
		return Setup{}, err
	}
	uri, err := totp.KeyURI(f.Secret, Issuer, account)
	if err != nil {
		return Setup{}, err
	}
	qr, err := qrcode.Encode(uri)
	if err != nil {
		return Setup{}, err
	}
	return Setup{
		QRCodeSVG: qr.SVG(),
		ManualKey: f.Secret.Grouped(),
		URI:       uri,
		Issuer:    Issuer,
		Account:   account,
		ExpiresAt: f.CreatedAt.Add(SetupTTL),
	}, nil
}

// Confirm turns two-factor sign-in on when code comes from the app set up
// with Begin, and returns the recovery codes to show once. Wrong codes are
// not counted: the user is signed in already, and the code only proves that
// the app has the secret.
func Confirm(f Factor, code string, now time.Time, rand io.Reader) (Factor, []string, error) {
	if err := f.setupError(now); err != nil {
		return f, nil, err
	}
	step, err := totp.Verify(f.Secret, code, now, f.LastStep)
	switch {
	case errors.Is(err, totp.ErrMalformed):
		return f, nil, &Error{Kind: KindAppCodeMalformed}
	case err != nil:
		return f, nil, &Error{Kind: KindCodeWrong}
	}
	set, codes, err := NewRecoveryCodes(rand, now)
	if err != nil {
		return f, nil, err
	}
	f.ConfirmedAt, f.LastStep, f.LastUsedAt, f.Recovery = now, step, now, set
	f.Revision++
	return f, codes, nil
}

// Method is how a second step was passed.
type Method string

const (
	MethodAppCode      Method = "app_code"
	MethodRecoveryCode Method = "recovery_code"
)

// Result is a passed second step and what to tell the user about it.
type Result struct {
	Method  Method   `json:"method"`
	Notices []Notice `json:"notices"`
}

// SignIn checks the second sign-in step: a code from the app, or an unused
// recovery code, which works even while app codes are locked or blocked.
// Start the full session only once the returned Factor is stored.
func SignIn(f Factor, code string, now time.Time) (Factor, Result, error) {
	if !f.On() {
		return f, Result{}, &Error{Kind: KindOff}
	}
	failures := f.Failures
	f, method, err := f.check(code, now)
	if err != nil {
		return f, Result{}, err
	}
	res := Result{Method: method, Notices: []Notice{}}
	if failures > 0 {
		res.Notices = append(res.Notices, newNotice(NoticeFailedAttempts, failures))
	}
	left := f.Recovery.Remaining()
	if method == MethodRecoveryCode {
		res.Notices = append(res.Notices, newNotice(NoticeRecoveryCodeUsed, left))
	}
	switch {
	case left == 0:
		res.Notices = append(res.Notices, newNotice(NoticeNoRecoveryCodes, 0))
	case left <= LowRecoveryCodes:
		res.Notices = append(res.Notices, newNotice(NoticeRecoveryCodesLow, left))
	}
	return f, res, nil
}

// Disable turns two-factor sign-in off. It needs the password and a code or
// recovery code, so neither a stolen session nor a stolen password can do
// it; a wrong code counts as at sign-in. On success it returns the zero
// Factor: delete the stored one.
func Disable(f Factor, passwordOK bool, code string, now time.Time) (Factor, error) {
	if !f.On() {
		return f, &Error{Kind: KindOff}
	}
	if !passwordOK {
		return f, &Error{Kind: KindPasswordWrong}
	}
	if next, _, err := f.check(code, now); err != nil {
		return next, err
	}
	return Factor{}, nil
}

// RenewRecoveryCodes replaces all recovery codes after the password and a
// code or recovery code, and returns the new codes to show once.
func RenewRecoveryCodes(f Factor, passwordOK bool, code string, now time.Time, rand io.Reader) (Factor, []string, error) {
	if !f.On() {
		return f, nil, &Error{Kind: KindOff}
	}
	if !passwordOK {
		return f, nil, &Error{Kind: KindPasswordWrong}
	}
	set, codes, err := NewRecoveryCodes(rand, now)
	if err != nil {
		return f, nil, err
	}
	next, _, err := f.check(code, now)
	if err != nil {
		return next, nil, err
	}
	next.Recovery = set
	return next, codes, nil
}

// check accepts an app code or a recovery code and counts wrong ones.
// Malformed input, reused app codes and app codes refused unchecked while
// locked or blocked are not guesses, so they are not counted.
func (f Factor) check(code string, now time.Time) (Factor, Method, error) {
	if norm, ok := normalizeRecovery(code); ok {
		set, ok := f.Recovery.use(norm)
		if !ok {
			return f.failed(now, KindRecoveryCodeWrong)
		}
		f.Recovery = set
		return f.passed(now), MethodRecoveryCode, nil
	}
	if _, ok := totp.Normalize(code); !ok {
		return f, "", &Error{Kind: KindCodeMalformed}
	}
	switch {
	case f.Failures >= BlockAfter:
		return f, "", &Error{Kind: KindAppCodesBlocked}
	case now.Before(f.LockedUntil):
		return f, "", &Error{Kind: KindAppCodesLocked, RetryAfter: f.LockedUntil.Sub(now)}
	}
	step, err := totp.Verify(f.Secret, code, now, f.LastStep)
	switch {
	case errors.Is(err, totp.ErrReused):
		return f, "", &Error{Kind: KindCodeReused}
	case err != nil:
		return f.failed(now, KindCodeWrong)
	}
	f.LastStep = step
	return f.passed(now), MethodAppCode, nil
}

func (f Factor) passed(now time.Time) Factor {
	f.Failures, f.LockedUntil, f.LastUsedAt = 0, time.Time{}, now
	f.Revision++
	return f
}

func (f Factor) failed(now time.Time, kind Kind) (Factor, Method, error) {
	f.Failures++
	f.Revision++
	switch {
	case f.Failures >= BlockAfter:
		f.LockedUntil = time.Time{}
	case f.Failures >= LockAfter:
		f.LockedUntil = now.Add(time.Minute << min(f.Failures-LockAfter, 4))
	}
	if kind == KindCodeWrong {
		switch {
		case f.Failures >= BlockAfter:
			kind = KindAppCodesBlocked
		case f.Failures >= LockAfter:
			return f, "", &Error{Kind: KindAppCodesLocked, RetryAfter: f.LockedUntil.Sub(now)}
		}
	}
	return f, "", &Error{Kind: kind}
}

// State is whether two-factor sign-in is off, being set up, or on.
type State string

const (
	StateOff     State = "off"
	StatePending State = "pending"
	StateOn      State = "on"
)

// Status is what the settings page shows about two-factor sign-in.
type Status struct {
	State               State     `json:"state"`
	SetupExpiresAt      time.Time `json:"setupExpiresAt,omitzero"`
	ConfirmedAt         time.Time `json:"confirmedAt,omitzero"`
	LastUsedAt          time.Time `json:"lastUsedAt,omitzero"`
	RecoveryCodesLeft   int       `json:"recoveryCodesLeft"`
	RecoveryCodesMadeAt time.Time `json:"recoveryCodesMadeAt,omitzero"`
	AppCodesLockedUntil time.Time `json:"appCodesLockedUntil,omitzero"`
	AppCodesBlocked     bool      `json:"appCodesBlocked"`
}

// Status describes f at now. A setup that expired counts as off.
func (f Factor) Status(now time.Time) Status {
	switch {
	case f.On():
		st := Status{
			State:               StateOn,
			ConfirmedAt:         f.ConfirmedAt,
			LastUsedAt:          f.LastUsedAt,
			RecoveryCodesLeft:   f.Recovery.Remaining(),
			RecoveryCodesMadeAt: f.Recovery.CreatedAt,
			AppCodesBlocked:     f.Failures >= BlockAfter,
		}
		if now.Before(f.LockedUntil) {
			st.AppCodesLockedUntil = f.LockedUntil
		}
		return st
	case f.setupError(now) == nil:
		return Status{State: StatePending, SetupExpiresAt: f.CreatedAt.Add(SetupTTL)}
	}
	return Status{State: StateOff}
}

// Challenge is what the sign-in page needs for the second step, and no
// more, since whoever sees it has only passed the password.
type Challenge struct {
	// Methods are what may be entered now: app codes unless locked or
	// blocked, and recovery codes while any are left.
	Methods             []Method  `json:"methods"`
	AppCodesLockedUntil time.Time `json:"appCodesLockedUntil,omitzero"`
	AppCodesBlocked     bool      `json:"appCodesBlocked"`
}

// Challenge describes the second step for f, which is on, at now.
func (f Factor) Challenge(now time.Time) Challenge {
	c := Challenge{Methods: []Method{}}
	switch {
	case f.Failures >= BlockAfter:
		c.AppCodesBlocked = true
	case now.Before(f.LockedUntil):
		c.AppCodesLockedUntil = f.LockedUntil
	default:
		c.Methods = append(c.Methods, MethodAppCode)
	}
	if f.Recovery.Remaining() > 0 {
		c.Methods = append(c.Methods, MethodRecoveryCode)
	}
	return c
}

// Kind says why a step was refused, for the dashboard to act on; the Error
// text says it in English.
type Kind string

const (
	// KindCodeMalformed is input that is neither an app code nor a recovery
	// code.
	KindCodeMalformed Kind = "code_malformed"
	// KindAppCodeMalformed is input that is not six digits where only an app
	// code works: while setting up.
	KindAppCodeMalformed  Kind = "app_code_malformed"
	KindCodeWrong         Kind = "code_wrong"
	KindCodeReused        Kind = "code_reused"
	KindRecoveryCodeWrong Kind = "recovery_code_wrong"
	// KindAppCodesLocked comes with Error.RetryAfter.
	KindAppCodesLocked  Kind = "app_codes_locked"
	KindAppCodesBlocked Kind = "app_codes_blocked"
	KindPasswordWrong   Kind = "password_wrong"
	KindNoSetup         Kind = "setup_missing"
	KindSetupExpired    Kind = "setup_expired"
	KindOff             Kind = "two_factor_off"
	KindOn              Kind = "two_factor_on"
)

// Error is a refused step.
type Error struct {
	Kind Kind
	// RetryAfter is how long app codes stay locked, for KindAppCodesLocked.
	RetryAfter time.Duration
}

func (e *Error) Error() string {
	switch e.Kind {
	case KindCodeMalformed:
		return "Enter the 6-digit code from your authenticator app, or one of your recovery codes."
	case KindAppCodeMalformed:
		return "Enter the 6-digit code from your authenticator app."
	case KindCodeWrong:
		return "That code is not correct."
	case KindCodeReused:
		return "That code has already been used."
	case KindRecoveryCodeWrong:
		return "That recovery code is not correct or has already been used."
	case KindAppCodesLocked:
		return "Too many wrong codes. Try again in " + minutes(e.RetryAfter) + "."
	case KindAppCodesBlocked:
		return fmt.Sprintf("Codes from your authenticator app are blocked after %d wrong codes in a row.", BlockAfter)
	case KindPasswordWrong:
		return "Your password is not correct."
	case KindNoSetup:
		return "Two-factor sign-in is not being set up."
	case KindSetupExpired:
		return "This two-factor setup has expired."
	case KindOff:
		return "Two-factor sign-in is off."
	case KindOn:
		return "Two-factor sign-in is already on."
	}
	return "Two-factor sign-in did not work."
}

// Hint says what to do next, or is empty.
func (e *Error) Hint() string {
	switch e.Kind {
	case KindAppCodeMalformed:
		return "After you scan the QR code or type the key, the app shows a code under Playkeeper."
	case KindCodeWrong:
		return "Codes change every 30 seconds. If they keep failing, check that your phone sets its clock automatically."
	case KindCodeReused:
		return "Wait for the app to show the next code."
	case KindRecoveryCodeWrong:
		return "Each recovery code works once. Try another one from your list."
	case KindAppCodesLocked:
		return "You can use a recovery code now instead."
	case KindAppCodesBlocked:
		return "Use a recovery code, or on the server run: sudo playkeeper reset-2fa <username>. Then change your password, because someone may know it."
	case KindNoSetup, KindSetupExpired:
		return "Start again to get a new QR code."
	case KindOn:
		return "To move it to a new phone, turn it off, then on again."
	}
	return ""
}

func minutes(d time.Duration) string {
	if m := int((d + time.Minute - 1) / time.Minute); m > 1 {
		return fmt.Sprintf("%d minutes", m)
	}
	return "1 minute"
}

// KindOf is err's Kind, or "" if err is not an *Error.
func KindOf(err error) Kind {
	var e *Error
	if errors.As(err, &e) {
		return e.Kind
	}
	return ""
}

// NoticeKind names something to tell the user after the second step.
type NoticeKind string

const (
	// NoticeFailedAttempts says Count wrong codes were entered since the
	// last correct one. The page can leave it out if it saw them all.
	NoticeFailedAttempts NoticeKind = "failed_attempts"
	// NoticeRecoveryCodeUsed says a recovery code was used; Count are left.
	NoticeRecoveryCodeUsed NoticeKind = "recovery_code_used"
	// NoticeRecoveryCodesLow says only Count recovery codes are left.
	NoticeRecoveryCodesLow NoticeKind = "recovery_codes_low"
	NoticeNoRecoveryCodes  NoticeKind = "no_recovery_codes"
)

// Notice is something to tell the user after the second step, with its
// English text.
type Notice struct {
	Kind  NoticeKind `json:"kind"`
	Count int        `json:"count"`
	Text  string     `json:"text"`
}

func newNotice(kind NoticeKind, count int) Notice {
	n := Notice{Kind: kind, Count: count}
	switch kind {
	case NoticeFailedAttempts:
		times := "once"
		if count != 1 {
			times = fmt.Sprintf("%d times", count)
		}
		n.Text = "A wrong code was entered " + times + " since your last sign-in. If that was not you, change your password: someone knows it."
	case NoticeRecoveryCodeUsed:
		n.Text = "You signed in with a recovery code. It will not work again."
	case NoticeRecoveryCodesLow:
		n.Text = fmt.Sprintf("Only %d recovery codes are left. Make new ones on the Account page.", count)
		if count == 1 {
			n.Text = "Only 1 recovery code is left. Make new ones on the Account page."
		}
	case NoticeNoRecoveryCodes:
		n.Text = "No recovery codes are left. Make new ones on the Account page, so you can still sign in if you lose your phone."
	}
	return n
}
