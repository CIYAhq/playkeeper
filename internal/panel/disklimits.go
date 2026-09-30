package panel

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/CIYAhq/playkeeper/internal/api"
	"github.com/CIYAhq/playkeeper/internal/invites"
)

// Disk limits, the dashboard's half, which also carry each server's
// processor share (cpuMilliPerGB). The servers a creator or customer
// creates may take their allowance's disk between them (see
// invites.Allowance.DiskBytes), and each machine's agent refuses what would
// pass that (internal/agent/disklimits.go). The dashboard sends each machine
// the limits of the accounts whose servers it has: at once when a creator
// creates a server or an account is removed, and every minute anyway, so a
// machine that was away catches up. What each account's servers take comes
// back every few minutes, for Settings › Team.

const (
	// diskLimitsEvery is how often the limits are sent again anyway, and
	// diskUseEvery how often what they take is counted, which scans the
	// machines' disks.
	diskLimitsEvery = time.Minute
	diskUseEvery    = 5 * time.Minute
	// diskLimitPrefix begins each account's limit id on a machine.
	diskLimitPrefix = "account-"
	// cpuMilliPerGB is the processor share each creator's or customer's
	// server gets for each GB of its memory: half a core, as Playkeeper
	// Cloud's plans give.
	cpuMilliPerGB = 500
)

// kickDiskLimits has the limits sent now rather than at the next tick.
func (s *Server) kickDiskLimits() {
	select {
	case s.diskKick <- struct{}{}:
	default:
	}
}

// runDiskLimits sends the disk limits until ctx ends: at once, when
// kicked, and every diskLimitsEvery.
func (s *Server) runDiskLimits(ctx context.Context) {
	t := time.NewTicker(diskLimitsEvery)
	defer t.Stop()
	for {
		s.syncDiskLimits(ctx)
		select {
		case <-ctx.Done():
			return
		case <-s.diskKick:
		case <-t.C:
		}
	}
}

// diskAllowances is the allowance of each account that has one, from its
// first membership, as its access reads it.
func (s *Server) diskAllowances(ctx context.Context) (map[int64]invites.Allowance, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT user_id, allowance_servers, allowance_memory_mb, allowance_disk_gb FROM project_members
		ORDER BY user_id, created_at`)
	if err != nil {
		return nil, errDB
	}
	defer rows.Close()
	out := map[int64]invites.Allowance{}
	seen := map[int64]bool{}
	for rows.Next() {
		var uid int64
		var al invites.Allowance
		if err := rows.Scan(&uid, &al.Servers, &al.MemoryMB, &al.DiskGB); err != nil {
			return nil, errDB
		}
		if seen[uid] {
			continue
		}
		seen[uid] = true
		if !al.IsZero() {
			out[uid] = al
		}
	}
	if rows.Err() != nil {
		return nil, errDB
	}
	return out, nil
}

// customerHolds is why each paused or suspended customer's servers may not
// start, by account, as the machine's refusal finishes "<server> can't
// start: …".
func (s *Server) customerHolds(ctx context.Context) (map[int64]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT user_id, state FROM customers WHERE state IN (?, ?)`, string(CustomerPaused), string(CustomerSuspended))
	if err != nil {
		return nil, errDB
	}
	defer rows.Close()
	out := map[int64]string{}
	for rows.Next() {
		var uid int64
		var state string
		if err := rows.Scan(&uid, &state); err != nil {
			return nil, errDB
		}
		out[uid] = "the plan it's on has ended."
		if CustomerState(state) == CustomerSuspended {
			out[uid] = "its account is suspended."
		}
	}
	if rows.Err() != nil {
		return nil, errDB
	}
	return out, nil
}

// diskInputs are what the limits are made from: each account's allowance
// and hold, whose each server is, the customers' machines, the accounts
// whose allowance is split between machines (splitDisk), and what each
// account's servers took on each machine when last counted.
type diskInputs struct {
	allowances map[int64]invites.Allowance
	holds      map[int64]string
	owners     map[string]int64
	homes      map[int64]homeRow
	split      map[int64]bool
	usedOn     map[string]map[int64]int64
}

