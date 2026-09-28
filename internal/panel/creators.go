package panel

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"

	"github.com/CIYAhq/playkeeper/internal/api"
	"github.com/CIYAhq/playkeeper/internal/invites"
	"github.com/CIYAhq/playkeeper/internal/pregen"
)

// Creators (the managed beta) create servers on the dashboard's own machine
// inside an allowance: how many servers, and how much memory between them
// (see invites.Allowance). A server a creator creates joins their servers
// and creator_servers. Only those count against the allowance, and only
// those may they delete or resize; a server the owner also gave them keeps
// its memory. s.creators serialises every such change, so two requests at
// once can't both fit into what is left.

// allowanceUse is what a creator's servers take of their allowance.
type allowanceUse struct {
	servers, memoryMB int
	// memory is each of their servers' memory, and others' the memory of
	// the other servers they can use.
	memory, others map[string]int
}

// creatorServers lists the servers userID created.
func (s *Server) creatorServers(userID int64) ([]string, error) {
	rows, err := s.db.Query(`SELECT server_id FROM creator_servers WHERE user_id = ?`, userID)
	if err != nil {
		return nil, errDB
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, errDB
		}
		ids = append(ids, id)
	}
	if rows.Err() != nil {
		return nil, errDB
	}
	return ids, nil
}

// allowanceUse counts the servers a created that the dashboard's machine m
// still has, and their memory, leaving out except's.
func (s *Server) allowanceUse(ctx context.Context, a access, m machine, except string) (allowanceUse, error) {
	owned, err := s.creatorServers(a.UserID)
	if err != nil {
		return allowanceUse{}, err
	}
	var list []api.ServerStatus
	status, err := m.agent.Do(asActor(ctx, a.Name), "GET", "/v1/servers", nil, nil, &list)
	if err == nil && status != http.StatusOK {
		err = fmt.Errorf("the agent answered %d to the server list", status)
	}
	if err != nil {
		return allowanceUse{}, err
	}
	use := allowanceUse{memory: map[string]int{}, others: map[string]int{}}
	for _, sv := range list {
		mb := 0
		if sv.Config != nil {
			mb = sv.Config.MemoryMB
		}
		switch {
		case slices.Contains(owned, sv.ID):
			use.servers++
			use.memory[sv.ID] = mb
			if sv.ID != except {
				use.memoryMB += mb
			}
		case a.covers(sv.ID):
			use.others[sv.ID] = mb
		}
	}
	return use, nil
}

// memoryRefusal says why server id may not get memoryMB of memory, for a
// creator whose servers use use, or "". A server they created must fit their
// allowance beside their others; one the owner gave them keeps its memory.
func memoryRefusal(al invites.Allowance, use allowanceUse, id string, memoryMB int) (msg, hint string) {
	if cur, ok := use.others[id]; ok {
		if memoryMB != cur {
			return "Only the owner can change this server's memory.", ""
		}
		return "", ""
	}
	if left := al.MemoryMB - use.memoryMB; memoryMB > left {
		return fmt.Sprintf("That's more memory than your allowance has left for this server: %s.", gbText(max(left, 0))), "Choose less, or ask the owner for more."
	}
	return "", ""
}

func gbText(mb int) string {
	if mb%1024 == 0 {
		return fmt.Sprintf("%d GB", mb/1024)
	}
	return fmt.Sprintf("%.1f GB", float64(mb)/1024)
}

// memoryField reads the memoryMB a JSON body asks for (0 when none) and
// leaves the body readable again for the request's forward.
func memoryField(w http.ResponseWriter, r *http.Request) (int, bool) {
	b, err := io.ReadAll(io.LimitReader(r.Body, 64<<10))
	if err != nil {
		writeErr(w, http.StatusBadRequest, api.CodeInvalid, "Invalid request body.", "")
		return 0, false
	}
	var req struct {
		MemoryMB int `json:"memoryMB"`
	}
	if len(bytes.TrimSpace(b)) > 0 && json.Unmarshal(b, &req) != nil {
		writeErr(w, http.StatusBadRequest, api.CodeInvalid, "Request body must be a JSON object.", "")
		return 0, false
	}
	r.Body = io.NopCloser(bytes.NewReader(b))
	return req.MemoryMB, true
}

