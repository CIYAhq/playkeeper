package agent

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/CIYAhq/playkeeper/internal/api"
	"github.com/CIYAhq/playkeeper/internal/dnszone"
)

// The zone the machine answers DNS for, as the dashboard builds it for
// port-free addresses: beta.playkeeper.me, whose parent delegates it to this
// machine, with each server's SRV record taking Java players to its machine
// and port. The machine answers on port 53 of each of its own addresses,
// over UDP and TCP, from the zone alone (internal/dnszone), and keeps the
// zone across restarts.

const kvDNSZone = "dns_zone"

const (
	// dnsTCPConns bounds the TCP connections answered at once, and dnsIdle
	// how long one may stay quiet.
	dnsTCPConns = 32
	dnsIdle     = 10 * time.Second
)

// dnsService answers the zone.
type dnsService struct {
	answerer dnszone.Answerer

	mu        sync.Mutex
	cancel    context.CancelFunc
	wg        sync.WaitGroup
	closers   []io.Closer
	listening []string
	problem   string
}

// loadDNSZone reads the zone the machine answered before it restarted.
func (a *Agent) loadDNSZone() error {
	v, ok, err := a.kvGet(kvDNSZone)
	if err != nil || !ok {
		return err
	}
	var z dnszone.Zone
	if err := json.Unmarshal([]byte(v), &z); err != nil {
		return err
	}
	a.dns.answerer.Set(z)
	return nil
}

// ownDNSAddrs are this machine's own addresses on port 53: every global
// unicast address but Docker's bridges'. The wildcard address isn't used, as
// the system's own resolver may hold port 53 on a loopback address.
func ownDNSAddrs() ([]string, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	var out []string
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || strings.HasPrefix(iface.Name, "docker") || strings.HasPrefix(iface.Name, "br-") || strings.HasPrefix(iface.Name, "veth") {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			ipnet, ok := addr.(*net.IPNet)
			if !ok {
				continue
			}
			ip, ok := netip.AddrFromSlice(ipnet.IP)
			if ok && ip.Unmap().IsGlobalUnicast() {
				out = append(out, netip.AddrPortFrom(ip.Unmap(), 53).String())
			}
		}
	}
	return out, nil
}

// startDNS answers the zone on the machine's addresses, or stops answering
// when it has none. Addresses that can't be used are left out, and the
// status says why.
func (a *Agent) startDNS() {
	d := &a.dns
	d.mu.Lock()
	defer d.mu.Unlock()
	a.stopDNSLocked()
	if d.answerer.Zone().Name == "" {
		return
	}
	addrs, err := a.opts.DNSAddrs()
	if err != nil {
		d.problem = fmt.Sprintf("The machine's addresses couldn't be read: %v", err)
		return
	}
	ctx, cancel := context.WithCancel(a.ctx)
	d.cancel = cancel
	var problems []string
	for _, addr := range addrs {
		pc, err := net.ListenPacket("udp", addr)
		if err != nil {
			problems = append(problems, dnsBindProblem(addr, err))
			continue
		}
		ln, err := net.Listen("tcp", pc.LocalAddr().String())
		if err != nil {
			pc.Close()
			problems = append(problems, dnsBindProblem(addr, err))
			continue
		}
		d.closers = append(d.closers, pc, ln)
		d.listening = append(d.listening, pc.LocalAddr().String())
		d.wg.Add(2)
		go func() {
			defer d.wg.Done()
			a.serveDNSUDP(pc)
		}()
		go func() {
			defer d.wg.Done()
			a.serveDNSTCP(ctx, ln)
		}()
	}
	switch {
	case len(problems) > 0:
		d.problem = strings.Join(problems, " ")
	case len(addrs) == 0:
		d.problem = "The machine has no address to answer DNS on."
	}
}

func dnsBindProblem(addr string, err error) string {
	if errors.Is(err, syscall.EADDRINUSE) {
		return fmt.Sprintf("Another program answers DNS on %s.", addr)
	}
	return fmt.Sprintf("DNS can't be answered on %s: %v.", addr, err)
}

