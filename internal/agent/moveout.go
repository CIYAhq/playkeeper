package agent

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/CIYAhq/playkeeper/internal/api"
	"github.com/CIYAhq/playkeeper/internal/backup"
)

// hMoveOut streams this server's whole folder (backup.CreateWhole) for its
// move to another machine, once the dashboard has stopped it: the dashboard
// sends it on as the upload that machine makes the server from (see
// hRestoreMoveIn). Nothing is written here, so nothing counts against the
// server's disk limit, and a server that hasn't made its world yet moves
// too. The operation lock is held while it streams, so nothing starts the
// server or changes its files meanwhile. A folder a move can't carry is
// refused before the first byte, and a stream that fails part way ends
// without its manifest, which the machine it was going to refuses.
func (s *server) hMoveOut(w http.ResponseWriter, r *http.Request) {
	actor := actorFromHeader(r)
	if actor == "unknown" {
		writeError(w, errInvalid("X-Playkeeper-Actor header is required"))
		return
	}
	release, ok := s.holdOpLock()
	if !ok {
		writeError(w, s.busyError())
		return
	}
	defer release()
	sc, err := s.serverConfig()
	if err == nil && sc == nil {
		err = errNotCreated()
	}
	if err != nil {
		writeError(w, err)
		return
	}
	if _, running, err := s.containerRunning(r.Context()); err != nil {
		writeError(w, err)
		return
	} else if running {
		writeError(w, errConflict(s.name()+" is running.", "Stop it before it's moved."))
		return
	}
	if _, err := backup.MeasureWhole(s.dataDir(), archiveLimits()); err != nil {
		s.audit(actor, "server.moved_out", "", "refused", err.Error())
		writeError(w, s.moveRefusal(err))
		return
	}
	w.Header().Set("Content-Type", "application/gzip")
	out := &countingWriter{w: w}
	m, err := backup.CreateWhole(out, s.dataDir(), s.archiveMeta(*sc, s.now()), archiveLimits())
	if err != nil {
		s.audit(actor, "server.moved_out", "", "failed", err.Error())
		if out.n == 0 {
			writeError(w, errConflict(fmt.Sprintf("%s's folder can't be moved: %v.", s.name(), err), ""))
			return
		}
		panic(http.ErrAbortHandler)
	}
	s.audit(actor, "server.moved_out", "", "succeeded", fmt.Sprintf("%d files, %d bytes", len(m.Files), m.TotalBytes))
}

// hMoveCheck sizes this server's whole folder as its move carries it
// (api.MoveCheck), without reading its files, or refuses a folder a move
// can't carry, saying why. The dashboard asks before any of a customer's
// servers stops, and again before each one's turn.
func (s *server) hMoveCheck(w http.ResponseWriter, r *http.Request) {
	size, err := backup.MeasureWhole(s.dataDir(), archiveLimits())
	if err != nil {
		writeError(w, s.moveRefusal(err))
		return
	}
	writeJSON(w, http.StatusOK, api.MoveCheck{DiskBytes: size.DiskBytes, ArchiveBytes: size.ArchiveBytes()})
}

// moveRefusal is why this server's folder can't move, in the owner's words,
// for a folder the archive limits refuse; other errors are passed on.
func (s *server) moveRefusal(err error) error {
	var refused *backup.RefusedError
	if !errors.As(err, &refused) {
		return err
	}
	why := strings.Replace(refused.Reason.Error(), "archive ", "its folder ", 1)
	return errConflict(fmt.Sprintf("%s can't be moved: %s.", s.name(), why), "Delete what it doesn't need from its folder, then move it again.")
}
