package service

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"sync"
	"syscall"
	"time"

	"github.com/CIYAhq/playkeeper/internal/usage"
)

// POST /v1/reach checks whether the Minecraft port of the machine asking
// answers from the internet, which the machine can't see for itself behind
// its provider's firewall. The service connects back to the request's own
// address (clientAddr), only when that's a public one, and only on a port
// usage.ReachRequest allows: a TCP connection, then a Minecraft status
// request, of whose answer it reads only the first few bytes, so never the
// players or the server's description. The request names no address and no
// install ID, and nothing of it is kept: the address decides the limits, in
// memory, as a report's does.

// Limits, documented for the owner in services/stats/README.md.
const (
	// Checks from one IPv4 address or IPv6 /64, on top of its limit for
	// every request. A dashboard checks when a server's page is open, at
	// most hourly, and when asked to.
	reachPerIPBurst, reachPerIPHour = 5, 10
	// reachAtOnce bounds the checks running at once, everyone together.
	// One address runs one at a time.
	reachAtOnce = 64
	// maxReachBody bounds a check's body.
	maxReachBody = 64
)

// reachState is what checks share.
type reachState struct {
	perIP   *limiter
	slots   chan struct{}
	mu      sync.Mutex
	running map[string]bool
	// connect and answer bound a check's waits for the connection and for
	// the server's answer. dial makes the connection, and public says which
	// addresses it may go to; tests replace them.
	connect, answer time.Duration
	dial            func(ctx context.Context, network, address string) (net.Conn, error)
	public          func(netip.Addr) bool
}

func newReachState(now func() time.Time) reachState {
	return reachState{
		perIP: newLimiter(reachPerIPBurst, reachPerIPHour, time.Hour, now), slots: make(chan struct{}, reachAtOnce), running: map[string]bool{},
		connect: 5 * time.Second, answer: 5 * time.Second, dial: (&net.Dialer{}).DialContext, public: publicAddr,
	}
}

// start marks key's check as running, unless one is already.
func (rs *reachState) start(key string) bool {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	if rs.running[key] {
		return false
	}
	rs.running[key] = true
	return true
}

func (rs *reachState) end(key string) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	delete(rs.running, key)
}

// hReach answers a check: usage.ReachAnswer, or why there's none.
func (s *Service) hReach(w http.ResponseWriter, r *http.Request) {
	if mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mt != "application/json" {
		writeError(w, http.StatusUnsupportedMediaType, "unsupported_media_type", "A check is JSON.")
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxReachBody+1))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "The check could not be read.")
		return
	}
	if len(body) > maxReachBody {
		writeError(w, http.StatusRequestEntityTooLarge, "too_large", "The check is too large.")
		return
	}
	var req usage.ReachRequest
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil || dec.More() {
		writeError(w, http.StatusBadRequest, "invalid_request", `A check is JSON naming the port alone, like {"port": 25565}.`)
		return
	}
	if req.Check() != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", fmt.Sprintf("port is not valid: a check connects to a Minecraft server's port, %d to %d.", usage.ReachPortMin, usage.ReachPortMax))
		return
	}
	addr, _ := s.clientAddr(r)
	if !s.reach.public(addr) {
		writeError(w, http.StatusForbidden, "not_public", "A check connects back to the address it came from, and that isn't a public one.")
		return
	}
	key := addrBucket(addr)
	if !s.reach.start(key) {
		w.Header().Set("Retry-After", "10")
		writeError(w, http.StatusTooManyRequests, "busy", "A check from this address is running already.")
		return
	}
	defer s.reach.end(key)
	if ok, wait := s.reach.perIP.allow(key); !ok {
		rateLimited(w, wait)
		return
	}
	select {
	case s.reach.slots <- struct{}{}:
		defer func() { <-s.reach.slots }()
	default:
		w.Header().Set("Retry-After", "60")
		writeError(w, http.StatusServiceUnavailable, "busy", "Too many checks are running; try again in a minute.")
		return
	}
	writeJSON(w, http.StatusOK, usage.ReachAnswer{Result: s.probe(r.Context(), netip.AddrPortFrom(addr, uint16(req.Port)))})
}

