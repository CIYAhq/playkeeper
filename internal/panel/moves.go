package panel

import (
	"cmp"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"math"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/CIYAhq/playkeeper/internal/agentclient"
	"github.com/CIYAhq/playkeeper/internal/api"
	"github.com/CIYAhq/playkeeper/internal/invites"
)

// Moving customers (step 8 of the fleet plan). The owner moves a customer to
// another machine that takes customers, has room for their plan, which is
// set aside there at once, and has room on its disk for their servers'
// folders: their new servers go there from then on, and their servers
// follow, one at a time. Every server is checked before any stops, so one
// whose folder a move can't carry refuses the move, saying why. Each one
// stops, and its machine streams its whole folder (the agent's move-out) to
// the other machine as an upload for a new server. That machine makes the
// server from it with the same id (the agent's move-in), starting it if it
// ran and its customer isn't paused or suspended, gets what the agent kept
// about it (its move state), and counts it against the customer's disk
// limit there before its requests go there. The upload itself doesn't, so a
// customer near their limit moves as they are. The machine it left then
// deletes it, keeping its whole folder movedBackupDays. It keeps its id, so
// its members, invites, slug, address and links stay as they are: the
// customer sees it stopped for a few minutes, and never a machine.
//
// A server being moved has a server_moves row, and no request reaches it
// (see machineForServer). The copy the machine it goes to makes isn't the
// server until its requests go there, and the copies a move leaves, on the
// machine it moved from or on the one a failed move was going to, aren't
// the server either: they have left_copies rows until they're deleted.
// Neither kind counts in its machine's listings, as the server or as a
// machine listing another's (see hiddenCopies). A move that fails before
// the server's requests go to the other machine leaves it where it was. A
// move a restart of the dashboard stopped carries on when it starts again
// (see runMoves), or leaves it where it was when it can't.

const (
	// movedBackupDays is how long the machine a server left keeps its whole
	// folder as its final backup.
	movedBackupDays = 7
	// moveStepWait is how long one step of a server's move may take: its
	// stop, its copy or its move-in.
	moveStepWait = time.Hour
	// moveRetry is how often machines that were away are asked again to
	// delete the copies moves left on them.
	moveRetry = 10 * time.Minute
	// leftCopyKept is how long a copy a move left stays recorded after it
	// went, so a listing asked for before then that arrives after one asked
	// for since still leaves it out.
	leftCopyKept = 10 * time.Minute

	codeServerMoving = "server_moving"
	serverMovingText = "This server is being moved. It's back in a few minutes."
)

// errServerMoving is a request to a server that's being moved: it goes to
// neither machine.
var errServerMoving = errors.New("the server is being moved")

// errCustomerMoving refuses a customer a new server while their servers
// move, or after a move of theirs stopped: their allowance counts only the
// servers on their machine.
var errCustomerMoving = &invites.Error{Code: api.CodeConflict, Status: http.StatusConflict,
	Msg: "Your servers are being moved.", Hint: "Try again in a few minutes."}

// The owner's refusals to move a customer.
var (
	errNotACustomer = &invites.Error{Code: api.CodeNotFound, Status: http.StatusNotFound, Msg: "No such customer."}
	errMoveWaiting  = &invites.Error{Code: api.CodeConflict, Status: http.StatusConflict,
		Msg: "This customer is waiting for room: they get a machine as soon as one has room."}
	errMoveLapsed = &invites.Error{Code: api.CodeConflict, Status: http.StatusConflict,
		Msg: "This customer's plan ended, so their servers are deleted or about to be.", Hint: "They get a machine again when they renew."}
	errMoveRunning   = &invites.Error{Code: api.CodeConflict, Status: http.StatusConflict, Msg: "This customer's servers are being moved already."}
	errMoveNowhere   = &invites.Error{Code: api.CodeConflict, Status: http.StatusConflict, Msg: "No other machine that takes customers has room for their plan.", Hint: "Add a machine, or make room on one."}
	errMoveThere     = &invites.Error{Code: api.CodeConflict, Status: http.StatusConflict, Msg: "This customer's servers are on that machine already."}
	errMoveMachine   = &invites.Error{Code: api.CodeNotFound, Status: http.StatusNotFound, Msg: "Machine not found."}
	errMoveUnguarded = &invites.Error{Code: api.CodeConflict, Status: http.StatusConflict, Msg: "That machine couldn't keep servers away from itself, so it takes no customers."}
)

// moveRuns are the customers whose servers are being moved now, one
// goroutine each.
type moveRuns struct {
	mu  sync.Mutex
	ids map[int64]bool
	wg  sync.WaitGroup
}

// start moves customer id's servers with move in ctx, unless that runs
// already or ctx has ended.
func (r *moveRuns) start(ctx context.Context, id int64, move func(context.Context, int64)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.ids[id] || ctx.Err() != nil {
		return
	}
	if r.ids == nil {
		r.ids = map[int64]bool{}
	}
	r.ids[id] = true
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		defer func() {
			r.mu.Lock()
			delete(r.ids, id)
			r.mu.Unlock()
		}()
		move(ctx, id)
	}()
}

// after runs fn in the background, unless ctx has ended, and stop waits for
// it too.
func (r *moveRuns) after(ctx context.Context, fn func()) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if ctx.Err() != nil {
		return
	}
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		fn()
	}()
}

func (r *moveRuns) running(id int64) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.ids[id]
}

// stop ends the moves with cancel, their context's, and waits for them.
func (r *moveRuns) stop(cancel context.CancelFunc) {
	r.mu.Lock()
	cancel()
	r.mu.Unlock()
	r.wg.Wait()
}

type rowsQuerier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// hiddenCopies are the servers machineID lists only as copies moves make or
// leave there, by id, each with when that copy went, or 0 while it's there:
// the copy it's making of a server moving to it, and each copy a move left
// on it, until it's deleted.
func hiddenCopies(ctx context.Context, q rowsQuerier, machineID string) (map[string]int64, error) {
	rows, err := q.QueryContext(ctx, `SELECT server_id, 0 FROM server_moves WHERE to_machine = ?
		UNION ALL SELECT server_id, left_at FROM left_copies WHERE machine_id = ?`, machineID, machineID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int64{}
	for rows.Next() {
		var id string
		var left int64
		if err := rows.Scan(&id, &left); err != nil {
			return nil, err
		}
		if seen, ok := out[id]; !ok || seen != 0 {
			out[id] = left
		}
	}
	return out, rows.Err()
}

// copyHidden reports whether a listing asked for at listedAt shows server id
// only as one of copies (see hiddenCopies): a copy that's there, or one
// that went after the listing was asked for.
func copyHidden(copies map[string]int64, id string, listedAt time.Time) bool {
	left, ok := copies[id]
	return ok && (left == 0 || millis(listedAt) <= left)
}

// forgetLeft forgets the copies moves left on machineID that went more than
// leftCopyKept before its listing asked for at listedAt. Until then a
// listing asked for since they went counts them as any (see copyHidden).
func (s *Server) forgetLeft(ctx context.Context, q querier, machineID string, listedAt time.Time) error {
	_, err := q.ExecContext(ctx, `DELETE FROM left_copies WHERE machine_id = ? AND left_at > 0 AND left_at < ?`, machineID, millis(listedAt.Add(-leftCopyKept)))
	return err
}

// customerMoving reports whether customer userID's servers are being moved,
// a move of theirs stopped before they all were, or any of them is on a
// machine that isn't theirs and wasn't removed, as when a removed machine's
// host joins again (serversApart). When that can't be read, they are.
func (s *Server) customerMoving(ctx context.Context, userID int64) bool {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM customer_moves WHERE user_id = ?) + (SELECT COUNT(*) FROM server_moves WHERE user_id = ?)`,
		userID, userID).Scan(&n)
	return err != nil || n > 0 || s.serversApart(ctx, userID)
}

// moveUnderWay reports whether customer userID's servers are being moved,
// or a restart stopped their move before it ended, rather than it stopping
// on an error.
func (s *Server) moveUnderWay(ctx context.Context, userID int64) bool {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM customer_moves WHERE user_id = ? AND error = '') + (SELECT COUNT(*) FROM server_moves WHERE user_id = ?)`, userID, userID).Scan(&n)
	return err == nil && n > 0
}

