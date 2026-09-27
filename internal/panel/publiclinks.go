package panel

import (
	"encoding/json"
	"errors"
	"net/http"
)

// Friends' pack pages and shared maps are public links the dashboard serves
// for servers on every machine. A link's token is made by the agent of the
// machine that runs its server, so the dashboard keeps a hash of it with that
// server and machine when the link is made, and asks only that machine about
// it: no machine can answer for another machine's link.

type linkKind string

const (
	packLink linkKind = "pack"
	mapLink  linkKind = "map"
)

// errNoLinkRecord is a token the dashboard didn't see made: a link from
// before it kept records, or one whose record couldn't be saved.
var errNoLinkRecord = errors.New("the dashboard has no record of this link")

// sharing forwards a request that switches a server's public link on or
// off, then records the link the machine's answer names (see recordLink)
// with the server in the path.
func (s *Server) sharing(pattern string, record func(m machine, serverID string, raw json.RawMessage)) func(http.ResponseWriter, *http.Request, *session) {
	return func(w http.ResponseWriter, r *http.Request, sess *session) {
		id := r.PathValue("id")
		s.forwardThen("POST", pattern, func(m machine, _ *session, raw json.RawMessage) { record(m, id, raw) })(w, r, sess)
	}
}

// recordLink keeps the server and machine a link's token was made for. A
// token kept for another server stays with it, so no machine takes over
// another's link by naming its token; for the same server it moves to the
// machine that runs it now, as after that machine joins again. Records
// outlive their links, so an old link never opens another server.
func (s *Server) recordLink(kind linkKind, token, serverID string, m machine) {
	res, err := s.db.Exec(`INSERT INTO public_links(kind, token_hash, server_id, machine_id, created_at) VALUES(?,?,?,?,?)
		ON CONFLICT(kind, token_hash) DO UPDATE SET machine_id = excluded.machine_id WHERE public_links.server_id = excluded.server_id`,
		string(kind), tokenHash(token), serverID, m.ID, millis(s.now()))
	if err != nil {
		s.log.Error("record a public link", "kind", kind, "server", serverID, "err", err)
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		s.log.Warn("a machine named another server's link as its own; the link stays with that server", "kind", kind, "machine", m.ID, "server", serverID)
	}
}

// linkMachine is the machine to ask about a link, and the link's server:
// the machine the link was made on, while it still runs that server. It
// returns errNoLinkRecord for a token the dashboard has no record of, and
// errNotFound when the link's machine was removed or no longer runs its
// server.
func (s *Server) linkMachine(kind linkKind, token string) (machine, string, error) {
	var serverID, machineID string
	err := s.db.QueryRow(`SELECT server_id, machine_id FROM public_links WHERE kind = ? AND token_hash = ?`, string(kind), tokenHash(token)).Scan(&serverID, &machineID)
	switch {
	case isNoRows(err):
		return machine{}, "", errNoLinkRecord
	case err != nil:
		return machine{}, "", err
	}
	m, err := s.machineForServer(serverID)
	if err != nil {
		return machine{}, "", err
	}
	if m.ID != machineID {
		return machine{}, "", errNotFound
	}
	return m, serverID, nil
}
