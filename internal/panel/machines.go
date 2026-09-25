package panel

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/CIYAhq/playkeeper/internal/agentclient"
	"github.com/CIYAhq/playkeeper/internal/api"
	"github.com/CIYAhq/playkeeper/internal/machinelink"
	"github.com/CIYAhq/playkeeper/internal/sizing"
	"github.com/CIYAhq/playkeeper/internal/version"
)

// errLinksOff is what requests to joined machines fail with in a process
// that runs without machine links, such as the root recovery commands.
var errLinksOff = errors.New("machine links are not running in this process")

type linksOff struct{}

func (linksOff) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.Body != nil {
		r.Body.Close()
	}
	return nil, errLinksOff
}

// startHub loads the dashboard's link key (making it on first start) and
// starts accepting machines. Every joined machine pins this key.
func (s *Server) startHub(routes []machinelink.Route) error {
	id, _, err := machinelink.LoadOrCreateIdentity(filepath.Join(s.cfg.PanelDir(), "link.key"))
	if err != nil {
		return err
	}
	s.hub, err = machinelink.NewHub(machinelink.HubOptions{Identity: id, Store: &linkStore{db: s.db}, Routes: routes,
		Version: version.Version, Now: s.now, Logger: s.log, OnEvent: s.onMachineEvent})
	return err
}

func (s *Server) machineTransport(id string) http.RoundTripper {
	if s.hub == nil {
		return linksOff{}
	}
	return s.hub.Transport(id)
}

// relayStatus is the status the panel answers with for a machine's error.
// A joined machine can't say the dashboard session ended, and only 4xx and
// 5xx pass.
func relayStatus(status int) int {
	if status < 400 || status > 599 || status == http.StatusUnauthorized || status == http.StatusProxyAuthRequired {
		return http.StatusBadGateway
	}
	return status
}

// failureOf is the status and API error for a failed request to a machine:
// the agent's own refusal, a machine link that could not carry it, or an
// agent that is not running.
func failureOf(err error) (int, api.Error) {
	var ae *agentclient.Error
	if errors.As(err, &ae) {
		return relayStatus(ae.Status), ae.Body
	}
	if errors.Is(err, agentclient.ErrBadAnswer) {
		return http.StatusBadGateway, api.Error{Error: "The machine's answer was not valid.", Code: machinelink.CodeProtocol,
			Hint: "Try again. If it keeps happening, update Playkeeper on both machines."}
	}
	var le *machinelink.Error
	if errors.As(err, &le) {
		status := http.StatusBadGateway
		switch le.Code {
		case machinelink.CodeNotConnected:
			status = http.StatusServiceUnavailable
		case machinelink.CodeMachineRemoved:
			status = http.StatusNotFound
		case machinelink.CodeTimeout:
			status = http.StatusGatewayTimeout
		case machinelink.CodeRouteNotAllowed, machinelink.CodeActorRequired:
			status = http.StatusInternalServerError
		}
		return status, api.Error{Error: le.Msg, Code: le.Code, Hint: le.Hint, Params: le.Params}
	}
	if errors.Is(err, errLinksOff) {
		return http.StatusServiceUnavailable, api.Error{Error: "This machine can't be reached from here.", Code: machinelink.CodeNotConnected}
	}
	return http.StatusServiceUnavailable, api.Error{Error: "The Playkeeper agent is not running, so the server cannot be seen or controlled right now.",
		Code: api.CodeAgentUnavailable, Hint: "On the server, check: sudo systemctl status playkeeper-agent"}
}

// onMachineEvent records what happened to a joined machine: every event in
// its recent events (a bounded number per machine), and joins, refusals and
// removals in the audit log. Refusals go through the refusal sink, so a
// flood of attempts can't fill the log.
func (s *Server) onMachineEvent(e machinelink.Event) {
	if e.MachineID != "" {
		s.machineEvent(e.MachineID, e.At, string(e.Kind), e.Actor, e.Address, e.Code)
	}
	switch e.Kind {
	case machinelink.EventJoined:
		s.audit(orUnknown(e.Actor), string(e.Kind), e.Name, "succeeded", "from "+e.Address)
	case machinelink.EventJoinRefused:
		s.joinRefused(e)
	case machinelink.EventRemoved, machinelink.EventLeft:
		s.audit(orUnknown(e.Actor), string(e.Kind), e.Name, "succeeded", "")
		if _, err := s.db.Exec(`DELETE FROM server_machines WHERE machine_id = ?`, e.MachineID); err != nil {
			s.log.Error("forget a removed machine's servers", "err", err)
		}
		if _, err := s.db.Exec(`UPDATE server_machines SET disputed_by = '' WHERE disputed_by = ?`, e.MachineID); err != nil {
			s.log.Error("forget a removed machine's servers", "err", err)
		}
	}
}

