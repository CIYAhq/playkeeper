package panel

import (
	"context"
	"errors"
	"maps"
	"net"
	"net/netip"
	"slices"
	"strings"
	"sync"

	"github.com/CIYAhq/playkeeper/internal/api"
	"github.com/CIYAhq/playkeeper/internal/dnszone"
	"github.com/CIYAhq/playkeeper/internal/machinelink"
)

// Addresses without a port on every machine (cloud-fleet.md, section 4):
// the dashboard's zone (dnsanswers.go) also names each joined machine and
// each server on one, so a customer placed on a joined machine joins at
// their server's name with no port, as on the dashboard's own machine. A
// server's SRV record points at the name of the machine it runs on,
// <machine id>.m under the zone, and the server's name itself at the
// dashboard's machine, whose public page shows the server there
// (joinedpage.go). Bedrock players join at the machine's IP address, which
// the dashboard shows them. A server's name is a single label, so no server
// can take a machine's name and send another machine's players to itself.

// machineSubzone is the label joined machines' names go under.
const machineSubzone = "m"

// machineAddress is where the joined machine m is reached: its name in the
// dashboard's zone, relative to the zone, and the public IP address it last
// reached the dashboard from, which its link reports. ok is false for the
// dashboard's own machine, whose own records give its address, for a joined
// machine that hasn't connected, and for one that connects from an address
// players can't reach, such as one on the dashboard's own network.
func machineAddress(m machine, link *machinelink.Status) (name string, ip netip.Addr, ok bool) {
	if m.Kind != remoteKind || link == nil || !reMachineID.MatchString(m.ID) {
		return "", netip.Addr{}, false
	}
	ip, ok = publicAddress(link.Address)
	if !ok {
		return "", netip.Addr{}, false
	}
	return m.ID + "." + machineSubzone, ip, true
}

// publicAddress is the public IP address in a host or host:port, if it is
// one: not loopback, private, link-local, shared (100.64.0.0/10) or
// otherwise not for the internet.
func publicAddress(hostport string) (netip.Addr, bool) {
	host := hostport
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		host = h
	}
	ip, err := netip.ParseAddr(strings.Trim(host, "[]"))
	if err != nil {
		return netip.Addr{}, false
	}
	ip = ip.Unmap().WithZone("")
	return ip, ip.IsGlobalUnicast() && !ip.IsPrivate() && !netip.MustParsePrefix("100.64.0.0/10").Contains(ip)
}

// zoneServer is a server on a joined machine for the zone: its id, label
// (its slug), game port, and its machine's id, name and address.
type zoneServer struct {
	id, label string
	port      int
	machineID string
	machine   string
	ip        netip.Addr
}

// zoneServers picks the servers on joined machines the zone can name from
// a server list (allServers): each needs a slug and a port, a machine with
// a public address, and no other machine claiming it.
func zoneServers(servers []map[string]any, machines []machine, links map[string]*machinelink.Status) []zoneServer {
	byID := make(map[string]machine, len(machines))
	for _, m := range machines {
		byID[m.ID] = m
	}
	var out []zoneServer
	for _, sv := range servers {
		mid, _ := sv["machineId"].(string)
		id, _ := sv["id"].(string)
		slug, _ := sv["slug"].(string)
		port, _ := sv["gamePort"].(float64)
		disputed, _ := sv["disputed"].(bool)
		m, known := byID[mid]
		if !known || disputed || slug == "" || port < 1 || port > 65535 {
			continue
		}
		name, ip, ok := machineAddress(m, links[mid])
		if !ok {
			continue
		}
		out = append(out, zoneServer{id: id, label: slug, port: int(port), machineID: mid, machine: name, ip: ip})
	}
	return out
}

// joinedZoneServers are the servers on joined machines the zone names now:
// none, without asking any machine, when no machine has joined.
func (s *Server) joinedZoneServers(ctx context.Context) ([]zoneServer, error) {
	list, err := s.machines()
	if err != nil {
		return nil, errDB
	}
	if !slices.ContainsFunc(list, func(m machine) bool { return m.Kind == remoteKind }) {
		return nil, nil
	}
	links, err := s.joinedLinks(ctx)
	if err != nil {
		return nil, err
	}
	servers, list, err := s.allServers(ctx)
	if err != nil {
		return nil, err
	}
	return zoneServers(servers, list, links), nil
}

// joinedLinks are the joined machines' link statuses by machine id. Unlike
// linkStatuses, links that can't be read are an error, so the machine keeps
// the joined machines' names it answers rather than losing them all.
func (s *Server) joinedLinks(ctx context.Context) (map[string]*machinelink.Status, error) {
	if s.hub == nil {
		return nil, errors.New("machine links aren't running")
	}
	list, err := s.hub.Status(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[string]*machinelink.Status, len(list))
	for i := range list {
		out[list[i].MachineID] = &list[i]
	}
	return out, nil
}

