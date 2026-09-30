package hetzner

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
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

// serversPage is a page of GET /servers as Hetzner answers it, cut to the
// fields that matter.
func serversPage(next string, servers ...string) string {
	return `{"servers":[` + strings.Join(servers, ",") + `],"meta":{"pagination":{"page":1,"per_page":25,"next_page":` + next + `}}}`
}

func TestServersReadsEveryPageAndEachServersAddresses(t *testing.T) {
	var asked []string
	c := fakeAPI(t, func(w http.ResponseWriter, r *http.Request) {
		asked = append(asked, r.URL.Path+"?"+r.URL.RawQuery)
		switch r.URL.Query().Get("page") {
		case "1":
			io.WriteString(w, serversPage("2",
				`{"id":42,"name":"fleet-1","public_net":{"ipv4":{"ip":"203.0.113.7"},"ipv6":{"ip":"2a01:4f8:c17:1234::/64"}}}`,
				`{"id":43,"name":"no-ipv4","public_net":{"ipv4":null,"ipv6":{"ip":"2a01:4f8:c17:5678::/64"}}}`))
		default:
			io.WriteString(w, serversPage("null", `{"id":44,"name":"fleet-2","public_net":{"ipv4":{"ip":"198.51.100.9"},"ipv6":null}}`))
		}
	})
	servers, err := c.Servers(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(asked) != 2 || asked[0] != "/servers?page=1&per_page=25" || asked[1] != "/servers?page=2&per_page=25" {
		t.Fatalf("asked %q", asked)
	}
	if len(servers) != 3 || servers[0].Name != "fleet-1" || servers[2].ID != 44 {
		t.Fatalf("servers: %+v", servers)
	}
	for _, c := range []struct {
		server int
		addr   string
		has    bool
	}{
		{0, "203.0.113.7", true},
		{0, "::ffff:203.0.113.7", true},
		{0, "2a01:4f8:c17:1234::1", true},
		{0, "2a01:4f8:c17:1235::1", false},
		{0, "203.0.113.8", false},
		{1, "2a01:4f8:c17:5678:abcd::2", true},
		{1, "0.0.0.0", false},
		{2, "198.51.100.9", true},
		{2, "2a01:4f8:c17:1234::1", false},
	} {
		if got := servers[c.server].Has(netip.MustParseAddr(c.addr)); got != c.has {
			t.Errorf("%s has %s: %v, want %v", servers[c.server].Name, c.addr, got, c.has)
		}
	}
}

func TestServersStopsAtAPageThatGoesBackOrTooFar(t *testing.T) {
	next := "1"
	c := fakeAPI(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, serversPage(next, `{"id":42,"name":"fleet-1","public_net":{}}`))
	})
	if servers, err := c.Servers(context.Background()); err != nil || len(servers) != 1 {
		t.Fatalf("a next page that isn't after this one: %v, %v", servers, err)
	}
	next = "999"
	if _, err := c.Servers(context.Background()); err == nil {
		t.Fatal("a project with more pages than Playkeeper reads was read")
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
