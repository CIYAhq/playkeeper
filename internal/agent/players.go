package agent

import (
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/CIYAhq/playkeeper/internal/api"
	"github.com/CIYAhq/playkeeper/internal/curated"
	"github.com/CIYAhq/playkeeper/internal/minecraft"
)

// Who can join, who can run commands, and kicking: each is a Minecraft
// console command sent over RCON, so the server must be online.

func (s *server) hWhitelist(w http.ResponseWriter, r *http.Request) {
	list, err := s.whitelist()
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

// playerCommand runs a console command about one player and audits it. It
// returns the server's reply, or false after writing an error.
func (s *server) playerCommand(w http.ResponseWriter, r *http.Request, name, actor, action, cmd string, failed func(reply string) bool) (string, bool) {
	_, bedrock := s.bedrockTag(name)
	if !minecraft.ValidPlayerName(name) && !bedrock {
		msg := "Minecraft usernames are 3–16 letters, numbers or underscores."
		if sc, err := s.serverConfig(); err == nil && crossplayOn(sc) {
			msg = "Minecraft usernames are 3–16 letters, numbers or underscores; a Bedrock player's is their Xbox gamertag after a dot, like .Steve."
		}
		writeError(w, errInvalid("%s", msg))
		return "", false
	}
	if !s.online(r.Context()) {
		writeError(w, errConflict("Start the server to change this.", ""))
		return "", false
	}
	out, err := s.rconCommand(cmd)
	if err != nil {
		writeError(w, &apiError{Status: http.StatusBadGateway, Code: api.CodeInternal, Msg: "The server did not respond: " + err.Error()})
		return "", false
	}
	out = minecraft.StripANSI(out)
	if failed(out) {
		s.audit(actor, action, name, "failed", out)
		hint := "Check the spelling of the Java Edition username."
		if bedrock {
			hint = "Check the spelling of the Xbox gamertag."
		}
		writeErr(w, http.StatusUnprocessableEntity, api.CodeInvalid, out, hint)
		return "", false
	}
	s.audit(actor, action, name, "succeeded", out)
	return out, true
}

var reGamertag = regexp.MustCompile(`^[A-Za-z0-9_]{1,16}$`)

// bedrockTag is the Xbox gamertag in name, when the server has crossplay
// and name is a Bedrock player's as Floodgate shows it: its prefix, then
// the gamertag with spaces as underscores.
func (s *server) bedrockTag(name string) (string, bool) {
	tag, ok := strings.CutPrefix(name, curated.FloodgatePrefix)
	if !ok || !reGamertag.MatchString(tag) {
		return "", false
	}
	if sc, err := s.serverConfig(); err != nil || !crossplayOn(sc) {
		return "", false
	}
	return tag, true
}

// bedrockListWait is how long a change to the allowlist Floodgate makes is
// waited for.
var bedrockListWait = 10 * time.Second

// bedrockWhitelistChange adds a Bedrock player to the allowlist, or takes
// one off, with Floodgate's own command: Floodgate looks the gamertag up at
// GeyserMC, which a Java server can't, and writes the allowlist a moment
// after it answers, with nothing to say. The allowlist shows how it went.
func (s *server) bedrockWhitelistChange(w http.ResponseWriter, r *http.Request, name, tag, actor, verb string) {
	// Floodgate names a player with a long gamertag within Java's 16
	// letters.
	java := name[:min(len(name), 16)]
	listed := func(list []api.WhitelistEntry) bool {
		return slices.ContainsFunc(list, func(e api.WhitelistEntry) bool { return strings.EqualFold(e.Name, java) })
	}
	want := verb == "add"
	if !s.online(r.Context()) {
		writeError(w, errConflict("Start the server to change this.", ""))
		return
	}
	if list, err := s.whitelist(); err == nil && want && listed(list) {
		writeJSON(w, http.StatusOK, api.WhitelistChange{Message: name + " is already on the allowlist.", Whitelist: list})
		return
	}
	if _, err := s.rconCommand("fwhitelist " + verb + " " + tag); err != nil {
		writeError(w, &apiError{Status: http.StatusBadGateway, Code: api.CodeInternal, Msg: "The server did not respond: " + err.Error()})
		return
	}
	deadline := time.Now().Add(bedrockListWait)
	for {
		list, err := s.whitelist()
		if err == nil && listed(list) == want {
			msg := "Added " + name + " to the whitelist"
			if !want {
				msg = "Removed " + name + " from the whitelist"
			}
			s.audit(actor, "whitelist."+verb, name, "succeeded", msg)
			writeJSON(w, http.StatusOK, api.WhitelistChange{Message: msg, Whitelist: list, Added: want})
			return
		}
		if time.Now().After(deadline) {
			break
		}
		select {
		case <-r.Context().Done():
			return
		case <-time.After(200 * time.Millisecond):
		}
	}
	msg, hint := "Floodgate found no Bedrock player called "+tag+".", "Check the spelling of the Xbox gamertag. Floodgate looks it up at GeyserMC, so this also fails while GeyserMC can't be reached."
	if !want {
		msg, hint = name+" is still on the allowlist: Floodgate didn't take them off.", "Try again in a minute."
	}
	s.audit(actor, "whitelist."+verb, name, "failed", msg)
	writeErr(w, http.StatusUnprocessableEntity, api.CodeInvalid, msg, hint)
}

func unknownPlayer(out string) bool {
	return strings.Contains(out, "does not exist") || strings.Contains(out, "Unknown") || strings.Contains(out, "No player was found")
}

func (s *server) whitelistChange(w http.ResponseWriter, r *http.Request, name, actor, verb string) {
	if tag, ok := s.bedrockTag(name); ok {
		s.bedrockWhitelistChange(w, r, name, tag, actor, verb)
		return
	}
	out, ok := s.playerCommand(w, r, name, actor, "whitelist."+verb, "whitelist "+verb+" "+name, unknownPlayer)
	if !ok {
		return
	}
	list, err := s.whitelist()
	if err != nil {
		list = []api.WhitelistEntry{}
	}
	writeJSON(w, http.StatusOK, api.WhitelistChange{Message: out, Whitelist: list, Added: verb == "add" && strings.HasPrefix(out, "Added")})
}

// hWhitelistAdd adds a player with the whitelist command. With a UUID (an
// invite link, whose name Mojang checked) a stopped server's list is written
// directly instead.
func (s *server) hWhitelistAdd(w http.ResponseWriter, r *http.Request) {
	var req api.WhitelistRequest
	if err := decode(r, &req); err != nil {
		writeError(w, err)
		return
	}
	actor, err := validActor(req.Actor)
	if err != nil {
		writeError(w, err)
		return
	}
	if req.UUID != "" && minecraft.ValidPlayerName(req.Name) && !s.online(r.Context()) {
		change, err := s.addToStoppedWhitelist(r, req.Name, req.UUID, actor)
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, change)
		return
	}
	s.whitelistChange(w, r, req.Name, actor, "add")
}

