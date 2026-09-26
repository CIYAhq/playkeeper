package agent

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"maps"
	"net"
	"net/http"
	"net/netip"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/CIYAhq/playkeeper/internal/api"
	"github.com/CIYAhq/playkeeper/internal/certs"
	"github.com/CIYAhq/playkeeper/internal/names"
)

// The machine's address: a free playkeeper.io name from the names service,
// or the admin's own domain. Its state is kept in kv "address"; the
// dashboard's certificate for it is in certificates.go.

const (
	kvAddress = "address"

	// Rules of the names service (internal/names/service): a name that is
	// not refreshed for 30 days stops pointing here, a released one is held
	// from others for 30 days, and one name gives at most 5 servers an
	// address.
	namesLapseAfter  = 30 * 24 * time.Hour
	namesReleaseHold = 30 * 24 * time.Hour
	namesMaxServers  = 5
	// namesUnansweredAfter: a name whose dashboard does not answer the
	// liveness check on port 8443 for a week stops pointing here too.
	namesUnansweredAfter = 7 * 24 * time.Hour

	freeRefreshEvery = 24 * time.Hour
	freeRetryEvery   = time.Hour
	// freePollEvery spaces out the loop's looks at records still being
	// published: the service allows a key about one request a minute.
	freePollEvery = 5 * time.Minute
	publishWait   = 10 * time.Minute
	// settleWait bounds what follows a step of a change of name that
	// failed: finding out what the step did at the names service, undoing
	// it and claiming the old name back. It is time of its own, as a
	// request that got no answer may have used up the change's.
	settleWait = 2 * time.Minute
	// ownRecheckPending and ownRecheckReady are how often the own domain's
	// records are looked up while they are not right yet, and after.
	ownRecheckPending = time.Minute
	ownRecheckReady   = 6 * time.Hour
)

// addressState is the address as it is saved.
type addressState struct {
	Kind  string    `json:"kind"`
	Host  string    `json:"host,omitempty"`
	Since time.Time `json:"since,omitzero"`
	// IP is the public address the dashboard was last opened with.
	IP string `json:"ip,omitempty"`
	// Free is the free name as the names service last described it.
	Free *freeState `json:"free,omitempty"`
	// Released is the free name given up last, which this machine's key
	// can claim again while it is held from others.
	Released string `json:"released,omitempty"`
	// Check is the last look at the own domain's records.
	Check *api.AddressCheck `json:"check,omitempty"`
}

type freeState struct {
	Name      names.Name `json:"name"`
	CheckedAt time.Time  `json:"checkedAt"`
	// NextRefresh is when the loop refreshes the name next.
	NextRefresh time.Time `json:"nextRefresh,omitzero"`
	// ServersWait is the names service's reason for giving the servers no
	// address yet (names.CodeServerNotYet or names.CodeNotAnswering), with
	// ServersFrom from the first. ServersFailed counts the updates of the
	// servers' records that failed otherwise, in a row. The loop tries
	// again at ServersRetry.
	ServersWait   string    `json:"serversWait,omitempty"`
	ServersFrom   time.Time `json:"serversFrom,omitzero"`
	ServersFailed int       `json:"serversFailed,omitempty"`
	ServersRetry  time.Time `json:"serversRetry,omitzero"`
}

// addressRuntime is the address's in-memory side. lock allows one change
// or operation at a time; kick wakes the address loop. The state's pointer
// fields are never changed in place, only replaced, so copies can be read
// without the mutex.
type addressRuntime struct {
	lock chan struct{}
	kick chan struct{}

	mu        sync.Mutex
	st        addressState
	op        *api.Operation
	namesErr  string
	namesAt   time.Time
	avail     map[string]availEntry
	recheck   time.Time
	lastPoll  time.Time
	serversUp bool
	// claiming is the free name being claimed right now: the names service
	// checks that a lapsed name's address answers before the claim does.
	claiming string
}

type availEntry struct {
	a  api.NameAvailability
	at time.Time
}

func (a *Agent) loadAddress() {
	a.addr.lock = make(chan struct{}, 1)
	a.addr.kick = make(chan struct{}, 1)
	a.addr.avail = map[string]availEntry{}
	v, ok, err := a.kvGet(kvAddress)
	if err != nil || !ok {
		return
	}
	if err := json.Unmarshal([]byte(v), &a.addr.st); err != nil {
		a.log.Warn("the saved machine address cannot be read", "err", err)
	}
}

func (a *Agent) address() addressState {
	a.addr.mu.Lock()
	defer a.addr.mu.Unlock()
	return a.addr.st
}

// updateAddress changes the state with fn and saves it, as one step.
func (a *Agent) updateAddress(fn func(st *addressState)) error {
	a.addr.mu.Lock()
	defer a.addr.mu.Unlock()
	st := a.addr.st
	fn(&st)
	b, err := json.Marshal(st)
	if err != nil {
		return err
	}
	if err := a.kvSet(kvAddress, string(b)); err != nil {
		return err
	}
	a.addr.st = st
	return nil
}

func (a *Agent) setAddress(st addressState) error {
	return a.updateAddress(func(s *addressState) { *s = st })
}

