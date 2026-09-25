package packs

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"
)

const testSum = "0123456789abcdef0123456789abcdef01234567"

func TestStore(t *testing.T) {
	ctx := context.Background()
	s := Store{Dir: filepath.Join(t.TempDir(), "resourcepacks")}
	pack := resourcePack(t)
	info, err := s.Put(ctx, bytes.NewReader(pack), int64(len(pack)))
	if err != nil {
		t.Fatal(err)
	}
	if want, err := inspect(t, pack, Resource, Limits{}); err != nil || !reflect.DeepEqual(info, want) {
		t.Errorf("Put = %+v, want %+v (%v)", info, want, err)
	}
	name := filepath.Join(s.Dir, info.SHA1+".zip")
	if got, err := os.ReadFile(name); err != nil || !bytes.Equal(got, pack) {
		t.Fatalf("stored %d bytes, %v", len(got), err)
	}
	first, err := os.Stat(name)
	if err != nil {
		t.Fatal(err)
	}
	if first.Mode().Perm() != 0o644 {
		t.Errorf("stored pack has mode %v, want 0644", first.Mode())
	}

	again, err := s.Put(ctx, bytes.NewReader(pack), int64(len(pack)))
	if err != nil || !reflect.DeepEqual(again, info) {
		t.Errorf("storing the pack again = %+v, %v", again, err)
	}
	if st, err := os.Stat(name); err != nil || !os.SameFile(st, first) {
		t.Error("storing the pack again rewrote it")
	}

	both := buildZip(t,
		file("pack.mcmeta", resourceMcmeta),
		file("assets/minecraft/lang/en_us.json", "{}"),
		file("data/test/function/hello.mcfunction", "say hello\n"),
	)
	bothInfo, err := s.Put(ctx, bytes.NewReader(both), int64(len(both)))
	if err != nil || bothInfo.Kind != Both {
		t.Errorf("storing a pack with data too = %+v, %v", bothInfo, err)
	}

	data := dataPack(t)
	_, err = s.Put(ctx, bytes.NewReader(data), int64(len(data)))
	wantCode(t, err, CodeWrongKind)
	_, err = Store{Dir: s.Dir, Limits: Limits{MaxBytes: 100}}.Put(ctx, bytes.NewReader(pack), int64(len(pack)))
	wantCode(t, err, CodeTooLarge)

	other := resourcePack(t, file("assets/minecraft/lang/en_us.json", "{}"))
	changing := &changingReaderAt{b: bytes.Clone(other), change: readsToInspect(t, other, Resource)}
	_, err = s.Put(ctx, changing, int64(len(other)))
	if e := wantCode(t, err, CodeFileFailed); e.Params["detail"] != "the pack changed while it was being copied" {
		t.Errorf("a pack changing while stored: %v", e.Params)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := s.Put(canceled, bytes.NewReader(other), int64(len(other))); !errors.Is(err, context.Canceled) {
		t.Errorf("Put with a canceled context = %v", err)
	}
	stored := []string{info.SHA1 + ".zip", bothInfo.SHA1 + ".zip"}
	slices.Sort(stored)
	if names := dirNames(t, s.Dir); !slices.Equal(names, stored) {
		t.Errorf("the store holds %q, want %q", names, stored)
	}

	if err := os.WriteFile(name, pack[:10], 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Put(ctx, bytes.NewReader(pack), int64(len(pack))); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(name); err != nil || !bytes.Equal(got, pack) {
		t.Errorf("Put kept a damaged copy of the pack: %d bytes, %v", len(got), err)
	}

	if err := s.Remove(info.SHA1); err != nil {
		t.Fatal(err)
	}
	if err := s.Remove(info.SHA1); err != nil {
		t.Errorf("removing a removed pack = %v", err)
	}
	if names := dirNames(t, s.Dir); !slices.Equal(names, []string{bothInfo.SHA1 + ".zip"}) {
		t.Errorf("after Remove the store holds %q", names)
	}
	for _, bad := range []string{"", "../../etc/passwd", strings.ToUpper(testSum), testSum + ".zip", testSum[:39]} {
		if e := wantCode(t, s.Remove(bad), CodeInvalidOffer); e.Params["field"] != "sha1" {
			t.Errorf("Remove(%q) has params %v", bad, e.Params)
		}
	}
	if err := (Store{Dir: filepath.Join(t.TempDir(), "missing")}).Remove(testSum); err != nil {
		t.Errorf("removing from a missing store = %v", err)
	}

	blocker := filepath.Join(t.TempDir(), "file")
	writeFile(t, blocker, "x")
	_, err = Store{Dir: filepath.Join(blocker, "packs")}.Put(ctx, bytes.NewReader(pack), int64(len(pack)))
	wantCode(t, err, CodeFileFailed)
}

func TestOfferSettings(t *testing.T) {
	url := "https://mc.example.com:8443/packs/" + testSum + ".zip"
	got, err := Offer{URL: url, SHA1: testSum, Required: true, Prompt: `Accept "our" pack <3 & enjoy — żółw`}.Settings()
	if err != nil {
		t.Fatal(err)
	}
	want := []Setting{
		{"resource-pack", "RESOURCE_PACK", url},
		{"resource-pack-sha1", "RESOURCE_PACK_SHA1", testSum},
		{"require-resource-pack", "RESOURCE_PACK_ENFORCE", "true"},
		{"resource-pack-prompt", "RESOURCE_PACK_PROMPT", `"Accept \"our\" pack <3 & enjoy — żółw"`},
		{"resource-pack-id", "RESOURCE_PACK_ID", "a048344f-fb08-5c9e-b6c8-d4b9c0e25aa8"},
	}
	if !slices.Equal(got, want) {
		t.Errorf("Settings =\n%q\nwant\n%q", got, want)
	}
	var prompt string
	if err := json.Unmarshal([]byte(got[3].Value), &prompt); err != nil || prompt != `Accept "our" pack <3 & enjoy — żółw` {
		t.Errorf("the prompt reads back as %q, %v", prompt, err)
	}

	got, err = Offer{URL: "http://203.0.113.7:8443/packs/" + testSum + ".zip", SHA1: testSum}.Settings()
	if err != nil {
		t.Fatal(err)
	}
	if got[2].Value != "false" || got[3].Value != "" {
		t.Errorf("an optional pack without a prompt = %q", got)
	}
	if b, err := json.Marshal(got[0]); err != nil || string(b) != `{"property":"resource-pack","env":"RESOURCE_PACK","value":"http://203.0.113.7:8443/packs/`+testSum+`.zip"}` {
		t.Errorf("Setting JSON = %s, %v", b, err)
	}

	cleared := ClearSettings()
	if len(cleared) != len(want) {
		t.Fatalf("ClearSettings = %q", cleared)
	}
	for i, s := range cleared {
		if s.Property != want[i].Property || s.Env != want[i].Env || s.Value != "" {
			t.Errorf("ClearSettings()[%d] = %q, want %s and %s set to empty", i, s, want[i].Property, want[i].Env)
		}
	}
}

func TestOfferRefusals(t *testing.T) {
	good := "https://mc.example.com/packs/" + testSum + ".zip"
	long := "https://mc.example.com/" + strings.Repeat("a", maxURLBytes-len("https://mc.example.com/"))
	if _, err := (Offer{URL: long, SHA1: testSum}).Settings(); err != nil {
		t.Errorf("a URL of %d bytes: %v", len(long), err)
	}
	for _, url := range []string{
		"", "/packs/" + testSum + ".zip", "mc.example.com/pack.zip", "ftp://mc.example.com/pack.zip",
		"javascript:alert(1)", "https:mc.example.com/pack.zip", "https:///pack.zip",
		"https://user:secret@mc.example.com/pack.zip",
		"https://mc.example.com/%RCON_PASSWORD%.zip", "https://mc.example.com/%env:CF_API_KEY%.zip",
		"https://mc.example.com/a%20b.zip", `https://mc.example.com/a\u0041.zip`,
		"https://mc.example.com/a b.zip", `https://mc.example.com/"pack".zip`, "https://mc.example.com/<pack>.zip",
		"https://mc.example.com/`pack`.zip", "https://mc.example.com/pack.zip\n", "https://mc.example.com/\x7f.zip",
		"https://mc.example.com/żółw.zip", long + "a",
	} {
		_, err := Offer{URL: url, SHA1: testSum}.Settings()
		e := wantCode(t, err, CodeInvalidOffer)
		if e.Params["field"] != "url" || e.Hint == "" {
			t.Errorf("URL %q: %v %q", url, e.Params, e.Hint)
		}
	}
	_, err := Offer{URL: "ftp://x.example/p.zip", SHA1: testSum}.Settings()
	if e := wantCode(t, err, CodeInvalidOffer); e.Msg != `"ftp://x.example/p.zip" isn't a resource pack URL a server can offer.` {
		t.Errorf("message %q", e.Msg)
	}

	for _, sum := range []string{"", strings.ToUpper(testSum), testSum[:39], testSum + "0", "g" + testSum[1:], " " + testSum[1:]} {
		_, err := Offer{URL: good, SHA1: sum}.Settings()
		if e := wantCode(t, err, CodeInvalidOffer); e.Params["field"] != "sha1" {
			t.Errorf("SHA-1 %q: %v", sum, e.Params)
		}
	}
	_, err = Offer{URL: good, SHA1: "abc"}.Settings()
	if e := wantCode(t, err, CodeInvalidOffer); e.Msg != `"abc" isn't a SHA-1 hash: 40 lowercase hexadecimal characters.` {
		t.Errorf("message %q", e.Msg)
	}

	if _, err := (Offer{URL: good, SHA1: testSum, Prompt: strings.Repeat("ż", maxPromptRunes)}).Settings(); err != nil {
		t.Errorf("a prompt of %d characters: %v", maxPromptRunes, err)
	}
	for _, prompt := range []string{
		strings.Repeat("ż", maxPromptRunes+1), "100% fun", "%RCON_PASSWORD%", `back\slash`, `\u0041`,
		"two\nlines", "tab\there", "a\u2028b", "a\u2029b", "a\u0085b", "bad\xff",
	} {
		_, err := Offer{URL: good, SHA1: testSum, Prompt: prompt}.Settings()
		e := wantCode(t, err, CodeInvalidPrompt)
		if e.Params["max"] != maxPromptRunes {
			t.Errorf("prompt %q: %v", prompt, e.Params)
		}
	}
}

func TestResourcePackID(t *testing.T) {
	// Computed with Python's uuid.uuid5(uuid.uuid5(uuid.NAMESPACE_URL,
	// "https://playkeeper.io/ns/resource-pack"), sum).
	for sum, want := range map[string]string{
		testSum: "a048344f-fb08-5c9e-b6c8-d4b9c0e25aa8",
		"da39a3ee5e6b4b0d3255bfef95601890afd80709": "e7726da9-c9ae-5df9-9964-6391c304f692",
		"ffffffffffffffffffffffffffffffffffffffff": "cc437a51-b720-53b9-a741-bf8855d87afb",
	} {
		if got := ResourcePackID(sum); got != want {
			t.Errorf("ResourcePackID(%s) = %s, want %s", sum, got, want)
		}
	}
	v5 := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-5[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	for _, sum := range []string{testSum, strings.Repeat("0", 40), strings.Repeat("a", 40)} {
		if id := ResourcePackID(sum); !v5.MatchString(id) {
			t.Errorf("ResourcePackID(%s) = %s, not a version 5 UUID", sum, id)
		}
	}
}

func TestPackURL(t *testing.T) {
	path := "/packs/" + testSum + ".zip"
	for _, tc := range []struct {
		origin Origin
		want   string
	}{
		{Origin{Host: "mc.example.com", Port: 8443}, "http://mc.example.com:8443" + path},
		{Origin{Host: "MC.Example.COM.", Port: 8443, HTTPS: true}, "https://mc.example.com:8443" + path},
		{Origin{Host: "mc.example.com", Port: 80}, "http://mc.example.com" + path},
		{Origin{Host: "mc.example.com", Port: 443, HTTPS: true}, "https://mc.example.com" + path},
		{Origin{Host: "mc.example.com", Port: 443}, "http://mc.example.com:443" + path},
		{Origin{Host: "mc.example.com", Port: 80, HTTPS: true}, "https://mc.example.com:80" + path},
		{Origin{Host: "xn--w-uga1v8h.example", Port: 25580}, "http://xn--w-uga1v8h.example:25580" + path},
		{Origin{Host: "203.0.113.7", Port: 8443}, "http://203.0.113.7:8443" + path},
		{Origin{Host: "10.0.0.5", Port: 8443}, "http://10.0.0.5:8443" + path},
		{Origin{Host: "::ffff:203.0.113.7", Port: 8443}, "http://203.0.113.7:8443" + path},
		{Origin{Host: "2001:db8::1", Port: 8443}, "http://[2001:db8::1]:8443" + path},
		{Origin{Host: "[2001:DB8::1]", Port: 443, HTTPS: true}, "https://[2001:db8::1]" + path},
	} {
		got, err := tc.origin.PackURL(testSum)
		if err != nil || got != tc.want {
			t.Errorf("%+v.PackURL = %q, %v, want %q", tc.origin, got, err, tc.want)
			continue
		}
		if _, err := (Offer{URL: got, SHA1: testSum}).Settings(); err != nil {
			t.Errorf("the pack URL %q can't be offered: %v", got, err)
		}
	}

	for host, problem := range map[string]string{
		"localhost": "local", "LOCALHOST": "local", "game.localhost": "local", "127.0.0.1": "local",
		"127.1.2.3": "local", "::1": "local", "[::1]": "local", "::ffff:127.0.0.1": "local",
		"0.0.0.0": "unreachable", "::": "unreachable", "169.254.1.1": "unreachable", "fe80::1": "unreachable",
		"224.0.0.1": "unreachable", "ff02::1": "unreachable", "ff01::1": "unreachable", "255.255.255.255": "unreachable",
		"fe80::1%eth0": "malformed", "": "malformed", ".": "malformed", "exa mple.com": "malformed",
		"mc.example.com:8443": "malformed", "-mc.example.com": "malformed", "mc-.example.com": "malformed",
		"mc_1.example.com": "malformed", "mc..example.com": "malformed", "127.1": "malformed",
		"1.2.3.4.5": "malformed", "0x7f.1": "malformed", "żółw.example": "malformed",
		"https://mc.example.com": "malformed", "mc.example.com/packs": "malformed",
		strings.Repeat("a", 64) + ".com": "malformed", strings.Repeat("a.", 126) + "com": "malformed",
	} {
		_, err := Origin{Host: host, Port: 8443}.PackURL(testSum)
		e := wantCode(t, err, CodeInvalidHost)
		if !reflect.DeepEqual(e.Params, map[string]any{"host": shortName(host), "problem": problem}) || e.Hint == "" {
			t.Errorf("host %q: %v, hint %q, want problem %s", host, e.Params, e.Hint, problem)
		}
	}
	for host, msg := range map[string]string{
		"localhost":    `"localhost" only reaches the machine it is used on, so players' games couldn't download the pack from it.`,
		"0.0.0.0":      `"0.0.0.0" isn't an address players' games can download the pack from.`,
		"exa mple.com": `"exa mple.com" isn't a valid host name or IP address.`,
	} {
		if _, err := (Origin{Host: host, Port: 8443}).PackURL(testSum); err == nil || err.Error() != msg {
			t.Errorf("host %q: %v, want %q", host, err, msg)
		}
	}

	for _, port := range []int{0, -1, 65536} {
		_, err := Origin{Host: "mc.example.com", Port: port}.PackURL(testSum)
		e := wantCode(t, err, CodeInvalidHost)
		if !reflect.DeepEqual(e.Params, map[string]any{"port": port, "problem": "port"}) {
			t.Errorf("port %d: %v", port, e.Params)
		}
	}
	_, err := Origin{Host: "mc.example.com", Port: 8443}.PackURL("../" + testSum)
	if e := wantCode(t, err, CodeInvalidOffer); e.Params["field"] != "sha1" {
		t.Errorf("a bad hash: %v", e.Params)
	}
}
