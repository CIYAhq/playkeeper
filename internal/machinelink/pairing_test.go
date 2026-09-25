package machinelink

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// otherCode is a well-formed code that is not code.
func otherCode(code string) string {
	if code[len(code)-1] == '0' {
		return code[:len(code)-1] + "1"
	}
	return code[:len(code)-1] + "0"
}

func TestJoin(t *testing.T) {
	th := startHub(t, nil)
	code, jc, err := th.NewJoinCode(context.Background(), "alice")
	if err != nil {
		t.Fatal(err)
	}
	id := mustIdentity(t)
	d, err := Join(context.Background(), JoinOptions{Address: th.addr, Code: strings.ToLower(code), Fingerprint: strings.ToLower(th.Fingerprint()),
		Identity: id, Name: "Home Server", Version: "0.4.0"})
	if err != nil {
		t.Fatal(err)
	}
	if !d.Key.Equal(th.id.PublicKey()) || d.Name != "Home Server" || d.Address != th.addr {
		t.Fatalf("the machine got %+v", d)
	}

	m, ok, _ := th.store.Machine(context.Background(), d.MachineID)
	if !ok || !m.PublicKey.Equal(id.PublicKey()) || m.Name != "Home Server" || m.CreatedBy != "alice" || m.JoinedFrom != "127.0.0.1" || m.Version != "0.4.0" {
		t.Fatalf("the dashboard stored %+v", m)
	}
	codes, _ := th.JoinCodes(context.Background())
	if len(codes) != 1 || codes[0].ID != jc.ID || codes[0].MachineID != m.ID || codes[0].State(th.clock.Now()) != JoinCodeUsed {
		t.Fatalf("join codes after joining: %+v", codes)
	}
	e, ok := th.events.last(EventJoined)
	if !ok || e.MachineID != m.ID || e.Actor != "alice" || e.Name != "Home Server" || e.Address != "127.0.0.1" {
		t.Fatalf("joined event %+v", e)
	}
}

func TestJoinWrongCode(t *testing.T) {
	th := startHub(t, nil)
	code := th.code(t)
	_, err := th.try(mustIdentity(t), otherCode(code))
	if e := wantCode(t, err, CodeJoinCodeWrong); e.Hint == "" {
		t.Error("no hint")
	}
	if machines, _ := th.store.Machines(context.Background()); len(machines) != 0 {
		t.Fatalf("a machine was added: %+v", machines)
	}
	if e, ok := th.events.last(EventJoinRefused); !ok || e.Code != CodeJoinCodeWrong || e.Address != "127.0.0.1" {
		t.Fatalf("refused event %+v", e)
	}
	if _, err := th.try(mustIdentity(t), code); err != nil {
		t.Fatalf("the right code after a wrong one: %v", err)
	}
}

func TestJoinExpiredCode(t *testing.T) {
	th := startHub(t, nil)
	code := th.code(t)
	th.clock.Add(CodeTTL)
	// Expired codes don't count toward the attempt limits: whoever has one
	// had the real code.
	for range 8 {
		_, err := th.try(mustIdentity(t), code)
		wantCode(t, err, CodeJoinCodeExpired)
	}
	if _, err := th.try(mustIdentity(t), th.code(t)); err != nil {
		t.Fatalf("a fresh code after expired ones: %v", err)
	}
}

func TestJoinCodeWorksOnce(t *testing.T) {
	th := startHub(t, nil)
	code := th.code(t)
	first := mustIdentity(t)
	d1, err := th.try(first, code)
	if err != nil {
		t.Fatal(err)
	}
	_, err = th.try(mustIdentity(t), code)
	wantCode(t, err, CodeJoinCodeUsed)

	// The machine that used the code may send it again, as it does when
	// the answer got lost, and is the same machine.
	d2, err := th.try(first, code)
	if err != nil || d2.MachineID != d1.MachineID {
		t.Fatalf("joining again with the same key: %+v, %v", d2, err)
	}
	machines, _ := th.store.Machines(context.Background())
	if len(machines) != 1 || th.events.count(EventJoined) != 1 {
		t.Fatalf("%d machines and %d joined events", len(machines), th.events.count(EventJoined))
	}

	// A new code can't add the same key twice, and isn't spent trying.
	next := th.code(t)
	_, err = th.try(first, next)
	if e := wantCode(t, err, CodeMachineAlreadyJoined); e.Params["name"] != "home-server" {
		t.Errorf("already joined as %q", e.Params["name"])
	}
	if _, err := th.try(mustIdentity(t), next); err != nil {
		t.Fatalf("the code was spent by the refused join: %v", err)
	}
}

func TestJoinAttemptLimits(t *testing.T) {
	th := startHub(t, nil)
	code := th.code(t)
	for range 5 {
		_, err := th.try(mustIdentity(t), otherCode(code))
		wantCode(t, err, CodeJoinCodeWrong)
	}
	_, err := th.try(mustIdentity(t), code)
	e := wantCode(t, err, CodeJoinRateLimited)
	if e.RetryAfter != 15*time.Minute || e.Params["wait"] != "15 minutes" || e.Hint != "Try again in 15 minutes." {
		t.Fatalf("rate limited: %+v", e)
	}
	th.clock.Add(15 * time.Minute)
	if _, err := th.try(mustIdentity(t), code); err != nil {
		t.Fatalf("after the window: %v", err)
	}
	if n := th.events.count(EventJoinRefused); n != 6 {
		t.Fatalf("%d refused events, want 6", n)
	}
}

