package agent

import (
	"context"
	"crypto/ed25519"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/CIYAhq/playkeeper/internal/api"
	"github.com/CIYAhq/playkeeper/internal/names"
)

// asBeforeTheMove leaves the machine's free name as a release from before
// the move to names.DefaultBase saved it: its address, its servers' and its
// certificate under names.PreviousBase. Then it stops the agent.
func (e *addressEnv) asBeforeTheMove(name string) {
	e.t.Helper()
	e.settled()
	release, err := e.a.holdAddress(e.t.Context(), 15*time.Second)
	if err != nil {
		e.t.Fatal(err)
	}
	defer release()
	old, host := names.Address(name, names.PreviousBase), names.Address(name, names.DefaultBase)
	st := e.a.address()
	f := *st.Free
	f.Name.Address, f.Name.Servers = old, slices.Clone(f.Name.Servers)
	for i, sv := range f.Name.Servers {
		f.Name.Servers[i].Address = names.ServerAddress(sv.Label, name, names.PreviousBase)
	}
	st.Host, st.Free = old, &f
	if err := e.a.setAddress(st); err != nil {
		e.t.Fatal(err)
	}
	row := e.a.loadCertificate(host)
	if row == nil || row.status.Certificate == nil {
		e.t.Fatalf("%s has no certificate", host)
	}
	pem, err := os.ReadFile(row.status.Certificate.File)
	if err != nil {
		e.t.Fatal(err)
	}
	c := *row.status.Certificate
	c.Names, c.File = []string{old}, filepath.Join(e.cfg.CertsDir(), old+".pem")
	if err := os.WriteFile(c.File, pem, 0o600); err != nil {
		e.t.Fatal(err)
	}
	row.name, row.status.Names, row.status.Certificate = old, c.Names, &c
	if err := e.a.saveCertificate(row); err != nil {
		e.t.Fatal(err)
	}
	e.a.deleteCertificate(host)
	e.stop()
}

// answersAlive reports whether the agent answers a liveness check of host
// with the machine's key, signed for base.
func (e *addressEnv) answersAlive(host, base string) bool {
	e.t.Helper()
	const nonce = "dGVzdC1ub25jZS0yMi1jaGFycw"
	var v names.Alive
	if code := e.callInto("GET", "/v1/address/alive/"+nonce+"?"+url.Values{"host": {host}}.Encode(), nil, &v); code != http.StatusOK {
		return false
	}
	key, err := names.LoadOrCreateKey(filepath.Join(e.cfg.AgentDir(), "names.key"))
	if err != nil {
		e.t.Fatal(err)
	}
	return names.VerifyAlive(key.Public().(ed25519.PublicKey), base, v.Name, nonce, v.Signature)
}

func (e *addressEnv) moves() []api.AuditEntry {
	e.t.Helper()
	list, err := e.a.listAudit(100)
	if err != nil {
		e.t.Fatal(err)
	}
	return slices.DeleteFunc(list, func(a api.AuditEntry) bool { return a.Action != "address.move" })
}

// published waits until the machine's free address is published with its
// certificate, and nothing works on it any more.
func (e *addressEnv) published(host string) api.Address {
	e.t.Helper()
	e.waitFor(host+" to be published with a certificate", func() bool {
		v := e.address()
		return v.Host == host && v.Free != nil && v.Free.DNS == names.DNSOK && v.Certificate != nil && v.Certificate.NotAfter != nil && v.Operation == nil
	})
	e.settled()
	return e.address()
}