// stopDNSLocked stops answering. The caller holds a.dns.mu.
func (a *Agent) stopDNSLocked() {
	d := &a.dns
	if d.cancel != nil {
		d.cancel()
	}
	for _, c := range d.closers {
		c.Close()
	}
	d.wg.Wait()
	d.cancel, d.closers, d.listening, d.problem = nil, nil, nil, ""
}

func (a *Agent) serveDNSUDP(pc net.PacketConn) {
	buf := make([]byte, 4096)
	for {
		n, from, err := pc.ReadFrom(buf)
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			continue
		}
		if out := a.dns.answerer.Answer(buf[:n], dnszone.MaxUDP); out != nil {
			pc.WriteTo(out, from)
		}
	}
}

func (a *Agent) serveDNSTCP(ctx context.Context, ln net.Listener) {
	slots := make(chan struct{}, dnsTCPConns)
	var conns sync.WaitGroup
	defer conns.Wait()
	for {
		c, err := ln.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) || ctx.Err() != nil {
				return
			}
			continue
		}
		select {
		case slots <- struct{}{}:
		default:
			c.Close()
			continue
		}
		conns.Add(1)
		go func() {
			defer conns.Done()
			defer func() { <-slots }()
			defer c.Close()
			stop := context.AfterFunc(ctx, func() { c.Close() })
			defer stop()
			a.answerDNSConn(c)
		}()
	}
}

// answerDNSConn answers the queries of one TCP connection, each prefixed
// with its length, until it closes or stays quiet for dnsIdle.
func (a *Agent) answerDNSConn(c net.Conn) {
	var size [2]byte
	for {
		c.SetDeadline(time.Now().Add(dnsIdle))
		if _, err := io.ReadFull(c, size[:]); err != nil {
			return
		}
		q := make([]byte, binary.BigEndian.Uint16(size[:]))
		if _, err := io.ReadFull(c, q); err != nil {
			return
		}
		out := a.dns.answerer.Answer(q, 65535)
		if out == nil {
			return
		}
		if _, err := c.Write(binary.BigEndian.AppendUint16(nil, uint16(len(out)))); err != nil {
			return
		}
		if _, err := c.Write(out); err != nil {
			return
		}
	}
}

func (a *Agent) dnsStatus() api.DNSZoneStatus {
	d := &a.dns
	d.mu.Lock()
	defer d.mu.Unlock()
	return api.DNSZoneStatus{Zone: d.answerer.Zone(), Listening: append([]string{}, d.listening...), Problem: d.problem}
}

func (a *Agent) hDNSZone(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, a.dnsStatus())
}

// hDNSZoneSet replaces the zone, numbering it past the last when it
// changed, and answers it at once.
func (a *Agent) hDNSZoneSet(w http.ResponseWriter, r *http.Request) {
	var req api.DNSZoneRequest
	if err := decode(r, &req); err != nil {
		writeError(w, err)
		return
	}
	actor, err := validActor(req.Actor)
	if err != nil {
		writeError(w, err)
		return
	}
	z := req.Zone
	if z.Name != "" {
		if err := z.Check(); err != nil {
			writeError(w, errInvalid("%s", err.Error()))
			return
		}
	} else {
		z = dnszone.Zone{}
	}
	old := a.dns.answerer.Zone()
	z.Serial = old.Serial
	changed := z.Name != old.Name || z.Nameserver != old.Nameserver || !slices.Equal(z.Records, old.Records)
	if changed {
		z.Serial = max(old.Serial+1, uint32(a.now().Unix()))
		raw, _ := json.Marshal(z)
		if err := a.kvSet(kvDNSZone, string(raw)); err != nil {
			writeError(w, err)
			return
		}
		a.dns.answerer.Set(z)
		a.recheckOwnSoon()
	}
	if changed && (z.Name == "") != (old.Name == "") || z.Name != "" && len(a.dnsStatus().Listening) == 0 {
		a.startDNS()
	}
	if changed {
		detail := fmt.Sprintf("%s, %d records", z.Name, len(z.Records))
		if z.Name == "" {
			detail = "answers turned off"
		}
		a.audit(actor, "dns_zone.set", "", "succeeded", detail)
	}
	writeJSON(w, http.StatusOK, a.dnsStatus())
}