// serversApart reports whether any server customer userID created is on a
// machine other than theirs that wasn't removed: their allowance counts only
// the servers on their machine, and their final backups can be kept on one
// machine only. When that can't be read, one is.
func (s *Server) serversApart(ctx context.Context, userID int64) bool {
	home, _, err := s.homeMachine(ctx, userID)
	if err != nil {
		return true
	}
	rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT sm.machine_id FROM creator_servers cs JOIN server_machines sm ON sm.server_id = cs.server_id
		WHERE cs.user_id = ? AND sm.machine_id != ?`, userID, home)
	if err != nil {
		return true
	}
	var ids []string
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	rows.Close()
	for _, id := range ids {
		if _, err := s.machineByID(id); !errors.Is(err, errNotFound) {
			return true
		}
	}
	return false
}

// customerHeld reports whether customer userID is paused or suspended, so
// their servers stay stopped (see customerHolds). When that can't be read,
// they are.
func (s *Server) customerHeld(ctx context.Context, userID int64) bool {
	var state string
	err := s.db.QueryRowContext(ctx, `SELECT state FROM customers WHERE user_id = ?`, userID).Scan(&state)
	return err != nil || CustomerState(state) != CustomerActive
}

// markMoving says which servers of a list are being moved.
func (s *Server) markMoving(ctx context.Context, servers []map[string]any) {
	rows, err := s.db.QueryContext(ctx, `SELECT server_id FROM server_moves`)
	if err != nil {
		s.log.Error("read the servers being moved", "err", err)
		return
	}
	defer rows.Close()
	moving := map[string]bool{}
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			moving[id] = true
		}
	}
	for _, sv := range servers {
		if id, _ := sv["id"].(string); moving[id] {
			sv["moving"] = true
		}
	}
}

// hCustomerMove moves a customer to the machine the body names, or with
// none to the fullest other machine with room for their plan (startMove).
func (s *Server) hCustomerMove(w http.ResponseWriter, r *http.Request, sess *session) {
	uid, err := strconv.ParseInt(r.PathValue("uid"), 10, 64)
	if err != nil || uid <= 0 {
		writeRefusal(w, errNotACustomer)
		return
	}
	var req struct {
		MachineID string `json:"machineId"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, api.CodeInvalid, "Invalid request.", "")
		return
	}
	to, err := s.startMove(r.Context(), uid, req.MachineID, sess.User.Username)
	var e *invites.Error
	switch {
	case errors.As(err, &e):
		writeRefusal(w, err)
	case err != nil:
		s.listFailure(w, err)
	default:
		writeJSON(w, http.StatusAccepted, map[string]string{"machineId": to})
	}
}

// startMove moves customer userID to machine to, or with to "" to the
// fullest other machine that takes customers, keeps servers away from
// itself and has room for their plan, and returns where. It sets their
// plan's memory aside there at once, so their new servers go there, and
// has their servers follow in the background (moveCustomer). Moving them
// to their machine brings back the servers a move that stopped left
// behind. A customer whose plan ended is moved only while a move of theirs
// has stopped, so their servers come together to be deleted (see
// deleteLapsedCustomer).
func (s *Server) startMove(ctx context.Context, userID int64, to, actor string) (string, error) {
	s.placeMu.Lock()
	defer s.placeMu.Unlock()
	var name, state string
	var deleteAfter, deletedAt int64
	var planMB int
	err := s.db.QueryRowContext(ctx, `SELECT u.username, c.state, c.delete_after, c.servers_deleted_at,
		COALESCE((SELECT MAX(allowance_memory_mb) FROM project_members WHERE user_id = c.user_id), 0)
		FROM customers c JOIN users u ON u.id = c.user_id WHERE c.user_id = ?`, userID).Scan(&name, &state, &deleteAfter, &deletedAt, &planMB)
	moving := s.customerMoving(ctx, userID)
	switch {
	case isNoRows(err):
		return "", errNotACustomer
	case err != nil:
		return "", errDB
	case s.moves.running(userID):
		return "", errMoveRunning
	case !moving && (deletedAt != 0 || CustomerState(state) == CustomerPaused && deleteAfter > 0 && deleteAfter <= s.now().UnixMilli()):
		return "", errMoveLapsed
	}
	home, placed, err := s.homeMachine(ctx, userID)
	switch {
	case err != nil:
		return "", err
	case !placed:
		return "", errMoveWaiting
	}
	sizes, err := s.moveSizes(ctx, userID, name)
	if err != nil {
		return "", err
	}
	rooms, err := s.fleetRooms(ctx, userID)
	if err != nil {
		return "", err
	}
	// Their servers on a machine already use their plan's memory there.
	for i := range rooms {
		rooms[i].FreeMB += rooms[i].ExceptMB
	}
	var target machineRoom
	if to == "" {
		// A machine a server of theirs a move can't carry would have to go
		// to is passed over; with none left, that's why they can't move.
		var refused *invites.Error
		others := slices.DeleteFunc(slices.Clone(rooms), func(r machineRoom) bool {
			if r.ID == home {
				return true
			}
			if why := carryRefusal(sizes, r.ID); why != nil {
				refused = cmp.Or(refused, why)
				return true
			}
			return !diskFits(r, diskNeed(sizes, r.ID))
		})
		for {
			var ok bool
			if target, ok = chooseMachine(others, planMB); !ok && refused != nil {
				return "", refused
			} else if !ok {
				return "", errMoveNowhere
			}
			err := guardOn(ctx, target, actor)
			if err == nil {
				break
			}
			s.log.Warn("a move couldn't keep servers away from a machine, so it passes it over", "machine", target.ID, "err", err)
			others = slices.DeleteFunc(others, func(r machineRoom) bool { return r.ID == target.ID })
		}
	} else {
		i := slices.IndexFunc(rooms, func(r machineRoom) bool { return r.ID == to })
		if i < 0 {
			return "", errMoveMachine
		}
		target = rooms[i]
		switch {
		case to == home && !moving:
			return "", errMoveThere
		case to == home:
		case !target.Takes:
			return "", &invites.Error{Code: api.CodeConflict, Status: http.StatusConflict, Msg: target.Why}
		case target.FreeMB < planMB:
			return "", &invites.Error{Code: api.CodeConflict, Status: http.StatusConflict,
				Msg: fmt.Sprintf("That machine can set aside %s, and their plan needs %s.", gbText(max(target.FreeMB, 0)), gbText(planMB))}
		}
		if refused := carryRefusal(sizes, target.ID); refused != nil {
			return "", refused
		}
		if need := diskNeed(sizes, target.ID); !diskFits(target, need) {
			return "", diskRefusal(target, name, need)
		}
		if guardOn(ctx, target, actor) != nil {
			return "", errMoveUnguarded
		}
	}
	now := millis(s.now())
	// A server the customer is making meanwhile is among theirs before their
	// move reads them, or refused once it's recorded (hCreateServer).
	s.creators.Lock()
	defer s.creators.Unlock()
	err = s.immediate(ctx, func(c *sql.Conn) error {
		if _, err := c.ExecContext(ctx, `INSERT INTO customer_moves(user_id, to_machine, started_at, started_by) VALUES(?,?,?,?)
			ON CONFLICT(user_id) DO UPDATE SET to_machine = excluded.to_machine, started_at = excluded.started_at, started_by = excluded.started_by, error = ''`,
			userID, target.ID, now, actor); err != nil {
			return err
		}
		_, err := c.ExecContext(ctx, `INSERT INTO customer_homes(user_id, machine_id, placed_at) VALUES(?,?,?)
			ON CONFLICT(user_id) DO UPDATE SET machine_id = excluded.machine_id, placed_at = excluded.placed_at`, userID, target.ID, now)
		return err
	})
	if err != nil {
		return "", errDB
	}
	s.kickDiskLimits()
	s.kickSaleRoom()
	s.audit(actor, "customer.move", name, "started", fmt.Sprintf("to %s, with %s set aside there", machineLabel(target.machine), gbText(planMB)))
	s.moves.start(s.movesCtx, userID, s.moveCustomer)
	return target.ID, nil
}

// moveDiskReserve is what a machine keeps free on its disk as it unpacks
// an upload (the agent's minFreeAfterBackup), which a move leaves it too.
const moveDiskReserve = 512 << 20

// serverSize is what one of a customer's servers takes where it moves
// (api.MoveCheck), the machine it's on, and the machines a move left a copy
// of it on that isn't deleted yet; or, for one a move can't carry or its
// machine couldn't check, why (refused).
type serverSize struct {
	api.MoveCheck
	machineID string
	copiesOn  []string
	refused   *invites.Error
}

