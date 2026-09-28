package agent

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/CIYAhq/playkeeper/internal/api"
	"github.com/CIYAhq/playkeeper/internal/certs"
)

// A server's own address under the machine's own domain, such as
// alex.example.com next to the machine's mc.example.com: players join the
// server there without a port, and a browser gets the server's own public
// page, with its own certificate. The machine's name keeps the page of every
// server. Nothing changes until an owner gives a server an address.

// ownCertsPerDay bounds the certificates the agent asks for servers' own
// addresses in a day, first ones and renewals alike: each counts against
// Let's Encrypt's 50 a week for the registered domain, which free names
// share when the domain is playkeeper.me.
const ownCertsPerDay = 3

// hOwnAddressSet gives the server its own address, or clears it.
func (s *server) hOwnAddressSet(w http.ResponseWriter, r *http.Request) {
	var req api.OwnAddressRequest
	if err := decode(r, &req); err != nil {
		writeError(w, err)
		return
	}
	actor, err := validActor(req.Actor)
	if err != nil {
		writeError(w, err)
		return
	}
	release, err := s.holdAddress(r.Context(), 20*time.Second)
	if err != nil {
		writeError(w, err)
		return
	}
	defer release()
	name, err := s.validOwnAddress(req.Address)
	if err != nil {
		writeError(w, err)
		return
	}
	var old string
	if err := s.db.QueryRow(`SELECT own_address FROM servers WHERE id = ?`, s.id).Scan(&old); err != nil {
		writeError(w, err)
		return
	}
	if old != name {
		if _, err := s.db.Exec(`UPDATE servers SET own_address = ? WHERE id = ?`, name, s.id); err != nil {
			writeError(w, err)
			return
		}
		s.forgetCertificate(old)
		s.audit(actor, "server.own_address", s.id, "changed", nonEmptyOr(name, "cleared"))
		s.serversChanged()
	}
	writeJSON(w, http.StatusOK, s.addressView())
}

// validOwnAddress is raw written the one way the agent keeps it, or "" for
// none. It must be a name of its own under the machine's own domain: not the
// machine's name, not another server's address, and one the plan can give
// records.
func (s *server) validOwnAddress(raw string) (string, error) {
	if strings.TrimSpace(raw) == "" {
		return "", nil
	}
	st := s.address()
	if st.Kind != api.AddressOwn {
		return "", errConflict("A server gets an address of its own under the machine's own domain.", "Give the machine your own domain first, in Machine settings › Address.")
	}
	name, err := certs.NormalizeName(raw)
	if err != nil {
		return "", problemError(err, http.StatusBadRequest)
	}
	if name == st.Host {
		return "", errInvalid("%s is the machine's own name. Give the server another.", name)
	}
	servers := s.joinServers()
	for i, js := range servers {
		switch {
		case js.id == s.id:
			servers[i].own = name
		case js.own == name:
			return "", errConflict("Another server has that address.", "")
		}
	}
	if _, err := ownPlan(st.Host, servers, s.machineIP(st)).Records(); err != nil {
		return "", problemError(err, http.StatusBadRequest)
	}
	return name, nil
}

// ownAddresses are the servers' own addresses, under an own domain only.
func (a *Agent) ownAddresses(st addressState) []joinServer {
	if st.Kind != api.AddressOwn {
		return nil
	}
	var out []joinServer
	for _, js := range a.joinServers() {
		if js.own != "" {
			out = append(out, js)
		}
	}
	return out
}

// forgetOwnCertificates forgets the certificates of the servers' own
// addresses, when the machine stops using its own domain.
func (a *Agent) forgetOwnCertificates() {
	for _, js := range a.joinServers() {
		a.forgetCertificate(js.own)
	}
}

// ownCertificateDue is the first own address that needs a certificate now:
// its records check out and it has none, or it's time to renew, or the
// wait after a failed attempt is over. held says the day's certificates
// are used up while one is due.
func (a *Agent) ownCertificateDue(st addressState) (host string, held bool) {
	if st.Check == nil {
		return "", false
	}
	for _, js := range a.ownAddresses(st) {
		if !ownNameOK(st.Check, js.id) {
			continue
		}
		if row := a.loadCertificate(js.own); row != nil && !row.status.Due(a.now()) {
			continue
		}
		if a.ownCertsToday(st) >= ownCertsPerDay {
			return "", true
		}
		return js.own, false
	}
	return "", false
}