func TestAFreeAddressFromBeforeTheMoveMovesWhenTheAgentStarts(t *testing.T) {
	e := newAddressEnv(t, nil)
	survival := e.addServerNamed("Survival")
	e.addServerNamed("Creative")
	e.claim("siya")
	e.asBeforeTheMove("siya")
	old, host := "siya."+names.PreviousBase, "siya."+names.DefaultBase
	refreshes, claims, certs := e.names.count("POST /v1/names/siya/address"), e.names.requests("PUT /v1/names/siya"), len(e.ca.requests())
	e.start()

	v := e.published(host)
	want := []api.JoinAddress{
		{ServerID: survival, Name: "Survival", Port: 25565, Label: "survival", Address: "survival." + host, Direct: "203.0.113.10", Published: true},
		{Name: "Creative", Port: 25566, Label: "creative", Address: "creative." + host, Direct: "203.0.113.10:25566", Published: true},
	}
	want[1].ServerID = v.Servers[1].ServerID
	if v.Kind != api.AddressPlaykeeper || v.Free.Name != "siya" || v.Base != names.DefaultBase || !slices.Equal(v.Servers, want) {
		t.Fatalf("the address after the move: %+v\n join addresses %+v", v, v.Servers)
	}
	if got := e.a.serverByID(survival).Status(context.Background()).JoinAddress; got != "survival."+host {
		t.Errorf("the server's join address after the move: %q", got)
	}
	// The fake names service checks that every request is signed for the new base.
	if e.names.count("POST /v1/names/siya/address") < refreshes+2 || e.names.requests("PUT /v1/names/siya") != claims || e.names.requests("DELETE /v1/names/siya") != 0 {
		t.Errorf("the move did not just refresh the name at the names service: %v", e.names.callLog())
	}
	reqs := e.ca.requests()[certs:]
	if len(reqs) != 1 || !slices.Equal(reqs[0].Names, []string{host}) || reqs[0].DNS01 == nil {
		t.Errorf("certificates asked for after the move: %+v", reqs)
	}
	if !exists(filepath.Join(e.cfg.CertsDir(), old+".pem")) || e.a.loadCertificate(old) == nil {
		t.Error("the certificate of the old address was not kept for links shared before the move")
	}
	moves := e.moves()
	if len(moves) != 1 || moves[0].Actor != "playkeeper" || moves[0].Target != host || moves[0].Result != "succeeded" ||
		moves[0].Detail != "Free address moved from siya.playkeeper.io to siya.playkeeper.me" {
		t.Errorf("the audit log's entry for the move: %+v", moves)
	}
	for h, base := range map[string]string{host + ":8443": names.DefaultBase, old + ":8443": names.PreviousBase} {
		if !e.answersAlive(h, base) {
			t.Errorf("the liveness check for %s is not answered for %s", h, base)
		}
	}

	e.stop()
	e.start()
	e.settled()
	if n := len(e.moves()); n != 1 {
		t.Errorf("%d moves after a second start", n)
	}
	if !exists(filepath.Join(e.cfg.CertsDir(), old+".pem")) {
		t.Error("a second start forgot the old certificate before it expired")
	}

	row := e.a.loadCertificate(old)
	c := *row.status.Certificate
	c.NotAfter = time.Now().Add(-time.Minute)
	row.status.Certificate = &c
	if err := e.a.saveCertificate(row); err != nil {
		t.Fatal(err)
	}
	e.loopRefreshes("siya")
	e.waitFor("the expired old certificate to be forgotten", func() bool { return e.a.loadCertificate(old) == nil })
	if exists(filepath.Join(e.cfg.CertsDir(), old+".pem")) || e.a.loadCertificate(host) == nil {
		t.Error("forgetting the expired old certificate went wrong")
	}
}

// The release can reach a machine before the owner moves the names service.
// The machine moves its address all the same, keeps the old one's
// certificate and liveness answers, and follows once the service moved.
func TestAMovedAddressWaitsForTheNamesServiceToMove(t *testing.T) {
	e := newAddressEnv(t, nil)
	e.addServerNamed("Survival")
	e.claim("siya")
	e.asBeforeTheMove("siya")
	e.names.moveTo(names.PreviousBase)
	old, host := "siya."+names.PreviousBase, "siya."+names.DefaultBase
	certs := len(e.ca.requests())
	e.start()

	e.waitFor("the refresh to be tried again within the hour", func() bool {
		st := e.a.address()
		return st.Free != nil && about(time.Until(st.Free.NextRefresh), freeRetryEvery)
	})
	v := e.address()
	if v.Host != host || v.Free.DNS != names.DNSPending || v.Servers[0].Address != "survival."+host || v.Servers[0].Published {
		t.Fatalf("a moved address before the names service moved: %+v %+v", v.Free, v.Servers)
	}
	if len(e.ca.requests()) != certs {
		t.Error("a certificate was asked for before the names service moved")
	}
	if !exists(filepath.Join(e.cfg.CertsDir(), old+".pem")) || !e.answersAlive(old+":8443", names.PreviousBase) {
		t.Error("the old address stopped working before the names service moved")
	}

	e.names.moveTo(names.DefaultBase)
	e.loopRefreshes("siya")
	v = e.published(host)
	if !v.Servers[0].Published || len(e.ca.requests()) != certs+1 {
		t.Errorf("once the names service moved: %+v, %d certificates asked for", v.Servers, len(e.ca.requests())-certs)
	}
}

