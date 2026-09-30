package panel

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"sync"

	"github.com/CIYAhq/playkeeper/internal/api"
)

// Placement (the fleet plan): each customer's home machine, where their
// plan's memory is set aside and every server they create runs. The hosting
// core calls placeCustomer once it has made a customer's account, and
// homeMachine for everything after (see hosting.go).
//
// A machine takes customers when it answers, is the owner's and keeps
// servers away from itself. The dashboard's own machine is always the
// owner's, so a standalone Playkeeper, such as a Whop blueprint seller's,
// places customers on itself with no Hetzner token and no other machine.
// A joined machine takes none until the owner confirms it's theirs (see
// machinecustomers.go), so a join code that leaked can't pull customers
// onto a stranger's machine. Of the machines with room for a plan, the
// fullest gets the customer, so machines fill one at a time.

// errNoRoom is placeCustomer's answer when no machine that takes customers
// has room for the plan: the customer waits without a home, and the core
// tells them their server is being set up.
var errNoRoom = errors.New("No machine that takes customers has room for this plan right now.")

// placementActor is who the audit log says placed a customer.
const placementActor = "playkeeper"

// machineRoom is one machine as placement sees it.
type machineRoom struct {
	machine
	// Takes says whether the machine takes new customers, and Why when it
	// doesn't.
	Takes bool
	Why   string
	// FreeMB is the memory it can still set aside: what its servers' budgets
	// leave, less the part of its customers' plans they haven't used yet.
	FreeMB int
	// Guarded says whether Keep servers away from this machine is on.
	Guarded bool
}

// homeRow is a customer_homes row: the machine, or "" while they wait.
type homeRow struct {
	machineID string
}

// customerHomes are the rows of customer_homes by account.
func (s *Server) customerHomes(ctx context.Context) (map[int64]homeRow, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT user_id, machine_id FROM customer_homes`)
	if err != nil {
		return nil, errDB
	}
	defer rows.Close()
	out := map[int64]homeRow{}
	for rows.Next() {
		var id int64
		var h homeRow
		if err := rows.Scan(&id, &h.machineID); err != nil {
			return nil, errDB
		}
		out[id] = h
	}
	if rows.Err() != nil {
		return nil, errDB
	}
	return out, nil
}

// creatorMemory is each creator account's memory allowance: the plan's
// memory for a customer, what the owner gave for an invited creator.
func (s *Server) creatorMemory(ctx context.Context) (map[int64]int, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT user_id, MAX(allowance_memory_mb) FROM project_members WHERE allowance_memory_mb > 0 GROUP BY user_id`)
	if err != nil {
		return nil, errDB
	}
	defer rows.Close()
	out := map[int64]int{}
	for rows.Next() {
		var id int64
		var mb int
		if err := rows.Scan(&id, &mb); err != nil {
			return nil, errDB
		}
		out[id] = mb
	}
	if rows.Err() != nil {
		return nil, errDB
	}
	return out, nil
}

