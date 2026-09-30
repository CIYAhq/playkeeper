package panel

import (
	"cmp"
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/CIYAhq/playkeeper/internal/api"
	"github.com/CIYAhq/playkeeper/internal/invites"
)

// Confirming joined machines (the fleet plan's step 4). A joined machine
// takes customers only once the owner confirms it's theirs, so a join code
// that leaked can't pull customers onto a stranger's machine (see
// placement.go). Confirming keeps servers away from the machine first, and
// that stays on while it takes customers or has any. Stopping sends new
// customers elsewhere; those it has stay.

// actTakeCustomers confirms a joined machine takes customers, or stops it
// taking new ones. It isn't in actNeeds, so only the owner may.
const actTakeCustomers action = "machines.customers"

// takesCustomers is when and by whom the owner confirmed a joined machine
// takes customers.
type takesCustomers struct {
	Since time.Time `json:"since"`
	By    string    `json:"by"`
}

// errGuardRefused is the answer when a machine didn't keep servers away from
// itself, so it can't take customers.
var errGuardRefused = &invites.Error{Code: api.CodeConflict, Status: http.StatusConflict,
	Msg: "The machine didn't keep servers away from itself, so it takes no customers.", Hint: "Update Playkeeper on it, then try again."}

// hMachineCustomers confirms a joined machine takes customers ({"on": true})
// or stops it taking new ones ({"on": false}), and answers with the machine.
func (s *Server) hMachineCustomers(w http.ResponseWriter, r *http.Request, sess *session) {
	m, ok := s.machineFromPath(w, r)
	if !ok {
		return
	}
	var req struct {
		On *bool `json:"on"`
	}
	if err := decodeJSON(r, &req); err != nil || req.On == nil {
		writeErr(w, http.StatusBadRequest, api.CodeInvalid, `Send {"on": true} or {"on": false}.`, "")
		return
	}
	if m.Kind != remoteKind {
		writeErr(w, http.StatusBadRequest, api.CodeInvalid, "The dashboard's own machine always takes customers.", "")
		return
	}
	switch err := s.setTakesCustomers(r.Context(), m.ID, sess.User.Username, *req.On); {
	case errors.Is(err, errNotFound):
		writeErr(w, http.StatusNotFound, api.CodeNotFound, "Machine not found.", "")
		return
	case errors.Is(err, errDB):
		writeErr(w, http.StatusInternalServerError, api.CodeInternal, "Database error.", "")
		return
	case errors.As(err, new(*invites.Error)):
		writeRefusal(w, err)
		return
	case err != nil:
		s.agentFailure(w, err)
		return
	}
	m, err := s.machineByID(m.ID)
	if err != nil {
		writeErr(w, http.StatusNotFound, api.CodeNotFound, "Machine not found.", "")
		return
	}
	writeJSON(w, http.StatusOK, s.machineView(r.Context(), m, s.linkStatuses(r.Context())[m.ID]))
}

// setTakesCustomers confirms the joined machine id takes customers, once it
// keeps servers away from itself, or stops it taking new ones. It holds
// placement's lock, so no customer is placed on a machine as the owner
// stops it. Confirming has the customers waiting for room placed.
func (s *Server) setTakesCustomers(ctx context.Context, id, actor string, on bool) error {
	s.placeMu.Lock()
	defer s.placeMu.Unlock()
	m, err := s.machineByID(id)
	switch {
	case errors.Is(err, errNotFound):
		return errNotFound
	case err != nil:
		return errDB
	}
	return s.takeCustomers(ctx, m, actor, on)
}

// takeCustomers is setTakesCustomers for m, with s.placeMu held. Stopping
// is remembered, so the Hetzner token doesn't confirm m again (see
// autoconfirm.go).
func (s *Server) takeCustomers(ctx context.Context, m machine, actor string, on bool) error {
	if on {
		g, err := setNetworkGuard(ctx, m, actor, true)
		if err != nil {
			return err
		}
		if !g.Host {
			return errGuardRefused
		}
	}
	if m.customersAt.IsZero() != on {
		return nil
	}
	at, by, stopped, result, detail := int64(0), "", millis(s.now()), "stopped", "takes no new customers; those it has stay"
	if on {
		at, by, stopped, result, detail = millis(s.now()), actor, millis(m.customersStopped), "confirmed", "takes customers, with servers kept away from it"
	}
	res, err := s.db.ExecContext(ctx, `UPDATE machines SET customers_at = ?, customers_by = ?, customers_stopped_at = ? WHERE id = ? AND kind = ? AND revoked_at = 0`,
		at, by, stopped, m.ID, remoteKind)
	if err != nil {
		return errDB
	}
	if n, err := res.RowsAffected(); err != nil || n == 0 {
		return errNotFound
	}
	s.audit(actor, "machine.customers", cmp.Or(m.Name, m.ID), result, detail)
	s.kickSaleRoom()
	kind := "machine.customers_off"
	if on {
		kind = "machine.customers_on"
		s.kickRoom()
	}
	s.machineEvent(m.ID, s.now(), kind, actor, "", "")
	return nil
}

// guardHeld is why servers must stay away from the joined machine id, with
// a hint, or "": it takes customers, or has some. The caller holds
// s.placeMu.
func (s *Server) guardHeld(ctx context.Context, id string) (msg, hint string, err error) {
	var at int64
	var homes int
	if err := s.db.QueryRowContext(ctx, `SELECT customers_at, (SELECT COUNT(*) FROM customer_homes WHERE machine_id = ?) FROM machines WHERE id = ?`, id, id).Scan(&at, &homes); err != nil {
		return "", "", errDB
	}
	switch {
	case at != 0:
		return "Servers stay away from this machine while it takes customers.", "Stop it taking customers in Settings › Machines first.", nil
	case homes > 0:
		return "Servers stay away from this machine while it has customers.", "", nil
	}
	return "", "", nil
}

// customersOn is how many customers placement gave the machine id, or 0
// when that can't be read.
func (s *Server) customersOn(id string) int {
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM customer_homes WHERE machine_id = ?`, id).Scan(&n); err != nil {
		s.log.Error("count a machine's customers", "machine", id, "err", err)
	}
	return n
}

// kickRoom has the customers waiting for room placed now, as a machine just
// started taking customers.
func (s *Server) kickRoom() {
	select {
	case s.roomKick <- struct{}{}:
	default:
	}
}

// runRoom places the customers waiting for room each time kickRoom says
// there may be more, until ctx ends. The core asks again every minute
// anyway (runCustomers).
func (s *Server) runRoom(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.roomKick:
			s.startWaitingCustomers(ctx)
		}
	}
}