// hCreateServer creates a server: as the route's forward for an admin of
// every server, and for a creator on the dashboard's own machine only,
// inside their allowance, the new server joining their servers.
func (s *Server) hCreateServer(w http.ResponseWriter, r *http.Request, sess *session) {
	a := sess.Access
	if !a.creator() {
		s.forwardThen("POST", "/v1/servers", s.claimCreatedBy)(w, r, sess)
		return
	}
	m, ok := s.machineFromPath(w, r)
	if !ok {
		return
	}
	if m.Kind != localKind {
		writeErr(w, http.StatusForbidden, api.CodeForbidden, "Creators create servers on the dashboard's own machine.", "")
		return
	}
	mb, ok := memoryField(w, r)
	if !ok {
		return
	}
	if mb <= 0 {
		writeErr(w, http.StatusBadRequest, api.CodeInvalid, "Choose the server's memory.", "")
		return
	}
	s.creators.Lock()
	defer s.creators.Unlock()
	use, err := s.allowanceUse(r.Context(), a, m, "")
	if err != nil {
		s.listFailure(w, err)
		return
	}
	if use.servers >= a.Allowance.Servers {
		writeErr(w, http.StatusConflict, api.CodeConflict, fmt.Sprintf("Your allowance has room for %s, and you have them.", serverCount(a.Allowance.Servers)),
			"Delete one to make room, or ask the owner for more.")
		return
	}
	if msg, hint := memoryRefusal(a.Allowance, use, "", mb); msg != "" {
		writeErr(w, http.StatusConflict, api.CodeConflict, msg, hint)
		return
	}
	s.forwardThen("POST", "/v1/servers", func(m machine, sess *session, raw json.RawMessage) {
		s.claimCreated(m, raw)
		s.claimForCreator(sess.Access, raw)
	})(w, r, sess)
}

func serverCount(n int) string {
	if n == 1 {
		return "1 server"
	}
	return fmt.Sprintf("%d servers", n)
}

// claimForCreator records a server a creator just created: it joins their
// servers and counts against their allowance.
func (s *Server) claimForCreator(a access, raw json.RawMessage) {
	var op struct {
		ServerID string `json:"serverId"`
	}
	if json.Unmarshal(raw, &op) != nil || !reMachineID.MatchString(op.ServerID) {
		return
	}
	ctx := context.Background()
	err := s.immediate(ctx, func(c *sql.Conn) error {
		var col string
		if err := c.QueryRowContext(ctx, `SELECT servers FROM project_members WHERE user_id = ? AND project_id = ?`, a.UserID, a.ProjectID).Scan(&col); err != nil {
			return err
		}
		sc, _ := invites.ParseScope(col)
		if !sc.All && !slices.Contains(sc.Servers, op.ServerID) {
			sc.Servers = append(sc.Servers, op.ServerID)
		}
		if _, err := c.ExecContext(ctx, `UPDATE project_members SET servers = ? WHERE user_id = ? AND project_id = ?`, sc.String(), a.UserID, a.ProjectID); err != nil {
			return err
		}
		_, err := c.ExecContext(ctx, `INSERT OR IGNORE INTO creator_servers(server_id, user_id, created_at) VALUES(?,?,?)`, op.ServerID, a.UserID, millis(s.now()))
		return err
	})
	if err != nil {
		s.log.Error("could not record the server a creator created", "server", op.ServerID, "creator", a.Name, "err", err)
	}
}

// hDeleteServer deletes a server: for an admin of every server as the
// route's forward, and for a creator only a server they created.
func (s *Server) hDeleteServer(w http.ResponseWriter, r *http.Request, sess *session) {
	forward := s.serverProxy("POST", "/v1/servers/{id}/delete")
	if !sess.Access.creator() {
		forward(w, r, sess)
		return
	}
	s.creators.Lock()
	defer s.creators.Unlock()
	owned, err := s.creatorServers(sess.Access.UserID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, api.CodeInternal, "Database error.", "")
		return
	}
	if !slices.Contains(owned, r.PathValue("id")) {
		writeErr(w, http.StatusForbidden, api.CodeForbidden, "You can delete only the servers you created.", "Ask the owner of this Playkeeper.")
		return
	}
	forward(w, r, sess)
}

// hServerSettings changes a server's settings. A creator's new memory must
// fit (see memoryRefusal).
func (s *Server) hServerSettings(w http.ResponseWriter, r *http.Request, sess *session) {
	forward := s.serverProxy("POST", "/v1/servers/{id}/settings")
	if !sess.Access.creator() {
		forward(w, r, sess)
		return
	}
	mb, ok := memoryField(w, r)
	if !ok {
		return
	}
	if mb == 0 {
		forward(w, r, sess)
		return
	}
	s.creators.Lock()
	defer s.creators.Unlock()
	if !s.creatorMemoryFits(w, r, sess.Access, r.PathValue("id"), mb) {
		return
	}
	forward(w, r, sess)
}

// creatorMemoryFits checks that a creator's server id may have memoryMB,
// and writes the refusal when not. The caller holds s.creators until the
// request is forwarded.
func (s *Server) creatorMemoryFits(w http.ResponseWriter, r *http.Request, a access, id string, memoryMB int) bool {
	m, ok := s.target(w, r)
	if !ok {
		return false
	}
	use, err := s.allowanceUse(r.Context(), a, m, id)
	if err != nil {
		s.listFailure(w, err)
		return false
	}
	if msg, hint := memoryRefusal(a.Allowance, use, id, memoryMB); msg != "" {
		writeErr(w, http.StatusConflict, api.CodeConflict, msg, hint)
		return false
	}
	return true
}

