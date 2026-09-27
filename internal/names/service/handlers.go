package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/netip"
	"regexp"
	"runtime/debug"
	"strconv"
	"time"

	"github.com/CIYAhq/playkeeper/internal/names"
)

const sourceURL = "https://github.com/CIYAhq/playkeeper"

var reChallengeValue = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)

// call is what the middleware learned about a request.
type call struct {
	addr   netip.Addr
	signer names.Signer
	key    string
	body   []byte
}

type handler func(w http.ResponseWriter, r *http.Request, c *call) error

// Handler is the service's HTTP API.
func (s *Service) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.index)
	mux.HandleFunc("GET /healthz", s.healthz)
	mux.HandleFunc("GET /v1/ip", s.limited(s.ip))
	mux.HandleFunc("GET /v1/names/{name}", s.limited(s.availability))
	mux.HandleFunc("GET /v1/names", s.signed(s.list))
	mux.HandleFunc("PUT /v1/names/{name}", s.signed(s.claim))
	mux.HandleFunc("DELETE /v1/names/{name}", s.signed(s.release))
	mux.HandleFunc("POST /v1/names/{name}/address", s.signed(s.refresh))
	mux.HandleFunc("PUT /v1/names/{name}/acme-challenge/{value}", s.signed(s.setChallenge))
	mux.HandleFunc("DELETE /v1/names/{name}/acme-challenge/{value}", s.signed(s.clearChallenge))
	mux.HandleFunc("PUT /v1/names/{name}/servers/{server}", s.signed(s.setServer))
	mux.HandleFunc("DELETE /v1/names/{name}/servers/{server}", s.signed(s.removeServer))
	return s.recoverer(mux)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (s *Service) writeError(w http.ResponseWriter, err error) {
	var e *names.Error
	if !errors.As(err, &e) {
		s.log.Error("Request failed", "error", err)
		e = &names.Error{Status: http.StatusInternalServerError, Code: names.CodeInternal, Message: "Something went wrong in the names service."}
	}
	if e.RetryAfter > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(int(math.Ceil(e.RetryAfter.Seconds()))))
	}
	writeJSON(w, e.Status, e.Body())
}

func (s *Service) recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				s.log.Error("Handler panic", "panic", v, "stack", string(debug.Stack()))
				s.writeError(w, errors.New("panic"))
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func invalid(format string, args ...any) error {
	return &names.Error{Status: http.StatusBadRequest, Code: names.CodeInvalidRequest, Message: fmt.Sprintf(format, args...)}
}

func rateLimited(wait time.Duration, what string) error {
	secs := int64(math.Ceil(wait.Seconds()))
	return &names.Error{Status: http.StatusTooManyRequests, Code: names.CodeRateLimited, RetryAfter: wait,
		Message: fmt.Sprintf("Too many %s; try again in %s.", what, (time.Duration(secs) * time.Second).String()),
		Params:  map[string]any{"retryAfterSeconds": secs}}
}

// limited finds the client address and applies the per-address limit.
func (s *Service) limited(h handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		addr, err := s.clientAddr(r)
		if err != nil {
			s.writeError(w, invalid("The request's client address could not be read."))
			return
		}
		if ok, wait := s.perIP.allow(addrBucket(addr, 64)); !ok {
			s.writeError(w, rateLimited(wait, "requests from this address"))
			return
		}
		if err := h(w, r, &call{addr: addr}); err != nil {
			s.writeError(w, err)
		}
	}
}