func TestJoinTotalLimit(t *testing.T) {
	th := startHub(t, func(o *HubOptions) {
		o.Limits = Limits{AddressFailures: 100, TotalFailures: 3, Window: time.Minute}
	})
	code := th.code(t)
	for range 3 {
		_, err := th.try(mustIdentity(t), otherCode(code))
		wantCode(t, err, CodeJoinCodeWrong)
	}
	_, err := th.try(mustIdentity(t), code)
	wantCode(t, err, CodeJoinRateLimited)
}

// A machine in the middle can present only its own key. The machine sees
// that it isn't the dashboard's before sending anything, so the code stays
// secret and still works on the real dashboard.
func TestJoinRefusesAMachineInTheMiddle(t *testing.T) {
	th := startHub(t, nil)
	code := th.code(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	evil := mustIdentity(t)
	got := make(chan []byte, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			got <- nil
			return
		}
		defer c.Close()
		tc := tls.Server(c, serverTLS(evil))
		tc.SetDeadline(time.Now().Add(5 * time.Second))
		var b []byte
		if tc.Handshake() == nil {
			b, _ = io.ReadAll(tc)
		}
		got <- b
	}()
	_, err = Join(context.Background(), JoinOptions{Address: ln.Addr().String(), Code: code, Fingerprint: th.Fingerprint(), Identity: mustIdentity(t)})
	if e := wantCode(t, err, CodeDashboardKeyMismatch); !strings.Contains(e.Msg, "fingerprint doesn't match") {
		t.Errorf("message %q", e.Msg)
	}
	select {
	case b := <-got:
		if len(b) != 0 {
			t.Fatalf("the machine in the middle received %d bytes", len(b))
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the machine in the middle is still waiting")
	}
	if _, err := th.try(mustIdentity(t), code); err != nil {
		t.Fatalf("the code no longer works on the real dashboard: %v", err)
	}
}

func TestJoinCodeNeverCrossesInTheClear(t *testing.T) {
	th := startHub(t, nil)
	p := startProxy(t, th.addr)
	code := th.code(t)
	if _, err := Join(context.Background(), JoinOptions{Address: p.addr(), Code: code, Fingerprint: th.Fingerprint(), Identity: mustIdentity(t)}); err != nil {
		t.Fatal(err)
	}
	_, err := th.try(mustIdentity(t), otherCode(code))
	wantCode(t, err, CodeJoinCodeWrong)
	seen := p.bytesSeen()
	if len(seen) == 0 {
		t.Fatal("the proxy saw nothing")
	}
	for _, s := range []string{code, strings.ReplaceAll(code, "-", ""), `"code"`, `"mode"`} {
		if bytes.Contains(seen, []byte(s)) {
			t.Fatalf("%q crossed the network in the clear", s)
		}
	}
	for _, s := range []string{code, strings.ReplaceAll(code, "-", ""), otherCode(code)} {
		if strings.Contains(th.logs.String(), s) {
			t.Fatalf("the dashboard logged a code: %s", th.logs.String())
		}
	}
}

func TestJoinRemovedMachine(t *testing.T) {
	th := startHub(t, nil)
	d, id := th.join(t, th.addr, "home-server")
	if err := th.Remove(context.Background(), d.MachineID, "alice"); err != nil {
		t.Fatal(err)
	}
	code := th.code(t)
	_, err := th.try(id, code)
	wantCode(t, err, CodeMachineRemoved)
	d2, err := th.try(mustIdentity(t), code)
	if err != nil {
		t.Fatalf("a new key with the same code: %v", err)
	}
	if d2.MachineID == d.MachineID || d2.Name != "home-server" {
		t.Fatalf("rejoined as %+v", d2)
	}
}

func TestJoinCodeHousekeeping(t *testing.T) {
	th := startHub(t, nil)
	var codes []string
	for range MaxWaitingCodes + 1 {
		codes = append(codes, th.code(t))
	}
	list, _ := th.JoinCodes(context.Background())
	if len(list) != MaxWaitingCodes {
		t.Fatalf("%d codes waiting, want %d", len(list), MaxWaitingCodes)
	}
	_, err := th.try(mustIdentity(t), codes[0])
	wantCode(t, err, CodeJoinCodeWrong)

	if err := th.CancelJoinCode(context.Background(), list[len(list)-1].ID); err != nil {
		t.Fatal(err)
	}
	_, err = th.try(mustIdentity(t), codes[len(codes)-1])
	wantCode(t, err, CodeJoinCodeWrong)

	names := map[string]bool{}
	for _, c := range codes[1 : len(codes)-1] {
		d, err := th.try(mustIdentity(t), c)
		if err != nil {
			t.Fatalf("code %d: %v", len(names)+1, err)
		}
		names[d.Name] = true
	}
	for _, n := range []string{"home-server", "home-server-2", "home-server-3", "home-server-4"} {
		if !names[n] {
			t.Errorf("no machine named %s among %v", n, names)
		}
	}

	// Used codes are forgotten an hour later.
	th.clock.Add(codeKeep + time.Minute)
	th.code(t)
	if list, _ := th.JoinCodes(context.Background()); len(list) != 1 {
		t.Fatalf("%d codes listed after an hour, want the new one", len(list))
	}
}

func TestJoinChecksTheCommandFirst(t *testing.T) {
	dials := 0
	dial := func(context.Context, string, string) (net.Conn, error) {
		dials++
		return nil, errors.New("no network in this test")
	}
	fp := mustIdentity(t).Fingerprint()
	for _, tc := range []struct{ addr, code, fp, want string }{
		{"panel.example.com", "hello", fp, CodeJoinCodeMalformed},
		{"panel.example.com", "7KQ2-M9XD", "Z287", CodeFingerprintInvalid},
		{"panel.example.com; reboot", "7KQ2-M9XD", fp, CodeAddressInvalid},
		{"http://panel.example.com", "7KQ2-M9XD", fp, CodeAddressInvalid},
	} {
		_, err := Join(context.Background(), JoinOptions{Address: tc.addr, Code: tc.code, Fingerprint: tc.fp, Identity: mustIdentity(t), Dial: dial})
		wantCode(t, err, tc.want)
	}
	if _, err := Join(context.Background(), JoinOptions{Address: "panel.example.com", Code: "7KQ2-M9XD", Fingerprint: fp, Dial: dial}); err == nil {
		t.Error("joined without an identity")
	}
	if dials != 0 {
		t.Fatalf("dialed %d times with a bad command", dials)
	}
	_, err := Join(context.Background(), JoinOptions{Address: "panel.example.com", Code: "7KQ2-M9XD", Fingerprint: fp, Identity: mustIdentity(t), Dial: dial})
	if e := wantCode(t, err, CodeDashboardUnreachable); !strings.Contains(e.Hint, "port 8443") {
		t.Errorf("unreachable hint %q", e.Hint)
	}
}

func TestJoinSomethingElse(t *testing.T) {
	fp := mustIdentity(t).Fingerprint()
	join := func(addr string) error {
		_, err := Join(context.Background(), JoinOptions{Address: addr, Code: "7KQ2-M9XD", Fingerprint: fp, Identity: mustIdentity(t), Timeout: 5 * time.Second})
		return err
	}

	web := httptest.NewUnstartedServer(http.NotFoundHandler())
	web.Config.ErrorLog = log.New(io.Discard, "", 0)
	web.StartTLS()
	defer web.Close()
	e := wantCode(t, join(strings.TrimPrefix(web.URL, "https://")), CodeNotADashboard)
	if !strings.Contains(e.Hint, "orange cloud") {
		t.Errorf("hint %q", e.Hint)
	}

	plain, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer plain.Close()
	go func() {
		for {
			c, err := plain.Accept()
			if err != nil {
				return
			}
			// Read the whole ClientHello first: closing with unread data
			// would reset the connection instead of answering.
			var hdr [5]byte
			if _, err := io.ReadFull(c, hdr[:]); err == nil {
				io.CopyN(io.Discard, c, int64(hdr[3])<<8|int64(hdr[4]))
			}
			c.Write([]byte("HTTP/1.1 400 Bad Request\r\nConnection: close\r\n\r\n"))
			c.Close()
		}
	}()
	wantCode(t, join(plain.Addr().String()), CodeNotADashboard)

	closed, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := closed.Addr().String()
	closed.Close()
	wantCode(t, join(addr), CodeDashboardUnreachable)
}

func TestHelloRefusals(t *testing.T) {
	th := startHub(t, nil)
	for name, tc := range map[string]struct {
		raw  []byte
		want string
	}{
		"huge":      {[]byte{0, 0x10, 0, 0}, CodeTooLarge},
		"not JSON":  {frame([]byte("nope")), CodeProtocol},
		"two":       {frame([]byte(`{"v":1,"mode":"join"} {}`)), CodeProtocol},
		"version":   {jsonFrame(t, hello{V: 2, Mode: modeLink}), CodeVersionUnsupported},
		"mode":      {jsonFrame(t, hello{V: 1, Mode: "shell"}), CodeProtocol},
		"malformed": {jsonFrame(t, hello{V: 1, Mode: modeJoin, Code: "$(reboot)"}), CodeJoinCodeMalformed},
		"link":      {jsonFrame(t, hello{V: 1, Mode: modeLink}), CodeMachineUnknown},
		"leave":     {jsonFrame(t, hello{V: 1, Mode: modeLeave}), CodeMachineUnknown},
	} {
		w := th.exchange(t, mustIdentity(t), tc.raw)
		if w.OK || w.Error == nil || w.Error.Code != tc.want || w.Error.Message == "" {
			t.Errorf("%s: welcome %+v", name, w)
		}
	}
	if th.Connected("") || th.events.count(EventConnected) != 0 {
		t.Fatal("a refused hello connected")
	}
}