func (s *server) hWhitelistRemove(w http.ResponseWriter, r *http.Request) {
	actor, err := validActor(r.URL.Query().Get("actor"))
	if err != nil {
		writeError(w, err)
		return
	}
	s.whitelistChange(w, r, r.PathValue("name"), actor, "remove")
}

// operators reads ops.json: the players who can run commands in the game.
func (s *server) operators() ([]api.OperatorEntry, error) {
	var entries []api.OperatorEntry
	if err := s.readPlayerList("ops.json", &entries); err != nil {
		return nil, gameFileError(err, "The list of operators could not be read.")
	}
	if entries == nil {
		entries = []api.OperatorEntry{}
	}
	return entries, nil
}

func (s *server) hOperators(w http.ResponseWriter, r *http.Request) {
	list, err := s.operators()
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *server) operatorChange(w http.ResponseWriter, r *http.Request, name, actor, verb string) {
	cmd, action := "op "+name, "operator.add"
	if verb == "remove" {
		cmd, action = "deop "+name, "operator.remove"
	}
	out, ok := s.playerCommand(w, r, name, actor, action, cmd, unknownPlayer)
	if !ok {
		return
	}
	list, _ := s.operators()
	writeJSON(w, http.StatusOK, map[string]any{"message": out, "operators": list})
}

func (s *server) hOperatorAdd(w http.ResponseWriter, r *http.Request) {
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
	s.operatorChange(w, r, req.Name, actor, "add")
}

func (s *server) hOperatorRemove(w http.ResponseWriter, r *http.Request) {
	actor, err := validActor(r.URL.Query().Get("actor"))
	if err != nil {
		writeError(w, err)
		return
	}
	s.operatorChange(w, r, r.PathValue("name"), actor, "remove")
}

func (s *server) hKick(w http.ResponseWriter, r *http.Request) {
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
	out, ok := s.playerCommand(w, r, req.Name, actor, "player.kicked", "kick "+req.Name+" Kicked from the Playkeeper dashboard", unknownPlayer)
	if !ok {
		return
	}
	s.letGo(r.Context(), req.Name)
	writeJSON(w, http.StatusOK, map[string]any{"message": out})
}
