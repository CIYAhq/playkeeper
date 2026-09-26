package templates

import (
	"bytes"
	"encoding/base64"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

func TestLinkRoundTrip(t *testing.T) {
	for _, name := range []string{"paper-server.json", "fabric-modpack.json"} {
		t.Run(name, func(t *testing.T) {
			tp := fixture(t, name)
			l := mustLink(t, tp)
			if l.URL != ShareURL+"#"+l.Payload || l.Warning != nil {
				t.Errorf("got %+v, want the share page's address with the data after #, short enough for chat", l)
			}
			var wrapped strings.Builder
			for i, r := range l.URL {
				if i > 0 && i%60 == 0 {
					wrapped.WriteString("\r\n")
				}
				wrapped.WriteRune(r)
			}
			for _, in := range []string{
				l.URL,
				l.Payload,
				"https://panel.example.com:8443/servers/new#template=" + l.Payload,
				"  " + l.URL + "\n",
				"<" + l.URL + ">",
				"(" + l.URL + ").",
				wrapped.String(),
			} {
				got, err := DecodeLink(in)
				if err != nil {
					t.Fatalf("DecodeLink(%.60q…): %v", in, err)
				}
				sameTemplate(t, got, tp)
				if got, err = Decode([]byte(in)); err != nil {
					t.Fatalf("Decode(%.60q…): %v", in, err)
				}
				sameTemplate(t, got, tp)
			}
		})
	}
}

// The site check opens the testdata/share-link*.txt links on the share page
// in a browser: the Paper fixture's link, the same with markup in its name,
// the same with the day it was made and an author filled in by hand, which
// the page never shows, and one holding the fixture padded with spaces past
// what a template can be. Decoding them keeps them in step with this
// package; run the tests with -update to write them again.
func TestShareLinkFixtures(t *testing.T) {
	paper := fixture(t, "paper-server.json")
	markup := fixture(t, "paper-server.json")
	markup.Name = "<b>Survival</b> & <i>friends</i>"
	signed := fixture(t, "paper-server.json")
	signed.Author, signed.Created = "siya", "2026-09-25"
	js, err := canonicalJSON(paper)
	if err != nil {
		t.Fatal(err)
	}
	padded := append(js, bytes.Repeat([]byte(" "), MaxFileSize)...)
	for _, c := range []struct {
		file string
		link string
		want *Template
	}{
		{"share-link.txt", mustLink(t, paper).URL, paper},
		{"share-link-markup.txt", mustLink(t, markup).URL, markup},
		{"share-link-author.txt", mustLink(t, signed).URL, signed},
		{"share-link-oversized.txt", ShareURL + "#" + craft(linkVersion, deflate(t, padded)), nil},
	} {
		if *update {
			if err := os.WriteFile(filepath.Join("testdata", c.file), []byte(c.link+"\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		got, err := DecodeLink(string(readFile(t, c.file)))
		switch {
		case c.want == nil:
			refused(t, err, KindTooLarge)
		case err != nil:
			t.Errorf("testdata/%s: %v (run go test -update to write it again)", c.file, err)
		default:
			sameTemplate(t, got, c.want)
		}
	}
}

func TestLinkCutOff(t *testing.T) {
	l := mustLink(t, fixture(t, "paper-server.json"))
	for n := range len(l.Payload) {
		_, err := DecodeLink(ShareURL + "#" + l.Payload[:n])
		if kindOf(err) != KindLinkIncomplete {
			t.Fatalf("cut after %d of %d characters: got %v, want %s", n, len(l.Payload), err, KindLinkIncomplete)
		}
	}
	refused(t, second(DecodeLink(ShareURL)), KindLinkIncomplete)
	refused(t, second(DecodeLink("https://panel.example.com:8443/servers/new#template=")), KindLinkIncomplete)
}

func TestLinkTampered(t *testing.T) {
	l := mustLink(t, fixture(t, "paper-server.json"))
	raw, err := base64.RawURLEncoding.DecodeString(l.Payload)
	if err != nil {
		t.Fatal(err)
	}
	for i := range raw {
		for bit := range 8 {
			b := bytes.Clone(raw)
			b[i] ^= 1 << bit
			_, err := DecodeLink(base64.RawURLEncoding.EncodeToString(b))
			switch k := kindOf(err); {
			case err == nil:
				t.Fatalf("byte %d bit %d: the altered link was read", i, bit)
			case i == 0 && (k == KindLinkDamaged || k == KindNewer || k == KindNotTemplate):
			case i > 0 && (k == KindLinkDamaged || k == KindLinkIncomplete):
			default:
				t.Fatalf("byte %d bit %d: got %v", i, bit, err)
			}
		}
	}
	mid := len(l.Payload) / 2
	for _, c := range []string{"!", "+", "/", "=", "%", "."} {
		refused(t, second(DecodeLink(l.Payload[:mid]+c+l.Payload[mid+1:])), KindNotTemplate)
	}
}

func TestCraftedLinks(t *testing.T) {
	tp := fixture(t, "paper-server.json")
	js, err := canonicalJSON(tp)
	if err != nil {
		t.Fatal(err)
	}
	with := func(old, new string) []byte {
		t.Helper()
		if !bytes.Contains(js, []byte(old)) {
			t.Fatalf("the template has no %q", old)
		}
		return deflate(t, bytes.Replace(js, []byte(old), []byte(new), 1))
	}
	body := deflate(t, js)
	cases := []struct {
		name    string
		payload string
		kind    Kind
		field   string
		problem string
	}{
		{name: "as Playkeeper writes it", payload: craft(1, body)},
		{name: "a newer link format", payload: craft(2, body), kind: KindNewer},
		{name: "format 0", payload: craft(0, body), kind: KindLinkDamaged},
		{name: "a newer template format", payload: craft(1, with(`"playkeeperTemplate":1`, `"playkeeperTemplate":2`)), kind: KindNewer},
		{name: "unknown field", payload: craft(1, with(`"game":`, `"owner":"siya","game":`)), kind: KindUnknownField, field: "owner"},
		{name: "forbidden field", payload: craft(1, with(`"settings":{`, `"settings":{"onlineMode":false,`)), kind: KindForbiddenField, field: "settings.onlineMode"},
		{name: "invalid value", payload: craft(1, with(`"viewDistance":10`, `"viewDistance":99`)), kind: KindInvalid, field: "settings.viewDistance", problem: "value"},
		{name: "hash in capitals", payload: craft(1, with("43ffecc6e6a734b7", "43FFECC6E6A734B7")), kind: KindInvalid, field: "addons[0].pin.hash", problem: "hash"},
		{name: "indented like a file", payload: craft(1, deflate(t, readFile(t, "paper-server.json"))), kind: KindInvalid, field: "", problem: "not_canonical"},
		{name: "fields in another order", payload: craft(1, with(`"name":"Survival with friends","description":"`+tp.Description+`"`,
			`"description":"`+tp.Description+`","name":"Survival with friends"`)), kind: KindInvalid, field: "", problem: "not_canonical"},
		{name: "not a template", payload: craft(1, deflate(t, []byte(`{"hello":"world"}`))), kind: KindLinkDamaged},
		{name: "not compressed", payload: craft(1, js), kind: KindLinkDamaged},
		{name: "stream cut short", payload: craft(1, body[:len(body)-8]), kind: KindLinkDamaged},
		{name: "bytes after the stream", payload: craft(1, append(bytes.Clone(body), 0, 0)), kind: KindLinkDamaged},
		{name: "empty stream", payload: craft(1, nil), kind: KindLinkDamaged},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := DecodeLink(c.payload)
			if c.kind == "" {
				if err != nil {
					t.Fatal(err)
				}
				sameTemplate(t, got, tp)
				return
			}
			e := refused(t, err, c.kind)
			if c.field != "" && e.Params["field"] != c.field || c.problem != "" && e.Params["problem"] != c.problem {
				t.Errorf("got field %q problem %q, want %q %q (%s)", e.Params["field"], e.Params["problem"], c.field, c.problem, e.Msg)
			}
		})
	}
}

func TestLinkDecompressionBomb(t *testing.T) {
	bomb := craft(1, deflate(t, make([]byte, 8<<20)))
	if len(bomb) > MaxLinkLength {
		t.Fatalf("the bomb is %d characters, too long to reach the decompressor", len(bomb))
	}
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	_, err := DecodeLink(ShareURL + "#" + bomb)
	runtime.ReadMemStats(&after)
	refused(t, err, KindTooLarge)
	if n := after.TotalAlloc - before.TotalAlloc; n > 2<<20 {
		t.Errorf("reading 8 MB packed into %d characters allocated %d bytes", len(bomb), n)
	}
	for _, c := range []struct {
		size     int
		tooLarge bool
	}{{MaxFileSize, false}, {MaxFileSize + 1, true}} {
		_, err := DecodeLink(craft(1, deflate(t, bytes.Repeat([]byte(" "), c.size))))
		if err == nil || (kindOf(err) == KindTooLarge) != c.tooLarge {
			t.Errorf("%d bytes decompressed: got %v, too large %v", c.size, err, c.tooLarge)
		}
	}
}

func TestNotALink(t *testing.T) {
	for _, in := range []string{"", "   ", "Survival with friends", "https://example.com/page#section", "https://playkeeper.io/",
		"AA+A", "12345", "{}", "#template=zzzz", "<script>alert(1)</script>"} {
		refused(t, second(DecodeLink(in)), KindNotTemplate)
	}
	for _, in := range []string{"", "Survival with friends", "https://example.com/page#section"} {
		if _, err := Decode([]byte(in)); kindOf(err) != KindNotTemplate {
			t.Errorf("Decode(%q): got %v", in, err)
		}
	}
}

func TestLinkLengths(t *testing.T) {
	tp := fixture(t, "paper-server.json")
	tp.Addons = manyAddons(150)
	e := refused(t, second(NewLink(tp)), KindLinkTooLong)
	if n, _ := strconv.Atoi(e.Params["length"]); n <= MaxLinkLength {
		t.Errorf("refused a link of %d characters", n)
	}
	if _, err := MarshalFile(tp); err != nil {
		t.Errorf("a template too long for a link is still a file: %v", err)
	}

	tp.Addons = manyAddons(15)
	l := mustLink(t, tp)
	if l.Warning == nil || l.Warning.Kind != KindLinkLong || l.Warning.Params["length"] != strconv.Itoa(len(l.URL)) || len(l.URL) <= ChatLinkLength {
		t.Errorf("a %d character link: got warning %+v", len(l.URL), l.Warning)
	} else {
		checkNotice(t, *l.Warning)
	}

	refused(t, second(DecodeLink(strings.Repeat("A", 2*MaxLinkLength+1))), KindTooLarge)
	refused(t, second(DecodeLink("A"+strings.Repeat("Q", MaxLinkLength))), KindTooLarge)
}

func second[T any](_ T, err error) error { return err }
