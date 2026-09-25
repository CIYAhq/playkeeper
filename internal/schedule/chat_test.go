package schedule

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// component parses a tellraw command's text component and checks it only
// uses the escapes valid in both JSON and SNBT.
func component(t *testing.T, cmd string) map[string]any {
	t.Helper()
	body, ok := strings.CutPrefix(cmd, "tellraw @a ")
	if !ok {
		t.Fatalf("%q is not a tellraw command", cmd)
	}
	for i := 0; i < len(body); i++ {
		if body[i] == '\\' {
			if i+1 >= len(body) || (body[i+1] != '\\' && body[i+1] != '"') {
				t.Fatalf("escape at %d in %q is not \\\\ or \\\"", i, body)
			}
			i++
		}
	}
	var c map[string]any
	if err := json.Unmarshal([]byte(body), &c); err != nil {
		t.Fatalf("component %q is not valid JSON: %v", body, err)
	}
	if len(c) != 2 || c["color"] == nil {
		t.Fatalf("component %q has keys other than text and color", body)
	}
	return c
}

func TestTellrawEscapesUserText(t *testing.T) {
	for _, text := range []string{
		`He said "hi" \o/`,
		`"},{"text":"x","clickEvent":{"action":"run_command","value":"/op me"}}`,
		`\"}` + "\u00a0" + `{"selector":"@a"}`,
		`'single' {curly} [square] @a @e[type=creeper] §cred`,
		`\u0022 \n \\\\ trailing backslash \`,
		"Grüße aus Köln 🌍 日本語",
	} {
		cmd, err := tellraw(text, colorAnnouncement)
		if err != nil {
			t.Fatalf("tellraw(%q): %v", text, err)
		}
		c := component(t, cmd)
		if c["text"] != text || c["color"] != "yellow" {
			t.Fatalf("tellraw(%q) shows %q", text, c["text"])
		}
		if strings.Count(cmd, "\n") != 0 {
			t.Fatalf("tellraw(%q) spans lines", text)
		}
	}
	got, _ := tellraw(`a "b" \c`, colorWarning)
	if want := `tellraw @a {"text":"a \"b\" \\c","color":"gold"}`; got != want {
		t.Fatalf("tellraw = %s, want %s", got, want)
	}
}

func TestTellrawRefusesUnsafeText(t *testing.T) {
	for _, text := range []string{"a\nb", "a\rb", "a\x00b", "a\x1b[31mb", "a\x7fb", "a\u0085b", "a\u2028b", "a\u202eb", "a\u2066b", "a\ufeffb", "a\xffb"} {
		if _, err := tellraw(text, colorAnnouncement); err == nil {
			t.Errorf("tellraw(%q) accepted", text)
		}
	}
	if _, err := tellraw(strings.Repeat("a", 2000), colorAnnouncement); err == nil {
		t.Error("tellraw accepted a message too long for one RCON request")
	}
}

func TestLongestMessagesFitOneRequest(t *testing.T) {
	for _, m := range []string{strings.Repeat("🌍", maxMessage), strings.Repeat(`"`, maxMessage), strings.Repeat(`\`, maxMessage)} {
		if err := validMessage(m); err != nil {
			t.Fatal(err)
		}
		cmd, err := restartWarning(10*time.Minute, m)
		if err != nil || len(cmd) > maxCommandBytes {
			t.Fatalf("warning with a %d-byte message: %d bytes, %v", len(m), len(cmd), err)
		}
	}
}

func TestWarningText(t *testing.T) {
	cases := []struct {
		remaining time.Duration
		extra     string
		want      string
	}{
		{10 * time.Minute, "", "The server restarts in 10 minutes."},
		{time.Minute, "", "The server restarts in 1 minute."},
		{30 * time.Second, "Back soon!", "The server restarts in 30 seconds. Back soon!"},
		{time.Second, "", "The server restarts in 1 second."},
		{90 * time.Second, "", "The server restarts in about 1 minute."},
		{7*time.Minute + 42*time.Second, "", "The server restarts in about 7 minutes."},
		{59*time.Second + 600*time.Millisecond, "", "The server restarts in 1 minute."},
		{200 * time.Millisecond, "", "The server restarts in 1 second."},
	}
	for _, c := range cases {
		cmd, err := restartWarning(c.remaining, c.extra)
		if err != nil {
			t.Fatal(err)
		}
		if got := component(t, cmd)["text"]; got != c.want {
			t.Errorf("warning(%v) = %q, want %q", c.remaining, got, c.want)
		}
	}
	for _, cmd := range []string{restartNow(), restartCalledOff()} {
		component(t, cmd)
	}
}

