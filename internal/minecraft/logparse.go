package minecraft

import (
	"regexp"
	"strconv"
	"strings"
)

// EventKind classifies server log lines Playkeeper acts on.
type EventKind string

const (
	EventNone        EventKind = ""
	EventJoin        EventKind = "join"
	EventLeave       EventKind = "leave"
	EventUUID        EventKind = "uuid"
	EventReady       EventKind = "ready"
	EventStopping    EventKind = "stopping"
	EventDownloading EventKind = "downloading"
	EventStarting    EventKind = "starting"
	EventPreparing   EventKind = "preparing"
	EventInitError   EventKind = "init_error"
	EventOOM         EventKind = "out_of_memory"
	EventBindFailed  EventKind = "bind_failed"
	// EventCrashed is the server reporting that it crashed. It may still
	// log "Stopping server" afterwards, as a clean stop does.
	EventCrashed EventKind = "crashed"
)

// Parsed is the structured meaning of one log line.
type Parsed struct {
	Kind   EventKind
	Player string
	UUID   string
	Detail string
}

// The prefix matches Paper ("[12:00:00 INFO]: "), vanilla, Fabric and Quilt
// ("[12:00:00] [Server thread/INFO]: ") and NeoForge and Forge, which name
// the logger too ("[12:00:00] [Server thread/INFO] [minecraft/MinecraftServer]: ").
// Player-controlled text (chat, /say, /me) always follows this prefix with
// "<", "[" or "*", and player names cannot contain spaces, so anchored
// patterns below cannot be forged from chat.
const prefix = `^\[\d{2}:\d{2}:\d{2}(?: (?:INFO|WARN|ERROR))?\](?: \[Server thread/(?:INFO|WARN|ERROR)\](?: \[[A-Za-z0-9_.$]+/[A-Za-z0-9_.$-]*\])?)?: `

var (
	reJoin      = regexp.MustCompile(prefix + `([A-Za-z0-9_]{1,16}) joined the game$`)
	reLeave     = regexp.MustCompile(prefix + `([A-Za-z0-9_]{1,16}) left the game$`)
	reUUID      = regexp.MustCompile(prefix + `UUID of player ([A-Za-z0-9_]{1,16}) is ([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})$`)
	reReady     = regexp.MustCompile(prefix + `Done \(([0-9.,]+)s\)! For help, type "help"`)
	reStopping  = regexp.MustCompile(prefix + `Stopping (?:the )?server$`)
	reStarting  = regexp.MustCompile(prefix + `Starting minecraft server version (\S+)`)
	rePreparing = regexp.MustCompile(prefix + `(?:Preparing level "|Preparing start region|Preparing spawn area)`)
	reBind      = regexp.MustCompile(`FAILED TO BIND TO PORT`)
	// Only ERROR and FATAL entries, which players cannot write.
	reCrashed = regexp.MustCompile(`^\[\d{2}:\d{2}:\d{2}(?: (?:ERROR|FATAL)\]|\] \[[^\]]{1,64}/(?:ERROR|FATAL)\])(?: \[[^\]]{1,120}\])?: ` +
		`(?:Encountered an unexpected exception|This crash report has been saved to: |Crash report saved to |The server has stopped responding!|Failed to start the minecraft server|A single server tick took )`)
	// Only raw JVM output, which has no log prefix, and WARN, ERROR and FATAL
	// entries whose message is the error: players write it in chat and
	// commands, which are INFO entries, and plugins log what players typed in
	// the messages of other exceptions.
	reOOM = regexp.MustCompile(`^(?:\[\d{2}:\d{2}:\d{2}(?: (?:WARN|ERROR|FATAL)\]|\] \[[^\]]{1,64}/(?:WARN|ERROR|FATAL)\])(?: \[[^\]]{1,120}\])?: )?` +
		`(?:Exception in thread "[^"]{1,120}" |Exception: |Caused by: |Terminating due to )?java\.lang\.OutOfMemoryError\b`)
	reANSI      = regexp.MustCompile(`\x1b\[[0-9;?]*[A-Za-z]|\[[0-9;]{1,8}m`)
	reIPv4      = regexp.MustCompile(`\b(?:\d{1,3}\.){3}\d{1,3}\b`)
	rePort      = regexp.MustCompile(`^:\d{1,5}\b`)
	reVersionOf = regexp.MustCompile(`(?i)(?:^|[^\w])(?:version|v|build|loader|neoforge|forge|minecraft|mc|fabric|quilt|paper|purpur|java):? ?$`)
	reModIDNext = regexp.MustCompile(`^ \([a-z][a-z0-9_]{1,63}\)`)
	reColour    = regexp.MustCompile(`§[0-9a-fk-orxA-FK-ORX]`)
	reIPv6Port  = regexp.MustCompile(`/\[?[0-9a-fA-F]{0,4}(?::[0-9a-fA-F]{0,4}){2,7}(?:%[\w.]+)?\]?(?::\d{1,5})?`)
	reName      = regexp.MustCompile(`^[A-Za-z0-9_]{3,16}$`)
	reLogName   = regexp.MustCompile(`^[A-Za-z0-9_]{1,16}$`)
	reListReply = regexp.MustCompile(`There are (\d+) of a max of (\d+) players online:?\s*(.*)$`)
)

// StripANSI removes terminal colour codes the container image emits.
func StripANSI(s string) string { return reANSI.ReplaceAllString(s, "") }

