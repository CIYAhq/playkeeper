package discord

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
)

// testToken has the shape of a webhook token (68 characters of URL-safe
// base64) but belongs to no webhook.
const (
	testID     = "1289345123456789012"
	testToken  = "xPXGwtE059o9ITOYFfjZTmNIKfk5jCdDYjY1LmIGLKskElTlJJtc2Z4Fhl_Eal5pfRQw"
	testThread = "1289345123456789099"
	testURL    = "https://discord.com/api/webhooks/" + testID + "/" + testToken
)

func testWebhook(t *testing.T, raw string) Webhook {
	t.Helper()
	if raw == "" {
		raw = testURL
	}
	w, err := ParseWebhookURL(raw)
	if err != nil {
		t.Fatalf("ParseWebhookURL: %v", err)
	}
	return w
}

func TestParseWebhookURLAcceptsWhatDiscordHandsOut(t *testing.T) {
	// The webhook in Discord's older docs, with a 100-digit hex token.
	const oldID, oldToken = "223704706495545344", "3d89bb7572e0fb30d8128367b3b1b44fecd1726de135cbe28a41f8b2f777c372ba2939e72279b94526ff5d1bd4358d65cf11"
	for _, c := range []struct {
		raw, id, token, thread string
	}{
		{testURL, testID, testToken, ""},
		{"https://discord.com/api/v10/webhooks/" + testID + "/" + testToken, testID, testToken, ""},
		{"https://discord.com/api/v9/webhooks/" + testID + "/" + testToken, testID, testToken, ""},
		{"https://canary.discord.com/api/webhooks/" + testID + "/" + testToken, testID, testToken, ""},
		{"https://ptb.discord.com/api/webhooks/" + testID + "/" + testToken, testID, testToken, ""},
		{"https://discordapp.com/api/webhooks/" + oldID + "/" + oldToken, oldID, oldToken, ""},
		{"  " + testURL + "\n", testID, testToken, ""},
		{"https://Discord.COM/api/webhooks/" + testID + "/" + testToken, testID, testToken, ""},
		{"https://discord.com:443/api/webhooks/" + testID + "/" + testToken, testID, testToken, ""},
		{testURL + "/", testID, testToken, ""},
		{testURL + "?thread_id=" + testThread, testID, testToken, testThread},
		{testURL + "?wait=true", testID, testToken, ""},
	} {
		w, err := ParseWebhookURL(c.raw)
		if err != nil {
			t.Errorf("%q: %v", c.raw, err)
			continue
		}
		want := "https://discord.com/api/webhooks/" + c.id + "/" + c.token
		if c.thread != "" {
			want += "?thread_id=" + c.thread
		}
		if w.ID() != c.id || w.ThreadID() != c.thread || w.SecretURL() != want {
			t.Errorf("%q: got id %q thread %q url %q, want %q", c.raw, w.ID(), w.ThreadID(), w.SecretURL(), want)
		}
	}
}

func TestParseWebhookURLRefusesEverythingElse(t *testing.T) {
	path := "/api/webhooks/" + testID + "/" + testToken
	for _, c := range []struct {
		raw, reason string
	}{
		{"", ReasonEmpty},
		{"   ", ReasonEmpty},
		{"discord.com" + path, ReasonNotHTTPS},
		{"http://discord.com" + path, ReasonNotHTTPS},
		{"ftp://discord.com" + path, ReasonNotHTTPS},
		{"https://%zz" + path, ReasonNotURL},
		{"https://discord.gg/abcdef", ReasonHost},
		{"https://evil.example" + path, ReasonHost},
		{"https://discord.com.evil.example" + path, ReasonHost},
		{"https://discord.com@evil.example" + path, ReasonHost},
		{"https://discord.com:x@discord.com" + path, ReasonHost},
		{"https://evil.example/?https://discord.com" + path, ReasonHost},
		{"https://canary.discordapp.com" + path, ReasonHost},
		{"https://discord.com:8443" + path, ReasonHost},
		{"https://discord.com/api/webhooks/" + testID, ReasonPath},
		{"https://discord.com/webhooks/" + testID + "/" + testToken, ReasonPath},
		{"https://discord.com" + path + "/github", ReasonPath},
		{"https://discord.com/api/v10/webhooks/" + testID + "/" + testToken + "/messages/" + testThread, ReasonPath},
		{"https://discord.com/api/v10/webhooks/" + testID + "/" + testToken[:30] + "%2F" + testToken[30:], ReasonPath},
		{"https://discord.com/channels/" + testID + "/" + testThread, ReasonPath},
		{"https://discord.com/api/webhooks/12345/" + testToken, ReasonID},
		{"https://discord.com/api/webhooks/12893451234567890ab/" + testToken, ReasonID},
		{"https://discord.com/api/webhooks/99999999999999999999/" + testToken, ReasonID},
		{"https://discord.com/api/webhooks/" + testID + "/" + testToken[:40], ReasonToken},
		{"https://discord.com/api/webhooks/" + testID + "/" + testToken[:67] + ".", ReasonToken},
		{"https://discord.com/api/webhooks/" + testID + "/" + testToken + strings.Repeat("a", 33), ReasonToken},
		{"https://discord.com/api/webhooks/" + testID + "/" + testToken[:67] + "é", ReasonToken},
		{"https://discord.com" + path + "?thread_id=general", ReasonThread},
		{"https://discord.com" + path + "?thread_id=1;2", ReasonNotURL},
	} {
		_, err := ParseWebhookURL(c.raw)
		var e *Error
		if !errors.As(err, &e) {
			t.Errorf("%q: got %v, want an *Error", c.raw, err)
			continue
		}
		if e.Code != CodeInvalidURL || e.Reason != c.reason {
			t.Errorf("%q: got %s/%s (%s), want %s/%s", c.raw, e.Code, e.Reason, e.Msg, CodeInvalidURL, c.reason)
		}
		if e.Msg == "" || e.Hint == "" {
			t.Errorf("%q: the error needs a message and a hint: %+v", c.raw, e)
		}
		for _, secret := range []string{testToken, testToken[:40], strings.TrimSpace(c.raw)} {
			if secret != "" && (strings.Contains(e.Msg, secret) || strings.Contains(e.Hint, secret)) {
				t.Errorf("%q: the error repeats the URL or its token: %+v", c.raw, e)
			}
		}
	}
}

