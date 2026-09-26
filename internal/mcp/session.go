package mcp

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"sync"
	"time"
)

// requests tracks the requests in progress for one client, so that
// notifications/cancelled can stop them.
type requests struct {
	parent   context.Context
	mu       sync.Mutex
	inflight map[string]*inflight
}

type inflight struct {
	cancel context.CancelFunc
	// cancelled is set when the client cancelled the request, which must then
	// go unanswered.
	cancelled bool
}

func newRequests(parent context.Context) requests {
	return requests{parent: parent, inflight: map[string]*inflight{}}
}

// begin registers request id and returns its context and a function to call
// when it finishes, which reports whether the client cancelled it. ok is
// false when a request with the same id is already in progress.
func (rs *requests) begin(id json.RawMessage) (ctx context.Context, finish func() (cancelled bool), ok bool) {
	key := string(id)
	rs.mu.Lock()
	defer rs.mu.Unlock()
	if rs.inflight[key] != nil {
		return nil, nil, false
	}
	ctx, cancel := context.WithCancel(rs.parent)
	f := &inflight{cancel: cancel}
	rs.inflight[key] = f
	return ctx, func() bool {
		rs.mu.Lock()
		delete(rs.inflight, key)
		cancelled := f.cancelled
		rs.mu.Unlock()
		cancel()
		return cancelled
	}, true
}

// cancel stops request id if it is in progress. Unknown ids are ignored: the
// request may have finished already.
func (rs *requests) cancel(id json.RawMessage) bool {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	f := rs.inflight[string(id)]
	if f == nil {
		return false
	}
	f.cancelled = true
	f.cancel()
	return true
}

// session is a legacy Streamable HTTP session: the protocol revision and
// client identity that initialize negotiated, bound to the principal that
// sent it. A session ID is not a credential; every request must still carry
// a valid token for the same principal.
type session struct {
	id        string
	principal string
	version   string
	client    ClientInfo
	created   time.Time
	cancel    context.CancelFunc // ends the session's requests
	reqs      requests

	lastSeen time.Time // guarded by the store's mutex
}

// sessionStore holds the legacy sessions of one HTTP handler. Memory is
// bounded by evicting the least recently used session when a cap is reached,
// and expired sessions are removed as they are found, so no background
// goroutine is needed.
type sessionStore struct {
	mu           sync.Mutex
	byID         map[string]*session
	parent       context.Context
	now          func() time.Time
	idle, maxAge time.Duration
	max          int
	perPrincipal int
}

func newSessionStore(parent context.Context, now func() time.Time, idle, maxAge time.Duration, max, perPrincipal int) *sessionStore {
	return &sessionStore{
		byID: map[string]*session{}, parent: parent, now: now,
		idle: idle, maxAge: maxAge, max: max, perPrincipal: perPrincipal,
	}
}

// create starts a session for principal and returns it.
func (st *sessionStore) create(principal, version string, client ClientInfo) *session {
	st.mu.Lock()
	defer st.mu.Unlock()
	now := st.now()
	for _, s := range st.byID {
		if st.expired(s, now) {
			st.removeLocked(s)
		}
	}
	for st.countLocked(principal) >= st.perPrincipal {
		st.evictLocked(principal)
	}
	for len(st.byID) >= st.max {
		st.evictLocked("")
	}
	ctx, cancel := context.WithCancel(st.parent)
	s := &session{
		id: rand.Text(), principal: principal, version: version, client: client,
		created: now, lastSeen: now, cancel: cancel, reqs: newRequests(ctx),
	}
	st.byID[s.id] = s
	return s
}

// get returns the live session with the given id that belongs to principal,
// and marks it used. A session of another principal is reported as missing,
// so that session IDs reveal nothing to other tokens.
func (st *sessionStore) get(id, principal string) *session {
	st.mu.Lock()
	defer st.mu.Unlock()
	s := st.byID[id]
	if s == nil || s.principal != principal {
		return nil
	}
	now := st.now()
	if st.expired(s, now) {
		st.removeLocked(s)
		return nil
	}
	s.lastSeen = now
	return s
}

// remove ends the session with the given id if it belongs to principal.
func (st *sessionStore) remove(id, principal string) bool {
	st.mu.Lock()
	defer st.mu.Unlock()
	s := st.byID[id]
	if s == nil || s.principal != principal {
		return false
	}
	st.removeLocked(s)
	return true
}

// removePrincipal ends every session of principal and returns how many
// there were.
func (st *sessionStore) removePrincipal(principal string) int {
	st.mu.Lock()
	defer st.mu.Unlock()
	n := 0
	for _, s := range st.byID {
		if s.principal == principal {
			st.removeLocked(s)
			n++
		}
	}
	return n
}

func (st *sessionStore) closeAll() {
	st.mu.Lock()
	defer st.mu.Unlock()
	for _, s := range st.byID {
		st.removeLocked(s)
	}
}

func (st *sessionStore) size() int {
	st.mu.Lock()
	defer st.mu.Unlock()
	return len(st.byID)
}

func (st *sessionStore) expired(s *session, now time.Time) bool {
	return now.Sub(s.lastSeen) >= st.idle || now.Sub(s.created) >= st.maxAge
}

func (st *sessionStore) countLocked(principal string) int {
	n := 0
	for _, s := range st.byID {
		if s.principal == principal {
			n++
		}
	}
	return n
}

// evictLocked ends the least recently used session, of principal when it is
// not empty.
func (st *sessionStore) evictLocked(principal string) {
	var oldest *session
	for _, s := range st.byID {
		if principal != "" && s.principal != principal {
			continue
		}
		if oldest == nil || s.lastSeen.Before(oldest.lastSeen) {
			oldest = s
		}
	}
	if oldest != nil {
		st.removeLocked(oldest)
	}
}

// removeLocked deletes s and cancels the requests still running in it.
func (st *sessionStore) removeLocked(s *session) {
	delete(st.byID, s.id)
	s.cancel()
}
