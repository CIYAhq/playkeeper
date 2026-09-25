package worldimport

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func parsedProperties(s string) [][2]string {
	var out [][2]string
	for _, p := range parseProperties([]byte(s)) {
		out = append(out, [2]string{p.key, p.value})
	}
	return out
}

func TestParseProperties(t *testing.T) {
	type kv = [2]string
	cases := []struct {
		name string
		in   string
		want []kv
	}{
		{"separators", "a=1\nb:2\nc 3\nd\t4\ne\f5\n", []kv{{"a", "1"}, {"b", "2"}, {"c", "3"}, {"d", "4"}, {"e", "5"}}},
		{"whitespace around separators", "  a = 1\n\tb : 2\nc   3\nd =   \n", []kv{{"a", "1"}, {"b", "2"}, {"c", "3"}, {"d", ""}}},
		{"comments and blank lines", "#Minecraft server properties\n#Tue Sep 01 12:00:00 UTC 2026\n! another comment\n   # indented comment\n\n \t \na=1\n", []kv{{"a", "1"}}},
		{"value keeps separators and trailing spaces", "motd=Hello = world: yes  \n", []kv{{"motd", "Hello = world: yes  "}}},
		{"second separator belongs to the value", "a = = b\n", []kv{{"a", "= b"}}},
		{"key without value", "lonely\nempty=\n", []kv{{"lonely", ""}, {"empty", ""}}},
		{"empty key", "=value\n", []kv{{"", "value"}}},
		{"line breaks", "a=1\r\nb=2\rc=3\nd=4", []kv{{"a", "1"}, {"b", "2"}, {"c", "3"}, {"d", "4"}}},
		{"continuation lines", "motd=Hello \\\n    World\\\r\n\t!\nnext=1\n", []kv{{"motd", "Hello World!"}, {"next", "1"}}},
		{"continued line is not a comment", "a=1\\\n# still a\n", []kv{{"a", "1# still a"}}},
		{"escaped backslash ends the line", "path=C:\\\\\nnext=1\n", []kv{{"path", `C:\`}, {"next", "1"}}},
		{"continuation at the end of the file", "a=1\\", []kv{{"a", "1"}}},
		{"escapes", `a=tab\tnew\nret\rfeed\fquote\q\\`, []kv{{"a", "tab\tnew\nret\rfeed\fquoteq\\"}}},
		{"unicode escapes", `motd=Caf\u00e9 \u00C9t\u00e9 \uD83D\uDE00`, []kv{{"motd", "Café Été 😀"}}},
		{"lone surrogate", `a=\uD83D!`, []kv{{"a", "\uFFFD!"}}},
		{"malformed unicode escapes", "a=\\u12\nb=\\uZZZZ\n", []kv{{"a", "u12"}, {"b", "uZZZZ"}}},
		{"escaped characters in keys", "my\\ key=1\na\\:b=2\na\\=b=3\na\\\\=4\n", []kv{{"my key", "1"}, {"a:b", "2"}, {"a=b", "3"}, {`a\`, "4"}}},
		{"key ends at whitespace", "key value with spaces\n", []kv{{"key", "value with spaces"}}},
		{"empty", "", nil},
		{"only comments", "# a\n! b", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := parsedProperties(c.in); !reflect.DeepEqual(got, c.want) {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}

func TestPropertyValue(t *testing.T) {
	b := []byte("level-name=first\nmotd=x\nlevel-name=second\n")
	if v, ok := propertyValue(b, "level-name"); !ok || v != "second" {
		t.Errorf("level-name: got %q, %v; want the last value", v, ok)
	}
	if v, ok := propertyValue(b, "level-seed"); ok || v != "" {
		t.Errorf("level-seed: got %q, %v; want nothing", v, ok)
	}
}

func TestCarrySettings(t *testing.T) {
	cases := []struct {
		name    string
		props   string
		level   *Level
		carry   []Setting
		refused []string
		ignored []string
	}{
		{
			name:  "level.dat first",
			props: "level-seed=999\ngamemode=survival\ndifficulty=easy\nhardcore=true\nmotd=Old server\n",
			level: &Level{Seed: "123", GameMode: "creative", Difficulty: "hard"},
			carry: []Setting{
				{"level-seed", "123", SourceLevel}, {"gamemode", "creative", SourceLevel},
				{"difficulty", "hard", SourceLevel}, {"hardcore", "false", SourceLevel},
			},
			ignored: []string{"motd"},
		},
		{
			name:  "server.properties without level.dat",
			props: "level-seed=  -42  \ngamemode=1\ndifficulty=3\nhardcore=TRUE\n",
			carry: []Setting{
				{"level-seed", "-42", SourceProperties}, {"gamemode", "creative", SourceProperties},
				{"difficulty", "hard", SourceProperties}, {"hardcore", "true", SourceProperties},
			},
		},
		{
			name:  "server.properties where level.dat has nothing",
			props: "level-seed=777\ngamemode=survival\ndifficulty=peaceful\n",
			level: &Level{GameMode: "adventure", Hardcore: true},
			carry: []Setting{
				{"level-seed", "777", SourceProperties}, {"gamemode", "adventure", SourceLevel},
				{"difficulty", "peaceful", SourceProperties}, {"hardcore", "true", SourceLevel},
			},
		},
		{
			name:  "invalid values",
			props: "level-seed=\ngamemode=hardcore\ndifficulty=insane\nhardcore=yes\n",
		},
		{
			name:  "seed with a control character",
			props: "level-seed=12\\u0007 34\n",
		},
		{
			name:  "seed too long",
			props: "level-seed=" + strings.Repeat("9", 129) + "\n",
		},
		{
			name:  "longest seed",
			props: "level-seed=" + strings.Repeat("9", 128) + "\n",
			carry: []Setting{{"level-seed", strings.Repeat("9", 128), SourceProperties}},
		},
		{
			name: "access settings",
			props: "enable-rcon=true\nrcon.password=hunter2\nrcon.port=25575\nserver-port=25565\nserver-ip=10.0.0.5\n" +
				"enable-query=true\nquery.port=25565\nonline-mode=false\nwhite-list=true\nenforce-whitelist=true\n" +
				"level-name=world\nmanagement-server-enabled=true\nmanagement-server-secret=s3cret\n" +
				"broadcast-rcon-to-ops=true\nenable-jmx-monitoring=false\nprevent-proxy-connections=false\n" +
				"max-players=20\nview-distance=10\n",
			refused: []string{
				"broadcast-rcon-to-ops", "enable-jmx-monitoring", "enable-query", "enable-rcon", "enforce-whitelist",
				"level-name", "management-server-enabled", "management-server-secret", "online-mode",
				"prevent-proxy-connections", "query.port", "rcon.password", "rcon.port", "server-ip", "server-port", "white-list",
			},
			ignored: []string{"max-players", "view-distance"},
		},
		{
			name:  "last value wins",
			props: "gamemode=creative\ngamemode=adventure\n",
			carry: []Setting{{"gamemode", "adventure", SourceProperties}},
		},
		{
			name:    "empty and hostile keys",
			props:   "=orphan\nbad\\u0007key=1\n",
			ignored: []string{"bad\uFFFDkey"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			carry, refused, ignored := CarrySettings([]byte(c.props), c.level)
			if !reflect.DeepEqual(carry, c.carry) {
				t.Errorf("carry: got %v, want %v", carry, c.carry)
			}
			if !reflect.DeepEqual(refused, c.refused) {
				t.Errorf("refused: got %q, want %q", refused, c.refused)
			}
			if !reflect.DeepEqual(ignored, c.ignored) {
				t.Errorf("ignored: got %q, want %q", ignored, c.ignored)
			}
			all := fmt.Sprint(carry, refused, ignored)
			for _, secret := range []string{"hunter2", "s3cret", "10.0.0.5"} {
				if strings.Contains(all, secret) {
					t.Errorf("the result shows %q: %s", secret, all)
				}
			}
		})
	}
}

func TestCarrySettingsListsAtMost64Keys(t *testing.T) {
	var b strings.Builder
	for i := range 100 {
		fmt.Fprintf(&b, "custom-%03d=1\nrcon.extra-%03d=1\n", i, i)
	}
	_, refused, ignored := CarrySettings([]byte(b.String()), nil)
	if len(refused) != maxListedKeys || len(ignored) != maxListedKeys {
		t.Fatalf("got %d refused and %d ignored keys, want %d each", len(refused), len(ignored), maxListedKeys)
	}
	if refused[0] != "rcon.extra-000" || ignored[maxListedKeys-1] != "custom-063" {
		t.Errorf("the lists don't start with the first keys in order: %q…, …%q", refused[0], ignored[maxListedKeys-1])
	}
}

func TestApplySettings(t *testing.T) {
	cases := []struct {
		name    string
		current string
		carry   []Setting
		want    string
	}{
		{
			name:    "replaces values in place",
			current: "#Minecraft server properties\r\ngamemode=survival\r\ndifficulty=easy\r\nmotd=A Minecraft Server\r\n",
			carry:   []Setting{{Key: "gamemode", Value: "creative"}, {Key: "difficulty", Value: "hard"}},
			want:    "#Minecraft server properties\r\ngamemode=creative\r\ndifficulty=hard\r\nmotd=A Minecraft Server\r\n",
		},
		{
			name:    "appends missing keys in order",
			current: "motd=x\n",
			carry:   []Setting{{Key: "level-seed", Value: "-42"}, {Key: "hardcore", Value: "true"}},
			want:    "motd=x\nlevel-seed=-42\nhardcore=true\n",
		},
		{
			name:    "adds a line break before appending",
			current: "motd=x",
			carry:   []Setting{{Key: "level-seed", Value: "1"}},
			want:    "motd=x\nlevel-seed=1\n",
		},
		{
			name:  "empty file",
			carry: []Setting{{Key: "gamemode", Value: "creative"}, {Key: "level-seed", Value: "1"}},
			want:  "gamemode=creative\nlevel-seed=1\n",
		},
		{
			name:    "replaced last line without a line break",
			current: "gamemode=survival",
			carry:   []Setting{{Key: "gamemode", Value: "creative"}, {Key: "level-seed", Value: "7"}},
			want:    "gamemode=creative\nlevel-seed=7\n",
		},
		{
			name:    "old Mac line breaks",
			current: "gamemode=survival\rmotd=x\r",
			carry:   []Setting{{Key: "gamemode", Value: "creative"}, {Key: "level-seed", Value: "7"}},
			want:    "gamemode=creative\rmotd=x\rlevel-seed=7\n",
		},
		{
			name:    "drops repeated keys",
			current: "gamemode=survival\nmotd=x\ngamemode=adventure\n",
			carry:   []Setting{{Key: "gamemode", Value: "creative"}},
			want:    "gamemode=creative\nmotd=x\n",
		},
		{
			name:    "replaces a continued line whole",
			current: "motd=x\n  gamemode = sur\\\n    vival\nmax-players=20\n",
			carry:   []Setting{{Key: "gamemode", Value: "creative"}},
			want:    "motd=x\ngamemode=creative\nmax-players=20\n",
		},
		{
			name:    "leaves comments alone",
			current: "#gamemode=survival\ngamemode=survival\n",
			carry:   []Setting{{Key: "gamemode", Value: "creative"}},
			want:    "#gamemode=survival\ngamemode=creative\n",
		},
		{
			name:    "finds escaped keys",
			current: "game\\mode=survival\n",
			carry:   []Setting{{Key: "gamemode", Value: "creative"}},
			want:    "gamemode=creative\n",
		},
		{
			name:    "normalizes values",
			current: "difficulty=easy\n",
			carry:   []Setting{{Key: "difficulty", Value: "3"}, {Key: "hardcore", Value: " TRUE "}, {Key: "gamemode", Value: "2"}},
			want:    "difficulty=hard\nhardcore=true\ngamemode=adventure\n",
		},
		{
			name:    "last carried value wins",
			current: "gamemode=survival\n",
			carry:   []Setting{{Key: "gamemode", Value: "creative"}, {Key: "gamemode", Value: "spectator"}},
			want:    "gamemode=spectator\n",
		},
		{
			name:    "ignores keys it doesn't carry and invalid values",
			current: "enable-rcon=false\nserver-port=25565\nmotd=x\ngamemode=survival\n",
			carry: []Setting{
				{Key: "enable-rcon", Value: "true"}, {Key: "rcon.password", Value: "hunter2"}, {Key: "server-port", Value: "1"},
				{Key: "motd", Value: "hi"}, {Key: "online-mode", Value: "false"}, {Key: "gamemode", Value: "god"},
				{Key: "level-seed", Value: ""}, {Key: "difficulty", Value: "insane"},
			},
			want: "enable-rcon=false\nserver-port=25565\nmotd=x\ngamemode=survival\n",
		},
		{
			name:    "escapes values",
			current: "level-seed=1\n",
			carry:   []Setting{{Key: "level-seed", Value: "é=1:#!\\"}},
			want:    "level-seed=\\u00E9\\=1\\:\\#\\!\\\\\n",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := string(ApplySettings([]byte(c.current), c.carry)); got != c.want {
				t.Errorf("got\n%q\nwant\n%q", got, c.want)
			}
		})
	}
}

func TestEscapeValue(t *testing.T) {
	cases := []struct{ in, want string }{
		{"plain-123", "plain-123"},
		{" leading space", `\ leading space`},
		{"inner and trailing ", "inner and trailing "},
		{"tab\tnew\nret\rfeed\f", `tab\tnew\nret\rfeed\f`},
		{"a=b:c#d!e", `a\=b\:c\#d\!e`},
		{`back\slash`, `back\\slash`},
		{"é", `\u00E9`},
		{"😀", `\uD83D\uDE00`},
		{"\x00\x1f\x7f~", `\u0000\u001F\u007F~`},
	}
	for _, c := range cases {
		got := escapeValue(c.in)
		if got != c.want {
			t.Errorf("escapeValue(%q) = %q, want %q", c.in, got, c.want)
		}
		if v, ok := propertyValue([]byte("k="+got+"\n"), "k"); !ok || v != c.in {
			t.Errorf("%q reads back as %q", got, v)
		}
	}
}