// splitDisk reports whether account userID's allowance is split between the
// machines its servers are on: they're on more than one while no move of
// theirs is under way. During a move the last counts are from before its
// servers switched machines, and the move counts them again once it ends.
func (s *Server) splitDisk(ctx context.Context, userID int64) bool {
	return s.serversApart(ctx, userID) && !s.moveUnderWay(ctx, userID)
}

// diskInputs reads what the limits for the machines in list are made from.
func (s *Server) diskInputs(ctx context.Context, list []machine) (diskInputs, error) {
	var in diskInputs
	var err error
	if in.allowances, err = s.diskAllowances(ctx); err != nil {
		return in, fmt.Errorf("the disk allowances: %w", err)
	}
	if in.holds, err = s.customerHolds(ctx); err != nil {
		return in, fmt.Errorf("which customers are paused: %w", err)
	}
	if in.owners, err = s.creatorServerOwners(ctx); err != nil {
		return in, fmt.Errorf("the creators' servers: %w", err)
	}
	if in.homes, err = s.customerHomes(ctx); err != nil {
		return in, fmt.Errorf("the customers' machines: %w", err)
	}
	in.split = map[int64]bool{}
	for uid := range in.allowances {
		in.split[uid] = s.splitDisk(ctx, uid)
	}
	in.usedOn = map[string]map[int64]int64{}
	s.diskUse.Lock()
	for _, m := range list {
		if used, ok := s.diskUse.usedOn[m.ID]; ok {
			in.usedOn[m.ID] = used
		}
	}
	s.diskUse.Unlock()
	return in, nil
}

// recountDisk has the limits sent now, counting what each account's
// servers take first, as a move that ended changed which machine has them.
// A count under way meanwhile began before that, so it doesn't stand for
// this one.
func (s *Server) recountDisk() {
	s.diskUse.Lock()
	s.diskUse.at = time.Time{}
	s.diskUse.recounts++
	s.diskUse.Unlock()
	s.kickDiskLimits()
}

// syncDiskLimits sends every machine that answers the limits of the
// accounts whose servers it has, and every diskUseEvery keeps what those
// servers take. The limits of an account split between machines come from
// the counts before a sync, so one that counts has another sync right after
// it make them from its counts.
func (s *Server) syncDiskLimits(ctx context.Context) {
	list, err := s.machines()
	if err != nil {
		s.log.Error("could not list the machines for their disk limits", "err", err)
		return
	}
	in, err := s.diskInputs(ctx, list)
	if err != nil {
		s.log.Error("could not read what the disk limits are made from", "err", err)
		return
	}
	s.diskUse.Lock()
	count := s.diskUse.used == nil || s.now().Sub(s.diskUse.at) >= diskUseEvery
	recounts := s.diskUse.recounts
	s.diskUse.Unlock()
	used := map[int64]int64{}
	on := map[string]map[int64]int64{}
	for _, m := range list {
		s.diskSending.Lock()
		got, err := s.sendDiskLimits(ctx, m, in, count, "")
		s.diskSending.Unlock()
		s.noteDiskLimitsFailure(m, err)
		for uid, n := range got {
			used[uid] += n
		}
		if prev, ok := in.usedOn[m.ID]; err != nil && ok {
			on[m.ID] = prev
		} else if err == nil {
			on[m.ID] = got
		}
	}
	if count {
		s.diskUse.Lock()
		s.diskUse.used, s.diskUse.usedOn = used, on
		if s.diskUse.recounts == recounts {
			s.diskUse.at = s.now()
		}
		s.diskUse.Unlock()
		for _, split := range in.split {
			if split {
				s.kickDiskLimits()
				break
			}
		}
	}
}