func orUnknown(actor string) string {
	if actor == "" {
		return "(unknown)"
	}
	return actor
}

// maxMachineEvents is how many recent events each machine keeps.
const maxMachineEvents = 200

func (s *Server) machineEvent(machineID string, at time.Time, kind, actor, addr, code string) {
	if _, err := s.db.Exec(`INSERT INTO machine_events(machine_id, ts, kind, actor, address, code) VALUES(?,?,?,?,?,?)`,
		machineID, millis(at), kind, actor, addr, code); err != nil {
		s.log.Error("record a machine event", "err", err)
		return
	}
	_, _ = s.db.Exec(`DELETE FROM machine_events WHERE machine_id = ? AND id <= (SELECT id FROM machine_events WHERE machine_id = ? ORDER BY id DESC LIMIT 1 OFFSET ?)`,
		machineID, machineID, maxMachineEvents)
}

type machineEventView struct {
	At      time.Time `json:"at"`
	Kind    string    `json:"kind"`
	Actor   string    `json:"actor,omitempty"`
	Address string    `json:"address,omitempty"`
	Code    string    `json:"code,omitempty"`
}

func (s *Server) hMachineEvents(w http.ResponseWriter, r *http.Request, _ *session) {
	m, ok := s.machineFromPath(w, r)
	if !ok {
		return
	}
	rows, err := s.db.Query(`SELECT ts, kind, actor, address, code FROM machine_events WHERE machine_id = ? ORDER BY id DESC LIMIT 50`, m.ID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, api.CodeInternal, "Database error.", "")
		return
	}
	defer rows.Close()
	out := []machineEventView{}
	for rows.Next() {
		var e machineEventView
		var ts int64
		if rows.Scan(&ts, &e.Kind, &e.Actor, &e.Address, &e.Code) == nil {
			e.At = fromMillis(ts)
			out = append(out, e)
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// hMachineRemove removes a joined machine: its key stops working at once,
// and its servers keep running there, out of the dashboard's reach.
func (s *Server) hMachineRemove(w http.ResponseWriter, r *http.Request, sess *session) {
	m, ok := s.machineFromPath(w, r)
	if !ok {
		return
	}
	if m.Kind != remoteKind || s.hub == nil {
		writeErr(w, http.StatusBadRequest, api.CodeInvalid, "This is the dashboard's own machine, so it can't be removed.", "")
		return
	}
	if err := s.hub.Remove(r.Context(), m.ID, sess.User.Username); err != nil {
		writeErr(w, http.StatusInternalServerError, api.CodeInternal, "Could not remove the machine.", "")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- connecting a machine ---

// dialAddress is an address another machine can dial to reach this
// dashboard: its friendly name or its IP, on the panel's port.
type dialAddress struct {
	Kind    string `json:"kind"`
	Address string `json:"address"`
	// Proxied is set when the name points at a proxy (Cloudflare's orange
	// cloud), which would stop the machine from checking the fingerprint.
	Proxied bool `json:"proxied,omitempty"`
}

const (
	dialName = "name"
	dialIP   = "ip"
)

// dialAddresses are the addresses joined machines and AI agents may use: the
// configured domain or the name the admin opened the dashboard with, and the
// IP they used or this host's first public one.
func (s *Server) dialAddresses(ctx context.Context, r *http.Request) []dialAddress {
	host := r.Host
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	var name, ip string
	if s.cfg.Domain != "" {
		name = s.cfg.Domain
	}
	if a, err := netip.ParseAddr(strings.Trim(host, "[]")); err == nil {
		if !a.IsLoopback() || s.cfg.Dev {
			ip = a.Unmap().String()
		}
	} else if name == "" && host != "" && (host != "localhost" || s.cfg.Dev) {
		name = host
	}
	if ip == "" {
		if s.cfg.Dev && name == "localhost" {
			ip = "127.0.0.1"
		} else if ips := HostIPs(); len(ips) > 0 {
			ip = ips[0].String()
			for _, x := range ips {
				if x.To4() != nil {
					ip = x.String()
					break
				}
			}
		}
	}
	var out []dialAddress
	port := strconv.Itoa(s.cfg.PanelPort)
	for _, c := range []struct{ kind, host string }{{dialName, name}, {dialIP, ip}} {
		if c.host == "" {
			continue
		}
		a, err := machinelink.ParseAddress(net.JoinHostPort(c.host, port))
		if err != nil {
			continue
		}
		d := dialAddress{Kind: c.kind, Address: a.HostPort()}
		if c.kind == dialName {
			d.Proxied = s.proxied(ctx, a.Host)
		}
		out = append(out, d)
	}
	return out
}

// proxyCheck remembers for a while whether a name points at a proxy.
type proxyCheck struct {
	at      time.Time
	proxied bool
}

type proxyCache struct {
	mu     sync.Mutex
	checks map[string]proxyCheck
}

func (s *Server) proxied(ctx context.Context, host string) bool {
	now := s.now()
	s.proxies.mu.Lock()
	if c, ok := s.proxies.checks[host]; ok && now.Sub(c.at) < 5*time.Minute {
		s.proxies.mu.Unlock()
		return c.proxied
	}
	s.proxies.mu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	addrs, err := s.opts.LookupIP(ctx, host)
	if err != nil {
		return false
	}
	p := behindCloudflare(addrs)
	s.proxies.mu.Lock()
	if s.proxies.checks == nil || len(s.proxies.checks) > 64 {
		s.proxies.checks = map[string]proxyCheck{}
	}
	s.proxies.checks[host] = proxyCheck{at: now, proxied: p}
	s.proxies.mu.Unlock()
	return p
}

// cloudflareRanges are Cloudflare's published proxy addresses
// (cloudflare.com/ips). A name that resolves into them is proxied.
var cloudflareRanges = func() []netip.Prefix {
	var out []netip.Prefix
	for _, p := range []string{
		"173.245.48.0/20", "103.21.244.0/22", "103.22.200.0/22", "103.31.4.0/22", "141.101.64.0/18",
		"108.162.192.0/18", "190.93.240.0/20", "188.114.96.0/20", "197.234.240.0/22", "198.41.128.0/17",
		"162.158.0.0/15", "104.16.0.0/13", "104.24.0.0/14", "172.64.0.0/13", "131.0.72.0/22",
		"2400:cb00::/32", "2606:4700::/32", "2803:f800::/32", "2405:b500::/32", "2405:8100::/32",
		"2a06:98c0::/29", "2c0f:f248::/32",
	} {
		out = append(out, netip.MustParsePrefix(p))
	}
	return out
}()

func behindCloudflare(addrs []netip.Addr) bool {
	for _, a := range addrs {
		for _, p := range cloudflareRanges {
			if p.Contains(a.Unmap()) {
				return true
			}
		}
	}
	return false
}

type joinCodeView struct {
	ID        string    `json:"id"`
	Name      string    `json:"name,omitempty"`
	Dials     string    `json:"dials,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
	ExpiresAt time.Time `json:"expiresAt"`
	CreatedBy string    `json:"createdBy,omitempty"`
	State     string    `json:"state"`
	MachineID string    `json:"machineId,omitempty"`
}

// joinCodes lists the codes that are waiting, and those used or run out in
// the last hour. It never has the codes themselves.
func (s *Server) joinCodes() ([]joinCodeView, error) {
	now := s.now()
	hourAgo := millis(now.Add(-time.Hour))
	rows, err := s.db.Query(`SELECT id, name, dials, created_at, expires_at, created_by, used_at, machine_id FROM machine_join_codes
		WHERE (used_at = 0 AND expires_at > ?) OR used_at > ? ORDER BY created_at, id`, hourAgo, hourAgo)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []joinCodeView{}
	for rows.Next() {
		var v joinCodeView
		var created, expires, used int64
		if err := rows.Scan(&v.ID, &v.Name, &v.Dials, &created, &expires, &v.CreatedBy, &used, &v.MachineID); err != nil {
			return nil, err
		}
		v.CreatedAt, v.ExpiresAt = fromMillis(created), fromMillis(expires)
		v.State = string(machinelink.JoinCode{ExpiresAt: v.ExpiresAt, UsedAt: fromMillis(used), MachineID: v.MachineID}.State(now))
		out = append(out, v)
	}
	return out, rows.Err()
}

type minimumView struct {
	Cores      int `json:"cores"`
	MemoryGB   int `json:"memoryGB"`
	FreeDiskGB int `json:"freeDiskGB"`
}

// hMachineLink is what the connect-a-machine screen needs: the dashboard's
// fingerprint, the addresses a machine can dial, whether joining is paused
// after too many wrong codes, the codes (for those who may make them) and
// the minimum machine.
func (s *Server) hMachineLink(w http.ResponseWriter, r *http.Request, sess *session) {
	out := map[string]any{
		"addresses": s.dialAddresses(r.Context(), r),
		"minimum":   minimumView{Cores: sizing.MinCores, MemoryGB: sizing.MinMemoryGB, FreeDiskGB: sizing.MinFreeDiskGB},
		"sizingUrl": "https://playkeeper.io/sizing",
		"available": s.hub != nil,
	}
	if s.hub != nil {
		out["fingerprint"] = s.hub.Fingerprint()
		out["joinPausedSeconds"] = int((s.hub.JoinPause() + time.Second - 1) / time.Second)
	}
	if permit(sess, actManageMachine) {
		codes, err := s.joinCodes()
		if err != nil {
			writeErr(w, http.StatusInternalServerError, api.CodeInternal, "Database error.", "")
			return
		}
		out["codes"] = codes
	}
	writeJSON(w, http.StatusOK, out)
}

type joinCommandView struct {
	joinCodeView
	// Code is shown this once; the dashboard keeps only a keyed hash.
	Code         string   `json:"code"`
	Install      string   `json:"install"`
	Join         string   `json:"join"`
	InstallLines []string `json:"installLines"`
	JoinLines    []string `json:"joinLines"`
}

// hJoinCodeCreate makes a join code and the command that uses it. Neither
// the code nor the command is logged or stored.
func (s *Server) hJoinCodeCreate(w http.ResponseWriter, r *http.Request, sess *session) {
	var req struct {
		Name string `json:"name"`
		Dial string `json:"dial"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, api.CodeInvalid, "Invalid request.", "")
		return
	}
	if s.hub == nil {
		writeErr(w, http.StatusServiceUnavailable, api.CodeInternal, "Connecting machines is not available right now.", "")
		return
	}
	name := machinelink.CleanName(req.Name)
	if strings.TrimSpace(req.Name) != "" && name == "" {
		writeErr(w, http.StatusBadRequest, api.CodeInvalid, "Use letters, numbers, spaces, dashes, dots or underscores in the name.", "")
		return
	}
	if wait := s.hub.JoinPause(); wait > 0 {
		secs := int((wait + time.Second - 1) / time.Second)
		w.Header().Set("Retry-After", strconv.Itoa(secs))
		writeJSON(w, http.StatusTooManyRequests, api.Error{Error: "Too many wrong codes. Try again in " + minutesText(wait) + ".",
			Code: machinelink.CodeJoinRateLimited, Params: map[string]string{"seconds": strconv.Itoa(secs)}})
		return
	}
	addrs := s.dialAddresses(r.Context(), r)
	var dial dialAddress
	for _, a := range addrs {
		if a.Kind == req.Dial || (req.Dial == "" && dial.Address == "") {
			dial = a
		}
	}
	if dial.Address == "" {
		writeErr(w, http.StatusBadRequest, api.CodeInvalid, "Choose the address the machine dials.", "")
		return
	}
	code, jc, err := s.hub.NewJoinCode(r.Context(), sess.User.Username)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, api.CodeInternal, "Could not make a join code.", "")
		return
	}
	cmd, err := machinelink.NewCommand(dial.Address, code, s.hub.Fingerprint())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, api.CodeInternal, "Could not make the command.", "")
		return
	}
	cmd.Name = name
	if _, err := s.db.Exec(`UPDATE machine_join_codes SET name = ?, dials = ? WHERE id = ?`, name, dial.Address, jc.ID); err != nil {
		writeErr(w, http.StatusInternalServerError, api.CodeInternal, "Database error.", "")
		return
	}
	s.audit(sess.User.Username, "machine.join_code", jc.ID, "created", "dials "+dial.Address)
	writeJSON(w, http.StatusCreated, joinCommandView{
		joinCodeView: joinCodeView{ID: jc.ID, Name: name, Dials: dial.Address, CreatedAt: jc.CreatedAt, ExpiresAt: jc.ExpiresAt,
			CreatedBy: jc.CreatedBy, State: string(machinelink.JoinCodeWaiting)},
		Code: cmd.Code, Install: cmd.Install(), Join: cmd.Join(), InstallLines: cmd.InstallLines(), JoinLines: cmd.JoinLines(),
	})
}

