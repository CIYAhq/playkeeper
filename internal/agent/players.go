package agent

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/CIYAhq/playkeeper/internal/api"
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
	if !minecraft.ValidPlayerName(name) {
		writeError(w, errInvalid("Minecraft usernames are 3–16 letters, numbers or underscores."))
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
		writeErr(w, http.StatusUnprocessableEntity, api.CodeInvalid, out, "Check the spelling of the Java Edition username.")
		return "", false
	}
	s.audit(actor, action, name, "succeeded", out)
	return out, true
}

func unknownPlayer(out string) bool {
	return strings.Contains(out, "does not exist") || strings.Contains(out, "Unknown") || strings.Contains(out, "No player was found")
}

func (s *server) whitelistChange(w http.ResponseWriter, r *http.Request, name, actor, verb string) {
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
	b, err := os.ReadFile(filepath.Join(s.dataDir(), "ops.json"))
	if os.IsNotExist(err) {
		return []api.OperatorEntry{}, nil
	}
	if err != nil {
		return nil, err
	}
	var entries []api.OperatorEntry
	if err := json.Unmarshal(b, &entries); err != nil {
		return nil, err
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
	writeJSON(w, http.StatusOK, map[string]any{"message": out})
}
