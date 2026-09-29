package panel

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/CIYAhq/playkeeper/internal/api"
	"github.com/CIYAhq/playkeeper/internal/dnszone"
)

// Port-free addresses (managed-beta step 8): the dashboard's machine answers
// DNS for its own domain, such as beta.playkeeper.me, once the domain's
// parent delegates it to the machine with two records the owner adds there.
// The dashboard builds the zone, and the machine's agent answers it on port
// 53 (internal/agent/dns.go). The zone holds the records the owner was asked
// for under the domain, so delegating it changes nothing else, and an SRV
// record for each server with an address under the domain, so that players
// type no port. Joined machines' servers join it once the fleet gives the
// dashboard their machines' addresses (machineAddress, cloud-fleet.md).

// metaDNSAnswers is the panel_meta key set while the owner has the answers
// on.
const metaDNSAnswers = "dns_answers"

// dnsAnswersEvery is how often the zone is built again. A server made or
// renamed meanwhile is reached through the wildcard, with its port.
const dnsAnswersEvery = time.Minute

func (s *Server) dnsAnswersOn(ctx context.Context) (bool, error) {
	var v string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM panel_meta WHERE key = ?`, metaDNSAnswers).Scan(&v)
	if isNoRows(err) {
		return false, nil
	}
	if err != nil {
		return false, errDB
	}
	return v == "on", nil
}

func (s *Server) setDNSAnswers(ctx context.Context, on bool) error {
	var err error
	if on {
		_, err = s.db.ExecContext(ctx, `INSERT INTO panel_meta(key, value) VALUES(?, 'on') ON CONFLICT(key) DO UPDATE SET value = excluded.value`, metaDNSAnswers)
	} else {
		_, err = s.db.ExecContext(ctx, `DELETE FROM panel_meta WHERE key = ?`, metaDNSAnswers)
	}
	if err != nil {
		return errDB
	}
	return nil
}

// dnsPlan is the zone the dashboard's machine answers for its address, the
// records the domain's parent adds, and the ones there the zone takes over;
// or why there's none (api.DNSUnavailable*).
type dnsPlan struct {
	zone        dnszone.Zone
	add, remove []api.DNSRecord
	unavailable string
}

// planDNS builds the zone for addr, the dashboard machine's address.
func planDNS(addr api.Address) dnsPlan {
	host := addr.Host
	ns := nameserverFor(host)
	switch {
	case addr.Kind != api.AddressOwn || host == "":
		return dnsPlan{unavailable: api.DNSUnavailableOwnDomain}
	case ns == "":
		return dnsPlan{zone: dnszone.Zone{Name: host}, unavailable: api.DNSUnavailableSubdomain}
	}
	p := dnsPlan{zone: dnszone.Zone{Name: host, Nameserver: ns}, add: []api.DNSRecord{}, remove: []api.DNSRecord{}}
	// machine are the domain's own A and AAAA records, which point at the
	// machine.
	var machine []dnszone.Record
	for _, r := range addr.Records {
		rel, ok := relName(r.Name, host)
		if !ok {
			continue
		}
		switch {
		case r.Type == dnszone.TypeA || r.Type == dnszone.TypeAAAA:
			rec := dnszone.Record{Name: rel, Type: r.Type, Value: r.Value}
			if rel == "" {
				machine = append(machine, rec)
			}
			p.addRecord(rec)
		case r.Type == dnszone.TypeSRV && r.SRV != nil:
			p.addRecord(dnszone.Record{Name: rel, Type: dnszone.TypeSRV, Value: r.SRV.Target, Port: r.SRV.Port})
		default:
			continue
		}
		p.remove = append(p.remove, r)
	}
	if len(machine) == 0 {
		return dnsPlan{zone: dnszone.Zone{Name: host, Nameserver: ns}, unavailable: api.DNSUnavailableAddress}
	}
	for _, j := range addr.Servers {
		if !j.Automatic || j.Label == "" || j.Port < 1 {
			continue
		}
		// The server's name has its own records, as its SRV record keeps the
		// wildcard from answering for it.
		for _, m := range machine {
			p.addRecord(dnszone.Record{Name: j.Label, Type: m.Type, Value: m.Value})
		}
		p.addRecord(dnszone.Record{Name: "_minecraft._tcp." + j.Label, Type: dnszone.TypeSRV, Value: host, Port: j.Port})
	}
	for _, m := range machine {
		p.add = append(p.add, api.DNSRecord{Type: m.Type, Name: ns, Value: m.Value, TTL: 300})
	}
	p.add = append(p.add, api.DNSRecord{Type: "NS", Name: host, Value: ns, TTL: 3600})
	return p
}

// addRecord adds r to the zone if the zone can hold it. One it can't, such
// as a server's label no DNS name has, is left out rather than the machine
// refusing the whole zone.
func (p *dnsPlan) addRecord(r dnszone.Record) {
	one := p.zone
	one.Records = []dnszone.Record{r}
	if one.Check() == nil {
		p.zone.Records = append(p.zone.Records, r)
	}
}

// relName is name relative to the zone host, if it's in the zone.
func relName(name, host string) (string, bool) {
	if name == host {
		return "", true
	}
	return strings.CutSuffix(name, "."+host)
}

// nameserverFor is the name host's parent delegates it to: ns-<its first
// label> beside it, such as ns-beta.playkeeper.me for beta.playkeeper.me,
// which a record at the parent points at the machine. It's "" for a host
// that isn't a name under another domain.
func nameserverFor(host string) string {
	first, parent, ok := strings.Cut(host, ".")
	if !ok || !strings.Contains(parent, ".") || len(first)+len("ns-") > 63 {
		return ""
	}
	return "ns-" + first + "." + parent
}

// syncDNSAnswers sends the dashboard's machine its zone while the owner has
// the answers on, as actor, and takes it away once they're off or the
// machine's address is no longer an own domain. While the address can't be
// read, or has no IP address, the machine keeps answering what it had: its
// domain's parent may send everyone to it already.
func (s *Server) syncDNSAnswers(ctx context.Context, actor string) error {
	s.dnsMu.Lock()
	defer s.dnsMu.Unlock()
	on, err := s.dnsAnswersOn(ctx)
	if err != nil {
		return err
	}
	var z dnszone.Zone
	if on {
		var addr api.Address
		if _, err := s.agent.Do(ctx, "GET", "/v1/address", nil, nil, &addr); err != nil {
			return err
		}
		p := planDNS(addr)
		switch p.unavailable {
		case "":
			z = p.zone
		case api.DNSUnavailableAddress:
			return nil
		}
	} else {
		var st api.DNSZoneStatus
		if _, err := s.agent.Do(ctx, "GET", "/v1/dns-zone", nil, nil, &st); err != nil || st.Zone.Name == "" {
			return err
		}
	}
	_, err = s.agent.Do(asActor(ctx, actor), "PUT", "/v1/dns-zone", nil, api.DNSZoneRequest{Zone: z, Actor: actor}, nil)
	return err
}

// runDNSAnswers keeps the dashboard machine's zone up to date as its
// servers change, until ctx ends.
func (s *Server) runDNSAnswers(ctx context.Context) {
	t := time.NewTicker(dnsAnswersEvery)
	defer t.Stop()
	last := ""
	for {
		err := s.syncDNSAnswers(ctx, "playkeeper")
		if msg := errorText(err); msg != last && ctx.Err() == nil {
			if err != nil {
				s.log.Warn("could not send the machine its DNS answers", "err", err)
			}
			last = msg
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func errorText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// dnsAnswers is how the answers stand, with the records the owner adds at
// the domain's parent.
func (s *Server) dnsAnswers(ctx context.Context) (api.DNSAnswers, error) {
	v := api.DNSAnswers{Add: []api.DNSRecord{}, Remove: []api.DNSRecord{}, Listening: []string{}}
	on, err := s.dnsAnswersOn(ctx)
	if err != nil {
		return v, err
	}
	var addr api.Address
	if _, err := s.agent.Do(ctx, "GET", "/v1/address", nil, nil, &addr); err != nil {
		return v, err
	}
	var st api.DNSZoneStatus
	if _, err := s.agent.Do(ctx, "GET", "/v1/dns-zone", nil, nil, &st); err != nil {
		return v, err
	}
	p := planDNS(addr)
	v.On, v.Zone, v.Nameserver, v.Unavailable = on, p.zone.Name, p.zone.Nameserver, p.unavailable
	if p.add != nil {
		v.Add, v.Remove = p.add, p.remove
	}
	v.Answering, v.Problem = st.Zone.Name, st.Problem
	if st.Listening != nil {
		v.Listening = st.Listening
	}
	for _, r := range st.Zone.Records {
		if r.Type == dnszone.TypeSRV {
			v.Servers++
		}
	}
	return v, nil
}

func (s *Server) hDNSAnswers(w http.ResponseWriter, r *http.Request, _ *session) {
	v, err := s.dnsAnswers(r.Context())
	if err != nil {
		s.dnsFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

// hDNSAnswersSet turns the answers on or off, and sends the machine its
// zone, or takes it away, at once.
func (s *Server) hDNSAnswersSet(w http.ResponseWriter, r *http.Request, sess *session) {
	var req struct {
		On *bool `json:"on"`
	}
	dec := json.NewDecoder(io.LimitReader(r.Body, 4<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil || req.On == nil || dec.More() {
		writeErr(w, http.StatusBadRequest, api.CodeInvalid, "Send {\"on\": true} or {\"on\": false}.", "")
		return
	}
	if err := s.setDNSAnswers(r.Context(), *req.On); err != nil {
		s.dnsFailure(w, err)
		return
	}
	if err := s.syncDNSAnswers(r.Context(), sess.User.Username); err != nil {
		s.dnsFailure(w, err)
		return
	}
	s.hDNSAnswers(w, r, sess)
}

func (s *Server) dnsFailure(w http.ResponseWriter, err error) {
	if errors.Is(err, errDB) {
		writeErr(w, http.StatusInternalServerError, api.CodeInternal, "Database error.", "")
		return
	}
	s.agentFailure(w, err)
}
