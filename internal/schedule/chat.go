package schedule

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/CIYAhq/playkeeper/internal/minecraft"
)

// In-game text goes out with tellraw as a text component. User text only
// ever appears inside the component's "text" string, where only \ and " need
// escaping, and the result is valid both as JSON (before 1.21.5) and as SNBT
// (1.21.5 and later). `say` is never sent with user text: it expands target
// selectors such as @a in its message. A scheduled `say` goes out as tellraw
// with the same "[Server]" prefix players know.

const (
	colorWarning      = "gold"
	colorAnnouncement = "yellow"
	colorSay          = "white"
	sayPrefix         = "[Server] "
	// maxCommandBytes is the largest command one RCON request carries.
	maxCommandBytes = 1446
	maxCommandLen   = 256
	maxDetail       = 300
)

var (
	ticksRe     = regexp.MustCompile(`^[0-9]{1,7}[dst]?$`)
	gameruleRe  = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,64}$`)
	ruleValueRe = regexp.MustCompile(`^(true|false|-?[0-9]{1,9})$`)
	minutesRe   = regexp.MustCompile(`^[0-9]{1,4}$`)
	formatRe    = regexp.MustCompile(`§.?`)
	spaceRe     = regexp.MustCompile(`\s+`)
)

// chatSafe reports whether s can be shown in chat as one line: valid UTF-8
// without control, line-separator or text-direction characters.
func chatSafe(s string) bool {
	if !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		switch {
		case r < 0x20, r >= 0x7f && r < 0xa0,
			r == 0x2028, r == 0x2029, r == 0xfeff,
			r >= 0x202a && r <= 0x202e, r >= 0x2066 && r <= 0x2069:
			return false
		}
	}
	return true
}

// tellraw returns a command that shows text to every player.
func tellraw(text, color string) (string, error) {
	if !chatSafe(text) {
		return "", errors.New("the message contains line breaks or control characters")
	}
	var b strings.Builder
	b.WriteString(`tellraw @a {"text":"`)
	for _, r := range text {
		switch r {
		case '\\':
			b.WriteString(`\\`)
		case '"':
			b.WriteString(`\"`)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteString(`","color":"` + color + `"}`)
	if b.Len() > maxCommandBytes {
		return "", errors.New("the message is too long to send in one command")
	}
	return b.String(), nil
}

// restartWarning is the in-game warning with remaining time left before a
// restart. A message with MinutesPlaceholder is the whole warning; any other
// message follows the standard one.
func restartWarning(remaining time.Duration, message string) (string, error) {
	if strings.Contains(message, MinutesPlaceholder) {
		return tellraw(fillMinutes(message, remaining), colorWarning)
	}
	text := "The server restarts in " + durationWords(remaining) + "."
	if message != "" {
		text += " " + message
	}
	return tellraw(text, colorWarning)
}

// fillMinutes puts the time left into a message. "{minutes} minutes" becomes
// the whole phrase, so it still reads right for "1 minute" or "30 seconds";
// a placeholder on its own becomes the number of minutes.
func fillMinutes(message string, remaining time.Duration) string {
	words := durationWords(remaining)
	for _, unit := range []string{" minutes", " minute"} {
		message = strings.ReplaceAll(message, MinutesPlaceholder+unit, words)
	}
	n := words
	if secs := int((remaining + time.Second/2) / time.Second); secs >= 60 {
		n = strconv.Itoa((secs + 30) / 60)
	}
	return strings.ReplaceAll(message, MinutesPlaceholder, n)
}

// sayText is the message of a `say` command.
func sayText(cmd string) (string, bool) {
	text, ok := strings.CutPrefix(cmd, "say ")
	if !ok || text == "" {
		return "", false
	}
	return text, true
}

func restartNow() string {
	cmd, _ := tellraw("The server is restarting now.", colorWarning)
	return cmd
}

func restartCalledOff() string {
	cmd, _ := tellraw("The planned restart was called off.", colorWarning)
	return cmd
}

// durationWords says how long remaining is: seconds under a minute, whole
// minutes otherwise, with "about" when that rounds down.
func durationWords(remaining time.Duration) string {
	secs := int((remaining + time.Second/2) / time.Second)
	switch {
	case secs < 60:
		return plural(max(secs, 1), "second")
	case secs%60 != 0:
		return "about " + plural(secs/60, "minute")
	}
	return plural(secs/60, "minute")
}

// CheckCommand reports whether cmd may run on a schedule. Scheduled commands
// run unattended, so only a short list of harmless ones is allowed. cmd must
// be normalized: no leading slash, single spaces.
func CheckCommand(cmd string) error {
	const field = "payload.command"
	if cmd == "" {
		return &ValidationError{Field: field, Code: "command_required", Msg: "Type the command to run, for example save-all."}
	}
	if len(cmd) > maxCommandLen {
		return &ValidationError{Field: field, Code: "command_too_long",
			Msg: fmt.Sprintf("Commands can be at most %d characters.", maxCommandLen), Params: map[string]any{"max": maxCommandLen}}
	}
	if !plainText(cmd) {
		return &ValidationError{Field: field, Code: "command_invalid", Msg: "Commands can't contain line breaks or control characters."}
	}
	f := strings.Split(cmd, " ")
	switch f[0] {
	case "stop", "restart":
		return &ValidationError{Field: field, Code: "command_use_restart",
			Msg: "Use a restart schedule instead, so Playkeeper knows the server was restarted on purpose."}
	}
	if f[0] == "say" {
		if len(f) == 1 {
			return &ValidationError{Field: field, Code: "say_required", Msg: "Write what to say after say."}
		}
		if !chatSafe(cmd) {
			return &ValidationError{Field: field, Code: "command_invalid", Msg: "Commands can't contain line breaks or control characters."}
		}
		return nil
	}
	if !allowedCommand(f) {
		return &ValidationError{Field: field, Code: "command_not_allowed",
			Msg:    fmt.Sprintf("%s can't run on a schedule.", quote(cmd)),
			Hint:   "Scheduled commands can be say, save-all, weather, time set, time add, difficulty, gamerule, setidletimeout and kill @e[type=item]. Restarts and backups have their own schedule types.",
			Params: map[string]any{"command": f[0]}}
	}
	return nil
}

func allowedCommand(f []string) bool {
	switch f[0] {
	case "save-all":
		return len(f) == 1 || len(f) == 2 && f[1] == "flush"
	case "weather":
		return (len(f) == 2 || len(f) == 3 && ticksRe.MatchString(f[2])) && oneOf(f[1], "clear", "rain", "thunder")
	case "time":
		if len(f) != 3 {
			return false
		}
		switch f[1] {
		case "set":
			return oneOf(f[2], "day", "noon", "night", "midnight") || ticksRe.MatchString(f[2])
		case "add":
			return ticksRe.MatchString(f[2])
		}
	case "difficulty":
		return len(f) == 2 && oneOf(f[1], "peaceful", "easy", "normal", "hard")
	case "gamerule":
		return len(f) == 3 && gameruleRe.MatchString(f[1]) && ruleValueRe.MatchString(f[2])
	case "setidletimeout":
		return len(f) == 2 && minutesRe.MatchString(f[1])
	case "kill":
		return len(f) == 2 && oneOf(f[1], "@e[type=item]", "@e[type=minecraft:item]")
	}
	return false
}

func oneOf(s string, options ...string) bool {
	for _, o := range options {
		if s == o {
			return true
		}
	}
	return false
}

func normCommand(c string) string {
	c = strings.TrimPrefix(strings.TrimSpace(c), "/")
	return strings.Join(strings.FieldsFunc(c, func(r rune) bool { return r == ' ' }), " ")
}

// rejected reports whether a command's output is the server refusing it.
func rejected(out string) bool {
	out = strings.TrimSpace(cleanOutput(out))
	for _, p := range []string{"Unknown or incomplete command", "Incorrect argument for command", "Unknown command", "Expected whitespace"} {
		if strings.HasPrefix(out, p) {
			return true
		}
	}
	return false
}

// cleanOutput makes server output safe to store and show: no colour codes,
// control characters or IP addresses, one line, shortened.
func cleanOutput(s string) string {
	s = strings.ToValidUTF8(s, "")
	s = formatRe.ReplaceAllString(minecraft.CleanLine(s), "")
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || (r >= 0x7f && r < 0xa0) {
			return ' '
		}
		return r
	}, s)
	return truncate(strings.TrimSpace(spaceRe.ReplaceAllString(s, " ")), maxDetail)
}

// truncate shortens s to at most n bytes without splitting a character.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n - len("…")
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}