// creatorRestoreFits checks a creator's restore into an existing server: the
// server takes the backup's memory, or the one the request names, which
// must fit as any memory change does. The caller holds s.creators.
func (s *Server) creatorRestoreFits(w http.ResponseWriter, r *http.Request, a access, p api.RestorePreview) bool {
	mb, ok := memoryField(w, r)
	if !ok {
		return false
	}
	if mb == 0 {
		mb = p.MemoryMB
	}
	return s.creatorMemoryFits(w, r, a, p.ServerID, mb)
}

// capCatalog trims a machine's catalog for a creator: memory choices only
// up to what their allowance has left for the server named server (or a new
// one), and what they use and may use.
func (s *Server) capCatalog(ctx context.Context, a access, m machine, server string, c map[string]json.RawMessage) error {
	use, err := s.allowanceUse(ctx, a, m, server)
	if err != nil {
		return err
	}
	left := max(a.Allowance.MemoryMB-use.memoryMB, 0)
	switch cur, ok := use.others[server]; {
	case ok:
		left = cur
	case server == "" && m.Kind != localKind:
		left = 0
	}
	var opts []int
	var rec, most, free int
	_ = json.Unmarshal(c["memoryOptionsMB"], &opts)
	_ = json.Unmarshal(c["recommendedMemoryMB"], &rec)
	_ = json.Unmarshal(c["maxMemoryMB"], &most)
	_ = json.Unmarshal(c["memoryFreeMB"], &free)
	kept := []int{}
	for _, o := range opts {
		if o <= left {
			kept = append(kept, o)
		}
	}
	if rec > left {
		rec = 0
		if len(kept) > 0 {
			rec = kept[len(kept)-1]
		}
	}
	c["memoryOptionsMB"], _ = json.Marshal(kept)
	c["recommendedMemoryMB"], _ = json.Marshal(rec)
	c["maxMemoryMB"], _ = json.Marshal(min(most, left))
	c["memoryFreeMB"], _ = json.Marshal(min(free, left))
	used := use.memoryMB + use.memory[server]
	c["allowance"], _ = json.Marshal(map[string]int{"servers": a.Allowance.Servers, "memoryMB": a.Allowance.MemoryMB,
		"serversUsed": use.servers, "memoryUsedMB": used})
	return nil
}

// creatorPregenRadius is the most a creator may pre-generate around spawn,
// as the managed beta's terms say: the map it fills takes the disk every
// server shares.
const creatorPregenRadius = 2_500

// creatorPreset reports whether a creator may pre-generate preset id.
func creatorPreset(id string) bool {
	for _, p := range pregen.Presets() {
		if p.ID == id {
			return p.Radius <= creatorPregenRadius
		}
	}
	return false
}

// hPregen is where pre-generating a server's map stands, offering a creator
// only the sizes they may start.
func (s *Server) hPregen(w http.ResponseWriter, r *http.Request, sess *session) {
	if !sess.Access.creator() {
		s.serverProxy("GET", "/v1/servers/{id}/pregen")(w, r, sess)
		return
	}
	m, ok := s.target(w, r)
	if !ok {
		return
	}
	var raw json.RawMessage
	status, err := m.agent.Do(asActor(r.Context(), sess.User.Username), "GET", agentPath("/v1/servers/{id}/pregen", r), r.URL.Query(), nil, &raw)
	if err != nil {
		s.agentFailure(w, err)
		return
	}
	var v map[string]json.RawMessage
	var presets []api.PregenPreset
	if status != http.StatusOK || json.Unmarshal(raw, &v) != nil || json.Unmarshal(v["presets"], &presets) != nil {
		writeJSON(w, status, raw)
		return
	}
	kept := []api.PregenPreset{}
	for _, p := range presets {
		if p.Radius <= creatorPregenRadius {
			kept = append(kept, p)
		}
	}
	v["presets"], _ = json.Marshal(kept)
	writeJSON(w, status, v)
}

// hPregenStart starts pre-generating a server's map, for a creator only up
// to creatorPregenRadius.
func (s *Server) hPregenStart(w http.ResponseWriter, r *http.Request, sess *session) {
	forward := s.serverProxy("POST", "/v1/servers/{id}/pregen/start")
	if !sess.Access.creator() {
		forward(w, r, sess)
		return
	}
	b, err := io.ReadAll(io.LimitReader(r.Body, 64<<10))
	if err != nil {
		writeErr(w, http.StatusBadRequest, api.CodeInvalid, "Invalid request body.", "")
		return
	}
	var req struct {
		Preset string `json:"preset"`
	}
	if json.Unmarshal(b, &req) != nil {
		writeErr(w, http.StatusBadRequest, api.CodeInvalid, "Request body must be a JSON object.", "")
		return
	}
	if !creatorPreset(req.Preset) {
		writeErr(w, http.StatusForbidden, api.CodeForbidden, "Creators pre-generate up to 2,500 blocks around spawn.", "Pick a smaller size, or ask the owner.")
		return
	}
	r.Body = io.NopCloser(bytes.NewReader(b))
	forward(w, r, sess)
}
