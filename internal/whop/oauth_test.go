package whop

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestOAuthURLIsTheAPIsOriginsOAuth(t *testing.T) {
	for api, want := range map[string]string{
		"":                          "https://api.whop.com/oauth",
		DefaultAPIURL:               "https://api.whop.com/oauth",
		SandboxAPIURL:               "https://sandbox-api.whop.com/oauth",
		"http://127.0.0.1:9999":     "http://127.0.0.1:9999/oauth",
		"http://localhost:1/api/v1": "http://localhost:1/oauth",
	} {
		if got, err := OAuthURL(api); err != nil || got != want {
			t.Errorf("OAuthURL(%q) = %q, %v; want %q", api, got, err, want)
		}
	}
	if _, err := OAuthURL("http://evil.example/api/v1"); err == nil {
		t.Error("a plain http address far away was taken")
	}
}

func TestAClientIDLooksLikeAWhopApp(t *testing.T) {
	for id, ok := range map[string]bool{"app_abc123XYZ": true, "app_": false, "biz_abc": false, "app_abc def": false, "app_abc/../x": false, "": false} {
		if ValidClientID(id) != ok {
			t.Errorf("ValidClientID(%q) != %v", id, ok)
		}
	}
}

func TestPKCEFollowsRFC7636(t *testing.T) {
	// RFC 7636, appendix B.
	if got := Challenge("dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"); got != "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM" {
		t.Fatalf("Challenge = %q", got)
	}
	a, _ := NewVerifier()
	b, _ := NewVerifier()
	if len(a) != 43 || a == b || strings.ContainsAny(a, "+/=") {
		t.Fatalf("verifiers %q and %q", a, b)
	}
}

func TestTheAuthorizeLinkAsksForWhoTheyAreWithPKCE(t *testing.T) {
	o := OAuth{URL: "https://api.whop.com/oauth", ClientID: "app_pip", RedirectURI: "https://beta.playkeeper.me:8443/api/public/whop/signin/callback"}
	u, err := url.Parse(o.AuthorizeURL("st", "no", "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"))
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	if u.Host != "api.whop.com" || u.Path != "/oauth/authorize" || q.Get("response_type") != "code" || q.Get("client_id") != "app_pip" ||
		q.Get("redirect_uri") != o.RedirectURI || q.Get("scope") != "openid profile" || q.Get("state") != "st" || q.Get("nonce") != "no" ||
		q.Get("code_challenge") != "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM" || q.Get("code_challenge_method") != "S256" {
		t.Fatalf("authorize link: %s", u)
	}
}

// Whop checks the app, its secret and its permission before the code, so
// a code nobody was given says whether sign-ins can work without making
// anything. Only Whop refusing the app counts.
func TestCheckClientRefusesOnlyAnAppWhopRefuses(t *testing.T) {
	for _, tc := range []struct {
		status  int
		answer  string
		refused string
	}{
		{http.StatusUnauthorized, `{"error":"invalid_client","error_description":"client_secret lacks oauth:token_exchange permission"}`, "client_secret lacks oauth:token_exchange permission"},
		{http.StatusUnauthorized, `{"error":"invalid_client","error_description":"client_secret is invalid"}`, "client_secret is invalid"},
		{http.StatusBadRequest, `{"error":"invalid_grant","error_description":"Authorization code is invalid"}`, ""},
		{http.StatusInternalServerError, `oops`, ""},
	} {
		var body map[string]string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/oauth/token" {
				t.Errorf("asked %s", r.URL.Path)
			}
			json.NewDecoder(r.Body).Decode(&body)
			w.WriteHeader(tc.status)
			io.WriteString(w, tc.answer)
		}))
		o := OAuth{URL: srv.URL + "/oauth", ClientID: "app_pip", ClientSecret: "sec_1", RedirectURI: "https://beta.playkeeper.me:8443/cb"}
		err := o.CheckClient(context.Background())
		srv.Close()
		if tc.refused == "" && err != nil || tc.refused != "" && (err == nil || !strings.Contains(err.Error(), tc.refused)) {
			t.Fatalf("Whop answering %d %s: %v", tc.status, tc.answer, err)
		}
		if body["grant_type"] != "authorization_code" || body["client_id"] != "app_pip" || body["client_secret"] != "sec_1" ||
			body["code"] != "playkeeper-check" || len(body["code_verifier"]) != 43 || body["redirect_uri"] != o.RedirectURI {
			t.Fatalf("the check asked: %v", body)
		}
	}
	down := OAuth{URL: "http://127.0.0.1:1/oauth", ClientID: "app_pip", ClientSecret: "sec_1"}
	if err := down.CheckClient(context.Background()); err != nil {
		t.Fatalf("Whop out of reach: %v", err)
	}
}

func TestExchangeUserInfoAndRevokeSpeakWhopsOAuth(t *testing.T) {
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		seen = append(seen, r.Method+" "+r.URL.Path+" "+r.Header.Get("Authorization")+" "+string(b))
		switch r.URL.Path {
		case "/oauth/token":
			var body map[string]string
			json.Unmarshal(b, &body)
			if body["code"] == "expired" {
				w.WriteHeader(http.StatusBadRequest)
				io.WriteString(w, `{"error":"invalid_grant","error_description":"Authorization code has expired"}`)
				return
			}
			io.WriteString(w, `{"access_token":"at_1","refresh_token":"rt_1","token_type":"bearer","expires_in":3600}`)
		case "/oauth/userinfo":
			io.WriteString(w, `{"sub":"user_alex","preferred_username":"alexplays","name":"Alex"}`)
		case "/oauth/revoke":
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer srv.Close()
	o := OAuth{URL: srv.URL + "/oauth", ClientID: "app_pip", ClientSecret: "sec_1", RedirectURI: "https://beta.playkeeper.me:8443/cb"}
	ctx := context.Background()
	tok, err := o.Exchange(ctx, "code_1", "verifier_1")
	if err != nil || tok.AccessToken != "at_1" || tok.RefreshToken != "rt_1" {
		t.Fatalf("exchange: %+v, %v", tok, err)
	}
	u, err := o.UserInfo(ctx, tok.AccessToken)
	if err != nil || u.Subject != "user_alex" || u.Username != "alexplays" {
		t.Fatalf("userinfo: %+v, %v", u, err)
	}
	if err := o.Revoke(ctx, tok.RefreshToken); err != nil {
		t.Fatal(err)
	}
	want := []string{
		`POST /oauth/token  {"client_id":"app_pip","client_secret":"sec_1","code":"code_1","code_verifier":"verifier_1","grant_type":"authorization_code","redirect_uri":"https://beta.playkeeper.me:8443/cb"}`,
		`GET /oauth/userinfo Bearer at_1 `,
		`POST /oauth/revoke  {"client_id":"app_pip","client_secret":"sec_1","token":"rt_1"}`,
	}
	if strings.Join(seen, "\n") != strings.Join(want, "\n") {
		t.Fatalf("requests:\n%s", strings.Join(seen, "\n"))
	}
	_, err = o.Exchange(ctx, "expired", "verifier_1")
	var oe *OAuthError
	if !errors.As(err, &oe) || oe.Code != "invalid_grant" || oe.Description != "Authorization code has expired" {
		t.Fatalf("an expired code: %v", err)
	}
}
