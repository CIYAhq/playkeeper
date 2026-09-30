package panel

import (
	"cmp"
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"time"
)

// Room for sale (the fleet plan's step 5): how many more of each plan the
// machines can take, which the billing side makes each plan's stock (see
// whop_stock.go), after every change in room and every saleRoomEvery. Any
// mix of one store's numbers fits at once: room goes out one server at a
// time, one of each of its plans in turn, paid plans first and the
// smallest first, then the free ones, each on the machine placement would
// pick for it (chooseMachine). The customers waiting for room get theirs
// before any plan does.
//
// Stores selling on the same machines (the hosted blueprint's 1.3) share
// their room: each store's plans get room as if the store sold alone, until
// what they're offered together reaches the store's share, the room the
// customers waiting leave divided evenly among the stores. Each plan is
// offered once while the room fits it, whatever its store's share, so no
// store shows sold out while there's room for its plan. Different stores'
// numbers can promise the same room, so a buyer who comes just as it runs
// out waits for room, told their server is being set up, as when two
// buyers race for the last of it.

// saleRoomEvery is how often the plans' room is worked out again anyway,
// and saleRoomSettle how long after the dashboard starts it's first worked
// out: joined machines connect again by then, and until they do they'd
// look like they have no room.
const (
	saleRoomEvery  = 5 * time.Minute
	saleRoomSettle = time.Minute
)

// roomForPlans is how many more of each plan fit in rooms, after the
// customers waiting for room, needing waiting's memory in turn, get
// theirs: any mix of one store's numbers at once, each store up to its
// share of the room.
func roomForPlans(rooms []machineRoom, waiting []int, plans []SalePlan) map[string]int {
	free := slices.Clone(rooms)
	for _, mb := range waiting {
		if mb > 0 {
			give(free, mb)
		}
	}
	left := map[string]int{}
	stores := map[string][]SalePlan{}
	for _, p := range plans {
		left[p.ID] = 0
		if p.MemoryMB > 0 {
			stores[p.Store] = append(stores[p.Store], p)
		}
	}
	if len(stores) == 0 {
		return left
	}
	pool := 0
	for _, r := range free {
		if r.Takes {
			pool += max(r.FreeMB, 0)
		}
	}
	for _, sold := range stores {
		roomForStore(slices.Clone(free), sold, pool/len(stores), left)
	}
	return left
}

// roomForStore hands out free to one store's plans, one server of each in
// turn, until what they're offered together reaches share, each plan's
// first whatever share is, and adds it to left.
func roomForStore(free []machineRoom, plans []SalePlan, share int, left map[string]int) {
	order := slices.Clone(plans)
	slices.SortStableFunc(order, func(a, b SalePlan) int {
		return cmp.Or(compareBool(a.Free, b.Free), cmp.Compare(a.MemoryMB, b.MemoryMB), cmp.Compare(a.ID, b.ID))
	})
	offered := 0
	for gave := true; gave; {
		gave = false
		for _, p := range order {
			if left[p.ID] >= maxWhopStock || (left[p.ID] > 0 && offered >= share) {
				continue
			}
			if give(free, p.MemoryMB) {
				left[p.ID]++
				offered += p.MemoryMB
				gave = true
			}
		}
	}
}

// give sets mb aside on the machine in free that placement would pick for
// it, and reports whether one has room.
func give(free []machineRoom, mb int) bool {
	m, ok := chooseMachine(free, mb)
	if !ok {
		return false
	}
	i := slices.IndexFunc(free, func(r machineRoom) bool { return r.ID == m.ID })
	free[i].FreeMB -= mb
	return true
}

// compareBool orders false before true.
func compareBool(a, b bool) int {
	switch {
	case a == b:
		return 0
	case !a:
		return -1
	}
	return 1
}