// moveSizes has each of customer name's servers checked by its machine
// (the agent's move-check) and returns what each takes where it goes, or
// why a move can't carry it. A server on a removed machine stays there, out
// of reach.
func (s *Server) moveSizes(ctx context.Context, userID int64, name string) (map[string]serverSize, error) {
	ids, err := s.creatorServers(userID)
	if err != nil {
		return nil, err
	}
	out := map[string]serverSize{}
	for _, id := range ids {
		at, err := s.recordedMachine(ctx, id)
		if err != nil {
			return nil, err
		}
		m, err := s.machineByID(at)
		if errors.Is(err, errNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		var check api.MoveCheck
		err = askAgent(ctx, m, http.MethodGet, "/v1/servers/"+id+"/move-check", nil, &check)
		if refused := moveCheckRefusal(m, name, err); refused != nil {
			s.log.Warn("a server wasn't checked for its move", "machine", m.ID, "server", id, "err", err)
			out[id] = serverSize{machineID: at, refused: refused}
			continue
		}
		copies, err := s.copiesLeftOf(ctx, id)
		if err != nil {
			return nil, err
		}
		out[id] = serverSize{MoveCheck: check, machineID: at, copiesOn: copies}
	}
	return out, nil
}

// copiesLeftOf lists the machines a move left a copy of server id on that
// isn't deleted yet.
func (s *Server) copiesLeftOf(ctx context.Context, id string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT machine_id FROM left_copies WHERE server_id = ? AND left_at = 0`, id)
	if err != nil {
		return nil, errDB
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var m string
		if err := rows.Scan(&m); err != nil {
			return nil, errDB
		}
		out = append(out, m)
	}
	if rows.Err() != nil {
		return nil, errDB
	}
	return out, nil
}

// carryRefusal is why a move of the servers sizes names to machine id can't
// go, naming the first server that would go there and that a move can't
// carry or its machine couldn't check, or nil. One already there stays.
func carryRefusal(sizes map[string]serverSize, id string) *invites.Error {
	for _, sid := range slices.Sorted(maps.Keys(sizes)) {
		if sz := sizes[sid]; sz.refused != nil && sz.machineID != id {
			return sz.refused
		}
	}
	return nil
}

// moveCheckRefusal refuses to move customer name for a server machine m
// couldn't check for the move, err saying why: m's own words for a folder
// a move can't carry, and that it doesn't answer otherwise. It's nil once m
// checked the server.
func moveCheckRefusal(m machine, name string, err error) *invites.Error {
	var ae *agentclient.Error
	switch {
	case err == nil:
		return nil
	case errors.As(err, &ae) && ae.Status == http.StatusConflict:
		return &invites.Error{Code: api.CodeConflict, Status: http.StatusConflict, Msg: ae.Body.Error, Hint: ae.Body.Hint}
	}
	return uncheckedRefusal(m, name)
}

// uncheckedRefusal refuses to move customer name while machine m doesn't
// answer for their servers there.
func uncheckedRefusal(m machine, name string) *invites.Error {
	return &invites.Error{Code: api.CodeConflict, Status: http.StatusConflict,
		Msg: fmt.Sprintf("%s doesn't answer, so %s's servers there can't be checked for the move.", machineSubject(m), name), Hint: "Try again once it's back."}
}

// diskNeed is what moving the servers sizes names to machine id takes on
// its disk: each of those not there already unpacked, and the largest's
// upload beside them meanwhile. One a move left a copy of there counts
// nothing unpacked, as that copy goes first (copyServer) and frees about as
// much; the check as its turn comes (checkRoomFor) sees what it truly freed.
func diskNeed(sizes map[string]serverSize, id string) int64 {
	var need, upload int64
	for _, sz := range sizes {
		if sz.machineID == id {
			continue
		}
		upload = max(upload, sz.ArchiveBytes)
		if !slices.Contains(sz.copiesOn, id) {
			need += sz.DiskBytes
		}
	}
	return need + upload
}

// diskFits reports whether r's disk has room for need and what it keeps
// free. A machine that doesn't say what its disk has free takes none.
func diskFits(r machineRoom, need int64) bool {
	return need == 0 || r.DiskFree != nil && *r.DiskFree >= need+moveDiskReserve
}

// diskRefusal refuses to move customer name to r, whose disk hasn't room
// for need.
func diskRefusal(r machineRoom, name string, need int64) *invites.Error {
	msg := fmt.Sprintf("%s doesn't say how much room its disk has, so %s's servers can't go there.", machineSubject(r.machine), name)
	if r.DiskFree != nil {
		msg = fmt.Sprintf("%s has %s free on its disk, and %s's servers need %s there.", machineSubject(r.machine), bytesText(*r.DiskFree), name, bytesText(need+moveDiskReserve))
	}
	return &invites.Error{Code: api.CodeConflict, Status: http.StatusConflict, Msg: msg, Hint: "Pick a machine with more room, or make room on its disk."}
}

// checkRoomFor has server id checked again as its turn comes, before it
// stops on from: its folder is one a move carries, and to's disk has room
// for it now, as that disk may have filled since the move started.
func checkRoomFor(ctx context.Context, from, to machine, id string) error {
	var check api.MoveCheck
	if err := askAgent(ctx, from, http.MethodGet, "/v1/servers/"+id+"/move-check", nil, &check); err != nil {
		var ae *agentclient.Error
		if errors.As(err, &ae) && ae.Status == http.StatusConflict {
			return errors.New(strings.TrimSuffix(ae.Body.Error, "."))
		}
		return fmt.Errorf("%s couldn't check it for the move: %w", machineLabel(from), err)
	}
	var live api.Machine
	if err := askAgent(ctx, to, http.MethodGet, "/v1/machine", nil, &live); err != nil {
		return fmt.Errorf("%s didn't say how much room its disk has: %w", machineLabel(to), err)
	}
	need := check.DiskBytes + check.ArchiveBytes + moveDiskReserve
	if live.DiskFreeBytes == nil || *live.DiskFreeBytes < need {
		free := int64(0)
		if live.DiskFreeBytes != nil {
			free = *live.DiskFreeBytes
		}
		return fmt.Errorf("there's no room for it on %s's disk: it needs %s, and %s is free", machineLabel(to), bytesText(need), bytesText(free))
	}
	return nil
}

// bytesText is n bytes in GB, as gbText writes them.
func bytesText(n int64) string {
	return gbText(int((max(n, 0) + 1<<20 - 1) >> 20))
}

// machineSubject is machineLabel starting a sentence: a machine's name as
// it is, and "The dashboard's machine".
func machineSubject(m machine) string {
	label := machineLabel(m)
	if rest, ok := strings.CutPrefix(label, "the "); ok {
		return "The " + rest
	}
	return label
}

// guardOn has r's machine keep servers away from itself, as a machine that
// takes customers does, unless it does already.
func guardOn(ctx context.Context, r machineRoom, actor string) error {
	if r.Guarded {
		return nil
	}
	g, err := setNetworkGuard(ctx, r.machine, actor, true)
	if err == nil && !g.Host {
		err = errors.New("the agent left servers free to reach the machine")
	}
	return err
}

// moveCustomer moves customer userID's servers that aren't on their machine
// there, one at a time, then forgets the move once none is left elsewhere:
// a server that got there meanwhile is moved too, and one still left stops
// the move. One that fails stops it, and the owner sees why. The dashboard
// stopping leaves it for its next start.
func (s *Server) moveCustomer(ctx context.Context, userID int64) {
	var name string
	_ = s.db.QueryRow(`SELECT username FROM users WHERE id = ?`, userID).Scan(&name)
	name = cmp.Or(name, strconv.FormatInt(userID, 10))
	moved, err := s.moveServers(ctx, userID)
	for pass := 1; err == nil && pass < 3 && s.serversApart(ctx, userID); pass++ {
		var more int
		more, err = s.moveServers(ctx, userID)
		moved += more
	}
	if err == nil && s.serversApart(ctx, userID) {
		err = errors.New("some of their servers are still on another machine")
	}
	if ctx.Err() != nil {
		return
	}
	if err != nil {
		s.log.Warn("a customer's move stopped", "user", userID, "err", err)
		if _, dbErr := s.db.Exec(`UPDATE customer_moves SET error = ? WHERE user_id = ?`, whyStopped(err), userID); dbErr != nil {
			s.log.Error("could not record why a customer's move stopped", "user", userID, "err", dbErr)
		}
		s.audit(placementActor, "customer.move", name, "failed", err.Error())
		s.recountDisk()
		return
	}
	if _, err := s.db.Exec(`DELETE FROM customer_moves WHERE user_id = ?`, userID); err != nil {
		s.log.Error("could not forget a customer's finished move", "user", userID, "err", err)
		return
	}
	s.audit(placementActor, "customer.move", name, "succeeded", fmt.Sprintf("%d server(s) moved", moved))
	s.recountDisk()
	s.kickSaleRoom()
}

// moveServers moves each server of customer userID that isn't on their
// machine there, and returns how many moved.
func (s *Server) moveServers(ctx context.Context, userID int64) (int, error) {
	home, placed, err := s.homeMachine(ctx, userID)
	if err != nil {
		return 0, err
	}
	to, err := s.machineByID(home)
	if errors.Is(err, errNotFound) {
		// With nowhere to go, the servers a restart left moving stay where
		// they were, as when a move fails.
		s.undoMoves(ctx, userID)
		if !placed {
			return 0, errors.New("they have no machine")
		}
		return 0, errors.New("the machine they're moving to was removed")
	}
	if err != nil {
		return 0, err
	}
	ids, err := s.creatorServers(userID)
	if err != nil {
		return 0, err
	}
	moved := 0
	for _, id := range ids {
		mv, moving, err := s.serverMoveOf(ctx, id)
		if err != nil {
			return moved, err
		}
		at, err := s.recordedMachine(ctx, id)
		if err != nil {
			return moved, err
		}
		if moving && mv.to != home {
			// A move a restart stopped, to where they aren't going now.
			s.undoMove(ctx, mv)
			moving = false
		}
		if moving {
			at = mv.from
		} else if at == "" || at == home {
			continue
		}
		from, err := s.machineByID(at)
		switch {
		case errors.Is(err, errNotFound):
			// A server on a removed machine stays there, out of reach, and
			// a copy a move was making of it goes.
			if moving {
				s.dropMove(ctx, mv)
			}
			continue
		case err != nil:
			return moved, err
		}
		did, err := s.moveServer(ctx, userID, id, from, to, mv, moving)
		if err != nil {
			return moved, err
		}
		if did {
			moved++
		}
	}
	return moved, nil
}

// serverMove is a server_moves row: server id being moved from one machine
// to another, whether it ran, sleeping too, and the operation making its
// copy there, once it started.
type serverMove struct {
	serverID string
	userID   int64
	from, to string
	ran      bool
	madeBy   string
}

// serverMoveOf is server id's move, and whether it's being moved.
func (s *Server) serverMoveOf(ctx context.Context, id string) (serverMove, bool, error) {
	mv := serverMove{serverID: id}
	err := s.db.QueryRowContext(ctx, `SELECT user_id, from_machine, to_machine, ran, made_by FROM server_moves WHERE server_id = ?`, id).Scan(&mv.userID, &mv.from, &mv.to, &mv.ran, &mv.madeBy)
	switch {
	case isNoRows(err):
		return mv, false, nil
	case err != nil:
		return mv, false, errDB
	}
	return mv, true, nil
}

// recordedMachine is the machine server id's record names, or "".
func (s *Server) recordedMachine(ctx context.Context, id string) (string, error) {
	var mid string
	err := s.db.QueryRowContext(ctx, `SELECT machine_id FROM server_machines WHERE server_id = ?`, id).Scan(&mid)
	if err != nil && !isNoRows(err) {
		return "", errDB
	}
	return mid, nil
}

// moveServer moves server id of customer userID from one machine to
// another, carrying on mv when moving says a restart of the dashboard
// stopped it, and reports whether it moved: one deleted meanwhile doesn't.
// Whatever fails until its requests go there, sending them there included,
// leaves it where it was.
// A server that ran starts where it goes, unless its customer is paused or
// suspended; one that crashed there doesn't. Its copy there gets the backup
// rules it had, a copy made before a restart too, and counts against the
// customer's disk limit, before its requests go there.
func (s *Server) moveServer(ctx context.Context, userID int64, id string, from, to machine, mv serverMove, moving bool) (bool, error) {
	var st api.ServerStatus
	found, err := serverOn(ctx, from, id, &st)
	switch {
	case err == nil && !found:
		// Its machine no longer has it, so its record there goes, as that
		// machine's next listing would drop it.
		if _, err := s.db.ExecContext(ctx, `DELETE FROM server_machines WHERE server_id = ? AND machine_id = ?`, id, from.ID); err != nil {
			return false, errDB
		}
		if moving {
			// Deleted meanwhile, so the copy this move made goes too.
			s.dropMove(ctx, mv)
		}
		return false, nil
	case err != nil:
		err = fmt.Errorf("%s didn't say how server %s is: %w", machineLabel(from), id, err)
	case st.Config == nil:
		err = fmt.Errorf("%s didn't give %s's settings", machineLabel(from), st.Name)
	}
	if err != nil {
		// A move a restart stopped doesn't carry on: its server stays where
		// it was.
		if moving && ctx.Err() == nil {
			s.abandonMove(ctx, mv, from)
		}
		return false, err
	}
	if !moving {
		// A sleeping server starts where it goes, and falls asleep again as
		// its sleep setting says; so does one a failed move left stopped.
		ran := (st.Desired == api.DesiredRunning || st.Desired == api.DesiredSleeping) && st.Phase != api.PhaseCrashed || s.restartPending(ctx, id, from.ID)
		mv = serverMove{serverID: id, userID: userID, from: from.ID, to: to.ID, ran: ran}
		if _, err := s.db.ExecContext(ctx, `INSERT INTO server_moves(server_id, user_id, from_machine, to_machine, ran) VALUES(?,?,?,?,?)`,
			id, userID, from.ID, to.ID, mv.ran); err != nil {
			return false, errDB
		}
	}
	slug := s.shownSlug(ctx, id, from, st)
	start := mv.ran && !s.customerHeld(ctx, userID)
	err = s.copyServer(ctx, mv, from, to, st, slug, start, moving)
	if err == nil {
		s.copyBackupRules(ctx, id, from, to)
		err = s.copyMoveState(ctx, id, from, to)
	}
	if err == nil {
		err = s.switchTo(ctx, mv, to, slug)
	}
	if err != nil {
		if ctx.Err() == nil {
			s.abandonMove(ctx, mv, from)
		}
		return false, fmt.Errorf("%s: %w", st.Name, err)
	}
	// Pausing a customer while it moved stopped it where its requests went
	// then, not here, where its copy may have started, before a restart of
	// the dashboard too.
	if mv.ran && s.customerHeld(ctx, userID) {
		if err := stopOn(ctx, to, id); err != nil {
			s.log.Warn("a server moved while its customer was paused didn't stop", "server", id, "err", err)
		}
	}
	if err := s.leaveCopy(ctx, id, from.ID); err != nil {
		s.log.Warn("the machine a server moved from couldn't delete its copy yet", "server", id, "machine", from.ID, "err", err)
	}
	return true, nil
}

// shownSlug is the slug the dashboard shows for server id on m (see
// stableSlugs): its record's for a joined machine's server, else its
// agent's.
func (s *Server) shownSlug(ctx context.Context, id string, m machine, st api.ServerStatus) string {
	var kept string
	if m.Kind == remoteKind && s.db.QueryRowContext(ctx, `SELECT slug FROM server_machines WHERE server_id = ?`, id).Scan(&kept) == nil && kept != "" {
		return kept
	}
	return st.Slug
}

// copyServer has machine to make mv's server from its whole folder on from,
// stopped there first, and start it when start says. A copy to has already
// is an old one it didn't delete, and goes first, unless resume says this
// move made it before a restart of the dashboard: the operation making it
// is the one mv names, and it's complete.
func (s *Server) copyServer(ctx context.Context, mv serverMove, from, to machine, st api.ServerStatus, slug string, start, resume bool) error {
	id := mv.serverID
	var there api.ServerStatus
	found, err := serverOn(ctx, to, id, &there)
	if err != nil {
		return fmt.Errorf("%s didn't say whether it has it: %w", machineLabel(to), err)
	}
	if found {
		if op := there.Operation; resume && op != nil && op.ID == mv.madeBy && op.Status == api.OpRunning {
			if _, err := waitMoveOp(ctx, to, op.ID); err == nil {
				return nil
			}
		} else if op := there.LastOperation; resume && op != nil && op.ID == mv.madeBy && op.Status == api.OpSucceeded {
			return nil
		}
		if err := deleteOn(ctx, to, id, there.Name, 0, 0); err != nil {
			return fmt.Errorf("the copy of it left on %s couldn't be deleted: %w", machineLabel(to), err)
		}
	}
	if err := checkRoomFor(ctx, from, to, id); err != nil {
		return err
	}
	if err := stopOn(ctx, from, id); err != nil {
		return fmt.Errorf("it didn't stop: %w", err)
	}
	rid, err := s.copyFolder(ctx, from, to, id)
	if err != nil {
		return fmt.Errorf("its folder couldn't be copied to %s: %w", machineLabel(to), err)
	}
	cfg := st.Config
	in := api.MoveInRequest{ServerID: id, Name: st.Name, Slug: slug, MemoryMB: cfg.MemoryMB, PlayStyle: cfg.PlayStyle, Start: start,
		CreatedAt: cfg.CreatedAt, EULAAcceptedAt: cfg.EULAAcceptedAt, EULAAcceptedBy: cfg.EULAAcceptedBy, Actor: placementActor}
	if err := s.moveIn(ctx, mv, to, rid, in); err != nil {
		discardUpload(ctx, to, rid)
		return fmt.Errorf("%s couldn't make it from its folder: %w", machineLabel(to), err)
	}
	return nil
}

// moveIn has machine to make mv's server from upload rid as in asks, and
// waits until it has. The operation making it is recorded first, so a
// restart of the dashboard meanwhile carries on with that copy alone.
func (s *Server) moveIn(ctx context.Context, mv serverMove, to machine, rid string, in api.MoveInRequest) error {
	var op api.Operation
	status, err := to.agent.Do(asActor(ctx, placementActor), http.MethodPost, "/v1/restore/"+rid+"/move-in", nil, in, &op)
	switch {
	case err != nil:
		return err
	case status != http.StatusAccepted || op.ID == "":
		return fmt.Errorf("the agent answered %d to the move-in", status)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE server_moves SET made_by = ? WHERE server_id = ?`, op.ID, mv.serverID); err != nil {
		return errDB
	}
	_, err = waitMoveOp(ctx, to, op.ID)
	return err
}

