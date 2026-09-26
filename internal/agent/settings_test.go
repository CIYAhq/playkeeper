package agent

import "testing"

func TestMergeProperties(t *testing.T) {
	for _, c := range []struct {
		name string
		cur  string
		set  map[string]string
		want string
	}{
		{"no file yet", "", map[string]string{"pvp": "false", "allow-flight": "true"}, "allow-flight=true\npvp=false\n"},
		{
			"keeps other lines and comments, replaces a key once",
			"#Minecraft server properties\n#Fri Sep 25\nmotd=Survival\nallow-flight=false\n\nlevel-name=world\nallow-flight=false\n",
			map[string]string{"allow-flight": "true", "spawn-protection": "0"},
			"#Minecraft server properties\n#Fri Sep 25\nmotd=Survival\nallow-flight=true\n\nlevel-name=world\nspawn-protection=0\n",
		},
		{"a last line without a newline", "motd=Survival", map[string]string{"pvp": "true"}, "motd=Survival\npvp=true\n"},
		{"keys written with colons or spaces", "pvp:true\nhardcore false\n", map[string]string{"pvp": "false", "hardcore": "true"}, "pvp=false\nhardcore=true\n"},
		{"a key that only starts the same", "pvp-extra=1\n", map[string]string{"pvp": "false"}, "pvp-extra=1\npvp=false\n"},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := string(mergeProperties([]byte(c.cur), c.set)); got != c.want {
				t.Fatalf("got\n%q\nwant\n%q", got, c.want)
			}
		})
	}
}

func TestEscapeProperty(t *testing.T) {
	for in, want := range map[string]string{
		"true":                         "true",
		`{"biome":"minecraft:plains"}`: `{"biome"\:"minecraft\:plains"}`,
		" leading space, inner spaces": `\ leading space, inner spaces`,
		`a=b#c!d\e`:                    `a\=b\#c\!d\\e`,
		"café":                         `caf\u00E9`,
		"pack 🎮":                       `pack \uD83C\uDFAE`,
	} {
		if got := escapeProperty(in); got != want {
			t.Errorf("escapeProperty(%q) = %q, want %q", in, got, want)
		}
	}
}
