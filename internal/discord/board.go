package discord

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
)

// maxBoardServers is how many servers the live status message lists before
// it says how many more there are.
const maxBoardServers = 25

// Board is the live status of every server on a dashboard, for a Notifier
// that posts about all of them in one message instead of one server.
type Board struct {
	Servers []BoardServer
}

// BoardServer is one server's line in the live status message.
type BoardServer struct {
	Server ServerInfo
	Status Status
}

// embed renders the board as the dashboard's settings screen previews it:
// one line per server, then where to join.
func (b Board) embed(dash ServerInfo) embed {
	var lines []string
	color := colorGrey
	join, joinOnline := "", false
	for i, s := range b.Servers {
		if i == maxBoardServers {
			lines = append(lines, "and "+strconv.Itoa(len(b.Servers)-i)+" more")
			break
		}
		lines = append(lines, s.line())
		online := s.Status.State == StateOnline
		switch s.Status.State {
		case StateCrashed:
			color = colorRed
		case StateOnline:
			if color != colorRed {
				color = colorGreen
			}
		case StateStarting:
			if color == colorGrey {
				color = colorAmber
			}
		case StateOffline:
		}
		// The join line names the first online server, or else the first.
		if addr := joinAddress(s.Server.Address); addr != "" && (join == "" || online && !joinOnline) {
			join, joinOnline = addr, online
		}
	}
	if len(lines) == 0 {
		lines = append(lines, "No servers yet.")
	}
	text := strings.Join(lines, "\n")
	link := dashboardLink(dash.DashboardURL)
	if link != "" {
		text += "\n\n[Open the dashboard](" + link + ")"
	}
	footer := "Live status from Playkeeper"
	if join != "" {
		// Footers are plain text, so the address goes without its code marks.
		footer = "Join: " + strings.Trim(join, "`")
	}
	e := embed{Description: text, Color: color, Footer: &embedFooter{Text: footer}}
	e.fit()
	return e
}

// line is "🟢 **Survival** · Online · 3 of 10 · mara_k, tobi2009".
func (s BoardServer) line() string {
	name := userText(s.Server.Name, 100)
	if name == "" {
		name = "Minecraft server"
	}
	var dot, state string
	switch s.Status.State {
	case StateOnline:
		dot, state = "🟢", "Online"
	case StateStarting:
		dot, state = "🟠", "Starting"
	case StateCrashed:
		dot, state = "🔴", "Crashed"
	default:
		dot, state = "⚪", "Stopped"
	}
	parts := []string{dot + " **" + name + "**", state}
	if s.Status.State == StateOnline {
		online := max(s.Status.PlayersOnline, len(s.Status.Players))
		count := strconv.Itoa(online)
		if s.Status.MaxPlayers > 0 {
			count += " of " + strconv.Itoa(s.Status.MaxPlayers)
		}
		parts = append(parts, count)
		if len(s.Status.Players) > 0 {
			parts = append(parts, clip(playerList(s.Status.Players), 300))
		}
	}
	return strings.Join(parts, " · ")
}

// states is each server's state on the board, without who is playing.
func (b Board) states() string {
	var sb strings.Builder
	for _, s := range b.Servers {
		sb.WriteString(s.Server.ID + "\x00" + string(s.Status.State) + "\n")
	}
	return sb.String()
}

// UpdateBoard gives a Notifier that serves a whole dashboard the status of
// every server. The live status message then lists them all, instead of
// the one server UpdateStatus describes, and is updated as UpdateStatus
// says.
func (n *Notifier) UpdateBoard(b Board) {
	list := make([]BoardServer, len(b.Servers))
	for i, s := range b.Servers {
		s.Status.Players = append([]string(nil), s.Status.Players...)
		list[i] = s
	}
	n.mu.Lock()
	n.board, n.haveStatus = &Board{Servers: list}, true
	n.mu.Unlock()
	n.signal()
}

// WebhookName asks Discord for the webhook's name, as set in the channel's
// integrations, to show which webhook is connected. It only reads; nothing
// is posted.
func WebhookName(ctx context.Context, hc *http.Client, w Webhook) (string, error) {
	if w.IsZero() {
		return "", errNotConnected()
	}
	c := newClient(hc)
	rctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	u := apiBase + "/webhooks/" + w.id + "/" + *w.token
	req, err := http.NewRequestWithContext(rctx, http.MethodGet, u, nil)
	if err != nil {
		return "", unprepared()
	}
	req.Header.Set("User-Agent", userAgent())
	resp, err := c.hc.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", unreachable(w, err)
	}
	defer resp.Body.Close()
	data, complete := readBody(resp)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var e apiError
		if complete {
			_ = json.Unmarshal(data, &e)
		}
		return "", classify(resp.StatusCode, e, resp.Header, w, false)
	}
	var info struct {
		Name string `json:"name"`
	}
	if !complete || json.Unmarshal(data, &info) != nil {
		return "", &Error{Code: CodeUnexpected, Msg: "Discord answered in a way Playkeeper did not expect.", Hint: "Try again later. If it keeps happening, please report it."}
	}
	return clip(oneLine(info.Name), 80), nil
}