// minutesText is a wait in whole minutes, rounded up, as the link's own
// refusals say it.
func minutesText(d time.Duration) string {
	m := int((d + time.Minute - 1) / time.Minute)
	if m <= 1 {
		return "1 minute"
	}
	return strconv.Itoa(m) + " minutes"
}

func (s *Server) hJoinCodeCancel(w http.ResponseWriter, r *http.Request, sess *session) {
	id := r.PathValue("cid")
	if !reMachineID.MatchString(id) || s.hub == nil {
		writeErr(w, http.StatusNotFound, api.CodeNotFound, "Join code not found.", "")
		return
	}
	switch err := s.hub.CancelJoinCode(r.Context(), id); {
	case errors.Is(err, machinelink.ErrNotFound):
		writeErr(w, http.StatusNotFound, api.CodeNotFound, "Join code not found.", "")
		return
	case err != nil:
		writeErr(w, http.StatusInternalServerError, api.CodeInternal, "Database error.", "")
		return
	}
	s.audit(sess.User.Username, "machine.join_code", id, "cancelled", "")
	w.WriteHeader(http.StatusNoContent)
}

// --- which machine runs each server ---
//
// server_machines says which machine runs each server, and a server's
// requests go only where it says: the panel never asks the machines. The
// dashboard's own machine is trusted, so its list decides its rows and
// takes ids over from joined machines. A joined machine gets the servers it
// creates through the dashboard, and others from its lists, first come
// first served. When a second joined machine lists a server that another
// one has, the server is disputed and its requests go to neither until one
// of them stops listing it or is removed: listing another machine's ids
// can't redirect that machine's requests.

