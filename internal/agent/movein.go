package agent

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"time"

	"github.com/CIYAhq/playkeeper/internal/api"
)

// Moving a server in (step 8 of the fleet plan). The dashboard moves a
// customer's servers to another machine through each one's whole folder
// (hMoveOut), uploaded here as for a new server and applied with move-in,
// which keeps the server's id: the dashboard's records of it, its members,
// invites, slug and address, stay as they are. The dashboard's pages forward
// restores to apply, whose server always gets a new id and which refuses a
// body naming one, so only the dashboard picks an id, for a server it moves.
// The server is off the public page until the dashboard gives it the
// settings it had (hMoveStatePut).

// moveIn is a checked api.MoveInRequest.
type moveIn struct {
	spec  newServerSpec
	mem   int
	prev  api.ServerConfig
	start bool
}

// checkMoveIn checks what a move-in asks for.
func (a *Agent) checkMoveIn(req api.MoveInRequest, actor string) (moveIn, error) {
	if !reServerID.MatchString(req.ServerID) {
		return moveIn{}, errInvalid("A server's id is 10 lower-case letters and digits.")
	}
	name, err := validName(req.Name)
	if err != nil {
		return moveIn{}, err
	}
	if req.Slug != "" && !reSlug.MatchString(req.Slug) {
		return moveIn{}, errInvalid("A server's slug is lower-case letters, digits and dashes.")
	}
	if !playStyles[req.PlayStyle] {
		return moveIn{}, errInvalid("Unknown play style %q.", req.PlayStyle)
	}
	by, err := validActor(req.EULAAcceptedBy)
	if err != nil || req.EULAAcceptedAt.IsZero() {
		return moveIn{}, errInvalid("A server moves in only with the Minecraft EULA acceptance it had: who accepted it, and when.")
	}
	if err := validAccount(req.Account); err != nil {
		return moveIn{}, err
	}
	if a.nameTaken(name, "", req.Account) {
		name = a.uniqueName(name, req.Account)
	}
	created := req.CreatedAt
	if created.IsZero() {
		created = a.now()
	}
	return moveIn{
		spec:  newServerSpec{id: req.ServerID, slug: req.Slug, name: name, account: req.Account, actor: actor, record: offThePage},
		mem:   req.MemoryMB,
		prev:  api.ServerConfig{EULAAcceptedAt: req.EULAAcceptedAt.UTC(), EULAAcceptedBy: by, CreatedAt: created.UTC(), PlayStyle: req.PlayStyle},
		start: req.Start,
	}, nil
}

// offThePage records a server moved in as off the public page, where the
// default would list it.
func offThePage(tx *sql.Tx, id string) error {
	_, err := tx.Exec(`UPDATE servers SET public_page = 0 WHERE id = ?`, id)
	return err
}

// hRestoreMoveIn applies a staged upload for a new server as a server moved
// in from another machine (api.MoveInRequest), claiming it as apply does.
func (a *Agent) hRestoreMoveIn(w http.ResponseWriter, r *http.Request) {
	var req api.MoveInRequest
	if err := decode(r, &req); err != nil {
		writeError(w, err)
		return
	}
	actor, err := validActor(req.Actor)
	if err != nil {
		writeError(w, err)
		return
	}
	refuse := func(err error) {
		a.audit(actor, "server.moved_in", req.ServerID, "refused", err.Error())
		writeError(w, err)
	}
	in, err := a.checkMoveIn(req, actor)
	if err != nil {
		refuse(err)
		return
	}
	rid := r.PathValue("id")
	release, err := a.claimStage(rid)
	if err != nil {
		writeError(w, err)
		return
	}
	started := false
	defer func() {
		if !started {
			release()
		}
	}()
	st, err := a.loadStage(rid)
	if err != nil {
		writeError(w, err)
		return
	}
	if st.preview.ServerID != "" {
		refuse(errInvalid("This upload is for a restore into a server this machine has."))
		return
	}
	done := func(bool) {}
	if st.limit != "" {
		if done, err = a.holdNamedLimit(r.Context(), st.limit, unpackedBytes(st.manifest)); err != nil {
			refuse(err)
			return
		}
	}
	st.stopped = !in.start
	if err := a.stageStopped(rid, st.stopped); err != nil {
		done(false)
		writeError(w, err)
		return
	}
	op, err := a.newFromStage(st, in.spec, in.mem, &in.prev, func(s *server) func(ctx context.Context, h *opHandle) error {
		if st.limit != "" {
			if err := a.joinDiskLimit(st.limit, s.id); err != nil {
				a.log.Warn("a server moved in couldn't join its disk limit", "server", s.id, "limit", st.limit, "err", err)
			}
		}
		return func(ctx context.Context, h *opHandle) error {
			defer release()
			err := s.restoreOp(ctx, h, st, api.RestoreApplyRequest{MemoryMB: in.mem}, actor)
			done(err == nil)
			return err
		}
	})
	if err != nil {
		done(false)
		refuse(err)
		return
	}
	started = true
	from := "a backup"
	if st.manifest.Whole {
		from = "its folder"
	}
	a.auditFor(in.spec.id, actor, "server.moved_in", rid, "started", fmt.Sprintf("as %s from %s with sha256 %s; the Minecraft EULA accepted by %s on %s",
		in.spec.name, from, shortSum(st.preview.SHA256), in.prev.EULAAcceptedBy, in.prev.EULAAcceptedAt.Format(time.DateOnly)))
	writeJSON(w, http.StatusAccepted, op)
}
