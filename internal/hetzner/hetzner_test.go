package hetzner

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const testToken = "Hz0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXyz"

// cx53Answer is GET /server_types?name=cx53 as Hetzner answers it, cut to
// the fields that matter: in stock in Falkenstein, not in Nuremberg or
// Helsinki, with a location whose name isn't one and a repeat to drop.
const cx53Answer = `{"server_types":[{"id":117,"name":"cx53","description":"CX53","cores":16,"memory":32.0,"disk":320,
"cpu_type":"shared","architecture":"x86","category":"cost_optimized","deprecation":null,
"locations":[{"id":1,"name":"fsn1","available":true,"recommended":true,"deprecation":null},
{"id":2,"name":"nbg1","available":false,"recommended":false,"deprecation":null},
{"id":3,"name":"hel1","available":false,"recommended":false,"deprecation":null},
{"id":9,"name":"Bad Name!","available":true},{"id":1,"name":"fsn1","available":false}]}],
"meta":{"pagination":{"page":1,"per_page":25,"previous_page":null,"next_page":null,"last_page":1,"total_entries":1}}}`

func fakeAPI(t *testing.T, h http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return &Client{APIURL: srv.URL, Token: testToken, UserAgent: "Playkeeper/test"}
}

func TestStockReadsEachLocation(t *testing.T) {
	var got *http.Request
	c := fakeAPI(t, func(w http.ResponseWriter, r *http.Request) {
		got = r
		io.WriteString(w, cx53Answer)
	})
	s, err := c.Stock(context.Background(), "cx53")
	if err != nil {
		t.Fatal(err)
	}
	if got.Method != http.MethodGet || got.URL.Path != "/server_types" || got.URL.Query().Get("name") != "cx53" {
		t.Fatalf("asked %s %s", got.Method, got.URL)
	}
	if got.Header.Get("Authorization") != "Bearer "+testToken || got.Header.Get("User-Agent") != "Playkeeper/test" {
		t.Fatalf("headers: %v", got.Header)
	}
	want := []Location{{"fsn1", true}, {"nbg1", false}, {"hel1", false}}
	if s.Type != "cx53" || s.Description != "CX53" || s.Cores != 16 || s.MemoryGB != 32 || s.DiskGB != 320 || len(s.Locations) != len(want) {
		t.Fatalf("stock: %+v", s)
	}
	for i, l := range want {
		if s.Locations[i] != l {
			t.Fatalf("location %d: %+v, want %+v", i, s.Locations[i], l)
		}
	}
}

func TestATypeHetznerDoesntSellIsNamed(t *testing.T) {
	c := fakeAPI(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"server_types":[],"meta":{}}`)
	})
	if _, err := c.Stock(context.Background(), "cx99"); !errors.Is(err, ErrNoSuchType) {
		t.Fatalf("err = %v", err)
	}
	if _, err := c.Stock(context.Background(), "cx53&name=cpx11"); !errors.Is(err, ErrNoSuchType) {
		t.Fatalf("a name that isn't one: err = %v", err)
	}
}

func TestRefusalsSayWhatHetznerSaid(t *testing.T) {
	status, body, reset := 0, "", ""
	c := fakeAPI(t, func(w http.ResponseWriter, r *http.Request) {
		if reset != "" {
			w.Header().Set("RateLimit-Reset", reset)
		}
		w.WriteHeader(status)
		io.WriteString(w, body)
	})

	status, body = 401, `{"error":{"code":"unauthorized","message":"unable to authenticate"}}`
	_, err := c.Stock(context.Background(), "cx53")
	var e *Error
	if !TokenRefused(err) || !errors.As(err, &e) || e.Code != "unauthorized" || e.Message != "unable to authenticate" {
		t.Fatalf("401: %v", err)
	}

	status, body, reset = 429, `{"error":{"code":"rate_limit_exceeded","message":"limit reached"}}`, "1790000000"
	_, err = c.Stock(context.Background(), "cx53")
	if limited, until := RateLimited(err); !limited || !until.Equal(time.Unix(1790000000, 0)) || TokenRefused(err) {
		t.Fatalf("429: %v %v %v", err, limited, until)
	}

	status, body, reset = 503, `not json`, ""
	_, err = c.Stock(context.Background(), "cx53")
	if !errors.As(err, &e) || e.Status != 503 || e.Message != "" || TokenRefused(err) {
		t.Fatalf("503: %v", err)
	}
	if limited, _ := RateLimited(err); limited {
		t.Fatal("a 503 isn't a rate limit")
	}
}

func TestTheTokenNeverShowsInErrors(t *testing.T) {
	c := &Client{APIURL: "http://127.0.0.1:1/" + testToken, Token: testToken}
	_, err := c.Stock(context.Background(), "cx53")
	if err == nil || strings.Contains(err.Error(), testToken) {
		t.Fatalf("err = %v", err)
	}
}

func TestATooLargeAnswerIsRefused(t *testing.T) {
	c := fakeAPI(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"server_types":[{"name":"cx53","description":"`+strings.Repeat("x", maxResponse)+`"}]}`)
	})
	if _, err := c.Stock(context.Background(), "cx53"); err == nil || !strings.Contains(err.Error(), "too large") {
		t.Fatalf("err = %v", err)
	}
}

func TestRedirectsAreNotFollowed(t *testing.T) {
	elsewhere := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("followed a redirect with %q", r.Header.Get("Authorization"))
	}))
	defer elsewhere.Close()
	c := fakeAPI(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, elsewhere.URL+"/server_types", http.StatusFound)
	})
	if _, err := c.Stock(context.Background(), "cx53"); err == nil {
		t.Fatal("a redirect read as stock")
	}
}

func TestTokensNamesAndLinks(t *testing.T) {
	if !ValidToken(testToken) || ValidToken(testToken[1:]) || ValidToken(testToken+"a") || ValidToken(strings.Replace(testToken, "a", "-", 1)) {
		t.Fatal("ValidToken")
	}
	if Ending(testToken) != "WXyz" || Ending("abc") != "" {
		t.Fatal("Ending")
	}
	if City("fsn1") != "Falkenstein" || City("nbg1") != "Nuremberg" || City("hel1") != "Helsinki" || City("xyz9") != "XYZ9" {
		t.Fatal("City")
	}
	if got := BuyURL("cx53", "fsn1"); got != "https://console.hetzner.com/create/server?location=fsn1&type=cx53&useIPv4=true" {
		t.Fatalf("BuyURL = %s", got)
	}
	for _, bad := range [][2]string{{"cx53", "fsn1&type=ccx63"}, {"CX53", "fsn1"}, {"", "fsn1"}, {"cx53", ""}} {
		if got := BuyURL(bad[0], bad[1]); got != "" {
			t.Fatalf("BuyURL(%q, %q) = %s", bad[0], bad[1], got)
		}
	}
}

func TestCheckAPIURL(t *testing.T) {
	for raw, want := range map[string]string{
		"":                               DefaultAPIURL,
		"https://api.hetzner.cloud/v1/":  "https://api.hetzner.cloud/v1",
		"http://127.0.0.1:8080":          "http://127.0.0.1:8080",
		"http://localhost:9/v1":          "http://localhost:9/v1",
		"http://api.hetzner.cloud/v1":    "",
		"https://user@api.hetzner.cloud": "",
		"https://api.hetzner.cloud/?a=b": "",
	} {
		got, err := CheckAPIURL(raw)
		if (err == nil) != (want != "") || got != want {
			t.Errorf("CheckAPIURL(%q) = %q, %v", raw, got, err)
		}
	}
}