// lastKnownAfter is how often a joined machine's server statuses are saved
// for when it is away.
const lastKnownAfter = 30 * time.Second

// maxMachineServers is how many of a joined machine's servers the
// dashboard keeps and shows.
const maxMachineServers = 100

// errDisputed is a server two joined machines both list.
var errDisputed = errors.New("two machines list this server")

const codeServerDisputed = "server_disputed"

// claimLocal records the servers the dashboard's own machine lists, taking
// them over from joined machines, keeps their statuses for when its agent
// stops answering, and forgets those it no longer lists. It writes only what
// changed.
func (s *Server) claimLocal(m machine, servers []map[string]any) {
	rows, err := s.db.Query(`SELECT server_id FROM server_machines WHERE machine_id = ?`, m.ID)
	if err != nil {
		s.log.Error("record server machines", "err", err)
		return
	}
	had := map[string]bool{}
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			had[id] = true
		}
	}
	rows.Close()
	now := s.now()
	listed := map[string]bool{}
	for _, sv := range servers {
		id, _ := sv["id"].(string)
		if !reMachineID.MatchString(id) {
			continue
		}
		listed[id] = true
		if !had[id] {
			s.takeServer(m, id)
		}
		b, err := json.Marshal(sv)
		if err != nil {
			continue
		}
		if _, err := s.db.Exec(`UPDATE server_machines SET status = ?, seen_at = ? WHERE server_id = ? AND machine_id = ? AND (seen_at < ? OR status = '')`,
			string(b), millis(now), id, m.ID, millis(now.Add(-lastKnownAfter))); err != nil {
			s.log.Error("record server machines", "err", err)
		}
	}
	for id := range had {
		if listed[id] {
			continue
		}
		if _, err := s.db.Exec(`DELETE FROM server_machines WHERE server_id = ? AND machine_id = ?`, id, m.ID); err != nil {
			s.log.Error("record server machines", "err", err)
		}
	}
}