// ownNameOK reports whether the check found the server's own address
// pointing here.
func ownNameOK(c *api.AddressCheck, serverID string) bool {
	return slices.ContainsFunc(c.Records, func(rc api.RecordCheck) bool {
		return rc.Record.ServerID == serverID && (rc.Record.Type == "A" || rc.Record.Type == "AAAA") && rc.OK
	})
}

// ownCertsToday counts the certificates asked for the servers' own
// addresses in the last day, whether Let's Encrypt gave them or not.
func (a *Agent) ownCertsToday(st addressState) int {
	n := 0
	since := a.now().Add(-24 * time.Hour)
	for _, js := range a.joinServers() {
		if js.own == "" || js.own == st.Host {
			continue
		}
		if row := a.loadCertificate(js.own); row != nil && row.status.LastAttempt.After(since) {
			n++
		}
	}
	return n
}

// noteOwnCertsHeld logs, at most once an hour, that the day's certificates
// for the servers' own addresses are used up while one waits.
func (a *Agent) noteOwnCertsHeld() {
	a.addr.mu.Lock()
	due := a.now().Sub(a.addr.ownHeldNoted) >= time.Hour
	if due {
		a.addr.ownHeldNoted = a.now()
	}
	a.addr.mu.Unlock()
	if due {
		a.log.Info("a server's own address waits for its certificate: the day's certificates for servers' own addresses are used up", "perDay", ownCertsPerDay)
	}
}

// issueOwnCertificate gets or renews the certificate for a server's own
// address over HTTP-01, once the public DNS shows that it points here.
func (a *Agent) issueOwnCertificate(host string) func(ctx context.Context, h *opHandle) error {
	return func(ctx context.Context, h *opHandle) error {
		st := a.address()
		if !slices.ContainsFunc(a.ownAddresses(st), func(js joinServer) bool { return js.own == host }) {
			return errConflict("No server has that address any more.", "")
		}
		h.set("name", host)
		h.phase("checking")
		nc := certs.CheckName(ctx, a.opts.Resolver, host, a.expectedAddrs(st))
		if !nc.OK {
			return &apiError{Status: http.StatusConflict, Code: nc.Code, Msg: nc.Message, Hint: nc.Hint, Params: noteParams(nc.Params)}
		}
		h.phase("certificate")
		row := a.loadCertificate(host)
		if row == nil {
			row = &certRow{name: host, status: certs.Status{Names: []string{host}}}
		}
		row.source, row.challenge = api.AddressOwn, "http-01"
		cert, err := a.opts.Issue(ctx, a.issuer(), certs.Request{Names: []string{host}, Dir: a.cfg.CertsDir(), Owner: a.certOwner(), HTTP01: a.http01})
		row.status.Record(a.now().UTC(), cert, err)
		if serr := a.saveCertificate(row); serr != nil {
			return serr
		}
		if err != nil {
			p := row.status.Problem
			return &apiError{Status: http.StatusBadGateway, Code: p.Code, Msg: p.Message, Hint: p.Hint}
		}
		h.set("notAfter", cert.NotAfter)
		return nil
	}
}

// ownPageServer is the server whose own address is host and which is on
// the public page, or nil.
func (a *Agent) ownPageServer(host string) *server {
	st := a.address()
	for _, js := range a.ownAddresses(st) {
		if !sameHost(host, js.own) {
			continue
		}
		if s := a.serverByID(js.id); s != nil && s.publicPageSettings().Enabled {
			return s
		}
		return nil
	}
	return nil
}

// ownPageHosts are the own addresses the public page answers for: those of
// the servers on it.
func (a *Agent) ownPageHosts() []string {
	var out []string
	for _, js := range a.ownAddresses(a.address()) {
		if s := a.serverByID(js.id); s != nil && s.publicPageSettings().Enabled {
			out = append(out, js.own)
		}
	}
	return out
}
