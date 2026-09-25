package agent

import (
	"context"
	"fmt"
	"os"

	"github.com/CIYAhq/playkeeper/internal/api"
)

// pendingRestore is a restore a previous agent process left running, with
// its stage and the stage's swap journal.
type pendingRestore struct {
	op       *api.Operation
	stageDir string
	journal  *swapJournal
}

// findInterruptedRestores finds the restores a previous agent process left
// running whose stage is still there, for their servers to finish when the
// agent starts, and returns their operations' IDs.
func (a *Agent) findInterruptedRestores() []string {
	rows, err := a.db.Query(`SELECT id FROM operations WHERE status = 'running' AND kind = 'restore'`)
	if err != nil {
		a.log.Error("find interrupted restores", "err", err)
		return nil
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err == nil {
			ids = append(ids, id)
		}
	}
	rows.Close()
	var found []string
	for _, id := range ids {
		op, err := a.loadOperation(id)
		if err != nil {
			continue
		}
		s := a.serverByID(op.ServerID)
		stageID, _ := op.Detail["stage"].(string)
		if s == nil || s.recovery != nil || !reStageID.MatchString(stageID) {
			continue
		}
		dir := a.stageDir(stageID)
		if !dirExists(dir) {
			continue
		}
		j, err := readSwapJournal(dir)
		if j == nil && err == nil {
			continue
		}
		if err != nil || j.ServerID != s.id || j.OpID != op.ID {
			a.log.Warn("an interrupted restore's journal does not belong to it, so it is not finished", "server", s.id, "operation", op.ID, "err", err)
			continue
		}
		s.recovery = &pendingRestore{op: op, stageDir: dir, journal: j}
		found = append(found, op.ID)
	}
	return found
}

// resuming is true if dir is the stage of a restore the agent is about to
// finish.
func (a *Agent) resuming(dir string) bool {
	for _, s := range a.serverList() {
		if s.recovery != nil && s.recovery.stageDir == dir {
			return true
		}
	}
	return false
}

// recoverAtStart finishes, as its own operation, the restore a previous
// agent process left running on the server.
func (s *server) recoverAtStart() {
	p := s.recovery
	s.recovery = nil
	if p == nil {
		return
	}
	s.opLock <- struct{}{}
	s.launchOp(p.op, func(ctx context.Context, h *opHandle) error { return s.recoverRestore(ctx, h, p) })
}

// recoverRestore finishes a restore a previous agent process was in the
// middle of: one moving the worlds finishes moving them, one checking its
// restored world checks it again and keeps it if it starts, a kept one
// finishes tidying up, and one being undone is undone.
func (s *server) recoverRestore(ctx context.Context, h *opHandle, p *pendingRestore) error {
	h.set("resumedAfterRestart", true)
	j := p.journal
	defer func() {
		if j.State == swapDone {
			os.RemoveAll(p.stageDir)
		}
	}()
	switch j.State {
	case swapMoving:
		if err := s.rollForward(p.stageDir, j); err != nil {
			if !j.HadLive || j.Previous == nil {
				return &apiError{Msg: "The restore could not be finished after the Playkeeper agent restarted: " + err.Error() + ".", Hint: "Nothing was deleted. Restore the backup again."}
			}
			j.Why = "The restore could not be finished after the Playkeeper agent restarted (" + err.Error() + ")."
			return s.revertRestore(h, p.stageDir, j)
		}
		return s.finishRestore(ctx, h, p.stageDir, j)
	case swapChecking:
		return s.finishRestore(ctx, h, p.stageDir, j)
	case swapKept:
		return s.keepRestore(h, j, false)
	case swapReverting:
		if j.Why == "" {
			j.Why = "The Playkeeper agent stopped while the restore was being undone."
		}
		return s.revertRestore(h, p.stageDir, j)
	}
	return fmt.Errorf("swap journal in an unknown state %q", j.State)
}