// probe connects to to and asks for its Minecraft status. It sends the
// status request alone, and reads no more of the answer than shows it is one.
func (s *Service) probe(ctx context.Context, to netip.AddrPort) string {
	cctx, cancel := context.WithTimeout(ctx, s.reach.connect)
	defer cancel()
	conn, err := s.reach.dial(cctx, "tcp", to.String())
	if err != nil {
		return dialResult(err)
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(s.reach.answer)); err != nil {
		return usage.ReachNotMinecraft
	}
	if _, err := conn.Write(statusRequest(to)); err != nil || !statusAnswer(conn) {
		return usage.ReachNotMinecraft
	}
	return usage.ReachReachable
}

// dialResult is what a connection that failed with err says about the port.
func dialResult(err error) string {
	var ne net.Error
	switch {
	case errors.Is(err, syscall.ECONNREFUSED):
		return usage.ReachRefused
	case errors.Is(err, syscall.EHOSTUNREACH), errors.Is(err, syscall.ENETUNREACH):
		return usage.ReachUnreachable
	case errors.Is(err, context.DeadlineExceeded), errors.As(err, &ne) && ne.Timeout():
		return usage.ReachTimeout
	}
	return usage.ReachUnreachable
}

// statusRequest is a Java Edition handshake into the status state, naming
// to as the server, then the status request.
func statusRequest(to netip.AddrPort) []byte {
	host := to.Addr().String()
	hs := appendVarInt(nil, 0x00) // the handshake's packet ID
	hs = appendVarInt(hs, -1)     // any protocol version
	hs = appendVarInt(hs, int32(len(host)))
	hs = append(hs, host...)
	hs = binary.BigEndian.AppendUint16(hs, to.Port())
	hs = appendVarInt(hs, 1) // next state: status
	out := appendVarInt(nil, int32(len(hs)))
	out = append(out, hs...)
	return append(out, 0x01, 0x00) // the status request: one byte, packet ID 0
}

// statusAnswer reports whether r begins a status response: a packet of a
// plausible length with ID 0, holding a JSON string. It reads only those
// first bytes, one at a time.
func statusAnswer(r io.Reader) bool {
	br := byteReader{r}
	n, err := readVarInt(br)
	if err != nil || n < 4 || n > 1<<20 {
		return false
	}
	if id, err := readVarInt(br); err != nil || id != 0 {
		return false
	}
	if l, err := readVarInt(br); err != nil || l < 2 || l >= n {
		return false
	}
	b, err := br.ReadByte()
	return err == nil && b == '{'
}

type byteReader struct{ io.Reader }

func (b byteReader) ReadByte() (byte, error) {
	var p [1]byte
	_, err := io.ReadFull(b.Reader, p[:])
	return p[0], err
}

func appendVarInt(b []byte, v int32) []byte {
	u := uint32(v)
	for u&^0x7F != 0 {
		b = append(b, byte(u&0x7F|0x80))
		u >>= 7
	}
	return append(b, byte(u))
}

func readVarInt(r io.ByteReader) (int32, error) {
	var v uint32
	for i := range 5 {
		b, err := r.ReadByte()
		if err != nil {
			return 0, err
		}
		v |= uint32(b&0x7F) << (7 * i)
		if b&0x80 == 0 {
			return int32(v), nil
		}
	}
	return 0, errors.New("varint too long")
}

// publicAddr reports whether a check may connect to a: a unicast address on
// the public internet, never one on a private network, the service's own
// machine, a cloud's metadata service or carrier-grade NAT.
func publicAddr(a netip.Addr) bool {
	a = a.Unmap()
	if !a.IsGlobalUnicast() || a.IsPrivate() {
		return false
	}
	for _, p := range notPublic {
		if p.Contains(a) {
			return false
		}
	}
	return true
}

// notPublic are the special ranges IsGlobalUnicast and IsPrivate let through.
var notPublic = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"), // carrier-grade NAT
	netip.MustParsePrefix("192.0.0.0/24"),  // IETF protocol assignments
	netip.MustParsePrefix("198.18.0.0/15"), // benchmarking
	netip.MustParsePrefix("240.0.0.0/4"),   // reserved, and broadcast
	netip.MustParsePrefix("64:ff9b::/96"),  // NAT64, which leads to any IPv4 address
	netip.MustParsePrefix("64:ff9b:1::/48"),
	netip.MustParsePrefix("100::/64"),  // discard-only
	netip.MustParsePrefix("2001::/23"), // IETF protocol assignments, Teredo among them
	netip.MustParsePrefix("2002::/16"), // 6to4, which leads to any IPv4 address
}
