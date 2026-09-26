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

// The prefix matches Paper ("[12:00:00 INFO]: ") and vanilla
// ("[12:00:00] [Server thread/INFO]: "). Player-controlled text (chat, /say,
// /me) always follows this prefix with "<", "[" or "*", and player names cannot
// contain spaces, so anchored patterns below cannot be forged from chat.
const prefix = `^\[\d{2}:\d{2}:\d{2}(?: (?:INFO|WARN|ERROR))?\](?: \[Server thread/(?:INFO|WARN|ERROR)\])?: `

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
		`(?:Encountered an unexpected exception|This crash report has been saved to: |The server has stopped responding!|Failed to start the minecraft server|A single server tick took )`)
	reOOM       = regexp.MustCompile(`java\.lang\.OutOfMemoryError`)
	reANSI      = regexp.MustCompile(`\x1b\[[0-9;?]*[A-Za-z]|\[[0-9;]{1,8}m`)
	reIPv4Port  = regexp.MustCompile(`/?\b(?:\d{1,3}\.){3}\d{1,3}(?::\d{1,5})?\b`)
	reIPv6Port  = regexp.MustCompile(`/\[?[0-9a-fA-F]{0,4}(?::[0-9a-fA-F]{0,4}){2,7}(?:%[\w.]+)?\]?(?::\d{1,5})?`)
	reName      = regexp.MustCompile(`^[A-Za-z0-9_]{3,16}$`)
	reLogName   = regexp.MustCompile(`^[A-Za-z0-9_]{1,16}$`)
	reListReply = regexp.MustCompile(`There are (\d+) of a max of (\d+) players online:?\s*(.*)$`)
)

// StripANSI removes terminal colour codes the container image emits.
func StripANSI(s string) string { return reANSI.ReplaceAllString(s, "") }

// RedactIPs removes IPv4/IPv6 addresses (player connection addresses appear in
// several vanilla log lines). Playkeeper never stores or displays them.
func RedactIPs(s string) string {
	s = reIPv6Port.ReplaceAllString(s, "/[ip redacted]")
	return reIPv4Port.ReplaceAllStringFunc(s, func(m string) string {
		if strings.HasPrefix(m, "/") {
			return "/[ip redacted]"
		}
		return "[ip redacted]"
	})
}

// CleanLine prepares a raw container line for display and storage.
func CleanLine(s string) string { return RedactIPs(StripANSI(s)) }

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
