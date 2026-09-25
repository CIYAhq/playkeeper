package certs

import (
	"context"
	"fmt"
	"net/netip"
	"strconv"
	"strings"
)

// MinecraftPort is the port Minecraft clients use when an address has none.
const MinecraftPort = 25565

// srvTTL is the TTL suggested for the records, short enough that mistakes
// are quickly corrected.
const srvTTL = 300

// maxPlanServers bounds the servers in one Plan.
const maxPlanServers = 64

// Plan gives a machine and its Minecraft servers friendly addresses. Servers
// share the machine's name and differ by port; an SRV record per server lets
// players type a name without the port.
type Plan struct {
	// Name is the machine's name, such as mc.example.com: the dashboard's
	// address and the target of the servers' SRV records.
	Name string `json:"name"`
	// IPv4 and IPv6 are the addresses Name's A and AAAA records hold; an
	// invalid address means no such record.
	IPv4    netip.Addr   `json:"ipv4,omitzero"`
	IPv6    netip.Addr   `json:"ipv6,omitzero"`
	Servers []JoinServer `json:"servers"`
}

// JoinServer is one Minecraft server of the machine.
type JoinServer struct {
	ID string `json:"id"`
	// Host is the name players type to join, such as survival.example.com
	// or Name itself. Empty means Name with the port (Name alone on port
	// 25565), which needs no record of its own.
	Host string `json:"host,omitempty"`
	Port int    `json:"port"`
}

// Record is a DNS record to create at the DNS provider.
type Record struct {
	// ServerID is the server an SRV record is for.
	ServerID string `json:"serverId,omitempty"`
	Type     string `json:"type"`
	// Name is the record's full name, without a trailing dot.
	Name string `json:"name"`
	// Value is the record's content as zone files and most providers show
	// it, such as "0 5 25566 mc.example.com." for SRV.
	Value string    `json:"value"`
	TTL   int       `json:"ttl"`
	SRV   *SRVParts `json:"srv,omitempty"`
}

// SRVParts are an SRV record's fields, for DNS providers that ask for them
// one by one.
type SRVParts struct {
	Service  string `json:"service"`
	Protocol string `json:"protocol"`
	// Host is the name players type; Name is _minecraft._tcp.Host.
	Host     string `json:"host"`
	Priority int    `json:"priority"`
	Weight   int    `json:"weight"`
	Port     int    `json:"port"`
	// Target is the name the record points to, without a trailing dot.
	Target string `json:"target"`
}

// JoinAddress is what players type to join one server.
type JoinAddress struct {
	ServerID string `json:"serverId"`
	// Address is the friendly address; it works once the plan's records do.
	Address string `json:"address"`
	// Direct is Name with the port, which works without SRV records.
	Direct string `json:"direct"`
}

// normalized checks the plan and returns it with every name normalized.
// Errors are *Problems with code invalid_name or invalid_plan.
func (p Plan) normalized() (Plan, error) {
	name, err := NormalizeName(p.Name)
	if err != nil {
		return Plan{}, err
	}
	out := Plan{Name: name}
	if p.IPv4.IsValid() {
		if !p.IPv4.Unmap().Is4() {
			return Plan{}, newProblem(nil, CodeInvalidPlan, map[string]string{"kind": "bad_address"})
		}
		out.IPv4 = p.IPv4.Unmap()
	}
	if p.IPv6.IsValid() {
		if !p.IPv6.Is6() || p.IPv6.Is4In6() {
			return Plan{}, newProblem(nil, CodeInvalidPlan, map[string]string{"kind": "bad_address"})
		}
		out.IPv6 = p.IPv6.WithZone("")
	}
	if len(p.Servers) > maxPlanServers {
		return Plan{}, newProblem(nil, CodeInvalidPlan, map[string]string{"kind": "too_many"})
	}
	ports := map[int]bool{}
	addresses := map[string]bool{}
	for _, s := range p.Servers {
		if s.Port < 1 || s.Port > 65535 {
			return Plan{}, newProblem(nil, CodeInvalidPlan, map[string]string{"kind": "bad_port", "port": strconv.Itoa(s.Port)})
		}
		if ports[s.Port] {
			return Plan{}, newProblem(nil, CodeInvalidPlan, map[string]string{"kind": "duplicate_port", "port": strconv.Itoa(s.Port)})
		}
		ports[s.Port] = true
		if s.Host != "" {
			if s.Host, err = NormalizeName(s.Host); err != nil {
				return Plan{}, err
			}
		}
		if a := out.bareAddress(s); a != "" {
			if addresses[a] {
				return Plan{}, newProblem(nil, CodeInvalidPlan, map[string]string{"kind": "duplicate_host", "host": a})
			}
			addresses[a] = true
		}
		out.Servers = append(out.Servers, s)
	}
	return out, nil
}

// bareAddress is the name players type for s without a port, or "" when
// they type Name with the port.
func (p Plan) bareAddress(s JoinServer) string {
	switch {
	case s.Host != "":
		return s.Host
	case s.Port == MinecraftPort:
		return p.Name
	}
	return ""
}

// needsSRV reports whether players reach s through an SRV record.
func (p Plan) needsSRV(s JoinServer) bool {
	return s.Host != "" && !(s.Host == p.Name && s.Port == MinecraftPort)
}