func TestWarningTemplate(t *testing.T) {
	const msg = "Survival restarts in {minutes} minutes. Hang tight, it's quick!"
	cases := []struct {
		remaining time.Duration
		message   string
		want      string
	}{
		{10 * time.Minute, msg, "Survival restarts in 10 minutes. Hang tight, it's quick!"},
		{time.Minute, msg, "Survival restarts in 1 minute. Hang tight, it's quick!"},
		{30 * time.Second, msg, "Survival restarts in 30 seconds. Hang tight, it's quick!"},
		{7*time.Minute + 42*time.Second, msg, "Survival restarts in about 7 minutes. Hang tight, it's quick!"},
		{5 * time.Minute, "Restart in {minutes} minute(s)", "Restart in 5 minutes(s)"},
		{5 * time.Minute, "Restart: {minutes}m", "Restart: 5m"},
		{7*time.Minute + 42*time.Second, "T-{minutes}", "T-8"},
		{10 * time.Second, "T-{minutes}", "T-10 seconds"},
		{2 * time.Minute, "{minutes} minutes, then {minutes} minutes", "2 minutes, then 2 minutes"},
	}
	for _, c := range cases {
		cmd, err := restartWarning(c.remaining, c.message)
		if err != nil {
			t.Fatal(err)
		}
		if got := component(t, cmd)["text"]; got != c.want {
			t.Errorf("warning(%v, %q) = %q, want %q", c.remaining, c.message, got, c.want)
		}
	}
}

func TestSayText(t *testing.T) {
	for cmd, want := range map[string]string{
		"say Weekend build contest starts now!": "Weekend build contest starts now!",
		"say @a is here":                        "@a is here",
		"say":                                   "",
		"save-all":                              "",
	} {
		got, ok := sayText(cmd)
		if got != want || ok != (want != "") {
			t.Errorf("sayText(%q) = %q, %v", cmd, got, ok)
		}
	}
}

func TestCheckCommand(t *testing.T) {
	for _, ok := range []string{
		"save-all", "save-all flush", "weather clear", "weather rain 600", "weather thunder 1d", "time set day",
		"time set midnight", "time set 6000", "time add 100t", "difficulty hard", "gamerule keepInventory true",
		"gamerule minecraft:keep_inventory false", "gamerule randomTickSpeed 3", "setidletimeout 30",
		"kill @e[type=item]", "kill @e[type=minecraft:item]", "say hi", "say Weekend build contest starts now!",
		"say @a \"quoted\" {curly}",
	} {
		if err := CheckCommand(ok); err != nil {
			t.Errorf("CheckCommand(%q): %v", ok, err)
		}
	}
	for _, bad := range []string{
		"", "stop", "restart", "op Steve", "deop Steve", "save-all now", "weather clear forever", "time set day; stop",
		"time query daytime", "gamerule keepInventory true extra", "kill @e", "kill @a", "kill @e[type=item] extra",
		"say", "say a\u202eb", "say a\u2028b", "SAY hi", "tellraw @a {}", "execute run stop", "/save-all", "save-all\nstop", "difficulty hard\x00",
		"whitelist off", "ban Steve", "gamerule a;b true", "gamerule keepInventory maybe", "weather clear 99999999",
		"save-all  flush", "SAVE-ALL", "reload", "save-off", strings.Repeat("a", 300),
	} {
		if err := CheckCommand(bad); err == nil {
			t.Errorf("CheckCommand(%q) accepted", bad)
		}
	}
	err := CheckCommand("op Steve")
	ve, _ := err.(*ValidationError)
	if ve == nil || ve.Code != "command_not_allowed" || ve.Params["command"] != "op" || ve.Hint == "" {
		t.Fatalf("CheckCommand(op) = %#v", err)
	}
}

func TestRejectedOutput(t *testing.T) {
	for out, want := range map[string]bool{
		"Unknown or incomplete command, see below for error":               true,
		"Incorrect argument for command":                                   true,
		"§cUnknown command. Type \"/help\" for help.":                      true,
		"Expected whitespace to end one argument, but found trailing data": true,
		"Saved the game":            false,
		"Changing to clear weather": false,
		"":                          false,
	} {
		if got := rejected(out); got != want {
			t.Errorf("rejected(%q) = %v, want %v", out, got, want)
		}
	}
}

func TestCleanOutput(t *testing.T) {
	got := cleanOutput("\x1b[31mSaved\x1b[0m the §agame\r\nfrom 192.168.1.20:25565\x00")
	if want := "Saved the game from [ip redacted]"; got != want {
		t.Fatalf("cleanOutput = %q, want %q", got, want)
	}
	long := cleanOutput(strings.Repeat("é", 400))
	if len(long) > maxDetail || !strings.HasSuffix(long, "…") {
		t.Fatalf("cleanOutput kept %d bytes", len(long))
	}
}