// addJoined names servers on joined machines in the plan's zone: their
// machine's name at its address, and for each server its name at the
// dashboard's machine, as the dashboard's own servers' names are, and an
// SRV record taking players to the machine's name and the server's port.
// A server whose name the zone has already, or can't hold, is left out, as
// is any once the zone is full. It returns the servers named.
func (p *dnsPlan) addJoined(servers []zoneServer) []zoneServer {
	taken := map[string]bool{}
	for _, r := range p.zone.Records {
		taken[r.Name] = true
	}
	var named []zoneServer
	for _, j := range servers {
		typ := dnszone.TypeA
		if j.ip.Is6() {
			typ = dnszone.TypeAAAA
		}
		host := dnszone.Record{Name: j.machine, Type: typ, Value: j.ip.String()}
		srv := dnszone.Record{Name: "_minecraft._tcp." + j.label, Type: dnszone.TypeSRV, Value: j.machine + "." + p.zone.Name, Port: j.port}
		var add []dnszone.Record
		for _, m := range p.machine {
			add = append(add, dnszone.Record{Name: j.label, Type: m.Type, Value: m.Value})
		}
		add = append(add, srv)
		if !taken[host.Name] {
			add = append(add, host)
		}
		switch {
		case strings.Contains(j.label, "."), taken[j.label], taken[srv.Name]:
			continue
		case len(p.zone.Records)+len(add) > dnszone.MaxRecords, !p.holds(add):
			continue
		}
		p.zone.Records = append(p.zone.Records, add...)
		for _, r := range add {
			taken[r.Name] = true
		}
		named = append(named, j)
	}
	return named
}

// holds reports whether the plan's zone can hold every one of records.
func (p *dnsPlan) holds(records []dnszone.Record) bool {
	z := p.zone
	z.Records = records
	return z.Check() == nil
}

// zoneAddresses are the addresses without a port of the servers on joined
// machines the zone names, by server id, kept while public DNS finds the
// zone (api.AddressCheck.PortFree), and those servers by address. The
// server list gives each in place of its machine's IP address and the
// server's port, and the public page shows the server there.
type zoneAddresses struct {
	mu     sync.Mutex
	byID   map[string]string
	byName map[string]joinedName
}

// joinedName is a server on a joined machine at the address without a
// port the zone gives it: its id, its label there, which is its slug on
// the dashboard, and the machine the zone sends its players to.
type joinedName struct {
	id, label, address, machineID string
}

// setZoneAddresses keeps the addresses of named under host while portFree,
// and forgets every address otherwise. The page forgets what it showed at
// them once they change.
func (s *Server) setZoneAddresses(named []zoneServer, host string, portFree bool) {
	byID, byName := map[string]string{}, map[string]joinedName{}
	if portFree {
		for _, j := range named {
			address := j.label + "." + host
			byID[j.id] = address
			byName[address] = joinedName{id: j.id, label: j.label, address: address, machineID: j.machineID}
		}
	}
	s.zoneAddrs.mu.Lock()
	changed := !maps.Equal(s.zoneAddrs.byName, byName)
	s.zoneAddrs.byID, s.zoneAddrs.byName = byID, byName
	s.zoneAddrs.mu.Unlock()
	if changed {
		s.pageChanged()
	}
}

// zoneName is the server on a joined machine whose address without a port
// is the Host header host, if the zone gives one that address.
func (s *Server) zoneName(host string) (joinedName, bool) {
	name := hostName(host)
	s.zoneAddrs.mu.Lock()
	defer s.zoneAddrs.mu.Unlock()
	j, ok := s.zoneAddrs.byName[name]
	return j, ok
}

// zoneNames are the servers on joined machines the zone gives addresses,
// by address.
func (s *Server) zoneNames() []joinedName {
	s.zoneAddrs.mu.Lock()
	defer s.zoneAddrs.mu.Unlock()
	out := slices.Collect(maps.Values(s.zoneAddrs.byName))
	slices.SortFunc(out, func(a, b joinedName) int { return strings.Compare(a.address, b.address) })
	return out
}

// zoneAddress is the address without a port of the server id on a joined
// machine, or "" while it has none.
func (s *Server) zoneAddress(id string) string {
	s.zoneAddrs.mu.Lock()
	defer s.zoneAddrs.mu.Unlock()
	return s.zoneAddrs.byID[id]
}

// portFree reports whether public DNS finds the zone of the dashboard
// machine's address, as its agent last checked.
func portFree(addr api.Address) bool {
	return addr.Check != nil && addr.Check.PortFree
}