// copyFolder sends server id's whole folder from one machine, where it's
// stopped, to another as an upload for a new server (the agent's move-out),
// and returns the upload's id there. It names no disk limit: the server
// counts against its customer's already, and the one made from it does once
// it's made (see moveServer). The folder's manifest names each file's
// checksum, and what arrived has to be what the dashboard sent.
func (s *Server) copyFolder(ctx context.Context, from, to machine, id string) (string, error) {
	down, err := from.agent.Raw(ctx, http.MethodGet, "/v1/servers/"+id+"/move-out", nil, nil,
		map[string]string{"X-Playkeeper-Actor": placementActor}, true)
	if err != nil {
		return "", err
	}
	defer down.Body.Close()
	if down.StatusCode >= 400 {
		return "", agentclient.DecodeError(down)
	}
	if down.StatusCode != http.StatusOK {
		return "", agentclient.ErrBadAnswer
	}
	sent := sha256.New()
	up, err := to.agent.Raw(ctx, http.MethodPost, "/v1/restore/upload", nil, io.TeeReader(down.Body, sent),
		map[string]string{"X-Playkeeper-Actor": placementActor, "Content-Type": "application/gzip"}, true)
	if err != nil {
		return "", err
	}
	defer up.Body.Close()
	if up.StatusCode >= 400 {
		return "", agentclient.DecodeError(up)
	}
	var p api.RestorePreview
	if up.StatusCode != http.StatusOK || json.NewDecoder(io.LimitReader(up.Body, 1<<20)).Decode(&p) != nil || !reUploadID.MatchString(p.ID) {
		return "", agentclient.ErrBadAnswer
	}
	if hex.EncodeToString(sent.Sum(nil)) != p.SHA256 {
		discardUpload(ctx, to, p.ID)
		return "", errors.New("it arrived changed")
	}
	return p.ID, nil
}