// sendDiskLimits sends m the limits of the accounts whose servers it has,
// or whose home it is, and with count, returns what each one's servers take
// there. An account's server on m counts against its limit only while m
// still has it, and a copy a move makes or leaves there only once it's the
// server, or when it's switching, about to be. An account whose allowance
// is split between machines gets on m what its servers on the others leave
// of it, as last counted, so it gets its allowance once between them.
func (s *Server) sendDiskLimits(ctx context.Context, m machine, in diskInputs, count bool, switching string) (map[int64]int64, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var servers []api.ServerStatus
	if err := askAgent(ctx, m, http.MethodGet, "/v1/servers", nil, &servers); err != nil {
		return nil, err
	}
	copies, err := hiddenCopies(ctx, s.db, m.ID)
	if err != nil {
		return nil, errDB
	}
	now := s.now()
	byAccount := map[int64][]string{}
	for _, sv := range servers {
		if sv.ID != switching && copyHidden(copies, sv.ID, now) {
			continue
		}
		if uid, ok := in.owners[sv.ID]; ok {
			if _, ok := in.allowances[uid]; ok {
				byAccount[uid] = append(byAccount[uid], sv.ID)
			}
		}
	}
	// The machine an account's servers go to has its limit before the first
	// one, so a world or backup uploaded for it counts from the start: a
	// customer's home, or the dashboard's own for a creator placement never
	// saw (homeMachine).
	for uid := range in.allowances {
		home, placed := in.homes[uid]
		if _, ok := byAccount[uid]; !ok && (placed && home.machineID == m.ID || !placed && m.Kind == localKind) {
			byAccount[uid] = []string{}
		}
	}
	limits := make([]api.DiskLimit, 0, len(byAccount))
	for uid, ids := range byAccount {
		limit := in.allowances[uid].DiskBytes()
		if in.split[uid] {
			for mid, used := range in.usedOn {
				if mid != m.ID {
					limit -= used[uid]
				}
			}
			limit = max(limit, 1)
		}
		limits = append(limits, api.DiskLimit{ID: diskLimitPrefix + strconv.FormatInt(uid, 10), LimitBytes: limit, Servers: ids, CPUMilliPerGB: cpuMilliPerGB, Hold: in.holds[uid]})
	}
	req := api.DiskLimitsRequest{Limits: limits, Actor: placementActor}
	if err := askAgent(ctx, m, http.MethodPut, "/v1/disk-limits", req, nil); err != nil || !count {
		return nil, err
	}
	var got []api.DiskLimit
	if err := askAgent(ctx, m, http.MethodGet, "/v1/disk-limits", nil, &got); err != nil {
		return nil, err
	}
	used := map[int64]int64{}
	for _, l := range got {
		rest, ok := strings.CutPrefix(l.ID, diskLimitPrefix)
		if uid, err := strconv.ParseInt(rest, 10, 64); ok && err == nil {
			used[uid] = l.UsedBytes
		}
	}
	return used, nil
}

// askAgent asks m's agent, as the dashboard, and wants an OK.
func askAgent(ctx context.Context, m machine, method, path string, body, out any) error {
	status, err := m.agent.Do(asActor(ctx, placementActor), method, path, nil, body, out)
	if err == nil && status != http.StatusOK {
		err = fmt.Errorf("the agent answered %d to %s %s", status, method, path)
	}
	return err
}

// noteDiskLimitsFailure logs a machine that couldn't take its limits, once
// until what went wrong changes or it takes them again.
func (s *Server) noteDiskLimitsFailure(m machine, err error) {
	why := ""
	if err != nil {
		why = err.Error()
	}
	s.diskUse.Lock()
	if s.diskUse.failed == nil {
		s.diskUse.failed = map[string]string{}
	}
	last := s.diskUse.failed[m.ID]
	s.diskUse.failed[m.ID] = why
	s.diskUse.Unlock()
	switch {
	case why != "" && why != last:
		s.log.Warn("a machine didn't take its disk limits", "machine", m.ID, "err", err)
	case why == "" && last != "":
		s.log.Info("a machine took its disk limits again", "machine", m.ID)
	}
}

// diskUsed is what the account's servers took when last counted, or nil
// before they have been.
func (s *Server) diskUsed(userID int64) *int64 {
	s.diskUse.Lock()
	defer s.diskUse.Unlock()
	if n, ok := s.diskUse.used[userID]; ok {
		return &n
	}
	return nil
}