// creatorServerOwners is who created each server a creator created.
func (s *Server) creatorServerOwners(ctx context.Context) (map[string]int64, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT server_id, user_id FROM creator_servers`)
	if err != nil {
		return nil, errDB
	}
	defer rows.Close()
	out := map[string]int64{}
	for rows.Next() {
		var id string
		var uid int64
		if err := rows.Scan(&id, &uid); err != nil {
			return nil, errDB
		}
		out[id] = uid
	}
	if rows.Err() != nil {
		return nil, errDB
	}
	return out, nil
}

// fleetRooms is every machine as placement sees it now. The customer except
// (0 for none) counts on no machine, as the one being placed.
func (s *Server) fleetRooms(ctx context.Context, except int64) ([]machineRoom, error) {
	list, err := s.machines()
	if err != nil {
		return nil, errDB
	}
	homes, err := s.customerHomes(ctx)
	if err != nil {
		return nil, err
	}
	memory, err := s.creatorMemory(ctx)
	if err != nil {
		return nil, err
	}
	owners, err := s.creatorServerOwners(ctx)
	if err != nil {
		return nil, err
	}
	var local string
	for _, m := range list {
		if m.Kind == localKind {
			local = m.ID
		}
	}
	rooms := make([]machineRoom, len(list))
	var wg sync.WaitGroup
	for i, m := range list {
		wg.Go(func() {
			rooms[i] = s.machineRoom(ctx, m, local, except, homes, memory, owners)
		})
	}
	wg.Wait()
	return rooms, nil
}

// machineRoom works out what m can still give customers. A creator is on
// the machine their home row names, or on the dashboard's own machine
// without one; one waiting for room is on none.
func (s *Server) machineRoom(ctx context.Context, m machine, local string, except int64, homes map[int64]homeRow, memory map[int64]int, owners map[string]int64) machineRoom {
	r := machineRoom{machine: m}
	ctx, cancel := context.WithTimeout(ctx, machineTimeout)
	defer cancel()
	var live api.Machine
	var servers []api.ServerStatus
	st, err := m.agent.Do(ctx, http.MethodGet, "/v1/machine", nil, nil, &live)
	if err == nil && st == http.StatusOK {
		st, err = m.agent.Do(ctx, http.MethodGet, "/v1/servers", nil, nil, &servers)
	}
	if err != nil || st != http.StatusOK {
		r.Why = "The machine isn't answering."
		return r
	}
	used := map[int64]int{}
	for _, sv := range servers {
		if uid, ok := owners[sv.ID]; ok && sv.Config != nil {
			used[uid] += sv.Config.MemoryMB
		}
	}
	aside := 0
	for uid, mb := range memory {
		home, ok := homes[uid]
		switch {
		case uid == except:
			continue
		case !ok:
			home.machineID = local
		}
		if home.machineID == m.ID {
			aside += max(0, mb-used[uid])
		}
	}
	r.FreeMB = live.MemoryFreeMB - aside
	r.Guarded = live.Guard != nil && live.Guard.Host
	switch {
	case m.Kind == localKind:
		r.Takes = true
	case m.Kind == remoteKind && !m.customersAt.IsZero():
		r.Takes = true
	default:
		r.Why = "Joined machines take customers once you confirm them."
	}
	return r
}

// chooseMachine is the machine to place a plan of needMB on: of those that
// take customers and have room, the one with the least room left, then the
// dashboard's own machine, then by id.
func chooseMachine(rooms []machineRoom, needMB int) (machineRoom, bool) {
	var fits []machineRoom
	for _, r := range rooms {
		if r.Takes && r.FreeMB >= needMB {
			fits = append(fits, r)
		}
	}
	if len(fits) == 0 {
		return machineRoom{}, false
	}
	return slices.MinFunc(fits, func(a, b machineRoom) int {
		return cmp.Or(cmp.Compare(a.FreeMB, b.FreeMB), cmp.Compare(kindRank(a.Kind), kindRank(b.Kind)), cmp.Compare(a.ID, b.ID))
	}), true
}

func kindRank(kind string) int {
	if kind == localKind {
		return 0
	}
	return 1
}

// placeCustomer gives the customer userID a home machine with room for
// plan's memory, and returns it. A customer who has one keeps it. With no
// room anywhere they wait, and the answer is errNoRoom. A machine that
// doesn't keep servers away from itself yet gets that turned on first, as
// a creator invite does, and is passed over if it can't be. The disk limits
// are sent at once, so the machine has the customer's before they upload a
// world or backup for their first server.
func (s *Server) placeCustomer(ctx context.Context, userID int64, plan CustomerPlan) (string, error) {
	if plan.MemoryMB <= 0 {
		return "", fmt.Errorf("the plan %q allows no memory", plan.ID)
	}
	s.placeMu.Lock()
	defer s.placeMu.Unlock()
	var have string
	waiting := false
	switch err := s.db.QueryRowContext(ctx, `SELECT machine_id FROM customer_homes WHERE user_id = ?`, userID).Scan(&have); {
	case err == nil && have != "":
		return have, nil
	case err == nil:
		waiting = true
	case !isNoRows(err):
		return "", errDB
	}
	rooms, err := s.fleetRooms(ctx, userID)
	if err != nil {
		return "", err
	}
	for {
		m, ok := chooseMachine(rooms, plan.MemoryMB)
		if !ok {
			if err := s.setHome(ctx, userID, ""); err != nil {
				return "", err
			}
			s.kickSaleRoom()
			if !waiting {
				s.audit(placementActor, "customer.place", fmt.Sprint(userID), "waiting", fmt.Sprintf("no machine has %s free for %s", gbText(plan.MemoryMB), cmp.Or(plan.Name, plan.ID)))
			}
			return "", errNoRoom
		}
		if !m.Guarded {
			if g, err := setNetworkGuard(ctx, m.machine, placementActor, true); err != nil || !g.Host {
				s.log.Warn("placement couldn't keep servers away from a machine, so it takes no customers", "machine", m.ID, "err", err)
				rooms = slices.DeleteFunc(rooms, func(r machineRoom) bool { return r.ID == m.ID })
				continue
			}
		}
		if err := s.setHome(ctx, userID, m.ID); err != nil {
			return "", err
		}
		s.kickDiskLimits()
		s.kickSaleRoom()
		s.audit(placementActor, "customer.place", fmt.Sprint(userID), "placed", fmt.Sprintf("on %s, with %s set aside for %s", cmp.Or(m.Name, m.ID), gbText(plan.MemoryMB), cmp.Or(plan.Name, plan.ID)))
		return m.ID, nil
	}
}

// setHome records the customer's home machine, or "" while they wait.
func (s *Server) setHome(ctx context.Context, userID int64, machineID string) error {
	if _, err := s.db.ExecContext(ctx, `INSERT INTO customer_homes(user_id, machine_id, placed_at) VALUES(?,?,?)
		ON CONFLICT(user_id) DO UPDATE SET machine_id = excluded.machine_id, placed_at = excluded.placed_at`,
		userID, machineID, s.now().UnixMilli()); err != nil {
		return errDB
	}
	return nil
}

// homeMachine is the machine userID's servers live on: the one placement
// gave them, or the dashboard's own for a creator placement never saw, as
// an owner's invite makes. ok is false for anyone else, and for a customer
// still waiting for room.
func (s *Server) homeMachine(ctx context.Context, userID int64) (machineID string, ok bool, err error) {
	switch err := s.db.QueryRowContext(ctx, `SELECT machine_id FROM customer_homes WHERE user_id = ?`, userID).Scan(&machineID); {
	case err == nil:
		return machineID, machineID != "", nil
	case !isNoRows(err):
		return "", false, errDB
	}
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM project_members WHERE user_id = ? AND (allowance_servers > 0 OR allowance_memory_mb > 0)`, userID).Scan(&n); err != nil {
		return "", false, errDB
	}
	if n == 0 {
		return "", false, nil
	}
	m, err := s.localMachine()
	if err != nil {
		return "", false, err
	}
	return m.ID, true, nil
}