func TestWebhookNeverShowsItsToken(t *testing.T) {
	w := testWebhook(t, testURL+"?thread_id="+testThread)
	type holder struct {
		Public  Webhook
		private Webhook
		Ptr     *Webhook
	}
	h := holder{w, w, &w}
	s := Settings{Webhook: w, Alerts: DefaultAlerts(), LiveStatus: true}
	var out []string
	out = append(out,
		w.String(), w.GoString(), fmt.Sprint(w), fmt.Sprintln(w),
		fmt.Sprintf("%v %+v %s %q %#v %x %X %T", w, w, w, w, w, w, w, w),
		fmt.Sprintf("%v %+v %#v %v", h, h, h, &h),
		fmt.Sprintf("%v %+v %#v", s, s, s),
		fmt.Sprintf("%v %v", []Webhook{w}, map[string]Webhook{"a": w}),
		fmt.Sprintf("%+v", []any{&w, &s}),
	)
	for _, v := range []any{w, h, s, []Webhook{w}} {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, string(b))
	}
	var logs bytes.Buffer
	for _, handler := range []slog.Handler{slog.NewTextHandler(&logs, nil), slog.NewJSONHandler(&logs, nil)} {
		slog.New(handler).Info("saved", "webhook", w, "holder", h, "settings", s, "ptr", &w)
	}
	out = append(out, logs.String())
	for _, o := range out {
		if strings.Contains(o, testToken) {
			t.Errorf("the token leaked into %q", o)
		}
	}
	if want := "https://discord.com/api/webhooks/" + testID + "/[redacted]?thread_id=" + testThread; w.String() != want {
		t.Errorf("String() = %q, want %q", w.String(), want)
	}
	if !strings.Contains(logs.String(), testID+"/[redacted]") {
		t.Errorf("logs should show the redacted URL: %s", logs.String())
	}
	if b, _ := json.Marshal(w); string(b) != `{"id":"`+testID+`","threadId":"`+testThread+`"}` {
		t.Errorf("JSON is %s", b)
	}
	if b, _ := json.Marshal(Webhook{}); string(b) != "null" {
		t.Errorf("JSON of no webhook is %s", b)
	}
	if !strings.Contains(w.SecretURL(), testToken) {
		t.Error("SecretURL must keep the token, to be stored")
	}
}

func TestWebhookEqual(t *testing.T) {
	a := testWebhook(t, "")
	same := testWebhook(t, "https://discordapp.com/api/v10/webhooks/"+testID+"/"+testToken)
	otherToken := testWebhook(t, "https://discord.com/api/webhooks/"+testID+"/"+strings.Repeat("Ab-_", 17))
	otherThread := testWebhook(t, testURL+"?thread_id="+testThread)
	switch {
	case !a.Equal(same):
		t.Error("the same webhook from another host must be equal")
	case a.Equal(otherToken), a.Equal(otherThread), a.Equal(Webhook{}), (Webhook{}).Equal(a):
		t.Error("a different token, thread or no webhook must not be equal")
	case !(Webhook{}).Equal(Webhook{}):
		t.Error("no webhook equals no webhook")
	}
	if a.redact("POST "+a.SecretURL()) != "POST https://discord.com/api/webhooks/"+testID+"/[redacted]" {
		t.Errorf("redact left the token in: %s", a.redact(a.SecretURL()))
	}
}
