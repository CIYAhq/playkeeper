package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/CIYAhq/playkeeper/internal/api"
	"github.com/CIYAhq/playkeeper/internal/invites"
	"github.com/CIYAhq/playkeeper/internal/minecraft"
	"github.com/CIYAhq/playkeeper/internal/mojang"
)

// A player's page under Players: what Playkeeper remembers of their
// sessions, plus a private message and a ban.

const (
	profileDays   = 14
	profileRecent = 50
	maxMessage    = 200
)

// dayParts name the parts of the day a profile says someone mostly plays
// in, with the local hour each one starts at.
var dayParts = []struct {
	name  string
	start int
}{{"night", 0}, {"morning", 5}, {"afternoon", 12}, {"evening", 17}, {"night", 22}}

func (s *server) hProfile(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	p, err := s.Profile(q.Get("name"), q.Get("tz"), s.now())
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

// Profile is one player's page. A player is known if Playkeeper remembers a
// session of theirs, or they are on the allowlist or an operator.
func (s *server) Profile(name, tzName string, now time.Time) (api.PlayerProfile, error) {
	if !minecraft.ValidPlayerName(name) {
		return api.PlayerProfile{}, errInvalid("Minecraft usernames are 3–16 letters, numbers or underscores.")
	}
	loc, err := time.LoadLocation(tzName)
	if err != nil || tzName == "" {
		return api.PlayerProfile{}, errInvalid("tz must be an IANA time zone name")
	}
	rows, err := s.db.Query(`SELECT id, player, COALESCE(uuid, ''), start_ts, end_ts, end_reason, start_uncertain, end_uncertain, source
		FROM sessions WHERE server_id = ? AND player = ? COLLATE NOCASE ORDER BY start_ts DESC`, s.id, name)
	if err != nil {
		return api.PlayerProfile{}, err
	}
	sessions, err := scanSessions(rows, now)
	if err != nil {
		return api.PlayerProfile{}, err
	}
	p := api.PlayerProfile{Name: name, TZ: tzName, Days: []api.PlayerDay{}, Recent: []api.Session{}}
	known := len(sessions) > 0
	if known {
		p.Name = sessions[0].Player
	}
	for i, ss := range sessions {
		if p.UUID == "" {
			p.UUID = ss.UUID
		}
		p.Sessions++
		p.PlaytimeSeconds += ss.DurationSeconds
		p.LongestSeconds = max(p.LongestSeconds, ss.DurationSeconds)
		if ss.StartUncertain || ss.EndUncertain {
			p.PlaytimeUncertain = true
		}
		if ss.End == nil && !p.Online {
			start := ss.Start
			p.Online, p.OnlineSince = true, &start
		}
		if i < profileRecent {
			p.Recent = append(p.Recent, ss)
		}
	}
	if known {
		first := sessions[len(sessions)-1].Start
		p.FirstSeen = &first
	}
	if list, err := s.whitelist(); err == nil {
		for _, e := range list {
			if strings.EqualFold(e.Name, name) {
				known, p.Allowlisted, p.Name = true, true, e.Name
				if e.UUID != "" {
					p.UUID = e.UUID
				}
			}
		}
	}
	if ops, err := s.operators(); err == nil {
		for _, e := range ops {
			if strings.EqualFold(e.Name, name) {
				known, p.Operator = true, true
				if p.UUID == "" {
					p.UUID = e.UUID
				}
			}
		}
	}
	if banned, err := s.bannedNames(); err == nil && slices.ContainsFunc(banned, func(n string) bool { return strings.EqualFold(n, name) }) {
		known, p.Banned = true, true
	}
	s.mu.Lock()
	if s.players != nil {
		for _, n := range s.players.Names {
			if strings.EqualFold(n, name) {
				known, p.Online = true, true
			}
		}
	}
	s.mu.Unlock()
	if !known {
		return api.PlayerProfile{}, errNotFound("Player")
	}
	localNow := now.In(loc)
	today := time.Date(localNow.Year(), localNow.Month(), localNow.Day(), 0, 0, 0, 0, loc)
	from := today.AddDate(0, 0, -(profileDays - 1))
	parts := map[string]int64{}
	for d := from; !d.After(today); d = d.AddDate(0, 0, 1) {
		dayEnd := d.AddDate(0, 0, 1)
		day := api.PlayerDay{Date: d.Format("2006-01-02")}
		for _, ss := range sessions {
			end := now
			if ss.End != nil {
				end = *ss.End
			}
			lo, hi := maxTime(ss.Start, d), minTime(end, dayEnd)
			if !hi.After(lo) {
				continue
			}
			day.PlaytimeSeconds += int64(hi.Sub(lo).Seconds())
			countDayParts(lo, hi, loc, parts)
		}
		p.Days = append(p.Days, day)
	}
	var most int64
	for _, part := range []string{"morning", "afternoon", "evening", "night"} {
		if parts[part] > most {
			p.Mostly, most = part, parts[part]
		}
	}
	return p, nil
}

// countDayParts adds the time from lo to hi to the parts of the day it falls
// in, by the local clock.
func countDayParts(lo, hi time.Time, loc *time.Location, parts map[string]int64) {
	for t := lo; t.Before(hi); {
		name, next := dayPart(t.In(loc))
		if !next.After(t) {
			next = t.Add(time.Hour)
		}
		stop := minTime(next, hi)
		parts[name] += int64(stop.Sub(t).Seconds())
		t = stop
	}
}

// dayPart is the part of the day local time lt falls in, and when it ends.
func dayPart(lt time.Time) (string, time.Time) {
	y, m, d := lt.Date()
	i := len(dayParts) - 1
	for lt.Hour() < dayParts[i].start {
		i--
	}
	if i+1 < len(dayParts) {
		return dayParts[i].name, time.Date(y, m, d, dayParts[i+1].start, 0, 0, 0, lt.Location())
	}
	return dayParts[i].name, time.Date(y, m, d+1, 0, 0, 0, 0, lt.Location())
}

// validMessage checks a private message: one line of printable text.
func validMessage(m string) (string, error) {
	m = strings.TrimSpace(m)
	if m == "" || utf8.RuneCountInString(m) > maxMessage || strings.ContainsFunc(m, func(r rune) bool { return !unicode.IsPrint(r) }) {
		return "", errInvalid("Write a message of up to %d characters on one line.", maxMessage)
	}
	return m, nil
}

// tellraw is the command that shows msg to one player the way the game shows
// a whisper, in their own language. A plain tell from RCON would say the
// message came from "Rcon".
func tellraw(name, msg string) (string, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	err := enc.Encode(map[string]any{
		"translate": "commands.message.display.incoming",
		"with":      []string{"Server", msg},
		"color":     "gray",
		"italic":    true,
	})
	if err != nil {
		return "", err
	}
	return "tellraw " + name + " " + strings.TrimSpace(b.String()), nil
}

// hMessage sends one player a private message. The audit log records that a
// message was sent, never what it said.
func (s *server) hMessage(w http.ResponseWriter, r *http.Request) {
	var req api.PlayerMessageRequest
	if err := decode(r, &req); err != nil {
		writeError(w, err)
		return
	}
	actor, err := validActor(req.Actor)
	if err != nil {
		writeError(w, err)
		return
	}
	if !minecraft.ValidPlayerName(req.Name) {
		writeError(w, errInvalid("Minecraft usernames are 3–16 letters, numbers or underscores."))
		return
	}
	msg, err := validMessage(req.Message)
	if err != nil {
		writeError(w, err)
		return
	}
	cmd, err := tellraw(req.Name, msg)
	if err != nil {
		writeError(w, err)
		return
	}
	if !s.online(r.Context()) {
		writeError(w, errConflict("Start the server to message players.", ""))
		return
	}
	out, err := s.rconCommand(cmd)
	if err != nil {
		writeError(w, &apiError{Status: http.StatusBadGateway, Code: api.CodeInternal, Msg: "The server did not respond: " + err.Error()})
		return
	}
	if unknownPlayer(minecraft.StripANSI(out)) {
		s.audit(actor, "player.message", req.Name, "failed", "not online")
		writeError(w, errConflict(req.Name+" is not online.", ""))
		return
	}
	s.audit(actor, "player.message", req.Name, "succeeded", "")
	writeJSON(w, http.StatusOK, map[string]any{"message": "Message sent."})
}

func (s *server) hBan(w http.ResponseWriter, r *http.Request) {
	var req api.PlayerRequest
	if err := decode(r, &req); err != nil {
		writeError(w, err)
		return
	}
	actor, err := validActor(req.Actor)
	if err != nil {
		writeError(w, err)
		return
	}
	out, ok := s.playerCommand(w, r, req.Name, actor, "player.banned", "ban "+req.Name+" Banned from the Playkeeper dashboard", unknownPlayer)
	if !ok {
		return
	}
	s.letGo(r.Context(), req.Name)
	writeJSON(w, http.StatusOK, map[string]any{"message": out})
}

// bannedNames reads the ban list the server keeps in banned-players.json.
func (s *server) bannedNames() ([]string, error) {
	var entries []struct {
		Name string `json:"name"`
	}
	if err := s.readPlayerList("banned-players.json", &entries); err != nil {
		return nil, gameFileError(err, "The ban list could not be read.")
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name)
	}
	return names, nil
}