// copyMoveState gives server id, moved to machine to, what the agent on
// from kept about it (api.MoveState): its schedules, its sleep and public
// page settings, its map's and pack page's links, and its copies somewhere
// else with their secrets and keys, without which they no longer open. An
// own address that doesn't fit to's is left out, and the audit log says so.
func (s *Server) copyMoveState(ctx context.Context, id string, from, to machine) error {
	var state api.MoveState
	if err := askAgent(ctx, from, http.MethodGet, "/v1/servers/"+id+"/move-state", nil, &state); err != nil {
		return fmt.Errorf("%s didn't say what it keeps about it: %w", machineLabel(from), err)
	}
	var res api.MoveStateResult
	if err := askAgent(ctx, to, http.MethodPut, "/v1/servers/"+id+"/move-state", api.MoveStateRequest{State: state, Actor: placementActor}, &res); err != nil {
		return fmt.Errorf("%s didn't take what %s kept about it: %w", machineLabel(to), machineLabel(from), err)
	}
	if slices.Contains(res.Left, "ownAddress") {
		s.audit(placementActor, "customer.move", id, "own address left out", fmt.Sprintf("its own address doesn't fit %s's address", machineLabel(to)))
	}
	return nil
}

// discardUpload deletes upload rid on m, which no move-in took.
func discardUpload(ctx context.Context, m machine, rid string) {
	_, _ = m.agent.Do(asActor(ctx, placementActor), http.MethodDelete, "/v1/restore/"+rid, url.Values{"actor": {placementActor}}, nil, nil)
}

// copyBackupRules gives server id, moved to machine to, the automatic
// backups and backup rules it had on from. One that can't have them keeps
// the machine's defaults, and the log says so.
func (s *Server) copyBackupRules(ctx context.Context, id string, from, to machine) {
	var view struct {
		Automatic struct {
			Enabled      bool `json:"enabled"`
			EveryHours   int  `json:"everyHours"`
			OnlyIfPlayed bool `json:"onlyIfPlayed"`
		} `json:"automatic"`
		Rules  json.RawMessage `json:"rules"`
		Custom bool            `json:"custom"`
	}
	err := askAgent(ctx, from, http.MethodGet, "/v1/servers/"+id+"/backup-rules", nil, &view)
	if err == nil {
		body := map[string]any{"actor": placementActor}
		if view.Automatic.Enabled {
			body["automatic"] = view.Automatic
		}
		if view.Custom && len(view.Rules) > 0 {
			body["rules"] = view.Rules
		}
		err = askAgent(ctx, to, http.MethodPost, "/v1/servers/"+id+"/backup-rules", body, nil)
	}
	if err != nil {
		s.log.Warn("a server moved to another machine didn't get the backup rules it had", "server", id, "err", err)
	}
}

// switchServer sends the requests of mv's server to machine to, whose copy
// of it is complete, with the slug the dashboard showed for it, and ends its
// move: the copy on the machine it left is a copy it left, its final backup
// kept there movedBackupDays. Its links go with it. A listing of to's asked
// for before now can't drop its record, and its record keeps the server as
// last listed, so it's still listed while to is away.
func (s *Server) switchServer(ctx context.Context, mv serverMove, to machine, slug string) error {
	kept := ""
	if to.Kind == remoteKind {
		kept = slug
	}
	now := millis(s.now())
	err := s.immediate(ctx, func(c *sql.Conn) error {
		if _, err := c.ExecContext(ctx, `INSERT INTO server_machines(server_id, machine_id, slug, seen_at) VALUES(?,?,?,?)
			ON CONFLICT(server_id) DO UPDATE SET machine_id = excluded.machine_id, slug = excluded.slug, seen_at = excluded.seen_at, disputed_by = ''`,
			mv.serverID, to.ID, kept, now); err != nil {
			return err
		}
		if _, err := c.ExecContext(ctx, `UPDATE public_links SET machine_id = ? WHERE server_id = ?`, to.ID, mv.serverID); err != nil {
			return err
		}
		if _, err := c.ExecContext(ctx, `DELETE FROM left_copies WHERE server_id = ? AND machine_id = ?`, mv.serverID, to.ID); err != nil {
			return err
		}
		if err := leftCopy(ctx, c, mv.serverID, mv.from, mv.userID, movedBackupDays, now); err != nil {
			return err
		}
		if _, err := c.ExecContext(ctx, `DELETE FROM move_restarts WHERE server_id = ?`, mv.serverID); err != nil {
			return err
		}
		_, err := c.ExecContext(ctx, `DELETE FROM server_moves WHERE server_id = ?`, mv.serverID)
		return err
	})
	if err != nil {
		return errDB
	}
	return nil
}

