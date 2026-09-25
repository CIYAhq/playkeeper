package discord

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
)

// ServerInfo describes the server in every message.
type ServerInfo struct {
	// Name is the server's name in Playkeeper.
	Name string
	// DashboardURL links to the server's page in the dashboard (https only);
	// "" leaves the link out.
	DashboardURL string
	// Address is what players type into Minecraft to join, as the dashboard
	// shows it ("play.example.com", "203.0.113.5:25566"); "" if unknown.
	Address string
	// Version is the Minecraft version, and MOTD the message of the day.
	Version string
	MOTD    string
}

func (info ServerInfo) plainName() string { return clip(oneLine(info.Name), 100) }

func (info ServerInfo) boldName() string {
	if name := userText(info.Name, 100); name != "" {
		return "**" + name + "**"
	}
	return "The Minecraft server"
}

// State is the server's state in the live status message.
type State string

const (
	StateOnline   State = "online"
	StateStarting State = "starting"
	StateOffline  State = "offline"
	StateCrashed  State = "crashed"
)

// Status is what the live status message shows about the server now.
type Status struct {
	State State
	// Since is when the server entered State; zero if unknown.
	Since time.Time
	// PlayersOnline and MaxPlayers count players and slots (0 if unknown);
	// Players are the names of those online.
	PlayersOnline int
	MaxPlayers    int
	Players       []string
}

// embed renders the status message. Discord shows <t:…:R> as a relative
// time ("2 hours ago") that stays current without edits.
func (s Status) embed(info ServerInfo) embed {
	var title, text string
	var color int
	switch s.State {
	case StateOnline:
		title, text, color = "Online", "Online", colorGreen
	case StateStarting:
		title, text, color = "Starting", "Starting up", colorAmber
	case StateCrashed:
		title, text, color = "Crashed", "Stopped unexpectedly", colorRed
	default:
		title, text, color = "Offline", "Offline", colorGrey
	}
	if !s.Since.IsZero() {
		sep := " since "
		if s.State == StateCrashed {
			sep = " "
		}
		text += sep + "<t:" + strconv.FormatInt(s.Since.Unix(), 10) + ":R>"
	}
	text += "."
	if motd := motdQuote(info.MOTD); motd != "" {
		text += "\n\n" + motd
	}
	e := info.card(title, text, color, time.Time{})
	e.Footer.Text = "Live status from Playkeeper"
	if s.State == StateOnline {
		online := max(s.PlayersOnline, len(s.Players))
		count := strconv.Itoa(online)
		if s.MaxPlayers > 0 {
			count += " of " + strconv.Itoa(s.MaxPlayers)
		}
		e.Fields = append(e.Fields, embedField{Name: "Players", Value: count, Inline: true})
	}
	if addr := joinAddress(info.Address); addr != "" {
		e.Fields = append(e.Fields, embedField{Name: "Address", Value: addr, Inline: true})
	}
	if v := userText(info.Version, 40); v != "" {
		e.Fields = append(e.Fields, embedField{Name: "Version", Value: v, Inline: true})
	}
	if s.State == StateOnline && len(s.Players) > 0 {
		e.Fields = append(e.Fields, embedField{Name: "Online now", Value: playerList(s.Players)})
	}
	e.fit()
	return e
}

// motdQuote is the message of the day as a markdown quote of up to two
// lines, as Minecraft shows it.
func motdQuote(motd string) string {
	var lines []string
	for _, l := range strings.Split(stripFormatting(motd), "\n") {
		if l = userText(l, 150); l != "" && len(lines) < 2 {
			lines = append(lines, "> "+l)
		}
	}
	return strings.Join(lines, "\n")
}

// playerList joins the names in alphabetical order, so that joins in a
// different order don't change the message, and ends with "and N more" when
// they don't all fit in a field.
func playerList(names []string) string {
	var clean []string
	for _, n := range names {
		if n = userText(n, 32); n != "" {
			clean = append(clean, n)
		}
	}
	slices.SortFunc(clean, func(a, b string) int { return strings.Compare(strings.ToLower(a), strings.ToLower(b)) })
	var b strings.Builder
	n := 0
	for i, name := range clean {
		if i > 0 {
			name = ", " + name
		}
		// Leave room to say how many names didn't fit after this one.
		need := textLen(name)
		if rest := len(clean) - i - 1; rest > 0 {
			need += textLen(fmt.Sprintf(" and %d more", rest))
		}
		if n+need > maxFieldValue {
			fmt.Fprintf(&b, " and %d more", len(clean)-i)
			break
		}
		b.WriteString(name)
		n += textLen(name)
	}
	return b.String()
}
