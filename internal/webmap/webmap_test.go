package webmap

import (
	"errors"
	"net/http"
	"net/http/cookiejar"
	"slices"
	"strconv"
	"strings"
	"testing"
)

func TestLayoutForEachServerType(t *testing.T) {
	for _, tc := range []struct{ typ, folder, dir string }{
		{"paper", "plugins", "plugins/squaremap"},
		{"purpur", "plugins", "plugins/squaremap"},
		{"fabric", "mods", "squaremap"},
		{"quilt", "mods", "squaremap"},
		{"neoforge", "mods", "squaremap"},
	} {
		got, err := LayoutFor(tc.typ)
		want := Layout{Type: tc.typ, Source: "modrinth", ProjectID: ModrinthProjectID, Folder: tc.folder, Dir: tc.dir}
		if err != nil || got != want {
			t.Errorf("%s: got %+v, %v; want %+v", tc.typ, got, err, want)
		}
	}
}

func TestLayoutForRefusesVanillaAndUnknownTypes(t *testing.T) {
	for _, tc := range []struct {
		typ, param string
		kind       Kind
	}{
		{"vanilla", "vanilla", KindUnsupported},
		{"forge", "forge", KindUnsupported},
		{"spigot", "spigot", KindUnknownType},
		{"", "", KindUnknownType},
		{"forge\n" + strings.Repeat("x", 60), "forge?" + strings.Repeat("x", 34) + "…", KindUnknownType},
	} {
		_, err := LayoutFor(tc.typ)
		var e *Error
		if !errors.As(err, &e) || e.Kind != tc.kind || e.Params["type"] != tc.param {
			t.Errorf("%q: got %#v", tc.typ, err)
			continue
		}
		if e.Hint == "" || !strings.HasSuffix(e.Msg, ".") || strings.ContainsAny(e.Msg, "\n\r") {
			t.Errorf("%q: message %q, hint %q", tc.typ, e.Msg, e.Hint)
		}
	}
}

func TestIdentifiersMatchModrinthAndHangar(t *testing.T) {
	var modrinth struct {
		ID, Slug string
		License  struct{ ID string }
		Loaders  []string
	}
	decodeTestdata(t, "modrinth-project-squaremap.json", &modrinth)
	if modrinth.ID != ModrinthProjectID || modrinth.Slug != ModrinthSlug || modrinth.License.ID != "MIT" {
		t.Errorf("Modrinth lists %+v", modrinth)
	}
	for _, loader := range []string{"paper", "fabric", "neoforge"} {
		if !slices.Contains(modrinth.Loaders, loader) {
			t.Errorf("Modrinth has no %s build: %v", loader, modrinth.Loaders)
		}
	}
	var hangar struct {
		ID                 int
		Namespace          struct{ Owner, Slug string }
		Settings           struct{ License struct{ Type string } }
		SupportedPlatforms map[string][]string
	}
	decodeTestdata(t, "hangar-project-squaremap.json", &hangar)
	if strconv.Itoa(hangar.ID) != HangarProjectID || hangar.Namespace.Owner+"/"+hangar.Namespace.Slug != HangarNamespace ||
		hangar.Settings.License.Type != "MIT" || len(hangar.SupportedPlatforms["PAPER"]) == 0 {
		t.Errorf("Hangar lists %+v", hangar)
	}
}

func TestNewClientGoesStraightToSquaremap(t *testing.T) {
	tr, ok := NewClient().Transport.(*http.Transport)
	if !ok || tr.Proxy != nil || tr.DialContext == nil || tr.ResponseHeaderTimeout <= 0 ||
		tr.MaxConnsPerHost != MaxConnsPerServer || tr.MaxIdleConnsPerHost > tr.MaxConnsPerHost {
		t.Fatalf("transport %#v", NewClient().Transport)
	}
}

func TestMapClientNeverFollowsRedirectsOrKeepsCookies(t *testing.T) {
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	shared := &http.Client{Jar: jar}
	for _, m := range []Map{{Client: shared}, {}} {
		c := m.client()
		if c.Jar != nil || c.CheckRedirect == nil || !errors.Is(c.CheckRedirect(nil, nil), http.ErrUseLastResponse) {
			t.Errorf("client %#v", c)
		}
	}
	if shared.Jar == nil || shared.CheckRedirect != nil {
		t.Error("the shared client was changed")
	}
	if tr := (Map{}).client().Transport.(*http.Transport); tr.Proxy != nil || !tr.DisableKeepAlives {
		t.Errorf("one-off transport %#v", tr)
	}
}

func TestMapAddrTakesOnlyAnIPAddressAndPort(t *testing.T) {
	for addr, want := range map[string]string{
		"172.18.0.5:25580":        "172.18.0.5:25580",
		"[fd00::5]:25580":         "[fd00::5]:25580",
		"":                        "",
		"localhost:25580":         "",
		"squaremap:25580":         "",
		"0.0.0.0:25580":           "",
		"[::]:25580":              "",
		"172.18.0.5":              "",
		"172.18.0.5:0":            "",
		"172.18.0.5:25580/tiles":  "",
		"http://172.18.0.5:25580": "",
	} {
		got, err := Map{Addr: addr}.addr()
		if got != want || (err == nil) != (want != "") {
			t.Errorf("%q: got %q, %v", addr, got, err)
		}
	}
}