// holdAddress takes the address lock for a change. The address loop holds
// it for a few seconds at a time, so a request waits up to wait for it; an
// address operation makes it busy at once.
func (a *Agent) holdAddress(ctx context.Context, wait time.Duration) (release func(), err error) {
	release = func() { <-a.addr.lock }
	select {
	case a.addr.lock <- struct{}{}:
		return release, nil
	default:
	}
	busy := func() error {
		op := a.addressOp()
		msg := "Playkeeper is busy with the address."
		if op != nil {
			msg = "Playkeeper is busy " + opLabels[op.Kind] + "."
		}
		return &apiError{Status: http.StatusConflict, Code: api.CodeBusy, Msg: msg, Hint: "Wait for it to finish, then try again.", Op: op}
	}
	if wait <= 0 || a.addressOp() != nil {
		return nil, busy()
	}
	t := time.NewTimer(wait)
	defer t.Stop()
	select {
	case a.addr.lock <- struct{}{}:
		return release, nil
	case <-t.C:
		return nil, busy()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (a *Agent) addressOp() *api.Operation {
	a.addr.mu.Lock()
	defer a.addr.mu.Unlock()
	return copyOp(a.addr.op)
}

// startAddressOp runs fn as the address's operation. The caller holds the
// address lock, which the operation releases when fn returns.
func (a *Agent) startAddressOp(kind, actor string, fn func(ctx context.Context, h *opHandle) error) *api.Operation {
	op := &api.Operation{ID: newID(), Kind: kind, Status: api.OpRunning, Actor: actor, StartedAt: a.now().UTC(), Detail: map[string]any{}}
	a.addr.mu.Lock()
	a.addr.op = op
	snap := copyOp(op)
	a.addr.mu.Unlock()
	a.saveOperation(snap)
	h := &opHandle{save: a.saveOperation, op: op, mu: func() func() { a.addr.mu.Lock(); return a.addr.mu.Unlock }}
	a.wg.Add(1)
	go func() {
		defer a.wg.Done()
		defer func() { <-a.addr.lock }()
		ctx, cancel := context.WithTimeout(a.ctx, 30*time.Minute)
		defer cancel()
		err := runOp(ctx, h, fn)
		a.addr.mu.Lock()
		done := finishOp(op, h, err, a.now().UTC())
		a.addr.op = nil
		a.addr.mu.Unlock()
		a.finishOperation("", "machine", &done)
		if err != nil {
			a.log.Warn("operation failed", "kind", kind, "err", err)
		}
	}()
	return snap
}

// serversChanged tells the address loop that a server was added or
// removed, so its records follow.
func (a *Agent) serversChanged() {
	a.addr.mu.Lock()
	a.addr.serversUp = true
	a.addr.mu.Unlock()
	select {
	case a.addr.kick <- struct{}{}:
	default:
	}
}

func (a *Agent) takeServersChanged() bool {
	a.addr.mu.Lock()
	defer a.addr.mu.Unlock()
	up := a.addr.serversUp
	a.addr.serversUp = false
	return up
}

// Names service.

// namesClient is a client for the names service, acting on the machine's
// free name; withKey loads the machine's key (created on first use).
func (a *Agent) namesClient(withKey bool) (*names.Client, error) {
	c := &names.Client{ServiceURL: nonEmptyOr(a.cfg.NamesURL, names.DefaultServiceURL), Now: a.now}
	if h := a.opts.NamesHTTP; h != nil {
		c.HTTP, c.HTTP4, c.HTTP6 = h, h, h
	}
	if withKey {
		key, err := names.LoadOrCreateKey(filepath.Join(a.cfg.AgentDir(), "names.key"))
		if err != nil {
			return nil, &apiError{Status: http.StatusInternalServerError, Code: api.CodeInternal, Msg: "Playkeeper could not read its key for the free address service: " + err.Error()}
		}
		c.Key = key
	}
	if st := a.address(); st.Kind == api.AddressPlaykeeper && st.Free != nil {
		c.Name = st.Free.Name.Name
	}
	return c, nil
}

// namesError is the answer for a names client error. The service's
// refusals keep their code, message and params; a service that cannot be
// reached is names_unreachable, and nothing else depends on it. A 401 from
// the service becomes 502, so the dashboard does not take it for its own
// session ending.
func (a *Agent) namesError(err error) error {
	var ae *apiError
	if errors.As(err, &ae) {
		return err
	}
	var ne *names.Error
	if !errors.As(err, &ne) || ne.Code == names.CodeUnavailable {
		a.noteNames(err.Error())
		return &apiError{Status: http.StatusServiceUnavailable, Code: api.CodeNamesUnreachable,
			Msg: "Playkeeper couldn't reach the free address service.", Hint: "Addresses that already work keep working. Try again later.",
			Params: map[string]any{"detail": err.Error()}}
	}
	if ne.Status != 0 && !slices.Contains(localNamesCodes, ne.Code) {
		a.noteNames("")
	}
	return namesRefusal(ne)
}

// localNamesCodes are refusals the names client can give without asking
// the service, so they say nothing about whether it can be reached.
var localNamesCodes = []string{names.CodeInvalidName, names.CodeInvalidServer, names.CodeInvalidPort, names.CodeInvalidChallenge, names.CodeNoName}

// namesRefusal is the answer for a request the names service (or the
// client, for a name that can't be valid) refused.
func namesRefusal(ne *names.Error) error {
	status := ne.Status
	switch {
	case status == 0:
		status = http.StatusBadRequest
	case status == http.StatusUnauthorized || status >= 500:
		status = http.StatusBadGateway
	}
	params := maps.Clone(ne.Params)
	if ne.RetryAfter > 0 {
		if params == nil {
			params = map[string]any{}
		}
		params["retryAfterSeconds"] = int64(ne.RetryAfter.Seconds())
	}
	return &apiError{Status: status, Code: ne.Code, Msg: ne.Message, Hint: ne.Hint, Params: params}
}

// noteNames records whether the names service answered: msg is empty when
// it did, else how it could not be reached.
func (a *Agent) noteNames(msg string) {
	a.addr.mu.Lock()
	a.addr.namesErr, a.addr.namesAt = msg, a.now().UTC()
	a.addr.mu.Unlock()
}

func namesCode(err error, code string) bool {
	var ne *names.Error
	return errors.As(err, &ne) && ne.Code == code
}

// maybeStored reports whether a claim or release that failed with err may
// have been carried out all the same: the service refuses a change before
// it stores it (4xx), and the client refuses one it doesn't send, but no
// answer, or a 5xx from the service or a proxy in front of it, can come
// after the change was stored.
func maybeStored(err error) bool {
	var ne *names.Error
	return !errors.As(err, &ne) || ne.Status >= 500
}

// listedName looks name up in the service's list of the names this key
// holds, released ones included.
func listedName(ctx context.Context, c *names.Client, name string) (n names.Name, listed bool, err error) {
	list, err := c.Names(ctx)
	if err != nil {
		return names.Name{}, false, err
	}
	for _, n := range list {
		if n.Name == name {
			return n, true, nil
		}
	}
	return names.Name{}, false, nil
}

// availability says whether name can be claimed, with similar free names
// when it is taken.
func (a *Agent) availability(ctx context.Context, name string) (api.NameAvailability, error) {
	st := a.address()
	if st.Kind == api.AddressPlaykeeper && st.Free != nil && st.Free.Name.Name == name {
		return api.NameAvailability{Name: name, Address: names.Address(name, names.DefaultBase), Available: true}, nil
	}
	now := a.now()
	a.addr.mu.Lock()
	e, ok := a.addr.avail[name]
	a.addr.mu.Unlock()
	if ok && now.Sub(e.at) < 30*time.Second {
		return e.a, nil
	}
	c, err := a.namesClient(false)
	if err != nil {
		return api.NameAvailability{}, err
	}
	res, err := c.Available(ctx, name)
	if err != nil {
		return api.NameAvailability{}, a.namesError(err)
	}
	if res.Code != names.CodeInvalidName {
		a.noteNames("")
	}
	av := api.NameAvailability{Name: res.Name, Address: res.Address, Available: res.Available, Code: res.Code, Message: res.Message, Params: res.Params}
	if !av.Available && av.Code == names.CodeNameHeld && name == st.Released {
		av = api.NameAvailability{Name: name, Address: names.Address(name, names.DefaultBase), Available: true}
	}
	if av.Code == names.CodeNameTaken || av.Code == names.CodeNameHeld {
		av.Suggestions = suggestNames(ctx, c, name)
	}
	a.addr.mu.Lock()
	if len(a.addr.avail) > 64 {
		clear(a.addr.avail)
	}
	a.addr.avail[name] = availEntry{a: av, at: now}
	a.addr.mu.Unlock()
	return av, nil
}

// suggestNames checks a few variations of a taken name and returns up to
// three that are free.
func suggestNames(ctx context.Context, c *names.Client, name string) []string {
	var candidates []string
	for _, s := range []string{name + "-mc", name + "craft", name + "-plays", name + "-server"} {
		if names.CheckName(s) == nil {
			candidates = append(candidates, s)
		}
	}
	free := make([]bool, len(candidates))
	var wg sync.WaitGroup
	for i, s := range candidates {
		wg.Go(func() {
			av, err := c.Available(ctx, s)
			free[i] = err == nil && av.Available
		})
	}
	wg.Wait()
	var out []string
	for i, s := range candidates {
		if free[i] && len(out) < 3 {
			out = append(out, s)
		}
	}
	return out
}

// claimFree claims name for this machine. A key holds one name at a time,
// so changing names releases the old one first, and claims it back when
// the new one can't be had or this machine can't save the change. A
// release or claim that got no answer, or one that doesn't say it failed,
// may have been carried out all the same: after a release the old name is
// claimed back, and after a claim the service's list of this key's names
// says which name the machine has; when it can't, a first name is released
// again. A name the service says the key holds, and the machine doesn't
// know of, becomes the machine's name (see followService).
func (a *Agent) claimFree(ctx context.Context, st addressState, name, actor string) error {
	c, err := a.namesClient(true)
	if err != nil {
		return err
	}
	old := ""
	if st.Kind == api.AddressPlaykeeper && st.Free != nil {
		old = st.Free.Name.Name
	}
	if old == name {
		return nil
	}
	if old != "" {
		// A name the service doesn't hold for this key needs no release.
		if _, err := c.Release(ctx); err != nil && !namesCode(err, names.CodeNotClaimed) && !namesCode(err, names.CodeNotYourName) {
			if maybeStored(err) {
				rctx, cancel := context.WithTimeout(a.ctx, settleWait)
				defer cancel()
				a.reclaim(rctx, c, old, actor)
			}
			return a.namesError(err)
		}
	}
	a.setClaiming(name)
	n, err := c.Claim(ctx, name)
	a.setClaiming("")
	if err != nil {
		sctx, cancel := context.WithTimeout(a.ctx, settleWait)
		defer cancel()
		unsure := false
		if maybeStored(err) {
			l, listed, lerr := listedName(sctx, c, name)
			if listed && l.State != names.StateReleased {
				n, err = l, nil
			}
			unsure = lerr != nil
		}
		if err != nil {
			switch {
			case old != "":
				if a.reclaim(sctx, c, old, actor) == name {
					// The service holds the new name for this key after all.
					return nil
				}
			case unsure:
				// Nothing says whether the claim was stored: give name up,
				// so that the key holds no name the machine doesn't use.
				c.Name = name
				if _, rerr := c.Release(sctx); rerr != nil && !namesCode(rerr, names.CodeNotClaimed) {
					a.log.Warn("could not release a free address whose claim got no clear answer", "name", name, "err", rerr)
				}
			case namesCode(err, names.CodeLimitReached):
				// The key holds a name the machine doesn't know of, left by
				// a claim that got no clear answer or couldn't be saved,
				// and couldn't be undone.
				if held, ok := a.followService(sctx, c, "", actor, err); ok {
					a.useFree(sctx, c, held)
				}
			}
			return a.namesError(err)
		}
	}
	a.noteNames("")
	now := a.now().UTC()
	host := names.Address(name, names.DefaultBase)
	released := st.Released
	if old != "" {
		released = old
	}
	if released == name {
		released = ""
	}
	if err := a.setAddress(addressState{Kind: api.AddressPlaykeeper, Host: host, Since: now, IP: st.IP, Free: &freeState{Name: n, CheckedAt: now}, Released: released}); err != nil {
		uctx, cancel := context.WithTimeout(a.ctx, settleWait)
		defer cancel()
		c.Name = name
		if _, rerr := c.Release(uctx); rerr != nil && !namesCode(rerr, names.CodeNotClaimed) {
			a.log.Warn("could not release a free address this machine couldn't save", "name", name, "err", rerr)
		} else if old != "" {
			a.reclaim(uctx, c, old, actor)
		}
		return err
	}
	if old != "" {
		a.forgetCertificate(names.Address(old, names.DefaultBase))
	}
	a.audit(actor, "address.claim", host, "succeeded", "")
	return nil
}

// reclaim takes back old, the name released for a change that failed, and
// returns the machine's name afterwards. When old can't be claimed back,
// the machine follows the names service (see followService).
func (a *Agent) reclaim(ctx context.Context, c *names.Client, old, actor string) string {
	n, err := c.Claim(ctx, old)
	if err != nil {
		a.log.Warn("could not claim the previous free address back", "name", old, "err", err)
		var held bool
		if n, held = a.followService(ctx, c, old, actor, err); !held {
			return n.Name
		}
	}
	a.useFree(ctx, c, n)
	return n.Name
}

// useFree points n, the name the names service holds for this key and the
// machine's name now, at the machine, and has the address loop give the
// servers their records under it.
func (a *Agent) useFree(ctx context.Context, c *names.Client, n names.Name) {
	c.Name = n.Name
	var next time.Time
	if r, err := c.Refresh(ctx); err == nil {
		n, next = r, a.now().UTC().Add(freeRefreshEvery)
	}
	a.saveFree(n, next)
	// Releasing a name removes its servers' records, and a name the
	// machine didn't use has none of them: the loop puts them in.
	a.serversChanged()
}

// followService makes the machine's free name follow the names service,
// after a step for old, the machine's name ("" for none), failed with err.
// The service's list of this key's names decides:
//   - a name the key holds (a key holds one at a time) is the machine's
//     name, old or not; a new one comes with its own records and
//     certificate, and old's certificate goes;
//   - while the key holds none, the machine gives old up once the service
//     has said, with err, that another key has it and doesn't list it for
//     this key any more; until then it keeps old, and the loop tries it
//     again within the hour.
//
// n is the machine's name afterwards ("" for none); held reports whether
// the service holds it for this key, n then being its description.
func (a *Agent) followService(ctx context.Context, c *names.Client, old, actor string, err error) (n names.Name, held bool) {
	list, lerr := c.Names(ctx)
	if lerr != nil {
		_ = a.namesError(lerr)
		a.retryFree(old)
		return names.Name{Name: old}, false
	}
	a.noteNames("")
	listed := false
	for _, l := range list {
		if l.State != names.StateReleased {
			if l.Name != old && !a.adoptFree(l, old, actor) {
				return names.Name{Name: old}, false
			}
			return l, true
		}
		listed = listed || l.Name == old
	}
	if listed || !takenElsewhere(err) {
		a.retryFree(old)
		return names.Name{Name: old}, false
	}
	st := a.address()
	_ = a.setAddress(addressState{IP: st.IP, Released: old})
	a.forgetCertificate(names.Address(old, names.DefaultBase))
	return names.Name{}, false
}

// adoptFree makes n, a name the names service holds for this key, the
// machine's name instead of old. The address loop refreshes it, gives the
// servers their records under it and gets its certificate.
func (a *Agent) adoptFree(n names.Name, old, actor string) bool {
	was := a.address()
	now := a.now().UTC()
	host := names.Address(n.Name, names.DefaultBase)
	released := old
	if released == "" {
		released = was.Released
	}
	if err := a.setAddress(addressState{Kind: api.AddressPlaykeeper, Host: host, Since: now, IP: was.IP, Free: &freeState{Name: n, CheckedAt: now}, Released: released}); err != nil {
		a.log.Warn("could not save the free address the names service holds for this machine", "name", n.Name, "err", err)
		return false
	}
	a.forgetCertificate(was.Host)
	a.log.Warn("the free address service holds another name for this machine; using it", "name", n.Name, "was", old)
	a.audit(actor, "address.claim", host, "succeeded", "")
	a.serversChanged()
	return true
}

// takenElsewhere reports whether err, the names service's answer to a
// request for a name, says another key has the name.
func takenElsewhere(err error) bool {
	return namesCode(err, names.CodeNameTaken) || namesCode(err, names.CodeNameHeld) || namesCode(err, names.CodeNotYourName)
}

// retryFree has the address loop refresh name, the machine's free name,
// again within the hour.
func (a *Agent) retryFree(name string) {
	retry := a.now().UTC().Add(freeRetryEvery)
	_ = a.updateAddress(func(st *addressState) {
		if st.Free != nil && st.Free.Name.Name == name {
			f := *st.Free
			f.NextRefresh = retry
			st.Free = &f
		}
	})
}

// saveFree records the names service's answer about the machine's name;
// next, when set, is when to refresh it again.
func (a *Agent) saveFree(n names.Name, next time.Time) {
	now := a.now().UTC()
	_ = a.updateAddress(func(st *addressState) {
		if st.Kind != api.AddressPlaykeeper || st.Free == nil || st.Free.Name.Name != n.Name {
			return
		}
		f := *st.Free
		f.Name, f.CheckedAt = n, now
		if !next.IsZero() {
			f.NextRefresh = next
		}
		st.Free = &f
	})
}

// refreshFree points the free name at this machine's current addresses,
// which also keeps it from lapsing, and claims it back when the names
// service has released it. A refresh the service answers says the key
// holds the name; when it doesn't work out, the service's list of this
// key's names says which name the machine has (see followService).
func (a *Agent) refreshFree(ctx context.Context) error {
	c, err := a.namesClient(true)
	if err != nil {
		return err
	}
	if c.Name == "" {
		return errConflict("This machine has no free address.", "Pick a name first.")
	}
	n, err := c.Refresh(ctx)
	if namesCode(err, names.CodeNotClaimed) {
		// Released for a change that failed, or given back to everyone
		// after two months without a refresh: claim it again while
		// nobody else has.
		if _, err = c.Claim(ctx, c.Name); err == nil {
			n, err = c.Refresh(ctx)
		}
	}
	if err != nil {
		if held, ok := a.followService(ctx, c, c.Name, "playkeeper", err); ok {
			c.Name = held.Name
			n, err = c.Refresh(ctx)
		}
	}
	if err != nil {
		a.retryFree(c.Name)
		return a.namesError(err)
	}
	a.noteNames("")
	a.saveFree(n, a.now().UTC().Add(freeRefreshEvery))
	return nil
}

// freeServers are the servers that get an address under the free name:
// the first namesMaxServers in display order, each labelled with its slug.
func freeServers(servers []joinServer) []joinServer {
	var out []joinServer
	for _, s := range servers {
		if len(out) < namesMaxServers && names.CheckServerLabel(s.slug) == nil {
			out = append(out, s)
		}
	}
	return out
}

// syncFreeServers makes the free name's server records match the servers:
// records of removed servers go first, since a name has a limited number.
// It returns the servers that should have a record now, as this sync saw
// them.
func (a *Agent) syncFreeServers(ctx context.Context) ([]joinServer, error) {
	st := a.address()
	if st.Kind != api.AddressPlaykeeper || st.Free == nil {
		return nil, nil
	}
	// This sync covers every server read below, so a change the loop hasn't
	// seen yet (it may have found the address busy with the claim) must not
	// make it ask again, before the names service allows server addresses.
	a.takeServersChanged()
	want := freeServers(a.joinServers())
	have := st.Free.Name.Servers
	var remove, set []joinServer
	for _, sv := range have {
		if !slices.ContainsFunc(want, func(s joinServer) bool { return s.slug == sv.Label }) {
			remove = append(remove, joinServer{slug: sv.Label})
		}
	}
	for _, s := range want {
		if !slices.ContainsFunc(have, func(sv names.Server) bool { return sv.Label == s.slug && sv.Port == s.port }) {
			set = append(set, s)
		}
	}
	if len(remove) == 0 && len(set) == 0 {
		a.saveServersSync("", time.Time{}, false)
		return want, nil
	}
	c, err := a.namesClient(true)
	if err != nil {
		a.saveServersSync("", time.Time{}, true)
		return want, err
	}
	var errs []error
	for _, s := range remove {
		if err := c.RemoveServer(ctx, s.slug); err != nil && !namesCode(err, names.CodeNotClaimed) {
			errs = append(errs, err)
		}
	}
	wait, from := "", time.Time{}
	for _, s := range set {
		_, err := c.SetServer(ctx, s.slug, s.port)
		var ne *names.Error
		if errors.As(err, &ne) && (ne.Code == names.CodeServerNotYet || ne.Code == names.CodeNotAnswering) {
			// The name gets no server addresses at all yet, so the
			// other servers would be refused too.
			wait = ne.Code
			if sec, ok := ne.Params["from"].(float64); ok {
				from = time.Unix(int64(sec), 0).UTC()
			}
			break
		}
		if err != nil {
			errs = append(errs, err)
		}
	}
	a.saveServersSync(wait, from, len(errs) > 0)
	a.pollFree(ctx, c)
	if len(errs) > 0 {
		return want, a.namesError(errs[0])
	}
	return want, nil
}

// saveServersSync records how an update of the servers' records went: why
// the names service gives the servers no address yet (wait, with from),
// whether it failed otherwise, or that it all worked. The loop tries again
// when the service allows after a wait, and soon after a failure, less
// often while failures go on; a failure says nothing new about a wait
// recorded before, whose time it doesn't bring forward.
func (a *Agent) saveServersSync(wait string, from time.Time, failed bool) {
	now := a.now().UTC()
	_ = a.updateAddress(func(st *addressState) {
		if st.Kind != api.AddressPlaykeeper || st.Free == nil || (wait == "" && !failed && st.Free.ServersWait == "" && st.Free.ServersFailed == 0) {
			return
		}
		f := *st.Free
		switch {
		case wait == names.CodeServerNotYet && from.After(now):
			f.ServersWait, f.ServersFrom, f.ServersRetry = wait, from, from
		case wait != "":
			f.ServersWait, f.ServersFrom, f.ServersRetry = wait, from, now.Add(freeRetryEvery)
		case !failed:
			f.ServersWait, f.ServersFrom, f.ServersRetry = "", time.Time{}, time.Time{}
		}
		f.ServersFailed = 0
		if failed {
			f.ServersFailed = st.Free.ServersFailed + 1
			if retry := now.Add(serversRetryAfter(f.ServersFailed)); retry.After(f.ServersRetry) {
				f.ServersRetry = retry
			}
		}
		st.Free = &f
	})
}

// serversRetryAfter is how long the loop waits to update the servers'
// records again after failed updates in a row: freePollEvery at first,
// twice as long after each further one, and at most freeRetryEvery.
func serversRetryAfter(failed int) time.Duration {
	d := freePollEvery
	for range failed - 1 {
		d *= 2
		if d >= freeRetryEvery {
			return freeRetryEvery
		}
	}
	return d
}

// pollFree asks the names service how the machine's name is doing.
func (a *Agent) pollFree(ctx context.Context, c *names.Client) {
	a.addr.mu.Lock()
	a.addr.lastPoll = a.now()
	a.addr.mu.Unlock()
	list, err := c.Names(ctx)
	if err != nil {
		_ = a.namesError(err)
		return
	}
	a.noteNames("")
	for _, n := range list {
		if n.Name == c.Name {
			a.saveFree(n, time.Time{})
		}
	}
}

// freePublished reports whether the free name's records and those of the
// servers that should have one are all published. Records the names
// service refused, or that couldn't be given, are not waited for: the loop
// asks for them again later.
func freePublished(st addressState, servers []joinServer) bool {
	if st.Free == nil || st.Free.Name.State != names.StateActive || st.Free.Name.DNS != names.DNSOK {
		return false
	}
	for _, s := range freeServers(servers) {
		if !st.Free.serverPublished(s) && st.Free.ServersWait == "" && st.Free.ServersFailed == 0 {
			return false
		}
	}
	return true
}

func (f *freeState) serverPublished(s joinServer) bool {
	return slices.ContainsFunc(f.Name.Servers, func(sv names.Server) bool {
		return sv.Label == s.slug && sv.Port == s.port && sv.DNS == names.DNSOK
	})
}

// publishFree points the free name at this machine, gives the servers their
// addresses, gets the dashboard's certificate and then waits, a while, for
// the records to be published. Servers added meanwhile are not waited for:
// the loop gives them their records afterwards.
func (a *Agent) publishFree(ctx context.Context, h *opHandle) error {
	h.phase("pointing")
	if err := a.refreshFree(ctx); err != nil {
		return err
	}
	synced, err := a.syncFreeServers(ctx)
	if err != nil {
		a.log.Warn("could not give every server its free address", "err", err)
	}
	h.phase("certificate")
	if a.certificateDue(a.address()) {
		// A failure is kept with the certificate, shown on its own, and
		// retried later; the addresses work without it.
		if err := a.issueCertificate(ctx, h); err != nil {
			a.log.Warn("could not get the dashboard's certificate", "err", err)
		}
	}
	h.phase("publishing")
	c, err := a.namesClient(true)
	if err != nil {
		return err
	}
	start := time.Now()
	for !freePublished(a.address(), synced) && time.Since(start) < publishWait {
		t := time.NewTimer(a.opts.PublishPoll)
		select {
		case <-ctx.Done():
			t.Stop()
			return ctx.Err()
		case <-t.C:
		}
		a.pollFree(ctx, c)
	}
	return nil
}

// releaseFree gives the free name up.
func (a *Agent) releaseFree(ctx context.Context, st addressState, actor string) error {
	c, err := a.namesClient(true)
	if err != nil {
		return err
	}
	if _, err := c.Release(ctx); err != nil && !namesCode(err, names.CodeNotClaimed) {
		return a.namesError(err)
	}
	a.noteNames("")
	if err := a.setAddress(addressState{IP: st.IP, Released: c.Name}); err != nil {
		return err
	}
	a.forgetCertificate(st.Host)
	a.audit(actor, "address.release", st.Host, "succeeded", "")
	return nil
}

// Own domain.

// ownPlan is the own domain's plan: the server on port 25565 is reached as
// the domain itself, every other one as <slug>.<domain> through an SRV
// record.
func ownPlan(host string, servers []joinServer, ip netip.Addr) certs.Plan {
	p := certs.Plan{Name: host}
	switch {
	case ip.Is4():
		p.IPv4 = ip
	case ip.Is6():
		p.IPv6 = ip
	}
	for _, s := range servers {
		js := certs.JoinServer{ID: s.id, Port: s.port}
		if s.port != certs.MinecraftPort {
			js.Host = s.slug + "." + host
		}
		p.Servers = append(p.Servers, js)
	}
	return p
}

// checkOwn looks up the own domain's records as players and Let's Encrypt
// see them, and keeps the result.
func (a *Agent) checkOwn(ctx context.Context) (*api.AddressCheck, error) {
	st := a.address()
	if st.Kind != api.AddressOwn {
		return nil, nil
	}
	pc, err := certs.CheckPlan(ctx, a.opts.Resolver, ownPlan(st.Host, a.joinServers(), a.machineIP(st)), a.expectedAddrs(st))
	if err != nil {
		return nil, problemError(err, http.StatusBadRequest)
	}
	check := &api.AddressCheck{At: a.now().UTC(), Name: nameCheck(pc.Name), Ready: pc.Ready}
	for _, rc := range pc.Records {
		check.Records = append(check.Records, api.RecordCheck{Note: api.Note(rc.Note), Record: dnsRecord(rc.Record), OK: rc.OK, Found: rc.Found})
	}
	a.saveCheck(st.Host, check)
	return check, nil
}

func (a *Agent) saveCheck(host string, check *api.AddressCheck) {
	_ = a.updateAddress(func(st *addressState) {
		if st.Kind == api.AddressOwn && st.Host == host {
			st.Check = check
		}
	})
	next := ownRecheckPending
	if check.Ready {
		next = ownRecheckReady
	}
	a.addr.mu.Lock()
	a.addr.recheck = a.now().Add(next)
	a.addr.mu.Unlock()
}

func nameCheck(nc certs.NameCheck) api.NameCheck {
	out := api.NameCheck{Note: api.Note(nc.Note), Name: nc.Name, OK: nc.OK}
	for _, r := range nc.Records {
		out.Records = append(out.Records, api.AddrRecord{Type: r.Type, Addr: r.Addr.String(), Here: r.Here, Kind: r.Kind})
	}
	return out
}

func dnsRecord(r certs.Record) api.DNSRecord {
	out := api.DNSRecord{ServerID: r.ServerID, Type: r.Type, Name: r.Name, Value: r.Value, TTL: r.TTL}
	if r.SRV != nil {
		p := api.SRVParts(*r.SRV)
		out.SRV = &p
	}
	return out
}

// problemError is the answer for an error from the certs package.
func problemError(err error, status int) error {
	var p *certs.Problem
	if !errors.As(err, &p) {
		return err
	}
	return &apiError{Status: status, Code: p.Code, Msg: p.Message, Hint: p.Hint, Params: noteParams(p.Params)}
}

func noteParams(p map[string]string) map[string]any {
	if len(p) == 0 {
		return nil
	}
	out := make(map[string]any, len(p))
	for k, v := range p {
		out[k] = v
	}
	return out
}

// Addresses and where they point.

// joinServer is a server with what its join address needs.
type joinServer struct {
	id, name, slug string
	port           int
}

// joinServers lists the servers in display order.
func (a *Agent) joinServers() []joinServer {
	rows, err := a.db.Query(`SELECT id, name, slug, game_port FROM servers ORDER BY position, created_at, id`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []joinServer
	for rows.Next() {
		var s joinServer
		if rows.Scan(&s.id, &s.name, &s.slug, &s.port) == nil {
			out = append(out, s)
		}
	}
	return out
}

// joinAddresses is what players type for each server with the address st.
func (a *Agent) joinAddresses(st addressState, servers []joinServer) []api.JoinAddress {
	ip := a.machineIP(st)
	free := freeServers(servers)
	out := make([]api.JoinAddress, 0, len(servers))
	for _, s := range servers {
		j := api.JoinAddress{ServerID: s.id, Name: s.name, Port: s.port}
		if ip.IsValid() {
			j.Direct = hostPort(ip.String(), s.port)
		}
		switch st.Kind {
		case api.AddressPlaykeeper:
			if st.Free == nil || !slices.Contains(free, s) {
				break
			}
			j.Label = s.slug
			j.Address = names.ServerAddress(s.slug, st.Free.Name.Name, names.DefaultBase)
			j.Published = st.Free.Name.State == names.StateActive && !freeLapsed(st.Free.Name, a.now()) && st.Free.serverPublished(s)
		case api.AddressOwn:
			// The server on 25565 is the domain itself and needs no SRV
			// record; the check lists one for it only when one points
			// elsewhere.
			bare := s.port == certs.MinecraftPort
			if bare {
				j.Address = st.Host
			} else {
				j.Label = s.slug
				j.Address = s.slug + "." + st.Host
			}
			j.Published = st.Check != nil && st.Check.Name.OK && srvOK(st.Check, s.id, bare)
		}
		out = append(out, j)
	}
	return out
}

// srvOK reports whether the check found the server's SRV record right, or,
// with absentOK, found none that is wrong.
func srvOK(c *api.AddressCheck, serverID string, absentOK bool) bool {
	for _, rc := range c.Records {
		if rc.Record.ServerID == serverID {
			return rc.OK
		}
	}
	return absentOK
}

// joinAddress is the server's friendly address once its records work.
func (s *server) joinAddress() string {
	st := s.address()
	if st.Kind == api.AddressNone {
		return ""
	}
	for _, j := range s.joinAddresses(st, s.joinServers()) {
		if j.ServerID == s.id && j.Published {
			return j.Address
		}
	}
	return ""
}

func hostPort(host string, port int) string {
	if port == certs.MinecraftPort {
		if strings.Contains(host, ":") {
			return "[" + host + "]"
		}
		return host
	}
	return net.JoinHostPort(host, strconv.Itoa(port))
}

// freeLapsed reports whether an active name has passed its refresh-by time,
// which the service lapses it for.
func freeLapsed(n names.Name, now time.Time) bool {
	return n.State == names.StateActive && !n.RefreshBy.IsZero() && !now.Before(n.RefreshBy)
}

// publicIP is the public IP address in a host or host:port, if it is one.
func publicIP(hostport string) (netip.Addr, bool) {
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

// notePanelHost remembers the public IP address the dashboard was opened
// with: behind NAT it is on no network interface, and it is what an own
// domain's A record needs.
func (a *Agent) notePanelHost(host string) {
	ip, ok := publicIP(host)
	if !ok || a.address().IP == ip.String() {
		return
	}
	_ = a.updateAddress(func(st *addressState) { st.IP = ip.String() })
}

// machineIP is this machine's public address as far as Playkeeper can tell:
// the one the dashboard was opened with, the one the names service saw, or
// a network interface's (IPv4 first).
func (a *Agent) machineIP(st addressState) netip.Addr {
	if ip, err := netip.ParseAddr(st.IP); err == nil {
		return ip
	}
	if st.Free != nil {
		if ip, err := netip.ParseAddr(st.Free.Name.IPv4); err == nil {
			return ip
		}
	}
	addrs := a.opts.PublicAddrs()
	for _, ip := range addrs {
		if ip.Is4() {
			return ip
		}
	}
	if len(addrs) > 0 {
		return addrs[0]
	}
	return netip.Addr{}
}

// expectedAddrs is where the own domain should point: this machine's
// public addresses.
func (a *Agent) expectedAddrs(st addressState) []netip.Addr {
	out := a.opts.PublicAddrs()
	if ip, err := netip.ParseAddr(st.IP); err == nil {
		out = append(out, ip)
	}
	return out
}

// addressViewWith is addressView answering a request that started op: a
// quick operation may have finished before the answer is made, and the
// dashboard follows op by its id either way.
func (a *Agent) addressViewWith(op *api.Operation) api.Address {
	v := a.addressView()
	if v.Operation == nil {
		v.Operation = op
	}
	return v
}

// addressView is the address as the dashboard shows it.
func (a *Agent) addressView() api.Address {
	st := a.address()
	servers := a.joinServers()
	v := api.Address{Kind: st.Kind, Host: st.Host, PanelPort: a.cfg.PanelPort, Base: names.DefaultBase,
		Servers: a.joinAddresses(st, servers), Operation: a.addressOp(), TermsAccepted: a.termsAccepted()}
	if ip := a.machineIP(st); ip.IsValid() {
		v.IP = ip.String()
	}
	if !st.Since.IsZero() {
		t := st.Since
		v.Since = &t
	}
	a.addr.mu.Lock()
	v.Names = api.NamesService{URL: nonEmptyOr(a.cfg.NamesURL, names.DefaultServiceURL), Unreachable: a.addr.namesErr != "", Error: a.addr.namesErr}
	if !a.addr.namesAt.IsZero() {
		t := a.addr.namesAt
		v.Names.CheckedAt = &t
	}
	a.addr.mu.Unlock()
	switch st.Kind {
	case api.AddressPlaykeeper:
		if st.Free != nil {
			v.Free = freeView(st.Free, a.now())
		}
	case api.AddressOwn:
		if recs, err := ownPlan(st.Host, servers, a.machineIP(st)).Records(); err == nil {
			for _, r := range recs {
				v.Records = append(v.Records, dnsRecord(r))
			}
		}
		v.Check = st.Check
	}
	if st.Host != "" {
		if row := a.loadCertificate(st.Host); row != nil {
			v.Certificate = certificateView(row)
		}
	}
	return v
}

func freeView(f *freeState, now time.Time) *api.FreeAddress {
	n := f.Name
	v := &api.FreeAddress{Name: n.Name, State: n.State, DNS: n.DNS, IPv4: n.IPv4, IPv6: n.IPv6, ClaimedAt: n.ClaimedAt,
		RefreshedAt: n.RefreshedAt, CheckedAt: f.CheckedAt, HoldDays: int(namesReleaseHold / (24 * time.Hour)), ServersWait: f.ServersWait}
	if !f.ServersFrom.IsZero() {
		t := f.ServersFrom
		v.ServersFrom = &t
	}
	switch {
	case n.State == names.StateLapsed && n.LapseReason == names.LapseNoAnswer:
		v.LapseReason = n.LapseReason
		t := n.ClaimedAt
		if n.AnsweredAt.After(t) {
			t = n.AnsweredAt
		}
		t = t.Add(namesUnansweredAfter)
		v.StoppedAt = &t
	case n.State == names.StateLapsed:
		v.LapseReason = names.LapseNotRefreshed
		t := n.RefreshedAt.Add(namesLapseAfter)
		v.StoppedAt = &t
	case freeLapsed(n, now):
		v.State, v.LapseReason = names.StateLapsed, names.LapseNotRefreshed
		t := n.RefreshBy
		v.StoppedAt = &t
	}
	return v
}

// Handlers.

func (a *Agent) hAddress(w http.ResponseWriter, r *http.Request) {
	a.notePanelHost(r.URL.Query().Get("panelHost"))
	writeJSON(w, http.StatusOK, a.addressView())
}

// hAddressAlive answers the names service's liveness check, which the panel
// passes on from the internet with the check's Host header in host.
func (a *Agent) hAddressAlive(w http.ResponseWriter, r *http.Request) {
	r = r.Clone(r.Context())
	r.Host = r.URL.Query().Get("host")
	names.AliveHandler(names.DefaultBase, a.aliveKey).ServeHTTP(w, r)
}

// aliveKey is the machine's names key if the machine holds name or is
// claiming it, else nil.
func (a *Agent) aliveKey(name string) ed25519.PrivateKey {
	st := a.address()
	a.addr.mu.Lock()
	claiming := a.addr.claiming
	a.addr.mu.Unlock()
	held := st.Kind == api.AddressPlaykeeper && st.Free != nil && st.Free.Name.Name == name && st.Free.Name.State != names.StateReleased
	if !held && (claiming == "" || claiming != name) {
		return nil
	}
	key, err := names.LoadOrCreateKey(filepath.Join(a.cfg.AgentDir(), "names.key"))
	if err != nil {
		a.log.Warn("could not read the key for the free address service's liveness check", "err", err)
		return nil
	}
	return key
}

func (a *Agent) setClaiming(name string) {
	a.addr.mu.Lock()
	a.addr.claiming = name
	a.addr.mu.Unlock()
}

func (a *Agent) hAddressAvailable(w http.ResponseWriter, r *http.Request) {
	name := names.NormalizeName(r.URL.Query().Get("name"), names.DefaultBase)
	av, err := a.availability(r.Context(), name)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, av)
}

func (a *Agent) hAddressClaim(w http.ResponseWriter, r *http.Request) {
	var req api.AddressClaimRequest
	if err := decode(r, &req); err != nil {
		writeError(w, err)
		return
	}
	actor, err := validActor(req.Actor)
	if err != nil {
		writeError(w, err)
		return
	}
	name := names.NormalizeName(req.Name, names.DefaultBase)
	if err := names.CheckName(name); err != nil {
		writeError(w, a.namesError(err))
		return
	}
	release, err := a.holdAddress(r.Context(), 20*time.Second)
	if err != nil {
		writeError(w, err)
		return
	}
	a.notePanelHost(req.PanelHost)
	if req.AcceptTerms {
		a.acceptTerms(actor)
	}
	st := a.address()
	if st.Kind == api.AddressOwn {
		release()
		writeError(w, errConflict("This machine uses its own domain.", "Stop using it first, then pick a free address."))
		return
	}
	ctx, cancel := detached(a.ctx)
	defer cancel()
	if err := a.claimFree(ctx, st, name, actor); err != nil {
		release()
		a.audit(actor, "address.claim", names.Address(name, names.DefaultBase), "failed", err.Error())
		writeError(w, err)
		return
	}
	op := a.startAddressOp("address.publish", actor, a.publishFree)
	writeJSON(w, http.StatusOK, a.addressViewWith(op))
}

func (a *Agent) hAddressRefresh(w http.ResponseWriter, r *http.Request) {
	a.addressAction(w, r, func(_ context.Context, st addressState, _ string) (string, error) {
		if st.Kind != api.AddressPlaykeeper {
			return "", errConflict("This machine has no free address.", "Pick a name first.")
		}
		return "address.publish", nil
	})
}

func (a *Agent) hAddressCertificate(w http.ResponseWriter, r *http.Request) {
	a.addressAction(w, r, func(_ context.Context, st addressState, _ string) (string, error) {
		if st.Kind == api.AddressNone {
			return "", errConflict("This machine has no address to get a certificate for.", "Pick a free address or add your own domain first.")
		}
		if row := a.loadCertificate(st.Host); row != nil && row.status.Problem != nil && row.status.Problem.RetryAt.After(a.now()) {
			until := row.status.Problem.RetryAt.UTC()
			who := "Let's Encrypt refuses"
			if row.status.Problem.Code == certs.CodeCertificateLimit {
				who = "The free address service refuses"
			}
			return "", &apiError{Status: http.StatusConflict, Code: api.CodeRetryLater, Msg: who + " new attempts until " + until.Format("2006-01-02 15:04 UTC") + ".",
				Hint: "Playkeeper tries again by itself then.", Params: map[string]any{"retryAt": until.Format(time.RFC3339)}}
		}
		return "certificate.issue", nil
	})
}

func (a *Agent) hAddressRelease(w http.ResponseWriter, r *http.Request) {
	a.addressAction(w, r, func(ctx context.Context, st addressState, actor string) (string, error) {
		if st.Kind != api.AddressPlaykeeper {
			return "", errConflict("This machine has no free address to release.", "")
		}
		return "", a.releaseFree(ctx, st, actor)
	})
}

// detached is a context for a change at the names service that has to
// finish even if the dashboard stops waiting for it.
func detached(parent context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(parent, 90*time.Second)
}

// addressAction runs a request with a body of AddressActionRequest: fn
// checks the state and does its work, and returns the kind of operation to
// start, if any.
func (a *Agent) addressAction(w http.ResponseWriter, r *http.Request, fn func(ctx context.Context, st addressState, actor string) (string, error)) {
	var req api.AddressActionRequest
	if err := decode(r, &req); err != nil {
		writeError(w, err)
		return
	}
	actor, err := validActor(req.Actor)
	if err != nil {
		writeError(w, err)
		return
	}
	release, err := a.holdAddress(r.Context(), 20*time.Second)
	if err != nil {
		writeError(w, err)
		return
	}
	a.notePanelHost(req.PanelHost)
	if req.AcceptTerms {
		a.acceptTerms(actor)
	}
	ctx, cancel := detached(a.ctx)
	defer cancel()
	kind, err := fn(ctx, a.address(), actor)
	var op *api.Operation
	switch {
	case err != nil:
		release()
		writeError(w, err)
		return
	case kind == "certificate.issue":
		op = a.startAddressOp(kind, actor, a.issueCertificate)
	case kind == "address.publish":
		op = a.startAddressOp(kind, actor, a.publishFree)
	default:
		release()
	}
	writeJSON(w, http.StatusOK, a.addressViewWith(op))
}

func (a *Agent) hAddressPlan(w http.ResponseWriter, r *http.Request) {
	a.notePanelHost(r.URL.Query().Get("panelHost"))
	domain, err := ownDomain(r.URL.Query().Get("domain"))
	if err != nil {
		writeError(w, err)
		return
	}
	st := a.address()
	servers := a.joinServers()
	recs, err := ownPlan(domain, servers, a.machineIP(st)).Records()
	if err != nil {
		writeError(w, problemError(err, http.StatusBadRequest))
		return
	}
	plan := api.AddressPlan{Domain: domain, Records: []api.DNSRecord{}, Servers: a.joinAddresses(addressState{Kind: api.AddressOwn, Host: domain, IP: st.IP}, servers)}
	for _, rec := range recs {
		plan.Records = append(plan.Records, dnsRecord(rec))
	}
	writeJSON(w, http.StatusOK, plan)
}

// ownDomain checks a domain for use as the machine's own address.
func ownDomain(raw string) (string, error) {
	domain, err := certs.NormalizeName(raw)
	if err != nil {
		return "", problemError(err, http.StatusBadRequest)
	}
	if domain == names.DefaultBase || strings.HasSuffix(domain, "."+names.DefaultBase) {
		return "", &apiError{Status: http.StatusBadRequest, Code: api.CodeInvalid, Msg: "Names under " + names.DefaultBase + " are free addresses.", Hint: "Pick one with the free address instead."}
	}
	return domain, nil
}

func (a *Agent) hAddressCheck(w http.ResponseWriter, r *http.Request) {
	var req api.AddressCheckRequest
	if err := decode(r, &req); err != nil {
		writeError(w, err)
		return
	}
	actor, err := validActor(req.Actor)
	if err != nil {
		writeError(w, err)
		return
	}
	domain, err := ownDomain(req.Domain)
	if err != nil {
		writeError(w, err)
		return
	}
	release, err := a.holdAddress(r.Context(), 20*time.Second)
	if err != nil {
		writeError(w, err)
		return
	}
	handed := false
	defer func() {
		if !handed {
			release()
		}
	}()
	a.notePanelHost(req.PanelHost)
	if req.AcceptTerms {
		a.acceptTerms(actor)
	}
	st := a.address()
	switch {
	case st.Kind == api.AddressPlaykeeper:
		writeError(w, errConflict("This machine has a free address.", "Release it first, then use your own domain."))
		return
	case st.Kind != api.AddressOwn || st.Host != domain:
		if err := a.setAddress(addressState{Kind: api.AddressOwn, Host: domain, Since: a.now().UTC(), IP: st.IP, Released: st.Released}); err != nil {
			writeError(w, err)
			return
		}
		if st.Kind == api.AddressOwn {
			a.forgetCertificate(st.Host)
		}
		a.audit(actor, "address.domain", domain, "succeeded", "")
	}
	check, err := a.checkOwn(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	var op *api.Operation
	if check.Name.OK && a.certificateDue(a.address()) {
		handed = true
		op = a.startAddressOp("certificate.issue", actor, a.issueCertificate)
	}
	writeJSON(w, http.StatusOK, a.addressViewWith(op))
}

func (a *Agent) hAddressDelete(w http.ResponseWriter, r *http.Request) {
	actor, err := validActor(r.URL.Query().Get("actor"))
	if err != nil {
		writeError(w, err)
		return
	}
	release, err := a.holdAddress(r.Context(), 20*time.Second)
	if err != nil {
		writeError(w, err)
		return
	}
	defer release()
	st := a.address()
	if st.Kind != api.AddressOwn {
		writeError(w, errConflict("This machine doesn't use its own domain.", ""))
		return
	}
	if err := a.setAddress(addressState{IP: st.IP, Released: st.Released}); err != nil {
		writeError(w, err)
		return
	}
	a.forgetCertificate(st.Host)
	a.audit(actor, "address.remove", st.Host, "succeeded", "")
	writeJSON(w, http.StatusOK, a.addressView())
}

// Loop.

// addressLoop keeps the address working: it refreshes a free name when the
// agent starts and daily, gives new servers their records, looks at an own
// domain's records again, and gets or renews the certificate when due.
func (a *Agent) addressLoop(ctx context.Context) {
	var tick <-chan time.Time
	if a.opts.AddressInterval > 0 {
		t := time.NewTicker(a.opts.AddressInterval)
		defer t.Stop()
		tick = t.C
	}
	a.addressTick(ctx, true)
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick:
		case <-a.addr.kick:
		}
		a.addressTick(ctx, false)
	}
}

func (a *Agent) addressTick(ctx context.Context, start bool) {
	release, err := a.holdAddress(ctx, 0)
	if err != nil {
		return
	}
	handed := false
	defer func() {
		if !handed {
			release()
		}
	}()
	st := a.address()
	now := a.now()
	switch st.Kind {
	case api.AddressPlaykeeper:
		if st.Free == nil {
			return
		}
		changed := a.takeServersChanged()
		if (st.Free.ServersWait != "" || st.Free.ServersFailed > 0) && !now.Before(st.Free.ServersRetry) {
			changed = true
		}
		switch {
		case start || !now.Before(st.Free.NextRefresh):
			if err := a.refreshFree(ctx); err != nil {
				a.log.Warn("could not refresh the free address", "err", err)
				return
			}
			changed = true
		case !freePublished(st, a.joinServers()):
			a.addr.mu.Lock()
			due := now.Sub(a.addr.lastPoll) >= freePollEvery
			a.addr.mu.Unlock()
			if due && !changed {
				if c, err := a.namesClient(true); err == nil {
					a.pollFree(ctx, c)
				}
			}
		}
		if changed {
			if _, err := a.syncFreeServers(ctx); err != nil {
				a.log.Warn("could not update the servers' free addresses", "err", err)
			}
		}
		if st = a.address(); st.Free == nil || st.Free.Name.State != names.StateActive {
			return
		}
	case api.AddressOwn:
		a.addr.mu.Lock()
		due := !now.Before(a.addr.recheck)
		a.addr.mu.Unlock()
		if a.takeServersChanged() || due || st.Check == nil {
			if _, err := a.checkOwn(ctx); err != nil {
				a.log.Warn("could not check the domain's records", "err", err)
				return
			}
		}
		if st = a.address(); st.Check == nil || !st.Check.Name.OK {
			return
		}
	default:
		a.takeServersChanged()
		return
	}
	if a.certificateDue(st) {
		handed = true
		a.startAddressOp("certificate.issue", "playkeeper", a.issueCertificate)
	}
}