// takeServer gives a server to the dashboard's own machine.
func (s *Server) takeServer(m machine, id string) {
	if _, err := s.db.Exec(`INSERT INTO server_machines(server_id, machine_id) VALUES(?, ?)
		ON CONFLICT(server_id) DO UPDATE SET machine_id = excluded.machine_id, status = '', seen_at = 0, disputed_by = ''`, id, m.ID); err != nil {
		s.log.Error("record server machines", "err", err)
	}
}

// claimServers records which of the servers a joined machine lists it runs,
// keeping their statuses for when it is away, and returns those. Listing a
// server another machine has disputes it (see above) and goes in the
// machine's events.
func (s *Server) claimServers(m machine, servers []map[string]any) []map[string]any {
	now := s.now()
	tx, err := s.db.Begin()
	if err != nil {
		s.log.Error("record server machines", "err", err)
		return nil
	}
	defer tx.Rollback()
	var out []map[string]any
	var runs, disputes []any
	var disputed []string
	for _, sv := range servers {
		id, _ := sv["id"].(string)
		if !reMachineID.MatchString(id) {
			continue
		}
		if len(runs)+len(disputes) == maxMachineServers {
			s.log.Warn("a machine lists more servers than the dashboard keeps", "machine", m.ID, "kept", maxMachineServers)
			break
		}
		b, err := json.Marshal(sv)
		if err != nil {
			continue
		}
		var owner, disputedBy string
		switch err := tx.QueryRow(`SELECT machine_id, disputed_by FROM server_machines WHERE server_id = ?`, id).Scan(&owner, &disputedBy); {
		case isNoRows(err):
			_, err = tx.Exec(`INSERT INTO server_machines(server_id, machine_id, status, seen_at) VALUES(?,?,?,?)`, id, m.ID, string(b), millis(now))
			if err != nil {
				s.log.Error("record server machines", "err", err)
				return nil
			}
		case err != nil:
			s.log.Error("record server machines", "err", err)
			return nil
		case owner != m.ID:
			disputes = append(disputes, id)
			if disputedBy != m.ID {
				if _, err := tx.Exec(`UPDATE server_machines SET disputed_by = ? WHERE server_id = ?`, m.ID, id); err != nil {
					s.log.Error("record server machines", "err", err)
					return nil
				}
				disputed = append(disputed, id)
				s.log.Warn("a machine lists a server another machine runs", "machine", m.ID, "server", id, "runs on", owner)
			}
			continue
		default:
			_, err = tx.Exec(`UPDATE server_machines SET status = ?, seen_at = ? WHERE server_id = ? AND (seen_at < ? OR status = '')`, string(b), millis(now), id, millis(now.Add(-lastKnownAfter)))
			if err != nil {
				s.log.Error("record server machines", "err", err)
				return nil
			}
			if disputedBy != "" {
				sv["disputed"] = true
			}
		}
		runs = append(runs, id)
		out = append(out, sv)
	}
	if _, err := tx.Exec(`DELETE FROM server_machines WHERE machine_id = ?`+notIn("server_id", len(runs)), append([]any{m.ID}, runs...)...); err != nil {
		s.log.Error("record server machines", "err", err)
		return nil
	}
	if _, err := tx.Exec(`UPDATE server_machines SET disputed_by = '' WHERE disputed_by = ?`+notIn("server_id", len(disputes)), append([]any{m.ID}, disputes...)...); err != nil {
		s.log.Error("record server machines", "err", err)
		return nil
	}
	if err := tx.Commit(); err != nil {
		s.log.Error("record server machines", "err", err)
		return nil
	}
	for _, id := range disputed {
		s.machineEvent(m.ID, now, "machine.server_disputed", "", "", id)
	}
	return out
}

