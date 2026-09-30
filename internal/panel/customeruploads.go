package panel

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"

	"github.com/CIYAhq/playkeeper/internal/api"
	"github.com/CIYAhq/playkeeper/internal/invites"
)

// Creators, customers included, make a new server from an uploaded world or
// backup (step 9 of the managed-beta plan): on their machine, as the
// servers they create, inside their allowance, and against their disk
// limit. The dashboard names that limit on each upload, never the browser,
// so the machine counts what they send from the first byte, and the server
// they make joins their servers.

// accountLimit names the disk limit of the account's servers on a machine.
func accountLimit(userID int64) string { return diskLimitPrefix + strconv.FormatInt(userID, 10) }

// uploadCreates is what making a new server from an upload takes for a: a
// creator's own upload, named with their disk limit, makes a server of
// their own; anyone else's needs every server.
func uploadCreates(a access, limit string) action {
	if a.creator() && limit == accountLimit(a.UserID) {
		return actCreateOwnServers
	}
	return actCreateServers
}

// newServerRefusal is why creator a may not make another server of memoryMB
// on m, or nil: only on their machine, once placed, and inside their
// allowance. memoryMB 0 checks only that there's room for a server. The
// caller holds s.creators until the request is forwarded.
func (s *Server) newServerRefusal(ctx context.Context, a access, m machine, memoryMB int) error {
	if err := s.homeRefusal(ctx, a, m); err != nil {
		return err
	}
	use, err := s.allowanceUse(ctx, a, m, "")
	if err != nil {
		return err
	}
	if use.servers >= a.Allowance.Servers {
		return &invites.Error{Code: api.CodeConflict, Status: http.StatusConflict,
			Msg: fmt.Sprintf("Your allowance has room for %s, and you have them.", serverCount(a.Allowance.Servers)), Hint: "Delete one to make room, or ask the owner for more."}
	}
	if memoryMB > 0 {
		if msg, hint := memoryRefusal(a.Allowance, use, "", memoryMB); msg != "" {
			return &invites.Error{Code: api.CodeConflict, Status: http.StatusConflict, Msg: msg, Hint: hint}
		}
	}
	return nil
}

// refuseNewServer writes creator a's refusal to make another server of
// memoryMB on m, and reports whether it did.
func (s *Server) refuseNewServer(w http.ResponseWriter, r *http.Request, a access, m machine, memoryMB int) bool {
	err := s.newServerRefusal(r.Context(), a, m, memoryMB)
	var e *invites.Error
	switch {
	case err == nil:
		return false
	case errors.As(err, &e):
		writeRefusal(w, err)
	default:
		s.listFailure(w, err)
	}
	return true
}

// creatorMade records a server a creator made: its machine, their servers,
// their disk limit and their backups.
func (s *Server) creatorMade(ctx context.Context) func(machine, *session, json.RawMessage) {
	return func(m machine, sess *session, raw json.RawMessage) {
		s.claimCreated(m, raw)
		if id := createdServer(raw); id != "" {
			s.claimForCreator(sess.Access, id)
			s.kickDiskLimits()
			s.startCreatorBackups(ctx, m, sess.Access, id)
		}
	}
}

// hWorldImportOpen opens a world upload for a new server. A creator's names
// their disk limit; nobody else's names one.
func (s *Server) hWorldImportOpen(w http.ResponseWriter, r *http.Request, sess *session) {
	m, ok := s.machineFromPath(w, r)
	if !ok {
		return
	}
	body := map[string]any{}
	if a := sess.Access; a.creator() {
		s.creators.Lock()
		defer s.creators.Unlock()
		if s.refuseNewServer(w, r, a, m, 0) {
			return
		}
		body["diskLimit"] = accountLimit(a.UserID)
	}
	b, _ := json.Marshal(body)
	r.Body = io.NopCloser(bytes.NewReader(b))
	s.forward("POST", "/v1/world-imports")(w, r, sess)
}

// hRestoreUploadNew uploads a backup for a new server. A creator's names
// their disk limit.
func (s *Server) hRestoreUploadNew(w http.ResponseWriter, r *http.Request, sess *session) {
	m, ok := s.target(w, r)
	if !ok {
		return
	}
	q := url.Values{}
	if a := sess.Access; a.creator() {
		s.creators.Lock()
		if s.refuseNewServer(w, r, a, m, 0) {
			s.creators.Unlock()
			return
		}
		s.creators.Unlock()
		q.Set("diskLimit", accountLimit(a.UserID))
	}
	s.relayUpload(w, r, m, "/v1/restore/upload", q, "application/gzip", sess)
}

// hWorldImportCreate makes a server from a world upload: as the route's
// forward for anyone who may create servers, and for a creator from their
// own upload only (importGuard), inside their allowance, the server joining
// theirs.
func (s *Server) hWorldImportCreate(w http.ResponseWriter, r *http.Request, sess *session) {
	a := sess.Access
	if !a.creator() {
		s.forwardLong("/v1/world-imports/{imp}/create")(w, r, sess)
		return
	}
	m, ok := s.machineFromPath(w, r)
	if !ok {
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
	if s.refuseNewServer(w, r, a, m, mb) {
		return
	}
	s.forwardLongThen("/v1/world-imports/{imp}/create", s.creatorMade(r.Context()))(w, r, sess)
}
