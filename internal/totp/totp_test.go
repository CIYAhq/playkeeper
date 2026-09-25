package totp

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"
)

// rfcKey is the SHA-1 seed of RFC 4226 appendix D and RFC 6238 appendix B.
const rfcKey = "12345678901234567890"

func rfcSecret(t *testing.T) Secret {
	t.Helper()
	s, err := NewSecret(bytes.NewReader([]byte(rfcKey)))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestHOTPMatchesRFC4226AppendixD(t *testing.T) {
	want := []string{"755224", "287082", "359152", "969429", "338314", "254676", "287922", "162583", "399871", "520489"}
	for counter, code := range want {
		if got := hotp([]byte(rfcKey), uint64(counter), 6); got != code {
			t.Errorf("HOTP(counter %d) = %s, want %s", counter, got, code)
		}
	}
}

func TestTOTPMatchesRFC6238AppendixB(t *testing.T) {
	s := rfcSecret(t)
	for _, tc := range []struct {
		unix  int64
		step  int64
		eight string
	}{
		{59, 0x1, "94287082"},
		{1111111109, 0x23523EC, "07081804"},
		{1111111111, 0x23523ED, "14050471"},
		{1234567890, 0x273EF07, "89005924"},
		{2000000000, 0x3F940AA, "69279037"},
		{20000000000, 0x27BC86AA, "65353130"},
	} {
		at := time.Unix(tc.unix, 0)
		if got := Step(at); got != tc.step {
			t.Errorf("Step(%d) = %#x, want %#x", tc.unix, got, tc.step)
		}
		if got := hotp(s.key, uint64(tc.step), 8); got != tc.eight {
			t.Errorf("8-digit TOTP at %d = %s, want %s", tc.unix, got, tc.eight)
		}
		six := tc.eight[2:]
		if got := Code(s, at); got != six {
			t.Errorf("Code at %d = %s, want %s", tc.unix, got, six)
		}
		if step, err := Verify(s, six, at, 0); err != nil || step != tc.step {
			t.Errorf("Verify(%s) at %d = %#x, %v; want %#x", six, tc.unix, step, err, tc.step)
		}
	}
}

func TestVerifyAcceptsOneStepEitherSideAndNoMore(t *testing.T) {
	s := rfcSecret(t)
	// Step 37037036 runs from 1111111080 to 1111111109 inclusive.
	const first, last = 1111111080, 1111111109
	stepAt := func(unix int64) int64 { return Step(time.Unix(unix, 0)) }
	codeFor := func(step int64) string { return Code(s, time.Unix(step*30, 0)) }
	for _, now := range []int64{first, first + 15, last} {
		cur := stepAt(now)
		for _, tc := range []struct {
			step int64
			ok   bool
		}{{cur - 2, false}, {cur - 1, true}, {cur, true}, {cur + 1, true}, {cur + 2, false}} {
			got, err := Verify(s, codeFor(tc.step), time.Unix(now, 0), 0)
			if tc.ok && (err != nil || got != tc.step) {
				t.Errorf("at %d, the code of step %+d must be accepted: step %d, %v", now, tc.step-cur, got, err)
			}
			if !tc.ok && !errors.Is(err, ErrWrong) {
				t.Errorf("at %d, the code of step %+d must be refused as wrong: step %d, %v", now, tc.step-cur, got, err)
			}
		}
	}
	// One second later a new step starts, and the oldest code stops working.
	oldest := codeFor(stepAt(first) - 1)
	if _, err := Verify(s, oldest, time.Unix(last, 0), 0); err != nil {
		t.Fatalf("the previous step's code works until the step ends: %v", err)
	}
	if _, err := Verify(s, oldest, time.Unix(last+1, 0), 0); !errors.Is(err, ErrWrong) {
		t.Fatalf("two steps back must be refused: %v", err)
	}
}

func TestVerifyToleratesPhoneClocksUpToThirtySecondsOff(t *testing.T) {
	s := rfcSecret(t)
	for _, server := range []int64{1234567860, 1234567875, 1234567889} {
		for _, tc := range []struct {
			offset int64
			ok     bool
		}{{-90, false}, {-60, false}, {-30, true}, {-1, true}, {0, true}, {1, true}, {30, true}, {60, false}, {90, false}} {
			phone := time.Unix(server+tc.offset, 0)
			_, err := Verify(s, Code(s, phone), time.Unix(server, 0), 0)
			if tc.ok != (err == nil) {
				t.Errorf("server at %d, phone %+ds off: err %v, want accepted=%v", server, tc.offset, err, tc.ok)
			}
		}
	}
}

func TestVerifyRefusesReplayAndOlderCodes(t *testing.T) {
	s := rfcSecret(t)
	now := time.Unix(1234567875, 0)
	cur := Step(now)
	step, err := Verify(s, Code(s, now), now, 0)
	if err != nil || step != cur {
		t.Fatalf("first use: %d %v", step, err)
	}
	if _, err := Verify(s, Code(s, now), now, step); !errors.Is(err, ErrReused) {
		t.Fatalf("the same code twice must be refused as reused: %v", err)
	}
	prev := now.Add(-Period)
	if _, err := Verify(s, Code(s, prev), now, step); !errors.Is(err, ErrReused) {
		t.Fatalf("an older code still in the window must be refused once a newer one was used: %v", err)
	}
	next := now.Add(Period)
	got, err := Verify(s, Code(s, next), now, step)
	if err != nil || got != cur+1 {
		t.Fatalf("the next step's code is fresh: %d %v", got, err)
	}
	if _, err := Verify(s, Code(s, now), next, got); !errors.Is(err, ErrReused) {
		t.Fatalf("after a phone ahead used step %d, step %d must be refused: %v", got, cur, err)
	}
	const wrong = "123456"
	for d := -1; d <= 1; d++ {
		if Code(s, now.Add(time.Duration(d)*Period)) == wrong {
			t.Fatalf("%s is a valid code at this time; pick another", wrong)
		}
	}
	if _, err := Verify(s, wrong, now, step); !errors.Is(err, ErrWrong) {
		t.Fatalf("a wrong code is wrong, not reused: %v", err)
	}
}

func TestVerifyNormalisesInput(t *testing.T) {
	s := rfcSecret(t)
	now := time.Unix(1111111111, 0) // code 050471
	for _, in := range []string{"050471", "050 471", " 050471\n", "050-471", "0 5 0 4 7 1", "050\u00a0471"} {
		if got, ok := Normalize(in); got != "050471" || !ok {
			t.Errorf("Normalize(%q) = %q, %v", in, got, ok)
		}
		if _, err := Verify(s, in, now, 0); err != nil {
			t.Errorf("Verify(%q) = %v", in, err)
		}
	}
	for _, in := range []string{"", "05047", "0504711", "05047a", "５０４７１", "050471" + strings.Repeat(" ", 40), "050.471"} {
		if _, ok := Normalize(in); ok {
			t.Errorf("Normalize(%q) accepted it", in)
		}
		if _, err := Verify(s, in, now, 0); !errors.Is(err, ErrMalformed) {
			t.Errorf("Verify(%q) = %v, want ErrMalformed", in, err)
		}
	}
	if _, err := Verify(Secret{}, "050471", now, 0); !errors.Is(err, ErrWrong) {
		t.Errorf("an empty secret must never accept a code: %v", err)
	}
}

func TestStepFloorsBefore1970(t *testing.T) {
	for _, tc := range []struct{ unix, step int64 }{{0, 0}, {29, 0}, {30, 1}, {-1, -1}, {-30, -1}, {-31, -2}} {
		if got := Step(time.Unix(tc.unix, 0)); got != tc.step {
			t.Errorf("Step(%d) = %d, want %d", tc.unix, got, tc.step)
		}
	}
}

func TestSecretEncodings(t *testing.T) {
	s := rfcSecret(t)
	if got, want := s.Base32(), "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ"; got != want {
		t.Fatalf("Base32 = %s, want %s", got, want)
	}
	if got, want := s.Grouped(), "GEZD GNBV GY3T QOJQ GEZD GNBV GY3T QOJQ"; got != want {
		t.Fatalf("Grouped = %s, want %s", got, want)
	}
	for _, in := range []string{s.Base32(), s.Grouped(), strings.ToLower(s.Grouped()), s.Base32() + "===="} {
		p, err := ParseSecret(in)
		if err != nil || !bytes.Equal(p.key, s.key) {
			t.Errorf("ParseSecret(%q) = %x, %v", in, p.key, err)
		}
	}
	for _, in := range []string{"", "GEZDGNBVGY3TQOJQ", "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJ1", strings.Repeat("A", 104)} {
		if _, err := ParseSecret(in); err == nil {
			t.Errorf("ParseSecret(%q) must fail (too short, bad character or too long)", in)
		}
	}
	fresh, err := NewSecret(bytes.NewReader(bytes.Repeat([]byte{0xa5}, 64)))
	if err != nil || len(fresh.key) != SecretSize || len(fresh.Base32()) != 32 {
		t.Fatalf("new secrets are %d bytes, 32 base32 characters: %x %v", SecretSize, fresh.key, err)
	}
	if _, err := NewSecret(bytes.NewReader(make([]byte, SecretSize-1))); err == nil {
		t.Fatal("a short read from the random source must fail")
	}
}

func TestSecretNeverPrintsItself(t *testing.T) {
	s := rfcSecret(t)
	wrapped := struct{ Secret Secret }{s}
	for _, out := range []string{
		fmt.Sprint(s), fmt.Sprintf("%v %+v %#v %s %q %x %X %d %o %b", s, s, s, s, s, s, s, s, s, s),
		fmt.Sprintf("%v %+v %#v %d", wrapped, wrapped, wrapped, wrapped), fmt.Sprint(&s),
	} {
		if strings.Contains(out, "GEZD") || strings.Contains(out, "1234") || strings.Contains(out, "49 50") || strings.Contains(out, "3132") {
			t.Errorf("secret leaked in %q", out)
		}
	}
	b, err := json.Marshal(wrapped)
	if err != nil || string(b) != `{"Secret":{}}` {
		t.Errorf("JSON = %s %v", b, err)
	}
}

func TestKeyURI(t *testing.T) {
	s := rfcSecret(t)
	const secret = "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ"
	for _, tc := range []struct{ account, label string }{
		{"alice", "alice"},
		{"Zoë Smith", "Zo%C3%AB%20Smith"},
		{"a+b", "a%2Bb"},
		{"a&b=c", "a%26b%3Dc"},
		{"a/b?c#d", "a%2Fb%3Fc%23d"},
		{"100%", "100%25"},
		{"ops.team_1-x~", "ops.team_1-x~"},
		{"sam@mc.example.com", "sam%40mc.example.com"},
		{"管理者", "%E7%AE%A1%E7%90%86%E8%80%85"},
	} {
		got, err := KeyURI(s, "Playkeeper", tc.account)
		want := "otpauth://totp/Playkeeper:" + tc.label + "?secret=" + secret + "&issuer=Playkeeper"
		if err != nil || got != want {
			t.Errorf("KeyURI(%q) = %s, %v\n want %s", tc.account, got, err, want)
			continue
		}
		u, err := url.Parse(got)
		if err != nil || u.Scheme != "otpauth" || u.Host != "totp" || u.Path != "/Playkeeper:"+tc.account ||
			u.Query().Get("secret") != secret || u.Query().Get("issuer") != "Playkeeper" || len(u.Query()) != 2 {
			t.Errorf("KeyURI(%q) does not parse back: %+v %v", tc.account, u, err)
		}
	}
	got, err := KeyURI(s, "Play keeper", "alice")
	if want := "otpauth://totp/Play%20keeper:alice?secret=" + secret + "&issuer=Play%20keeper"; err != nil || got != want {
		t.Errorf("issuer with a space: %s %v", got, err)
	}
	for _, tc := range []struct{ issuer, account, why string }{
		{"Playkeeper", "", "empty"},
		{"", "alice", "empty"},
		{"Playkeeper", "al:ice", "colon"},
		{"Play:keeper", "alice", "colon"},
		{"Playkeeper", "alice\n", "control"},
		{"Playkeeper", "\xff", "UTF-8"},
		{"Playkeeper", strings.Repeat("a", 129), "128 bytes"},
	} {
		if _, err := KeyURI(s, tc.issuer, tc.account); err == nil || !strings.Contains(err.Error(), tc.why) {
			t.Errorf("KeyURI(%q, %q): want an error about %q, got %v", tc.issuer, tc.account, tc.why, err)
		}
	}
	if _, err := KeyURI(Secret{}, "Playkeeper", "alice"); err == nil {
		t.Error("KeyURI without a secret must fail")
	}
}