// waitingMemory is the memory each active customer waiting for room needs,
// in the order they'd be placed (see startWaitingCustomers).
func (s *Server) waitingMemory(ctx context.Context) ([]int, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT COALESCE(MAX(m.allowance_memory_mb), 0) FROM customers c
		JOIN customer_homes h ON h.user_id = c.user_id LEFT JOIN project_members m ON m.user_id = c.user_id
		WHERE c.state = ? AND h.machine_id = '' GROUP BY c.user_id ORDER BY c.created_at`, string(CustomerActive))
	if err != nil {
		return nil, errDB
	}
	defer rows.Close()
	var out []int
	for rows.Next() {
		var mb int
		if err := rows.Scan(&mb); err != nil {
			return nil, errDB
		}
		out = append(out, mb)
	}
	if rows.Err() != nil {
		return nil, errDB
	}
	return out, nil
}

// saleRoom is the plans the store sells, how many more of each fit, and
// the machines as placement sees them now.
type saleRoom struct {
	plans []SalePlan
	left  map[string]int
	rooms []machineRoom
}

// currentSaleRoom works saleRoom out, with no customer being placed
// meanwhile.
func (s *Server) currentSaleRoom(ctx context.Context) (saleRoom, error) {
	plans, err := s.sales.SalePlans(ctx)
	if err != nil {
		return saleRoom{}, errDB
	}
	s.placeMu.Lock()
	defer s.placeMu.Unlock()
	rooms, err := s.fleetRooms(ctx, 0)
	if err != nil {
		return saleRoom{}, err
	}
	waiting, err := s.waitingMemory(ctx)
	if err != nil {
		return saleRoom{}, err
	}
	return saleRoom{plans: plans, left: roomForPlans(rooms, waiting, plans), rooms: rooms}, nil
}

// syncSaleRoom tells the billing side how many more of each plan fit.
func (s *Server) syncSaleRoom(ctx context.Context) {
	room, err := s.currentSaleRoom(ctx)
	if err != nil {
		s.log.Warn("could not work out the plans' room", "err", err)
		return
	}
	if len(room.plans) == 0 {
		return
	}
	if err := s.sales.SetAvailability(ctx, room.left); err != nil {
		s.log.Warn("could not give the billing side the plans' room", "err", err)
	}
}

// kickSaleRoom has the plans' room worked out again now, as the room just
// changed.
func (s *Server) kickSaleRoom() {
	select {
	case s.saleRoomKick <- struct{}{}:
	default:
	}
}

// roomChanged is kickSaleRoom after a request that changed what a
// machine's servers take.
func (s *Server) roomChanged(machine, *session, json.RawMessage) { s.kickSaleRoom() }

// runSaleRoom keeps the plans' stock following the room until ctx ends:
// saleRoomSettle after it starts, when kicked, and every saleRoomEvery.
func (s *Server) runSaleRoom(ctx context.Context) {
	t := time.NewTicker(saleRoomEvery)
	defer t.Stop()
	settled := time.After(saleRoomSettle)
	for {
		select {
		case <-ctx.Done():
			return
		case <-settled:
		case <-s.saleRoomKick:
		case <-t.C:
		}
		s.syncSaleRoom(ctx)
	}
}

// saleRoomView is Settings › Machines' room for customers, for the owner:
// how many more of each plan fit, with the store selling it, and what each
// machine can still set aside for customers, or why it takes none.
type saleRoomView struct {
	Plans    []planRoomView    `json:"plans"`
	Machines []machineRoomView `json:"machines"`
}

type planRoomView struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Store     string `json:"store"`
	StoreName string `json:"storeName"`
	MemoryMB  int    `json:"memoryMB"`
	Free      bool   `json:"free"`
	Left      int    `json:"left"`
}

type machineRoomView struct {
	ID     string `json:"id"`
	FreeMB int    `json:"freeMB"`
	Takes  bool   `json:"takes"`
	Why    string `json:"why,omitempty"`
}

// hSaleRoom is saleRoomView.
func (s *Server) hSaleRoom(w http.ResponseWriter, r *http.Request, _ *session) {
	room, err := s.currentSaleRoom(r.Context())
	if err != nil {
		s.listFailure(w, err)
		return
	}
	v := saleRoomView{Plans: []planRoomView{}, Machines: []machineRoomView{}}
	for _, p := range room.plans {
		v.Plans = append(v.Plans, planRoomView{ID: p.ID, Name: p.Name, Store: p.Store, StoreName: p.StoreName, MemoryMB: p.MemoryMB, Free: p.Free, Left: room.left[p.ID]})
	}
	for _, m := range room.rooms {
		v.Machines = append(v.Machines, machineRoomView{ID: m.ID, FreeMB: max(m.FreeMB, 0), Takes: m.Takes, Why: m.Why})
	}
	writeJSON(w, http.StatusOK, v)
}