func (p Plan) srvRecord(s JoinServer) Record {
	parts := &SRVParts{Service: "_minecraft", Protocol: "_tcp", Host: s.Host, Priority: 0, Weight: 5, Port: s.Port, Target: p.Name}
	return Record{
		ServerID: s.ID,
		Type:     "SRV",
		Name:     "_minecraft._tcp." + s.Host,
		Value:    fmt.Sprintf("%d %d %d %s.", parts.Priority, parts.Weight, parts.Port, parts.Target),
		TTL:      srvTTL,
		SRV:      parts,
	}
}

// Records lists the DNS records to create: A and AAAA for Name, then an SRV
// record for every server with its own Host (or Host = Name on another port
// than 25565).
func (p Plan) Records() ([]Record, error) {
	p, err := p.normalized()
	if err != nil {
		return nil, err
	}
	var out []Record
	if p.IPv4.IsValid() {
		out = append(out, Record{Type: "A", Name: p.Name, Value: p.IPv4.String(), TTL: srvTTL})
	}
	if p.IPv6.IsValid() {
		out = append(out, Record{Type: "AAAA", Name: p.Name, Value: p.IPv6.String(), TTL: srvTTL})
	}
	for _, s := range p.Servers {
		if p.needsSRV(s) {
			out = append(out, p.srvRecord(s))
		}
	}
	return out, nil
}

// Join lists what players type to join each server.
func (p Plan) Join() ([]JoinAddress, error) {
	p, err := p.normalized()
	if err != nil {
		return nil, err
	}
	out := make([]JoinAddress, 0, len(p.Servers))
	for _, s := range p.Servers {
		direct := p.Name
		if s.Port != MinecraftPort {
			direct += ":" + strconv.Itoa(s.Port)
		}
		address := direct
		if s.Host != "" {
			address = s.Host
		}
		out = append(out, JoinAddress{ServerID: s.ID, Address: address, Direct: direct})
	}
	return out, nil
}

// PlanCheck is the state of a plan's records in DNS.
type PlanCheck struct {
	Name    NameCheck     `json:"name"`
	Records []RecordCheck `json:"records,omitempty"`
	// Ready means Name points here and every SRV record is right.
	Ready bool `json:"ready"`
}

// RecordCheck is the state of one SRV record: one the plan needs, or one
// that must go because it would send players elsewhere.
type RecordCheck struct {
	Note
	Record Record `json:"record"`
	OK     bool   `json:"ok"`
	// Found lists the SRV records found, as "priority weight port target.".
	Found []string `json:"found,omitempty"`
}

// CheckPlan checks the plan's records: Name with CheckName, and every SRV
// record, including that no SRV record redirects players who type Name for
// the server on port 25565.
func CheckPlan(ctx context.Context, r Resolver, p Plan, expected []netip.Addr) (PlanCheck, error) {
	p, err := p.normalized()
	if err != nil {
		return PlanCheck{}, err
	}
	pc := PlanCheck{Name: CheckName(ctx, r, p.Name, expected)}
	pc.Ready = pc.Name.OK
	for _, s := range p.Servers {
		var rc RecordCheck
		switch {
		case p.needsSRV(s):
			rc = p.checkSRV(ctx, r, s, p.srvRecord(s), false)
		case p.bareAddress(s) == p.Name:
			rc = p.checkSRV(ctx, r, s, Record{ServerID: s.ID, Type: "SRV", Name: "_minecraft._tcp." + p.Name, TTL: srvTTL}, true)
			if rc.OK {
				continue
			}
		default:
			continue
		}
		pc.Records = append(pc.Records, rc)
		pc.Ready = pc.Ready && rc.OK
	}
	return pc, nil
}

// checkSRV compares the SRV records at rec.Name with s. With absentOK, no
// record is right too: that is for the server players reach as Name on
// port 25565, where only a record pointing elsewhere is wrong.
func (p Plan) checkSRV(ctx context.Context, r Resolver, s JoinServer, rec Record, absentOK bool) RecordCheck {
	host := strings.TrimPrefix(rec.Name, "_minecraft._tcp.")
	params := map[string]string{"host": host, "port": strconv.Itoa(s.Port), "target": p.Name, "record": rec.Name}
	rc := RecordCheck{Record: rec}
	_, srvs, err := r.LookupSRV(ctx, "minecraft", "tcp", host)
	var code string
	switch {
	case err != nil && isNotFound(err) || err == nil && len(srvs) == 0:
		code = CodeSRVMissing
		if absentOK {
			code = CodeSRVOK
		}
	case err != nil:
		code = CodeSRVLookupFailed
	default:
		matches := 0
		for _, v := range srvs {
			target := strings.TrimSuffix(strings.ToLower(v.Target), ".")
			rc.Found = append(rc.Found, fmt.Sprintf("%d %d %d %s.", v.Priority, v.Weight, v.Port, target))
			if target == p.Name && int(v.Port) == s.Port {
				matches++
			}
		}
		params["found"] = strings.Join(rc.Found, ", ")
		switch {
		case matches == len(srvs):
			code = CodeSRVOK
		case absentOK:
			code = CodeSRVConflict
		default:
			code = CodeSRVWrong
		}
	}
	rc.OK = code == CodeSRVOK
	rc.Note, _ = note(code, params)
	return rc
}