// leftCopy records the copy of server id a move left on machineID, whose
// final backup is kept there keepDays when it's deleted, and which stopped
// being the server at switchedAt, or never was for 0.
func leftCopy(ctx context.Context, q querier, id, machineID string, userID int64, keepDays int, switchedAt int64) error {
	_, err := q.ExecContext(ctx, `INSERT INTO left_copies(server_id, machine_id, user_id, keep_days, switched_at) VALUES(?,?,?,?,?)
		ON CONFLICT(server_id, machine_id) DO UPDATE SET user_id = excluded.user_id, keep_days = excluded.keep_days, switched_at = excluded.switched_at, left_at = 0`,
		id, machineID, userID, keepDays, switchedAt)
	return err
}

// leftOnRemoved picks server id's copies a move left on machines since
// removed that weren't deleted.
const leftOnRemoved = `server_id = ? AND left_at = 0 AND machine_id NOT IN (SELECT id FROM machines WHERE revoked_at = 0)`

// adoptLeftCopy takes server id, as machine machineID lists it (sv), for
// one of the copies moves left of it on machines since removed, and has
// machineID delete it as that machine would have: a removed machine's host
// joins again only as another machine. It's taken only when it surely is
// one: when it stopped before the server's requests went where it moved, as
// the copy did when its move stopped it; or, while the server's own machine
// is joined and connected (ownerOnline), whatever it is, since that machine's
// host is another, and has the server. Otherwise one running, stopped since
// or not saying when may be the server, so it isn't. Each copy is taken
// once, and the others stay for their hosts. It reports whether it was
// taken.
func adoptLeftCopy(ctx context.Context, q querier, id, machineID string, sv map[string]any, ownerOnline bool) (bool, error) {
	var from string
	err := q.QueryRowContext(ctx, `SELECT machine_id FROM left_copies WHERE `+leftOnRemoved+` AND switched_at > ? ORDER BY switched_at, machine_id LIMIT 1`, id, listedStop(sv)).Scan(&from)
	if isNoRows(err) && ownerOnline {
		err = q.QueryRowContext(ctx, `SELECT machine_id FROM left_copies WHERE `+leftOnRemoved+` ORDER BY switched_at, machine_id LIMIT 1`, id).Scan(&from)
	}
	switch {
	case isNoRows(err):
		return false, nil
	case err != nil:
		return false, err
	}
	if _, err := q.ExecContext(ctx, `INSERT INTO left_copies(server_id, machine_id, user_id, keep_days, switched_at)
		SELECT server_id, ?, user_id, keep_days, switched_at FROM left_copies WHERE server_id = ? AND machine_id = ?
		ON CONFLICT(server_id, machine_id) DO NOTHING`, machineID, id, from); err != nil {
		return false, err
	}
	_, err = q.ExecContext(ctx, `DELETE FROM left_copies WHERE server_id = ? AND machine_id = ?`, id, from)
	return err == nil, err
}

// listedStop is when the server listed as sv stopped, in milliseconds, or
// the latest time there is for one that isn't stopped or doesn't say when.
// Docker's stop times are real ones, so it's after any switch time of 0.
func listedStop(sv map[string]any) int64 {
	stoppedAt, _ := sv["stoppedAt"].(string)
	stopped, err := time.Parse(time.RFC3339Nano, stoppedAt)
	if sv["phase"] != string(api.PhaseStopped) || err != nil {
		return math.MaxInt64
	}
	return millis(stopped)
}

// copiesLeftOnRemoved reports whether a move left a copy of server id on a
// machine since removed, which a listing of it can't surely be told from
// once the server's own machine was removed too.
func copiesLeftOnRemoved(ctx context.Context, q querier, id string) (bool, error) {
	var n int
	err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM left_copies WHERE `+leftOnRemoved, id).Scan(&n)
	return n > 0, err
}

// abandonMove ends mv, a move that failed before its server's requests
// went to the machine it was going to: they go where it is again, it
// starts there again if it ran and its customer isn't paused or suspended,
// and that machine deletes the copy it made. A start that fails is tried
// again once that machine answers (retryRestarts). A customer paused while
// it moved had it stopped already, and the hold on its machine may not be
// there yet to refuse the start, so pausing waits for this: coming first,
// it keeps the server stopped, and after, it stops it where it is.
func (s *Server) abandonMove(ctx context.Context, mv serverMove, from machine) {
	if !s.endMove(ctx, mv) {
		return
	}
	s.customersMu.Lock()
	if mv.ran && !s.customerHeld(ctx, mv.userID) {
		if err := startOn(ctx, from, mv.serverID); err != nil {
			s.log.Warn("a server whose move failed didn't start again where it is yet", "server", mv.serverID, "err", err)
			s.pendRestart(ctx, mv)
		} else {
			s.forgetRestart(mv.serverID)
		}
	}
	s.customersMu.Unlock()
	s.leaveMoveCopy(ctx, mv)
}

// startOn starts server id on m.
func startOn(ctx context.Context, m machine, id string) error {
	var op api.Operation
	_, err := m.agent.Do(asActor(ctx, placementActor), http.MethodPost, "/v1/servers/"+id+"/start", nil, api.ActionRequest{Actor: placementActor}, &op)
	return err
}

// pendRestart records that mv's server, which ran, is to start again where
// it is (see retryRestarts).
func (s *Server) pendRestart(ctx context.Context, mv serverMove) {
	if _, err := s.db.ExecContext(ctx, `INSERT INTO move_restarts(server_id, machine_id, user_id) VALUES(?,?,?)
		ON CONFLICT(server_id) DO UPDATE SET machine_id = excluded.machine_id, user_id = excluded.user_id`, mv.serverID, mv.from, mv.userID); err != nil {
		s.log.Error("could not record that a server is to start again", "server", mv.serverID, "err", err)
	}
}

// restartPending reports whether server id is to start again on machineID,
// where a failed move left it stopped.
func (s *Server) restartPending(ctx context.Context, id, machineID string) bool {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM move_restarts WHERE server_id = ? AND machine_id = ?`, id, machineID).Scan(&n)
	return err == nil && n > 0
}

func (s *Server) forgetRestart(id string) {
	if _, err := s.db.Exec(`DELETE FROM move_restarts WHERE server_id = ?`, id); err != nil {
		s.log.Error("could not forget that a server was to start again", "server", id, "err", err)
	}
}

// runProxy forwards a start, stop or restart of a server, which then isn't
// started again for a failed move: whoever started or stopped it since
// decides whether it runs.
func (s *Server) runProxy(pattern string) func(http.ResponseWriter, *http.Request, *session) {
	return func(w http.ResponseWriter, r *http.Request, sess *session) {
		id := r.PathValue("id")
		s.forwardThen(http.MethodPost, pattern, func(machine, *session, json.RawMessage) { s.forgetRestart(id) })(w, r, sess)
	}
}

// retryRestarts starts again the servers failed moves left stopped though
// they ran, on machineID, or on every machine for "": each where it is,
// unless it moved or went since, a move of it is under way, or its
// customer was paused or suspended.
func (s *Server) retryRestarts(ctx context.Context, machineID string) {
	rows, err := s.db.QueryContext(ctx, `SELECT server_id, machine_id, user_id FROM move_restarts WHERE ? = '' OR machine_id = ?`, machineID, machineID)
	if err != nil {
		s.log.Error("could not read the servers to start again", "err", err)
		return
	}
	var list []serverMove
	for rows.Next() {
		var mv serverMove
		if rows.Scan(&mv.serverID, &mv.from, &mv.userID) == nil {
			list = append(list, mv)
		}
	}
	rows.Close()
	for _, mv := range list {
		at, err := s.recordedMachine(ctx, mv.serverID)
		if err != nil {
			continue
		}
		if _, moving, err := s.serverMoveOf(ctx, mv.serverID); err != nil || moving {
			continue
		}
		m, err := s.machineByID(mv.from)
		switch {
		case at != mv.from || errors.Is(err, errNotFound):
			s.forgetRestart(mv.serverID)
			continue
		case err != nil:
			continue
		}
		s.customersMu.Lock()
		held := s.customerHeld(ctx, mv.userID)
		if held {
			s.forgetRestart(mv.serverID)
		} else if err := startOn(ctx, m, mv.serverID); err == nil {
			s.forgetRestart(mv.serverID)
		}
		s.customersMu.Unlock()
	}
}

