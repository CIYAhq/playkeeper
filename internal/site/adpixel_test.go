package site

import (
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// adPixels name the ad pixels and trackers a page could load: their hosts,
// and the snippets that load them.
var adPixels = []string{
	"whop.tw", "data-whop-pixel", // Whop's
	"connect.facebook.net", "facebook.com/tr", "fbq(", // Meta's
	"googletagmanager.com", "google-analytics.com", "googleadservices.com", "doubleclick.net", // Google's
	"analytics.tiktok.com",                      // TikTok's
	"static.ads-twitter.com", "ads-twitter.com", // X's
	"snap.licdn.com", "px.ads.linkedin.com", // LinkedIn's
	"sc-static.net", "tr.snapchat.com", // Snapchat's
	"redditstatic.com/ads", "alb.reddit.com", // Reddit's
	"bat.bing.com", "clarity.ms", // Microsoft's
	"ct.pinterest.com", "s.pinimg.com/ct", // Pinterest's
	"hotjar.com", "fullstory.com", "mouseflow.com", // session recorders
}

// The site loads no ad pixel or other tracker, and sets no cookie: either
// would need visitors' consent first, and the site asks for none. Its one
// count of visits keeps no cookie (Settings.Analytics). So every policy
// nginx sends lets in no more than the site itself, that counter, GitHub's
// API for the star count and the stats service, and no page, script or
// stylesheet the site serves, nor nginx.conf, names an ad pixel or a cookie.
func TestNoPageLoadsAnAdPixel(t *testing.T) {
	o := build(t, Default)
	allowed := map[string][]string{
		"default-src":     {"'none'"},
		"script-src":      {"'self'", "https://analytics-c.ciya.so"},
		"style-src":       {"'self'"},
		"img-src":         {"'self'"},
		"media-src":       {"'self'"},
		"connect-src":     {"'self'", "https://api.github.com", "https://analytics-c.ciya.so", "https://stats.playkeeper.io"},
		"base-uri":        {"'none'"},
		"form-action":     {"'none'"},
		"frame-ancestors": {"'none'"},
	}
	policies := []string{o.Policy, o.FilmPolicy}
	for _, m := range regexp.MustCompile(`set \$csp "([^"]*)";`).FindAllStringSubmatch(string(o.Nginx), -1) {
		policies = append(policies, m[1])
	}
	for _, p := range policies {
		for _, d := range strings.Split(p, ";") {
			f := strings.Fields(d)
			if len(f) == 0 {
				continue
			}
			want, ok := allowed[f[0]]
			if !ok {
				t.Errorf("a policy has %s, which lets in more than the site's own: %s", f[0], p)
				continue
			}
			for _, src := range f[1:] {
				if !slices.Contains(want, src) {
					t.Errorf("a policy's %s lets in %s: %s", f[0], src, p)
				}
			}
		}
	}
	conf, err := os.ReadFile("../../site/nginx.conf")
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]string{"site/nginx.conf": string(conf), "nginx's include": string(o.Nginx)}
	for name, b := range o.Files {
		if strings.HasSuffix(name, ".html") || strings.HasSuffix(name, ".js") || strings.HasSuffix(name, ".css") {
			files[name] = string(b)
		}
	}
	for name, s := range files {
		for _, sign := range adPixels {
			if strings.Contains(s, sign) {
				t.Errorf("%s names %s, an ad pixel's", name, sign)
			}
		}
		for _, cookie := range []string{"document.cookie", "Set-Cookie", "userid on"} {
			if strings.Contains(s, cookie) {
				t.Errorf("%s has %s: the site sets no cookie", name, cookie)
			}
		}
	}
}