func TestGivingUpAMovedAddressForgetsBothCertificates(t *testing.T) {
	e := newAddressEnv(t, nil)
	e.claim("siya")
	e.asBeforeTheMove("siya")
	e.start()
	e.published("siya." + names.DefaultBase)
	var v api.Address
	if code := e.callInto("POST", "/v1/address/release", map[string]any{"actor": "admin"}, &v); code != http.StatusOK || v.Kind != api.AddressNone {
		t.Fatalf("release: %d %+v", code, v)
	}
	for _, base := range []string{names.PreviousBase, names.DefaultBase} {
		if host := "siya." + base; e.a.loadCertificate(host) != nil || exists(filepath.Join(e.cfg.CertsDir(), host+".pem")) {
			t.Errorf("the certificate of %s was kept after the release", host)
		}
	}
}

// A whole address pasted from before the move names the same name.
func TestAnAddressPastedFromBeforeTheMoveNamesTheName(t *testing.T) {
	e := newAddressEnv(t, nil)
	var av api.NameAvailability
	if code := e.callInto("GET", "/v1/address/available?name=Siya.PlayKeeper.IO.", nil, &av); code != http.StatusOK || !av.Available || av.Name != "siya" || av.Address != "siya."+names.DefaultBase {
		t.Fatalf("availability of a pasted old address: %d %+v", code, av)
	}
	var v api.Address
	if code := e.callInto("POST", "/v1/address/claim", map[string]any{"name": "siya." + names.PreviousBase, "actor": "admin"}, &v); code != http.StatusOK || v.Host != "siya."+names.DefaultBase {
		t.Fatalf("claiming a pasted old address: %d %+v", code, v)
	}
	if v.Operation != nil {
		if op := e.waitOp(v.Operation.ID); op.Status != api.OpSucceeded {
			t.Fatalf("publishing: %+v", op)
		}
	}
	if n, owner := e.names.name("siya"); n.State != names.StateActive || owner == "" {
		t.Errorf("the names service has %+v", n)
	}
	if v := e.address(); v.Base != names.DefaultBase || v.PreviousBase != names.PreviousBase {
		t.Errorf("the address names the bases %q and %q", v.Base, v.PreviousBase)
	}
}

func TestOwnDomainsUnderEitherFreeBaseAreRefused(t *testing.T) {
	for _, d := range []string{"play.playkeeper.me", "PlayKeeper.ME", "survival.play.playkeeper.me", "www.play.playkeeper.me", "play.playkeeper.io", "playkeeper.io", "beta.playkeeper.io", "ai.playkeeper.io"} {
		if _, err := ownDomain(d); err == nil {
			t.Errorf("%s was accepted as an own domain", d)
		}
	}
	for _, d := range []string{"play.example.com", "playkeeper.me.example.com"} {
		if got, err := ownDomain(d); err != nil || got != d {
			t.Errorf("%s: %q, %v", d, got, err)
		}
	}
}

// The project's own machines sit at playkeeper.me names nobody can claim,
// with a record made by hand, like the managed beta's beta.playkeeper.me.
func TestOwnDomainsNobodyCanClaimUnderTheFreeBaseAreAccepted(t *testing.T) {
	for raw, want := range map[string]string{
		"beta.playkeeper.me":    "beta.playkeeper.me",
		"BETA.PlayKeeper.me.":   "beta.playkeeper.me",
		"ai.playkeeper.me":      "ai.playkeeper.me",
		"eu.beta.playkeeper.me": "eu.beta.playkeeper.me",
	} {
		if got, err := ownDomain(raw); err != nil || got != want {
			t.Errorf("%s: %q, %v; want %q", raw, got, err, want)
		}
	}
}
