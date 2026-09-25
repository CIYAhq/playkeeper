package templates

import (
	"bytes"
	"compress/flate"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/CIYAhq/playkeeper/internal/addons"
)

var update = flag.Bool("update", false, "rewrite testdata/share-link.txt from testdata/paper-server.json")

func readFile(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// fixture reads one of the template files in testdata.
func fixture(t *testing.T, name string) *Template {
	t.Helper()
	tp, err := ParseFile(readFile(t, name))
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return tp
}

// installedRows are the addons table's rows of the server the Paper
// fixture was exported from, with the versions, files and hashes Modrinth
// and Hangar publish.
func installedRows(t *testing.T) []addons.Installed {
	t.Helper()
	var rows []addons.Installed
	if err := json.Unmarshal(readFile(t, "installed-paper.json"), &rows); err != nil {
		t.Fatal(err)
	}
	return rows
}

// kindOf is the Kind of a refusal, "" for none.
func kindOf(err error) Kind {
	var e *Error
	switch {
	case err == nil:
		return ""
	case errors.As(err, &e):
		return e.Kind
	}
	return Kind("not a refusal: " + err.Error())
}

// refused checks that err refuses with kind k, in sentences.
func refused(t *testing.T, err error, k Kind) *Error {
	t.Helper()
	var e *Error
	if !errors.As(err, &e) {
		t.Fatalf("got %v, want a refusal of kind %s", err, k)
	}
	if e.Kind != k {
		t.Fatalf("got %s (%q), want %s", e.Kind, e.Msg, k)
	}
	checkNotice(t, e.Notice)
	return e
}

// checkNotice checks that a notice has a kind and reads as sentences.
func checkNotice(t *testing.T, n Notice) {
	t.Helper()
	if n.Kind == "" || !strings.HasSuffix(n.Msg, ".") || n.Hint != "" && !strings.HasSuffix(n.Hint, ".") {
		t.Errorf("notice %+v needs a kind, and a message and hint that are sentences", n)
	}
}

// kinds lists the kinds of notices, checking each on the way.
func kinds(t *testing.T, ns []Notice) []Kind {
	t.Helper()
	out := []Kind{}
	for _, n := range ns {
		checkNotice(t, n)
		out = append(out, n.Kind)
	}
	return out
}

// wantKinds checks the kinds of notices, and reports whether they match,
// so that a test only looks into notices it has.
func wantKinds(t *testing.T, what string, ns []Notice, want ...Kind) bool {
	t.Helper()
	if got := kinds(t, ns); !slices.Equal(got, want) {
		t.Errorf("%s: got %v, want %v", what, got, want)
		return false
	}
	return true
}

// sameTemplate compares templates by their canonical JSON.
func sameTemplate(t *testing.T, got, want *Template) {
	t.Helper()
	g, err := canonicalJSON(got)
	if err != nil {
		t.Fatal(err)
	}
	w, err := canonicalJSON(want)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(g, w) {
		t.Errorf("templates differ:\n got %s\nwant %s", g, w)
	}
}

// roundTrip checks that a valid template comes back unchanged from its file
// and, unless it is too large for one, its link.
func roundTrip(t *testing.T, tp *Template) {
	t.Helper()
	file, err := MarshalFile(tp)
	if err != nil {
		t.Fatalf("MarshalFile: %v", err)
	}
	if len(file) > MaxFileSize {
		t.Fatalf("the file is %d bytes, more than a template can be", len(file))
	}
	back, err := ParseFile(file)
	if err != nil {
		t.Fatalf("ParseFile of what MarshalFile wrote: %v", err)
	}
	sameTemplate(t, back, tp)
	l, err := NewLink(tp)
	if kindOf(err) == KindLinkTooLong {
		return
	}
	if err != nil {
		t.Fatalf("NewLink: %v", err)
	}
	back, err = DecodeLink(l.URL)
	if err != nil {
		t.Fatalf("DecodeLink of NewLink: %v", err)
	}
	sameTemplate(t, back, tp)
}

func mustLink(t *testing.T, tp *Template) *Link {
	t.Helper()
	l, err := NewLink(tp)
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func boolPtr(b bool) *bool { return &b }

// digest is a made-up hex hash of the algorithm's length, the same for the
// same seed.
func digest(algo, seed string) string {
	switch algo {
	case "sha512":
		s := sha512.Sum512([]byte(seed))
		return hex.EncodeToString(s[:])
	case "sha1":
		s := sha1.Sum([]byte(seed))
		return hex.EncodeToString(s[:])
	}
	s := sha256.Sum256([]byte(seed))
	return hex.EncodeToString(s[:])
}

// manyAddons are n Modrinth add-ons at pinned versions.
func manyAddons(n int) []Addon {
	as := make([]Addon, n)
	for i := range as {
		as[i] = Addon{Source: addons.Modrinth, Project: fmt.Sprintf("P%07d", i), Slug: fmt.Sprintf("mod-%d", i), Name: fmt.Sprintf("Mod %d", i),
			Pin: &Pin{VersionID: fmt.Sprintf("V%07d", i), VersionNumber: "1.0." + strconv.Itoa(i), Channel: "release", HashAlgo: "sha512", Hash: digest("sha512", strconv.Itoa(i))}}
	}
	return as
}

// dataPack is the i-th of made-up data packs.
func dataPack(i int) Pack {
	return Pack{Kind: DataPack, Name: fmt.Sprintf("pack-%d", i), URL: fmt.Sprintf("https://cdn.modrinth.com/data/pack%d/pack-%d.zip", i, i),
		SHA256: digest("sha256", "pack"+strconv.Itoa(i))}
}

// craft builds a link's data around any bytes, with the header and
// checksum right, as someone altering links on purpose would.
func craft(version byte, body []byte) string {
	sum := sha256.Sum256(body)
	raw := append([]byte{version, byte(len(body) >> 8), byte(len(body))}, sum[:4]...)
	return base64.RawURLEncoding.EncodeToString(append(raw, body...))
}

func deflate(t *testing.T, data []byte) []byte {
	t.Helper()
	var b bytes.Buffer
	w, err := flate.NewWriter(&b, flate.BestCompression)
	if err != nil {
		t.Fatal(err)
	}
	w.Write(data)
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}
