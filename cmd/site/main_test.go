package main

import (
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/CIYAhq/playkeeper/internal/site"
)

// -serve sends the Content-Security-Policy nginx sends, /start's on /start,
// so a page that breaks the policy breaks in a local browser too.
func TestServeSendsThePolicy(t *testing.T) {
	o, err := site.Build(site.Options{Root: os.DirFS("../.."), Settings: site.Default, Now: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	h := handler(o)
	for path, want := range map[string]string{"/": o.Policy, "/tools/server-icon": o.Policy, "/no-such-page": o.Policy, "/start": o.StartPolicy} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if got := w.Header().Get("Content-Security-Policy"); got != want || !strings.Contains(got, "default-src 'none'") {
			t.Errorf("%s: Content-Security-Policy %q, want %q", path, got, want)
		}
	}
	if o.Policy == o.StartPolicy {
		t.Error("/start's policy is the site's; it lets in Whop's pixel as well")
	}
}