// dropMove ends mv, a move whose server is gone from the machine it was
// on, removed with it or deleted, and the machine it was going to deletes
// the copy it made.
func (s *Server) dropMove(ctx context.Context, mv serverMove) {
	if s.endMove(ctx, mv) {
		s.leaveMoveCopy(ctx, mv)
	}
}

// undoMove ends mv, a move that can't go on: its server stays where it was
// (abandonMove), or out of reach on a removed machine (dropMove).
func (s *Server) undoMove(ctx context.Context, mv serverMove) {
	from, err := s.machineByID(mv.from)
	switch {
	case err == nil:
		s.abandonMove(ctx, mv, from)
	case errors.Is(err, errNotFound):
		s.dropMove(ctx, mv)
	default:
		s.log.Error("could not undo a server's move", "server", mv.serverID, "err", err)
	}
}

// undoMoves ends the moves of customer userID's servers, which can't go on
// (undoMove).
func (s *Server) undoMoves(ctx context.Context, userID int64) {
	rows, err := s.db.QueryContext(ctx, `SELECT server_id, from_machine, to_machine, ran FROM server_moves WHERE user_id = ?`, userID)
	if err != nil {
		s.log.Error("could not read a customer's servers being moved", "user", userID, "err", err)
		return
	}
	var list []serverMove
	for rows.Next() {
		mv := serverMove{userID: userID}
		if rows.Scan(&mv.serverID, &mv.from, &mv.to, &mv.ran) == nil {
			list = append(list, mv)
		}
	}
	rows.Close()
	for _, mv := range list {
		s.undoMove(ctx, mv)
	}
}

// endMove ends mv before its server's requests went to the machine it was
// going to: the copy that machine made is a copy left there. It reports
// whether it could.
func (s *Server) endMove(ctx context.Context, mv serverMove) bool {
	err := s.immediate(ctx, func(c *sql.Conn) error {
		if err := leftCopy(ctx, c, mv.serverID, mv.to, mv.userID, 0, 0); err != nil {
			return err
		}
		_, err := c.ExecContext(ctx, `DELETE FROM server_moves WHERE server_id = ?`, mv.serverID)
		return err
	})
	if err != nil {
		s.log.Error("could not undo a server's move", "server", mv.serverID, "err", err)
	}
	return err == nil
}

// leaveMoveCopy has the machine mv was going to delete the copy it made.
func (s *Server) leaveMoveCopy(ctx context.Context, mv serverMove) {
	if err := s.leaveCopy(ctx, mv.serverID, mv.to); err != nil {
		s.log.Warn("the machine a failed move was going to couldn't delete its copy yet", "server", mv.serverID, "err", err)
	}
}