// leaveWait bounds how long a kick or ban waits for the server log to show
// the player leaving.
var leaveWait = 2 * time.Second

// letGo follows a kick or ban, so whoever did it sees the player offline
// straight away: it drops them from the last sample of who is online, and
// waits up to leaveWait for the server log to close their session, which
// happens a moment after the command.
func (s *server) letGo(ctx context.Context, name string) {
	s.mu.Lock()
	if p := s.players; p != nil {
		names := slices.DeleteFunc(slices.Clone(p.Names), func(n string) bool { return strings.EqualFold(n, name) })
		if gone := len(p.Names) - len(names); gone > 0 {
			snap := *p
			snap.Names, snap.Online = names, max(0, p.Online-gone)
			s.players = &snap
		}
	}
	s.mu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, leaveWait)
	defer cancel()
	for {
		var open int
		if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sessions WHERE server_id = ? AND player = ? COLLATE NOCASE AND end_ts IS NULL`, s.id, name).Scan(&open); err != nil || open == 0 {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// addToStoppedWhitelist writes a player into a stopped server's
// whitelist.json, which it reads when it starts. It holds the operation lock
// so the server can't start meanwhile; a running server must be told with
// the whitelist command instead, or it would write its own list back.
func (s *server) addToStoppedWhitelist(r *http.Request, name, uuid, actor string) (api.WhitelistChange, error) {
	release, ok := s.holdOpLock()
	if !ok {
		return api.WhitelistChange{}, s.busyError()
	}
	defer release()
	sc, err := s.serverConfig()
	if err != nil {
		return api.WhitelistChange{}, err
	}
	if sc == nil {
		return api.WhitelistChange{}, errNotCreated()
	}
	if _, running, err := s.containerRunning(r.Context()); err != nil {
		return api.WhitelistChange{}, err
	} else if running {
		return api.WhitelistChange{}, errConflict("The server is starting or stopping. Try again in a minute.", "")
	}
	if err := s.ensureDirs(); err != nil {
		return api.WhitelistChange{}, err
	}
	d, err := s.gameFiles()
	if err != nil {
		return api.WhitelistChange{}, err
	}
	defer d.Close()
	file, err := d.ReadFile("whitelist.json", maxPlayerList)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return api.WhitelistChange{}, gameFileError(err, "The allowlist could not be read.")
	}
	out, added, err := invites.AddToWhitelist(file, mojang.Profile{ID: uuid, Name: name})
	if err != nil {
		s.audit(actor, "whitelist.add", name, "failed", err.Error())
		return api.WhitelistChange{}, errInvalid("%s", err.Error())
	}
	msg := "Player is already whitelisted"
	if added {
		if err := d.WriteFile("whitelist.json", out, 0o640); err != nil {
			s.audit(actor, "whitelist.add", name, "failed", err.Error())
			return api.WhitelistChange{}, gameFileError(err, "The allowlist could not be changed.")
		}
		msg = "Added " + name + " to the whitelist"
	}
	s.audit(actor, "whitelist.add", name, "succeeded", msg)
	list, err := s.whitelist()
	if err != nil {
		list = []api.WhitelistEntry{}
	}
	return api.WhitelistChange{Message: msg, Whitelist: list, Added: added}, nil
}