// signed also checks the request's signature and nonce and applies the
// per-key limit.
func (s *Service) signed(h handler) http.HandlerFunc {
	return s.limited(func(w http.ResponseWriter, r *http.Request, c *call) error {
		body, err := io.ReadAll(io.LimitReader(r.Body, maxBody+1))
		if err != nil {
			return invalid("The request body could not be read.")
		}
		if len(body) > maxBody {
			return invalid("The request body is too large.")
		}
		signer, err := names.VerifyRequest(r.Header, r.Method, r.URL.RequestURI(), body, s.base, s.now())
		if err != nil {
			return err
		}
		res, err := s.db.ExecContext(r.Context(), `INSERT INTO nonces (key, nonce, expires_at) VALUES (?, ?, ?) ON CONFLICT DO NOTHING`,
			signer.EncodedKey(), signer.Nonce, signer.Time.Add(names.MaxSkew).Unix())
		if err != nil {
			return err
		}
		if n, err := res.RowsAffected(); err != nil || n == 0 {
			return &names.Error{Status: http.StatusUnauthorized, Code: names.CodeReplayed, Message: "This signed request was already used once."}
		}
		c.signer, c.key, c.body = signer, signer.EncodedKey(), body
		if ok, wait := s.perKey.allow(c.key); !ok {
			return rateLimited(wait, "requests from this server")
		}
		return h(w, r, c)
	})
}

func decodeBody(b []byte, v any) error {
	if len(bytes.TrimSpace(b)) == 0 {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return invalid("The request body is not valid: %v", err)
	}
	if dec.More() {
		return invalid("The request body has unexpected data after the JSON.")
	}
	return nil
}

func (s *Service) index(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	fmt.Fprintf(w, "playkeeper-names: free yourname.%s addresses for Playkeeper servers.\nSource code (AGPL-3.0): %s\n", s.base, sourceURL)
}

func (s *Service) healthz(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if err := s.db.PingContext(r.Context()); err != nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprintln(w, "database unavailable")
		return
	}
	fmt.Fprintln(w, "ok")
}

func (s *Service) ip(w http.ResponseWriter, r *http.Request, c *call) error {
	writeJSON(w, http.StatusOK, names.IPInfo{IP: c.addr.String(), Family: family(c.addr), Public: publicUnicast(c.addr) && !inAny(cloudflareEdge, c.addr)})
	return nil
}

// usable refuses addresses a name cannot point at.
func (s *Service) usable(a netip.Addr) error {
	if inAny(cloudflareEdge, a) {
		s.log.Error("A request came from a Cloudflare proxy address: the names record must be DNS only (see services/names/README.md)", "ip", a.String())
		return &names.Error{Status: http.StatusServiceUnavailable, Code: names.CodeMisconfigured, RetryAfter: time.Hour,
			Message: "The names service cannot see this server's address right now, because the service is set up wrongly.",
			Hint:    "The Playkeeper project has to fix it; addresses that already work keep working."}
	}
	if !publicUnicast(a) {
		return &names.Error{Status: http.StatusForbidden, Code: names.CodeNotPublic, Params: map[string]any{"ip": a.String()},
			Message: fmt.Sprintf("The request came from %s, which is not a public internet address, so a name cannot point at it.", a),
			Hint:    "Run Playkeeper on a machine with a public IP address, with no VPN or proxy in between."}
	}
	return nil
}

// claimable says whether key may claim name, given its current row.
func (s *Service) claimable(row *nameRow, name, key string) error {
	addr := names.Address(name, s.base)
	params := map[string]any{"name": name}
	if reservedName(name) || s.block.has(name) {
		return &names.Error{Status: http.StatusForbidden, Code: names.CodeNameReserved, Params: params,
			Message: addr + " is reserved.", Hint: "Choose another name."}
	}
	if row == nil || row.Key == key {
		return nil
	}
	if row.State == names.StateReleased {
		until := time.Unix(row.ReleasedAt, 0).UTC().Add(releaseHold)
		params["until"] = until.Unix()
		return &names.Error{Status: http.StatusConflict, Code: names.CodeNameHeld, Params: params,
			Message: fmt.Sprintf("%s was given up recently and is held until %s, so nobody can take over its players.", addr, until.Format("2 January 2006")),
			Hint:    "Choose another name."}
	}
	return &names.Error{Status: http.StatusConflict, Code: names.CodeNameTaken, Params: params,
		Message: addr + " is already taken.", Hint: "Choose another name."}
}