// leaveCopy has machineID delete the copy of server id a move left on it,
// keeping the final backup its left_copies row says, and records when it
// went: that machine's listings count again from one asked for after that
// (see forgetLeft). A copy that is the server, or one it's moving to,
// stays. A removed machine's copy stays recorded, for its host joining
// again (see adoptLeftCopy).
func (s *Server) leaveCopy(ctx context.Context, id, machineID string) error {
	var userID int64
	var days int
	err := s.db.QueryRowContext(ctx, `SELECT user_id, keep_days FROM left_copies WHERE server_id = ? AND machine_id = ? AND left_at = 0`, id, machineID).Scan(&userID, &days)
	switch {
	case isNoRows(err):
		return nil
	case err != nil:
		return errDB
	}
	var busy int
	if err := s.db.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM server_machines WHERE server_id = ? AND machine_id = ?) + (SELECT COUNT(*) FROM server_moves WHERE server_id = ? AND to_machine = ?)`,
		id, machineID, id, machineID).Scan(&busy); err != nil {
		return errDB
	}
	m, err := s.machineByID(machineID)
	switch {
	case busy > 0:
		_, err = s.db.ExecContext(ctx, `DELETE FROM left_copies WHERE server_id = ? AND machine_id = ?`, id, machineID)
		return err
	case errors.Is(err, errNotFound):
		return nil
	case err != nil:
		return err
	}
	var st api.ServerStatus
	found, err := serverOn(ctx, m, id, &st)
	if err != nil {
		return err
	}
	if found {
		if err := deleteOn(ctx, m, id, st.Name, days, userID); err != nil {
			return err
		}
	}
	_, err = s.db.ExecContext(ctx, `UPDATE left_copies SET left_at = ? WHERE server_id = ? AND machine_id = ?`, millis(s.now()), id, machineID)
	return err
}

// serverOn asks m how server id is into st, and reports whether m has it.
func serverOn(ctx context.Context, m machine, id string, st *api.ServerStatus) (bool, error) {
	_, err := m.agent.Do(asActor(ctx, placementActor), http.MethodGet, "/v1/servers/"+id, nil, nil, st)
	var ae *agentclient.Error
	switch {
	case errors.As(err, &ae) && ae.Status == http.StatusNotFound:
		return false, nil
	case err != nil:
		return false, err
	}
	return true, nil
}

// stopOn stops server id on m, and waits until it has.
func stopOn(ctx context.Context, m machine, id string) error {
	var op api.Operation
	status, err := m.agent.Do(asActor(ctx, placementActor), http.MethodPost, "/v1/servers/"+id+"/stop", nil, api.ActionRequest{Actor: placementActor}, &op)
	if err == nil && status == http.StatusAccepted {
		_, err = waitMoveOp(ctx, m, op.ID)
	}
	return err
}

// deleteOn deletes server id, called name, on m, keeping its whole folder
// for userID days when days isn't 0, as the copy a move left.
func deleteOn(ctx context.Context, m machine, id, name string, days int, userID int64) error {
	req := api.DeleteServerRequest{Confirm: name, Actor: placementActor, ForgetKey: true}
	if days > 0 {
		req.KeepFinalBackupDays, req.KeptFor, req.KeepWhole = days, movedKeptFor(userID), true
	}
	_, err := agentOp(ctx, m, "/v1/servers/"+id+"/delete", req)
	return err
}

// movedKeptFor labels the final backups of the copies an account's moved
// servers left, apart from the final backups of its deleted servers
// (keptFor), which its dashboard lists.
func movedKeptFor(userID int64) string { return "moved-" + keptFor(userID) }

// agentOp starts an operation on m, posting body to path as the dashboard,
// and waits until it ends: an error unless it succeeded.
func agentOp(ctx context.Context, m machine, path string, body any) (api.Operation, error) {
	var op api.Operation
	status, err := m.agent.Do(asActor(ctx, placementActor), http.MethodPost, path, nil, body, &op)
	switch {
	case err != nil:
		return op, err
	case status != http.StatusAccepted || op.ID == "":
		return op, fmt.Errorf("the agent answered %d to %s", status, path)
	}
	return waitMoveOp(ctx, m, op.ID)
}

// waitMoveOp waits, for at most moveStepWait, until m's operation id has
// ended: an error unless it succeeded.
func waitMoveOp(ctx context.Context, m machine, id string) (api.Operation, error) {
	ctx, cancel := context.WithTimeout(ctx, moveStepWait)
	defer cancel()
	t := time.NewTicker(opPoll)
	defer t.Stop()
	for {
		var op api.Operation
		status, err := m.agent.Do(ctx, http.MethodGet, "/v1/operations/"+id, nil, nil, &op)
		switch {
		case err == nil && status == http.StatusOK && op.Status == api.OpSucceeded:
			return op, nil
		case err == nil && status == http.StatusOK && op.Status != api.OpRunning:
			return op, errors.New(cmp.Or(op.Error, "it failed"))
		}
		select {
		case <-ctx.Done():
			return api.Operation{}, fmt.Errorf("it didn't end within %s", moveStepWait)
		case <-t.C:
		}
	}
}

// switchTo sends to the disk limits with mv's copy counted as the server,
// then has the server's requests go there (switchServer). Sending machines
// their limits waits meanwhile, so a sync that read to's servers while the
// copy was hidden can't send them after the switch, leaving the server out
// of its customer's limit, hold and processor share there.
func (s *Server) switchTo(ctx context.Context, mv serverMove, to machine, slug string) error {
	s.diskSending.Lock()
	defer s.diskSending.Unlock()
	if err := s.sendLimitsTo(ctx, to, mv.serverID); err != nil {
		return fmt.Errorf("%s didn't take their disk limit: %w", machineLabel(to), err)
	}
	if err := s.switchServer(ctx, mv, to, slug); err != nil {
		return fmt.Errorf("its requests couldn't go to %s: %w", machineLabel(to), err)
	}
	return nil
}

// sendLimitsTo sends m the disk limits it should have now, as
// syncDiskLimits does for every machine, with the copy of server
// switching there counted as the server it's about to be.
func (s *Server) sendLimitsTo(ctx context.Context, m machine, switching string) error {
	list, err := s.machines()
	if err != nil {
		return err
	}
	in, err := s.diskInputs(ctx, list)
	if err != nil {
		return err
	}
	_, err = s.sendDiskLimits(ctx, m, in, false, switching)
	return err
}

// machineLabel is how the log and the owner's messages name a machine.
func machineLabel(m machine) string {
	if m.Name == "" && m.Kind == localKind {
		return "the dashboard's machine"
	}
	return cmp.Or(m.Name, m.ID)
}

// whyStopped is why a move stopped, for the owner: it may begin with a
// server's name, whose case stays as it is.
func whyStopped(err error) string {
	msg := err.Error()
	if !strings.HasSuffix(msg, ".") {
		msg += "."
	}
	return msg
}

// runMoves places again the customers whose machine was removed while the
// dashboard was stopped or before it did that, carries on the moves a
// restart stopped whose machines answer, then every moveRetry carries on
// those whose machines answer since, starts again the servers failed moves
// left stopped and asks machines that were away to delete the copies moves
// left on them, until ctx ends. A joined machine connecting does the first
// two for it at once (see movesReconnected).
func (s *Server) runMoves(ctx context.Context) {
	s.rehomeStranded(ctx)
	s.resumeMoves(ctx)
	t := time.NewTicker(moveRetry)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.resumeMoves(ctx)
			s.retryRestarts(ctx, "")
			s.leaveLeftovers(ctx)
		}
	}
}

// movesReconnected carries on what waited for machineID, a joined machine
// that connected: the moves it's in, and the servers failed moves left
// stopped on it.
func (s *Server) movesReconnected(machineID string) {
	s.moves.after(s.movesCtx, func() {
		s.retryRestarts(s.movesCtx, machineID)
		s.resumeMoves(s.movesCtx)
	})
}

// resumeMoves carries on each customer's move that hadn't stopped, and each
// server's move a restart left under way, once the machines it needs
// answer: a joined machine connects some time after the dashboard starts,
// and a move asking it anything before then would fail.
func (s *Server) resumeMoves(ctx context.Context) {
	rows, err := s.db.QueryContext(ctx, `SELECT user_id FROM customer_moves WHERE error = '' UNION SELECT user_id FROM server_moves`)
	if err != nil {
		s.log.Error("could not read the moves to carry on", "err", err)
		return
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	rows.Close()
	for _, id := range ids {
		if s.moveMachinesUp(ctx, id) {
			s.moves.start(s.movesCtx, id, s.moveCustomer)
		}
	}
}

// moveMachinesUp reports whether the joined machines customer userID's move
// asks are connected: theirs, and each their servers are on or being moved
// between. A removed machine asks nothing: a move leaves what's there (see
// moveServers).
func (s *Server) moveMachinesUp(ctx context.Context, userID int64) bool {
	rows, err := s.db.QueryContext(ctx, `SELECT machine_id FROM customer_homes WHERE user_id = ?
		UNION SELECT sm.machine_id FROM creator_servers cs JOIN server_machines sm ON sm.server_id = cs.server_id WHERE cs.user_id = ?
		UNION SELECT from_machine FROM server_moves WHERE user_id = ?
		UNION SELECT to_machine FROM server_moves WHERE user_id = ?`, userID, userID, userID, userID)
	if err != nil {
		return false
	}
	var ids []string
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	rows.Close()
	for _, id := range ids {
		if m, err := s.machineByID(id); err == nil && m.Kind == remoteKind && (s.hub == nil || !s.hub.Connected(m.ID)) {
			return false
		}
	}
	return true
}

// leaveLeftovers asks machines to delete the copies moves left on them,
// but not a customer's whose servers are being moved now.
func (s *Server) leaveLeftovers(ctx context.Context) {
	rows, err := s.db.QueryContext(ctx, `SELECT server_id, machine_id, user_id FROM left_copies WHERE left_at = 0`)
	if err != nil {
		s.log.Error("could not read the copies moves left", "err", err)
		return
	}
	type left struct {
		id, machineID string
		userID        int64
	}
	var list []left
	for rows.Next() {
		var l left
		if rows.Scan(&l.id, &l.machineID, &l.userID) == nil {
			list = append(list, l)
		}
	}
	rows.Close()
	for _, l := range list {
		if s.moves.running(l.userID) {
			continue
		}
		if err := s.leaveCopy(ctx, l.id, l.machineID); err != nil {
			s.log.Warn("a machine couldn't delete the copy a move left on it yet", "server", l.id, "machine", l.machineID, "err", err)
		}
	}
}

// machineCustomer is a customer a machine has, as the owner sees them on its
// page: its own, or one with servers there still (customersOnMachine).
type machineCustomer struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	Handle   string `json:"handle,omitempty"`
	State    string `json:"state"`
	PlanID   string `json:"planId,omitempty"`
	MemoryMB int    `json:"memoryMB"`
	Servers  int    `json:"servers"`
	// MachineID is their machine, "" while they wait for one, and Here how
	// many of their servers are on the machine listing them.
	MachineID string            `json:"machineId"`
	Here      int               `json:"here"`
	Move      *customerMoveView `json:"move,omitempty"`
}

// customerMoveView is a customer's move under way, or one that stopped.
type customerMoveView struct {
	StartedAt time.Time `json:"startedAt"`
	StartedBy string    `json:"startedBy"`
	// Left is how many of their servers aren't on their machine yet.
	Left  int    `json:"left"`
	Error string `json:"error,omitempty"`
}

// hMachineCustomerList lists the customers a machine has: its own, with
// their moves there, and those with servers there still, as a move that
// stopped leaves them, so a machine isn't taken for empty while it has any.
func (s *Server) hMachineCustomerList(w http.ResponseWriter, r *http.Request, _ *session) {
	m, ok := s.machineFromPath(w, r)
	if !ok {
		return
	}
	rows, err := s.db.QueryContext(r.Context(), `SELECT c.user_id, u.username, c.handle, c.state, c.plan_id,
		COALESCE((SELECT MAX(allowance_memory_mb) FROM project_members WHERE user_id = c.user_id), 0),
		(SELECT COUNT(*) FROM creator_servers WHERE user_id = c.user_id),
		COALESCE(h.machine_id, ''),
		(SELECT COUNT(*) FROM creator_servers cs JOIN server_machines sm ON sm.server_id = cs.server_id WHERE cs.user_id = c.user_id AND sm.machine_id = ?),
		(SELECT COUNT(*) FROM creator_servers cs JOIN server_machines sm ON sm.server_id = cs.server_id WHERE cs.user_id = c.user_id AND sm.machine_id != COALESCE(h.machine_id, '')),
		mv.user_id IS NOT NULL, COALESCE(mv.started_at, 0), COALESCE(mv.started_by, ''), COALESCE(mv.error, '')
		FROM customers c JOIN users u ON u.id = c.user_id LEFT JOIN customer_homes h ON h.user_id = c.user_id
		LEFT JOIN customer_moves mv ON mv.user_id = c.user_id
		WHERE c.user_id IN (`+customersOnMachine+`) ORDER BY u.username`, m.ID, m.ID, m.ID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, api.CodeInternal, "Database error.", "")
		return
	}
	defer rows.Close()
	out := []machineCustomer{}
	for rows.Next() {
		var c machineCustomer
		var moving bool
		var mv customerMoveView
		var started int64
		if err := rows.Scan(&c.ID, &c.Name, &c.Handle, &c.State, &c.PlanID, &c.MemoryMB, &c.Servers, &c.MachineID, &c.Here, &mv.Left, &moving, &started, &mv.StartedBy, &mv.Error); err != nil {
			writeErr(w, http.StatusInternalServerError, api.CodeInternal, "Database error.", "")
			return
		}
		if moving {
			mv.StartedAt = fromMillis(started)
			c.Move = &mv
		}
		out = append(out, c)
	}
	writeJSON(w, http.StatusOK, out)
}