// StripColours removes Minecraft's § formatting codes, which plugins put in
// their command replies and log lines (hex colours are "§x" and six more).
func StripColours(s string) string { return reColour.ReplaceAllString(s, "") }

// RedactIPs removes IPv4/IPv6 addresses (player connection addresses appear in
// several vanilla log lines). Playkeeper never stores or displays them.
func RedactIPs(s string) string {
	s = reIPv6Port.ReplaceAllString(s, "/[ip redacted]")
	var b strings.Builder
	last := 0
	for _, m := range reIPv4.FindAllStringIndex(s, -1) {
		end, ok := ipv4End(s, m[0], m[1])
		if !ok {
			continue
		}
		b.WriteString(s[last:m[0]])
		b.WriteString("[ip redacted]")
		last = end
	}
	b.WriteString(s[last:])
	return b.String()
}

// ipv4End decides whether the dotted quad s[start:end] is an address, and
// where the address ends (after its port). Java writes addresses as
// "/203.0.113.7:50284", so a quad after "/" or before a port is one. Four-part
// version numbers look the same (NeoForge 26.2.0.88), so a quad inside a
// name or path ("neoforge-26.2.0.88-universal.jar", "/26.2.0.88/"), after a
// word like "version" or "NeoForge", or in a mod list row ("Waystones
// 21.1.0.4 (waystones)") is left as it is.
func ipv4End(s string, start, end int) (int, bool) {
	var before byte
	if start > 0 {
		before = s[start-1]
	}
	after := s[end:]
	if p := rePort.FindString(after); p != "" && !versionChar(after, len(p)) {
		return end + len(p), true
	}
	if before == '/' {
		return end, !versionChar(after, 0) && !strings.HasPrefix(after, "/")
	}
	if strings.ContainsRune("-_.+\\", rune(before)) || isWord(before) || versionChar(after, 0) || strings.HasPrefix(after, "/") {
		return 0, false
	}
	if reVersionOf.MatchString(s[:start]) || reModIDNext.MatchString(after) {
		return 0, false
	}
	return end, true
}

// versionChar reports whether s[i] carries on a name or version number: a
// letter or digit, or a joining mark before one ("-universal", ".5"), not a
// full stop at the end of a sentence.
func versionChar(s string, i int) bool {
	if i >= len(s) {
		return false
	}
	if strings.IndexByte("-_.+", s[i]) >= 0 {
		return i+1 < len(s) && isWord(s[i+1])
	}
	return isWord(s[i])
}

func isWord(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
}

// CleanLine prepares a raw container line for display and storage.
func CleanLine(s string) string { return RedactIPs(StripColours(StripANSI(s))) }

// Parse classifies a cleaned log line (without the Docker timestamp).
func Parse(line string) Parsed {
	switch {
	case strings.HasPrefix(line, "[init] [ERROR]") || strings.Contains(line, "'install-paper' command failed"):
		return Parsed{Kind: EventInitError, Detail: strings.TrimSpace(line)}
	case strings.HasPrefix(line, "[init] Resolving type") || strings.HasPrefix(line, "[mc-image-helper]") && strings.Contains(line, "Downloading"):
		return Parsed{Kind: EventDownloading}
	}
	if !strings.HasPrefix(line, "[") {
		if reOOM.MatchString(line) {
			return Parsed{Kind: EventOOM}
		}
		return Parsed{}
	}
	if m := reJoin.FindStringSubmatch(line); m != nil {
		return Parsed{Kind: EventJoin, Player: m[1]}
	}
	if m := reLeave.FindStringSubmatch(line); m != nil {
		return Parsed{Kind: EventLeave, Player: m[1]}
	}
	if m := reUUID.FindStringSubmatch(line); m != nil {
		return Parsed{Kind: EventUUID, Player: m[1], UUID: m[2]}
	}
	if m := reReady.FindStringSubmatch(line); m != nil {
		return Parsed{Kind: EventReady, Detail: m[1]}
	}
	if reStopping.MatchString(line) {
		return Parsed{Kind: EventStopping}
	}
	if reCrashed.MatchString(line) {
		return Parsed{Kind: EventCrashed}
	}
	if m := reStarting.FindStringSubmatch(line); m != nil {
		return Parsed{Kind: EventStarting, Detail: m[1]}
	}
	if rePreparing.MatchString(line) {
		return Parsed{Kind: EventPreparing}
	}
	if reBind.MatchString(line) {
		return Parsed{Kind: EventBindFailed}
	}
	if reOOM.MatchString(line) {
		return Parsed{Kind: EventOOM}
	}
	return Parsed{}
}

// ValidPlayerName reports whether s is an acceptable Java Edition username for
// whitelist and invite input.
func ValidPlayerName(s string) bool { return reName.MatchString(s) }

// ParseList parses the reply of the vanilla `list` command.
func ParseList(reply string) (online, max int, names []string, ok bool) {
	m := reListReply.FindStringSubmatch(strings.TrimSpace(StripANSI(reply)))
	if m == nil {
		return 0, 0, nil, false
	}
	online, _ = strconv.Atoi(m[1])
	max, _ = strconv.Atoi(m[2])
	for _, n := range strings.Split(m[3], ",") {
		if n = strings.TrimSpace(n); reLogName.MatchString(n) {
			names = append(names, n)
		}
	}
	return online, max, names, true
}
