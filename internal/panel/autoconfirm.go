package panel

import (
	"context"
	"errors"
	"net/netip"
	"regexp"
	"slices"
	"strconv"
	"time"

	"github.com/CIYAhq/playkeeper/internal/hetzner"
	"github.com/CIYAhq/playkeeper/internal/machinelink"
)

// Confirming joined machines by themselves (the fleet plan): a joined
// machine connected from a server in the owner's Hetzner project, as their
// read-only token reads it, is theirs, so the dashboard confirms it takes
// customers as the owner would by hand (takeCustomers): servers kept away
// from it first, and the audit log and the machine's events saying so. A
// machine the owner stopped taking customers stays stopped, and one outside
// the project waits for the owner as before.

// hetznerActor begins the actor that confirmed a machine found in the
// owner's Hetzner project; the server's name there follows.
const hetznerActor = "hetzner:"

// confirmRetry is how long a machine that couldn't be confirmed waits
// before the next try, so one that won't keep servers away from itself
// isn't asked every minute.
const confirmRetry = 15 * time.Minute

// reHetznerName is the shape of a Hetzner server's name, which the actor
// carries into the audit log and the machine's events.
var reHetznerName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.-]{0,62}$`)

// unconfirmed is a joined machine the owner hasn't confirmed or stopped,
// and the address its link comes from now.
type unconfirmed struct {
	id string
	ip netip.Addr
}

// unconfirmedMachines are the joined machines that are connected now, take
// no customers and were never stopped, less those that couldn't be
// confirmed lately. The caller holds s.hetznerMu.
func (s *Server) unconfirmedMachines(ctx context.Context) ([]unconfirmed, error) {
	list, err := s.machines()
	if err != nil {
		return nil, errDB
	}
	links := s.linkStatuses(ctx)
	var out []unconfirmed
	for _, m := range list {
		l := links[m.ID]
		if m.Kind != remoteKind || !m.customersAt.IsZero() || !m.customersStopped.IsZero() || l == nil || l.State != machinelink.StateConnected {
			continue
		}
		ip, err := netip.ParseAddr(l.Address)
		if err != nil {
			continue
		}
		if at, ok := s.confirmFailed[m.ID]; ok && s.now().Sub(at) < confirmRetry {
			continue
		}
		out = append(out, unconfirmed{id: m.ID, ip: ip})
	}
	return out, nil
}

// confirmFromHetzner confirms each unconfirmed machine whose address is a
// server in the project c reads. The caller holds s.hetznerMu.
func (s *Server) confirmFromHetzner(ctx context.Context, c *hetzner.Client) error {
	machines, err := s.unconfirmedMachines(ctx)
	if err != nil || len(machines) == 0 {
		return err
	}
	servers, err := c.Servers(ctx)
	if err != nil {
		return err
	}
	for _, m := range machines {
		i := slices.IndexFunc(servers, func(sv hetzner.Server) bool { return sv.Has(m.ip) })
		if i < 0 {
			continue
		}
		name := servers[i].Name
		if !reHetznerName.MatchString(name) {
			name = strconv.FormatInt(servers[i].ID, 10)
		}
		if err := s.confirmFound(ctx, m.id, hetznerActor+name); err != nil {
			if s.confirmFailed == nil {
				s.confirmFailed = map[string]time.Time{}
			}
			s.confirmFailed[m.id] = s.now()
			s.log.Warn("could not confirm a joined machine found in the Hetzner project", "machine", m.id, "err", err)
			continue
		}
		delete(s.confirmFailed, m.id)
	}
	return nil
}

// confirmFound confirms the joined machine id takes customers, as actor,
// unless it did meanwhile or the owner stopped it.
func (s *Server) confirmFound(ctx context.Context, id, actor string) error {
	s.placeMu.Lock()
	defer s.placeMu.Unlock()
	m, err := s.machineByID(id)
	switch {
	case errors.Is(err, errNotFound):
		return nil
	case err != nil:
		return errDB
	case m.Kind != remoteKind || !m.customersAt.IsZero() || !m.customersStopped.IsZero():
		return nil
	}
	return s.takeCustomers(ctx, m, actor, true)
}