// notIn is " AND column NOT IN (?, ...)" for n values, or "" for none.
func notIn(column string, n int) string {
	if n == 0 {
		return ""
	}
	return " AND " + column + " NOT IN (?" + strings.Repeat(",?", n-1) + ")"
}

// lastKnownServers are a machine's servers as it last listed them, each
// with lastKnownAt, for while it can't be reached.
func (s *Server) lastKnownServers(m machine) []map[string]any {
	rows, err := s.db.Query(`SELECT status, seen_at, disputed_by FROM server_machines WHERE machine_id = ? ORDER BY server_id`, m.ID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var raw, disputedBy string
		var seen int64
		if rows.Scan(&raw, &seen, &disputedBy) != nil {
			continue
		}
		var sv map[string]any
		if json.Unmarshal([]byte(raw), &sv) != nil {
			continue
		}
		sv["lastKnownAt"] = fromMillis(seen)
		if disputedBy != "" {
			sv["disputed"] = true
		}
		out = append(out, sv)
	}
	return out
}

// claimCreated records the server a machine route created (a new server or
// a restore into a new one), so its requests go to that machine at once. A
// joined machine only gets an id no machine has.
func (s *Server) claimCreated(m machine, raw json.RawMessage) {
	var op struct {
		ServerID string `json:"serverId"`
	}
	if json.Unmarshal(raw, &op) != nil || !reMachineID.MatchString(op.ServerID) {
		return
	}
	if m.Kind == localKind {
		s.takeServer(m, op.ServerID)
		return
	}
	if _, err := s.db.Exec(`INSERT OR IGNORE INTO server_machines(server_id, machine_id, seen_at) VALUES(?,?,?)`, op.ServerID, m.ID, millis(s.now())); err != nil {
		s.log.Error("record server machines", "err", err)
	}
}