func (s *Service) availability(w http.ResponseWriter, r *http.Request, c *call) error {
	name := r.PathValue("name")
	a := names.Availability{Name: name}
	if err := names.CheckName(name); err != nil {
		e := err.(*names.Error)
		a.Code, a.Message, a.Params = e.Code, e.Message, e.Params
		writeJSON(w, http.StatusOK, a)
		return nil
	}
	a.Address = names.Address(name, s.base)
	row, err := s.getName(r.Context(), s.db, name)
	if err != nil {
		return err
	}
	if err := s.claimable(row, name, ""); err != nil {
		e := err.(*names.Error)
		a.Code, a.Message, a.Params = e.Code, e.Message, e.Params
	} else {
		a.Available, a.Message = true, a.Address+" is free."
	}
	writeJSON(w, http.StatusOK, a)
	return nil
}

func (s *Service) limitReached(ctx context.Context, q queryer, key, name string) error {
	n, err := s.heldBy(ctx, q, key, name)
	if err != nil {
		return err
	}
	if n < s.cfg.MaxNamesPerKey {
		return nil
	}
	return &names.Error{Status: http.StatusForbidden, Code: names.CodeLimitReached, Params: map[string]any{"limit": s.cfg.MaxNamesPerKey},
		Message: fmt.Sprintf("This server already has %d %s address, the most it can have.", n, s.base),
		Hint:    "Release it first to choose another one."}
}

// networkFull refuses a name in the network nw (see network) when nw already
// holds as many names that are not released, other than except, as allowed.
func (s *Service) networkFull(ctx context.Context, q queryer, nw, except string) error {
	var n int
	if err := q.QueryRowContext(ctx, `SELECT count(*) FROM names WHERE network = ? AND name != ? AND state != ?`, nw, except, names.StateReleased).Scan(&n); err != nil {
		return err
	}
	if n < s.cfg.MaxNamesPerNetwork {
		return nil
	}
	return &names.Error{Status: http.StatusForbidden, Code: names.CodeNetworkLimit, Params: map[string]any{"limit": s.cfg.MaxNamesPerNetwork, "network": nw},
		Message: fmt.Sprintf("Servers in this network (%s) already have %d %s addresses, the most one network can have.", nw, n, s.base),
		Hint:    "Use your own domain instead."}
}

func (s *Service) claim(w http.ResponseWriter, r *http.Request, c *call) error {
	ctx := r.Context()
	name := r.PathValue("name")
	if err := names.CheckName(name); err != nil {
		return err
	}
	if err := decodeBody(c.body, &struct{}{}); err != nil {
		return err
	}
	if err := s.usable(c.addr); err != nil {
		return err
	}
	row, err := s.getName(ctx, s.db, name)
	if err != nil {
		return err
	}
	if err := s.claimable(row, name, c.key); err != nil {
		return err
	}
	answered := false
	if row == nil || row.State == names.StateReleased {
		if err := s.limitReached(ctx, s.db, c.key, name); err != nil {
			return err
		}
		if err := s.networkFull(ctx, s.db, network(c.addr), name); err != nil {
			return err
		}
	} else if answered, err = s.recheck(ctx, row, c.addr); err != nil {
		return err
	}
	if row == nil {
		if ok, wait := s.claimsAddr.allow(addrBucket(c.addr, 56)); !ok {
			return rateLimited(wait, "new names from this address")
		}
		if err := s.checkFree(ctx, name); err != nil {
			return err
		}
		// Taken last, so claims of names in use cannot use up everyone's.
		if ok, wait := s.claimsAll.allow("all"); !ok {
			s.alerts.send(alertClaims, fmt.Sprintf("New names are refused for now: the %d a day that %s allows are used up. See \"Limits\" in services/names/README.md.", s.cfg.ClaimsPerDay, EnvClaimsPerDay))
			return rateLimited(wait, "new names today")
		}
	}
	if err := s.commitClaim(ctx, name, c, answered); err != nil {
		return err
	}
	if row == nil || row.State == names.StateReleased {
		s.log.Info("Claimed a name", "name", name, "key", c.signer.KeyID(), "ip", c.addr.String())
	}
	return s.respond(w, r, name)
}

