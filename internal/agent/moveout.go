package agent

import (
	"fmt"
	"net/http"

	"github.com/CIYAhq/playkeeper/internal/backup"
)

// hMoveOut streams this server's whole folder (backup.CreateWhole) for its
// move to another machine, once the dashboard has stopped it: the dashboard
// sends it on as the upload that machine makes the server from (see
// hRestoreMoveIn). Nothing is written here, so nothing counts against the
// server's disk limit, and a server that hasn't made its world yet moves
// too. The operation lock is held while it streams, so nothing starts the
// server or changes its files meanwhile. A stream that fails part way ends
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
