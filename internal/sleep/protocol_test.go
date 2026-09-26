package sleep

import (
	"bufio"
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestVarInt(t *testing.T) {
	cases := []struct {
		v   int32
		hex string
	}{
		{0, "00"}, {1, "01"}, {127, "7f"}, {128, "8001"}, {255, "ff01"}, {25565, "ddc701"},
		{2097151, "ffff7f"}, {2147483647, "ffffffff07"}, {-1, "ffffffff0f"}, {-2147483648, "8080808008"},
	}
	for _, c := range cases {
		if got := hex.EncodeToString(appendVarInt(nil, c.v)); got != c.hex {
			t.Errorf("encode %d: %s, want %s", c.v, got, c.hex)
		}
		b, _ := hex.DecodeString(c.hex)
		if got, err := readVarInt(bytes.NewReader(b), 5); err != nil || got != c.v {
			t.Errorf("decode %s: %d, %v", c.hex, got, err)
		}
	}
	if _, err := readVarInt(bytes.NewReader([]byte{0x80, 0x80, 0x80, 0x80, 0x80, 0x01}), 5); !errors.Is(err, errMalformed) {
		t.Errorf("six-byte VarInt: %v", err)
	}
}

func TestReadFrameCapsTheLength(t *testing.T) {
	cases := map[string][]byte{
		"four-byte length":  {0x80, 0x80, 0x80, 0x01},
		"zero length":       {0x00},
		"over the cap":      appendVarInt(nil, 17),
		"largest length":    {0xff, 0xff, 0x7f},
		"negative VarInt":   {0xff, 0xff, 0xff, 0xff, 0x0f},
		"id not terminated": {0x01, 0x80},
	}
	for name, b := range cases {
		if _, err := readFrame(bufio.NewReader(bytes.NewReader(b)), 16); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	p, err := readFrame(bufio.NewReader(bytes.NewReader([]byte{0x03, 0x01, 0xaa, 0xbb})), 16)
	if err != nil || p.id != 1 || p.r.Len() != 2 {
		t.Fatalf("got %+v, %v", p, err)
	}
}

func TestPacketString(t *testing.T) {
	str := func(s string) packet { return packet{r: bytes.NewReader(appendString(nil, s))} }
	ok := []string{"", "Steve", strings.Repeat("é", 16), strings.Repeat("😀", 8), strings.Repeat("字", 16)}
	for _, s := range ok {
		p := str(s)
		if got, err := p.string(16); err != nil || got != s {
			t.Errorf("%q: %q, %v", s, got, err)
		}
	}
	bad := map[string]packet{
		"17 characters":      str(strings.Repeat("a", 17)),
		"17 two-byte chars":  str(strings.Repeat("é", 17)),
		"9 emoji, 18 units":  str(strings.Repeat("😀", 9)),
		"not UTF-8":          {r: bytes.NewReader([]byte{0x03, 0xff, 0xfe, 0xfd})},
		"negative length":    {r: bytes.NewReader([]byte{0xff, 0xff, 0xff, 0xff, 0x0f})},
		"longer than packet": {r: bytes.NewReader([]byte{0x05, 'a', 'b'})},
		"over 3 bytes each":  {r: bytes.NewReader(append([]byte{49}, strings.Repeat("a", 49)...))},
	}
	for name, p := range bad {
		if got, err := p.string(16); err == nil {
			t.Errorf("%s: accepted %q", name, got)
		}
	}
}

func TestParseHandshake(t *testing.T) {
	body := appendVarInt(nil, 767)
	body = appendString(body, "play.example.com")
	body = append(body, 0x63, 0xdd)
	body = appendVarInt(body, 2)
	p := packet{id: 0, r: bytes.NewReader(body)}
	h, err := parseHandshake(p)
	if err != nil || h.protocol != 767 || h.intent != 2 {
		t.Fatalf("got %+v, %v", h, err)
	}
	if _, err := parseHandshake(packet{id: 0, r: bytes.NewReader(append(body, 0))}); err == nil {
		t.Error("accepted a trailing byte")
	}
	if _, err := parseHandshake(packet{id: 0, r: bytes.NewReader(body[:len(body)-2])}); err == nil {
		t.Error("accepted a handshake without port and intent")
	}
	if _, err := parseHandshake(packet{id: 1, r: bytes.NewReader(body)}); err == nil {
		t.Error("accepted packet id 1")
	}
}

type statusDoc struct {
	Version struct {
		Name     string `json:"name"`
		Protocol int    `json:"protocol"`
	} `json:"version"`
	Players struct {
		Max    int `json:"max"`
		Online int `json:"online"`
	} `json:"players"`
	Description textComponent `json:"description"`
	Favicon     string        `json:"favicon"`
}

func (c textComponent) plain() string {
	s := c.Text
	for _, e := range c.Extra {
		s += e.plain()
	}
	return s
}

func TestStatusJSON(t *testing.T) {
	st := Status{Name: `Bob's "Realm" <1>`, Version: "Paper 26.2", Protocol: 775, MaxPlayers: 20, Icon: iconPrefix + "AAAA"}
	var doc statusDoc
	if err := json.Unmarshal([]byte(statusJSON(st, 767)), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Version.Protocol != 767 || doc.Version.Name != "Paper 26.2" || doc.Players.Max != 20 || doc.Players.Online != 0 || doc.Favicon != st.Icon {
		t.Fatalf("got %+v", doc)
	}
	if got := doc.Description.plain(); got != "Bob's \"Realm\" <1>\nAsleep · join to wake it" {
		t.Fatalf("description %q", got)
	}
	for _, p := range []int32{-1, 0} {
		_ = json.Unmarshal([]byte(statusJSON(st, p)), &doc)
		if doc.Version.Protocol != 775 {
			t.Errorf("client protocol %d: reported %d, want the server's", p, doc.Version.Protocol)
		}
	}

	st.Icon = iconPrefix + strings.Repeat("A", maxStatusJSON)
	s := statusJSON(st, 767)
	if len(s) > maxStatusJSON || strings.Contains(s, "favicon") {
		t.Fatalf("oversized icon kept: %d bytes", len(s))
	}

	var unnamed statusDoc
	_ = json.Unmarshal([]byte(statusJSON(Status{Version: "Minecraft"}, 767)), &unnamed)
	if got := unnamed.Description.plain(); got != sleepingText {
		t.Fatalf("description without a name: %q", got)
	}
}

func TestLegacyEncoding(t *testing.T) {
	if got := hex.EncodeToString(legacyKick("§1😀")); got != "ff0004"+"00a7"+"0031"+"d83dde00" {
		t.Fatalf("got %s", got)
	}
	st := Status{Name: "Survival", Version: "Paper 26.2", MaxPlayers: 20}
	if got := legacyStatus(st, true); got != "Survival - Asleep · join to wake it§0§20" {
		t.Errorf("beta: %q", got)
	}
	if got := legacyStatus(st, false); got != "§1\x00127\x00Paper 26.2\x00Survival - Asleep · join to wake it\x000\x0020" {
		t.Errorf("1.4: %q", got)
	}
}

func TestOneLine(t *testing.T) {
	cases := map[string]string{
		"Survival":                     "Survival",
		"  My\tServer\n\nTwo  ":        "My Server Two",
		"§4Red§r text":                 "4Redr text",
		"bell\x07null\x00del\x7f":      "bell null del",
		"bad \xff UTF-8":               "bad UTF-8",
		"line\u2028sep":                "line sep",
		"zero\u200bwidth":              "zero width",
		strings.Repeat("abcdefgh", 10): strings.Repeat("abcdefgh", 6),
	}
	for in, want := range cases {
		if got := oneLine(in, maxNameRunes); got != want {
			t.Errorf("oneLine(%q) = %q, want %q", in, got, want)
		}
	}
}