// commitClaim stores a claim; answered is for a name the key already holds
// (see setAddress).
func (s *Service) commitClaim(ctx context.Context, name string, c *call, answered bool) error {
	return s.writeTx(ctx, func(q queryer) error {
		row, err := s.getName(ctx, q, name)
		if err != nil {
			return err
		}
		if err := s.claimable(row, name, c.key); err != nil {
			return err
		}
		if row != nil && row.State != names.StateReleased {
			return s.setAddress(ctx, q, row, c.addr, false, answered)
		}
		if err := s.limitReached(ctx, q, c.key, name); err != nil {
			return err
		}
		if err := s.networkFull(ctx, q, network(c.addr), name); err != nil {
			return err
		}
		now := s.now().Unix()
		v4, v6 := "", ""
		if c.addr.Is4() {
			v4 = c.addr.String()
		} else {
			v6 = c.addr.String()
		}
		// A new row starts at version 1 and synced 0, so it is published.
		res, err := q.ExecContext(ctx, `INSERT INTO names (name, key, state, ipv4, ipv6, network, claimed_at, refreshed_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT (name) DO UPDATE SET state = excluded.state, ipv4 = excluded.ipv4, ipv6 = excluded.ipv6, network = excluded.network,
			claimed_at = excluded.claimed_at, refreshed_at = excluded.refreshed_at, lapsed_at = 0, released_at = 0, version = version + 1,
			alive_at = 0, checked_at = 0, failed_checks = 0, lapse_reason = ''
			WHERE names.key = excluded.key`,
			name, c.key, names.StateActive, v4, v6, network(c.addr), now, now)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n != 1 {
			return errors.New("the name changed hands during the claim")
		}
		return nil
	})
}

// setAddress points row at addr (keeping the other IP version's address
// unless clearOther) and counts it as a refresh. A name that lapsed because
// its address did not answer comes back only when answered: addr just
// answered the liveness check (see recheck). A lapsed name gets
// minFailedChecks new checks before it can lapse for not answering again.
// A name that moves to another network counts against that one, so the
// move is refused when it already holds as many names as allowed.
func (s *Service) setAddress(ctx context.Context, q queryer, row *nameRow, addr netip.Addr, clearOther, answered bool) error {
	v4, v6 := row.IPv4, row.IPv6
	if addr.Is4() {
		v4 = addr.String()
		if clearOther {
			v6 = ""
		}
	} else {
		v6 = addr.String()
		if clearOther {
			v4 = ""
		}
	}
	// A name from before networks were recorded was in that of its address.
	was := row.Network
	if was == "" {
		was = nameNetwork("", row.IPv4, row.IPv6)
	}
	nw := nameNetwork(was, v4, v6)
	if nw != was {
		if err := s.networkFull(ctx, q, nw, row.Name); err != nil {
			return err
		}
	}
	bump := 0
	if v4 != row.IPv4 || v6 != row.IPv6 || row.State != names.StateActive {
		bump = 1
	}
	now := s.now().Unix()
	answeredAt := int64(0)
	if answered {
		answeredAt = now
	}
	res, err := q.ExecContext(ctx, `UPDATE names SET ipv4 = ?, ipv6 = ?, network = ?, state = ?, lapsed_at = 0, lapse_reason = '', refreshed_at = ?, version = version + ?,
		failed_checks = CASE WHEN ? > 0 OR state = ? THEN 0 ELSE failed_checks END, alive_at = max(alive_at, ?), checked_at = max(checked_at, ?)
		WHERE name = ? AND key = ? AND (state != ? OR lapse_reason != ? OR ? > 0)`,
		v4, v6, nw, names.StateActive, now, bump,
		answeredAt, names.StateLapsed, answeredAt, answeredAt,
		row.Name, row.Key, names.StateLapsed, names.LapseNoAnswer, answeredAt)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil || n > 0 {
		return err
	}
	cur, err := s.getName(ctx, q, row.Name)
	if err != nil || cur == nil || cur.Key != row.Key || cur.State != names.StateLapsed || cur.LapseReason != names.LapseNoAnswer {
		return err
	}
	return &names.Error{Status: http.StatusConflict, Code: names.CodeNotAnswering, Params: map[string]any{"name": row.Name, "ip": addr.String(), "port": names.AlivePort},
		Message: fmt.Sprintf("%s stays off, because this server's dashboard did not answer at %s.", names.Address(row.Name, s.base), netip.AddrPortFrom(addr, names.AlivePort)),
		Hint:    fmt.Sprintf("Open port %d to the internet, in the machine's firewall and at the hosting provider, then refresh again.", names.AlivePort)}
}

