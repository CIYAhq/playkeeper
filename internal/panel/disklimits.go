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

// Disk limits, the dashboard's half. The servers a creator or customer
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

// syncDiskLimits sends every machine that answers the limits of the
// accounts whose servers it has, and every diskUseEvery keeps what those
// servers take.
func (s *Server) syncDiskLimits(ctx context.Context) {
	allowances, err := s.diskAllowances(ctx)
	if err != nil {
		s.log.Error("could not read the disk allowances", "err", err)
		return
	}
	holds, err := s.customerHolds(ctx)
	if err != nil {
		s.log.Error("could not read which customers are paused", "err", err)
		return
	}
	owners, err := s.creatorServerOwners(ctx)
	if err != nil {
		s.log.Error("could not read the creators' servers", "err", err)
		return
	}
	list, err := s.machines()
	if err != nil {
		s.log.Error("could not list the machines for their disk limits", "err", err)
		return
	}
	s.diskUse.Lock()
	count := s.diskUse.used == nil || s.now().Sub(s.diskUse.at) >= diskUseEvery
	s.diskUse.Unlock()
	used := map[int64]int64{}
	for _, m := range list {
		got, err := s.sendDiskLimits(ctx, m, allowances, holds, owners, count)
		s.noteDiskLimitsFailure(m, err)
		for uid, n := range got {
			used[uid] += n
		}
	}
	if count {
		s.diskUse.Lock()
		s.diskUse.at, s.diskUse.used = s.now(), used
		s.diskUse.Unlock()
	}
}

// sendDiskLimits sends m the limits of the accounts whose servers it has,
// and with count, returns what each one's servers take there. An account's
// server on m counts against its limit only while m still has it.
func (s *Server) sendDiskLimits(ctx context.Context, m machine, allowances map[int64]invites.Allowance, holds map[int64]string, owners map[string]int64, count bool) (map[int64]int64, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var servers []api.ServerStatus
	if err := askAgent(ctx, m, http.MethodGet, "/v1/servers", nil, &servers); err != nil {
		return nil, err
	}
	byAccount := map[int64][]string{}
	for _, sv := range servers {
		if uid, ok := owners[sv.ID]; ok {
			if _, ok := allowances[uid]; ok {
				byAccount[uid] = append(byAccount[uid], sv.ID)
			}
		}
	}
	limits := make([]api.DiskLimit, 0, len(byAccount))
	for uid, ids := range byAccount {
		limits = append(limits, api.DiskLimit{ID: diskLimitPrefix + strconv.FormatInt(uid, 10), LimitBytes: allowances[uid].DiskBytes(), Servers: ids, Hold: holds[uid]})
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