// owned returns name's row if key holds it.
func (s *Service) owned(ctx context.Context, name, key string) (*nameRow, error) {
	if err := names.CheckName(name); err != nil {
		return nil, err
	}
	row, err := s.getName(ctx, s.db, name)
	if err != nil {
		return nil, err
	}
	addr := names.Address(name, s.base)
	params := map[string]any{"name": name}
	switch {
	case row == nil || (row.Key == key && row.State == names.StateReleased):
		return nil, &names.Error{Status: http.StatusNotFound, Code: names.CodeNotClaimed, Params: params,
			Message: addr + " is not claimed by this server.", Hint: "Claim it first."}
	case row.Key != key:
		return nil, &names.Error{Status: http.StatusForbidden, Code: names.CodeNotYourName, Params: params,
			Message: addr + " belongs to another server."}
	}
	return row, nil
}

func (s *Service) notLapsed(row *nameRow) error {
	if row.State != names.StateLapsed {
		return nil
	}
	addr := names.Address(row.Name, s.base)
	if row.LapseReason == names.LapseNoAnswer {
		days := int(unansweredAfter.Hours() / 24)
		return &names.Error{Status: http.StatusConflict, Code: names.CodeNameLapsed,
			Params:  map[string]any{"name": row.Name, "reason": row.LapseReason, "days": days, "port": names.AlivePort},
			Message: fmt.Sprintf("%s stopped pointing at this server because its dashboard did not answer on port %d for %d days.", addr, names.AlivePort, days),
			Hint:    fmt.Sprintf("Open port %d to the internet, then refresh the address.", names.AlivePort)}
	}
	days := int(lapseAfter.Hours() / 24)
	return &names.Error{Status: http.StatusConflict, Code: names.CodeNameLapsed,
		Params:  map[string]any{"name": row.Name, "reason": names.LapseNotRefreshed, "days": days},
		Message: fmt.Sprintf("%s stopped pointing at this server because it was not refreshed for %d days.", addr, days),
		Hint:    "Refresh the address first."}
}

// respond syncs name's records if they changed and answers with the name.
func (s *Service) respond(w http.ResponseWriter, r *http.Request, name string) error {
	row, err := s.syncedRow(r.Context(), name)
	if err != nil {
		return err
	}
	if row == nil {
		return &names.Error{Status: http.StatusNotFound, Code: names.CodeNotClaimed, Params: map[string]any{"name": name},
			Message: names.Address(name, s.base) + " is not claimed by this server.", Hint: "Claim it first."}
	}
	n, err := s.info(r.Context(), row)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, n)
	return nil
}

func (s *Service) syncedRow(ctx context.Context, name string) (*nameRow, error) {
	row, err := s.getName(ctx, s.db, name)
	if err != nil || row == nil || row.Synced >= row.Version {
		return row, err
	}
	s.syncDetached(ctx, name)
	return s.getName(ctx, s.db, name)
}

func (s *Service) refresh(w http.ResponseWriter, r *http.Request, c *call) error {
	var req names.RefreshRequest
	if err := decodeBody(c.body, &req); err != nil {
		return err
	}
	row, err := s.owned(r.Context(), r.PathValue("name"), c.key)
	if err != nil {
		return err
	}
	if err := s.usable(c.addr); err != nil {
		return err
	}
	answered, err := s.recheck(r.Context(), row, c.addr)
	if err != nil {
		return err
	}
	// In one transaction, so two names can't both take a network's last
	// place.
	if err := s.writeTx(r.Context(), func(q queryer) error {
		return s.setAddress(r.Context(), q, row, c.addr, req.ClearOther, answered)
	}); err != nil {
		return err
	}
	return s.respond(w, r, row.Name)
}

func (s *Service) release(w http.ResponseWriter, r *http.Request, c *call) error {
	ctx := r.Context()
	row, err := s.owned(ctx, r.PathValue("name"), c.key)
	if err != nil {
		return err
	}
	if err := s.releaseName(ctx, row.Name, "released by its server"); err != nil {
		return err
	}
	return s.respond(w, r, row.Name)
}

// releaseName removes a name's records and holds it from others.
func (s *Service) releaseName(ctx context.Context, name, why string) error {
	err := s.writeTx(ctx, func(q queryer) error {
		for _, query := range []string{`DELETE FROM challenges WHERE name = ?`, `DELETE FROM servers WHERE name = ?`} {
			if _, err := q.ExecContext(ctx, query, name); err != nil {
				return err
			}
		}
		_, err := q.ExecContext(ctx, `UPDATE names SET state = ?, released_at = ?, version = version + 1 WHERE name = ?`,
			names.StateReleased, s.now().Unix(), name)
		return err
	})
	if err != nil {
		return err
	}
	s.log.Info("Released a name", "name", name, "why", why)
	return nil
}

func (s *Service) list(w http.ResponseWriter, r *http.Request, c *call) error {
	rows, err := s.namesOf(r.Context(), c.key)
	if err != nil {
		return err
	}
	out := names.NameList{Names: []names.Name{}}
	for _, row := range rows {
		n, err := s.info(r.Context(), row)
		if err != nil {
			return err
		}
		out.Names = append(out.Names, n)
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

func (s *Service) challengeValue(r *http.Request) (string, error) {
	v := r.PathValue("value")
	if !reChallengeValue.MatchString(v) {
		return "", &names.Error{Status: http.StatusBadRequest, Code: names.CodeInvalidChallenge,
			Message: "An ACME DNS-01 challenge value is 43 characters of base64url."}
	}
	return v, nil
}

func (s *Service) setChallenge(w http.ResponseWriter, r *http.Request, c *call) error {
	ctx := r.Context()
	row, err := s.owned(ctx, r.PathValue("name"), c.key)
	if err != nil {
		return err
	}
	if err := s.notLapsed(row); err != nil {
		return err
	}
	value, err := s.challengeValue(r)
	if err != nil {
		return err
	}
	now := s.now()
	expires := now.Add(challengeTTL)
	var live int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM challenges WHERE name = ? AND value = ? AND expires_at > ?`, row.Name, value, now.Unix()).Scan(&live); err != nil {
		return err
	}
	if live == 0 {
		if err := s.checkRoom(ctx, 1, forChallenge); err != nil {
			return err
		}
	}
	var set certSet
	err = s.writeTx(ctx, func(q queryer) error {
		var others int
		if err := q.QueryRowContext(ctx, `SELECT count(*) FROM challenges WHERE name = ? AND value != ? AND expires_at > ?`, row.Name, value, now.Unix()).Scan(&others); err != nil {
			return err
		}
		if others >= maxChallenges {
			return &names.Error{Status: http.StatusConflict, Code: names.CodeTooManyTXT, Params: map[string]any{"max": maxChallenges},
				Message: fmt.Sprintf("A name can have at most %d challenge records at once.", maxChallenges),
				Hint:    "Clear the old ones first; they also go away on their own after an hour."}
		}
		if live == 0 {
			var err error
			if set, err = s.countChallenge(ctx, q, row, now); err != nil {
				return err
			}
		}
		if _, err := q.ExecContext(ctx, `INSERT INTO challenges (name, value, expires_at) VALUES (?, ?, ?)
			ON CONFLICT (name, value) DO UPDATE SET expires_at = excluded.expires_at`, row.Name, value, expires.Unix()); err != nil {
			return err
		}
		_, err := q.ExecContext(ctx, `UPDATE names SET version = version + 1 WHERE name = ?`, row.Name)
		return err
	})
	if err != nil {
		return err
	}
	s.logCertSet(ctx, row.Name, set, now)
	return s.respondChallenge(w, r, row.Name, value, expires)
}

func (s *Service) clearChallenge(w http.ResponseWriter, r *http.Request, c *call) error {
	ctx := r.Context()
	row, err := s.owned(ctx, r.PathValue("name"), c.key)
	if err != nil {
		return err
	}
	value, err := s.challengeValue(r)
	if err != nil {
		return err
	}
	res, err := s.db.ExecContext(ctx, `DELETE FROM challenges WHERE name = ? AND value = ?`, row.Name, value)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n > 0 {
		if _, err := s.db.ExecContext(ctx, `UPDATE names SET version = version + 1 WHERE name = ?`, row.Name); err != nil {
			return err
		}
	}
	return s.respondChallenge(w, r, row.Name, value, time.Time{})
}

func (s *Service) respondChallenge(w http.ResponseWriter, r *http.Request, name, value string, expires time.Time) error {
	row, err := s.syncedRow(r.Context(), name)
	if err != nil {
		return err
	}
	ch := names.Challenge{FQDN: names.ChallengeFQDN(name, s.base), Value: value, ExpiresAt: expires.UTC(), DNS: names.DNSOK}
	if row == nil || row.Synced < row.Version {
		ch.DNS = names.DNSPending
	}
	writeJSON(w, http.StatusOK, ch)
	return nil
}

func serverLabel(r *http.Request) (string, error) {
	label := r.PathValue("server")
	if label == "@" {
		return "", nil
	}
	return label, names.CheckServerLabel(label)
}

func (s *Service) setServer(w http.ResponseWriter, r *http.Request, c *call) error {
	ctx := r.Context()
	row, err := s.owned(ctx, r.PathValue("name"), c.key)
	if err != nil {
		return err
	}
	if err := s.notLapsed(row); err != nil {
		return err
	}
	label, err := serverLabel(r)
	if err != nil {
		return err
	}
	var req names.ServerRequest
	if len(bytes.TrimSpace(c.body)) == 0 {
		return invalid("The request needs a body with the server's port.")
	}
	if err := decodeBody(c.body, &req); err != nil {
		return err
	}
	if req.Port < 1 || req.Port > 65535 {
		return &names.Error{Status: http.StatusBadRequest, Code: names.CodeInvalidPort, Params: map[string]any{"port": req.Port},
			Message: "The port must be between 1 and 65535."}
	}
	servers, err := s.serverRows(ctx, row.Name)
	if err != nil {
		return err
	}
	current := -1
	for _, sv := range servers {
		if sv.Label == label {
			current = sv.Port
		}
	}
	if current < 0 {
		if err := s.serverAddressesAllowed(row, req.Port); err != nil {
			return err
		}
		if err := s.serversFull(ctx, s.db, row, label); err != nil {
			return err
		}
		if err := s.checkRoom(ctx, 1, forServer); err != nil {
			return err
		}
	}
	if current != req.Port {
		err := s.writeTx(ctx, func(q queryer) error {
			if err := s.serversFull(ctx, q, row, label); err != nil {
				return err
			}
			if _, err := q.ExecContext(ctx, `INSERT INTO servers (name, label, port) VALUES (?, ?, ?)
				ON CONFLICT (name, label) DO UPDATE SET port = excluded.port`, row.Name, label, req.Port); err != nil {
				return err
			}
			_, err := q.ExecContext(ctx, `UPDATE names SET version = version + 1 WHERE name = ?`, row.Name)
			return err
		})
		if err != nil {
			return err
		}
	}
	return s.respondServer(w, r, row.Name, label, req.Port)
}

// serverAddressesAllowed refuses a name's first server addresses until it
// is serverAddressAge old and its address has answered the liveness check;
// port is the server's, for the hint.
func (s *Service) serverAddressesAllowed(row *nameRow, port int) error {
	addr := names.Address(row.Name, s.base)
	if from := time.Unix(row.ClaimedAt, 0).Add(serverAddressAge); s.now().Before(from) {
		return &names.Error{Status: http.StatusConflict, Code: names.CodeServerNotYet, Params: map[string]any{"name": row.Name, "from": from.Unix()},
			Message: fmt.Sprintf("%s can have server addresses from %s, %d days after it was claimed.", addr, from.UTC().Format("2 January 2006 15:04 UTC"), int(serverAddressAge.Hours()/24)),
			Hint:    fmt.Sprintf("Until then players can join at %s:%d.", addr, port)}
	}
	if row.AliveAt == 0 {
		return &names.Error{Status: http.StatusConflict, Code: names.CodeNotAnswering, Params: map[string]any{"name": row.Name, "port": names.AlivePort},
			Message: fmt.Sprintf("%s can have server addresses once this server's dashboard has answered from the internet on port %d.", addr, names.AlivePort),
			Hint:    fmt.Sprintf("Until then players can join at %s:%d.", addr, port)}
	}
	return nil
}

// serversFull refuses another server address under row's name when its
// install, or the network the name counts against, already has as many as
// allowed. The label itself is not counted, so a port can always change.
func (s *Service) serversFull(ctx context.Context, q queryer, row *nameRow, label string) error {
	var byKey, byNetwork int
	err := q.QueryRowContext(ctx, `SELECT coalesce(sum(n.key = ?), 0), coalesce(sum(n.network = ?), 0)
		FROM servers sv JOIN names n ON n.name = sv.name
		WHERE (n.key = ? OR n.network = ?) AND NOT (sv.name = ? AND sv.label = ?)`,
		row.Key, row.Network, row.Key, row.Network, row.Name, label).Scan(&byKey, &byNetwork)
	if err != nil {
		return err
	}
	e := &names.Error{Status: http.StatusConflict, Code: names.CodeTooManyServers, Hint: "Remove one you no longer use first."}
	switch {
	case byKey >= serversPerKey:
		e.Params = map[string]any{"max": serversPerKey, "scope": "install"}
		e.Message = fmt.Sprintf("This server already has %d server addresses, the most it can have.", byKey)
	case byNetwork >= serversPerNetwork:
		e.Params = map[string]any{"max": serversPerNetwork, "scope": "network", "network": row.Network}
		e.Message = fmt.Sprintf("Servers in this network (%s) already have %d server addresses, the most one network can have.", row.Network, byNetwork)
	default:
		return nil
	}
	return e
}

func (s *Service) removeServer(w http.ResponseWriter, r *http.Request, c *call) error {
	ctx := r.Context()
	row, err := s.owned(ctx, r.PathValue("name"), c.key)
	if err != nil {
		return err
	}
	label, err := serverLabel(r)
	if err != nil {
		return err
	}
	res, err := s.db.ExecContext(ctx, `DELETE FROM servers WHERE name = ? AND label = ?`, row.Name, label)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n > 0 {
		if _, err := s.db.ExecContext(ctx, `UPDATE names SET version = version + 1 WHERE name = ?`, row.Name); err != nil {
			return err
		}
	}
	return s.respondServer(w, r, row.Name, label, 0)
}

func (s *Service) respondServer(w http.ResponseWriter, r *http.Request, name, label string, port int) error {
	row, err := s.syncedRow(r.Context(), name)
	if err != nil {
		return err
	}
	sv := names.Server{Label: label, Address: names.ServerAddress(label, name, s.base), Port: port, DNS: names.DNSOK}
	if row == nil || row.Synced < row.Version {
		sv.DNS = names.DNSPending
	}
	writeJSON(w, http.StatusOK, sv)
	return nil
}
