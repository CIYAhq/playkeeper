package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/CIYAhq/playkeeper/internal/names"
)

func TestOwnsOnlyMarkedRecordsInTheServicesOwnPatterns(t *testing.T) {
	s := &Service{base: testBase}
	rec := func(typ, name, comment string) cfRecord {
		return cfRecord{ID: "1", Type: typ, Name: name, Comment: comment}
	}
	for _, tc := range []struct {
		owner string
		r     cfRecord
		want  bool
	}{
		{"alice", rec("A", "alice.playkeeper.io", "playkeeper-names alice"), true},
		{"alice", rec("AAAA", "alice.playkeeper.io", "playkeeper-names alice"), true},
		{"alice", rec("A", "Alice.PlayKeeper.io.", "playkeeper-names alice"), true},
		{"alice", rec("TXT", "_acme-challenge.alice.playkeeper.io", "playkeeper-names alice"), true},
		{"alice", rec("SRV", "_minecraft._tcp.alice.playkeeper.io", "playkeeper-names alice"), true},
		{"alice", rec("SRV", "_minecraft._tcp.survival.alice.playkeeper.io", "playkeeper-names alice"), true},

		{"alice", rec("A", "alice.playkeeper.io", ""), false},
		{"alice", rec("A", "alice.playkeeper.io", "playkeeper-names bob"), false},
		{"alice", rec("A", "alice.playkeeper.io", "playkeeper-names alice (edited)"), false},
		{"alice", rec("A", "alice.playkeeper.io", "Playkeeper-names alice"), false},
		{"alice", rec("CNAME", "alice.playkeeper.io", "playkeeper-names alice"), false},
		{"alice", rec("MX", "alice.playkeeper.io", "playkeeper-names alice"), false},
		{"alice", rec("NS", "alice.playkeeper.io", "playkeeper-names alice"), false},
		{"alice", rec("TXT", "alice.playkeeper.io", "playkeeper-names alice"), false},
		{"alice", rec("TXT", "_acme-challenge.survival.alice.playkeeper.io", "playkeeper-names alice"), false},
		{"alice", rec("A", "survival.alice.playkeeper.io", "playkeeper-names alice"), false},
		{"alice", rec("A", "malice.playkeeper.io", "playkeeper-names alice"), false},
		{"alice", rec("A", "alice.playkeeper.io.example.com", "playkeeper-names alice"), false},
		{"alice", rec("SRV", "_minecraft._udp.alice.playkeeper.io", "playkeeper-names alice"), false},
		{"alice", rec("SRV", "_minecraft._tcp.a.b.alice.playkeeper.io", "playkeeper-names alice"), false},
		{"alice", rec("SRV", "_minecraft._tcp.-x.alice.playkeeper.io", "playkeeper-names alice"), false},
		{"alice", rec("SRV", "_minecraft._tcp..alice.playkeeper.io", "playkeeper-names alice"), false},
		{"alice", rec("SRV", "_minecraft._tcp.alice.playkeeper.io.evil.com", "playkeeper-names alice"), false},
		{"alice", rec("SRV", "_sip._tcp.alice.playkeeper.io", "playkeeper-names alice"), false},
		{"alice", rec("A", "playkeeper.io", "playkeeper-names alice"), false},

		{"playkeeper", rec("A", "playkeeper.playkeeper.io", "playkeeper-names playkeeper"), false},
		{"myplaykeeper", rec("A", "myplaykeeper.playkeeper.io", "playkeeper-names myplaykeeper"), false},
		{"www", rec("A", "www.playkeeper.io", "playkeeper-names www"), false},
		{"www", rec("TXT", "_acme-challenge.www.playkeeper.io", "playkeeper-names www"), false},
		{"names", rec("AAAA", "names.playkeeper.io", "playkeeper-names names"), false},
		{"names", rec("SRV", "_minecraft._tcp.names.playkeeper.io", "playkeeper-names names"), false},
		{"mail", rec("A", "mail.playkeeper.io", "playkeeper-names mail"), false},
		{"mx", rec("A", "mx.playkeeper.io", "playkeeper-names mx"), false},
		{"autodiscover", rec("A", "autodiscover.playkeeper.io", "playkeeper-names autodiscover"), false},
		{"install", rec("A", "install.playkeeper.io", "playkeeper-names install"), false},
		{"Alice", rec("A", "Alice.playkeeper.io", "playkeeper-names Alice"), false},
		{"", rec("A", ".playkeeper.io", "playkeeper-names "), false},
	} {
		if got := s.owns(tc.owner, tc.r); got != tc.want {
			t.Errorf("owns(%q, %s %s comment %q) = %v, want %v", tc.owner, tc.r.Type, tc.r.Name, tc.r.Comment, got, tc.want)
		}
	}
	other := &Service{base: "example.com"}
	if other.owns("alice", rec("A", "alice.playkeeper.io", "playkeeper-names alice")) {
		t.Error("a service for example.com must not own records under playkeeper.io")
	}
}

// Seed records of testdata/cloudflare/dns_records.json.
const (
	seedApexA       = "372e67954025e0ba6aaa6d586b9e0b59"
	seedNamesA      = "4f1bd3f8a8c7e1c9d2a3b4c5d6e7f809"
	seedMX1         = "b1f2a9d0c3e84f5a96b7c8d9e0f1a2b3"
	seedSPF         = "0a1b2c3d4e5f60718293a4b5c6d7e8f9"
	seedCarolA      = "3d4e5f60718293a4b5c6d7e8f90a1b2c"
	seedApexMarked  = "4e5f60718293a4b5c6d7e8f90a1b2c3d"
	seedWWWMarked   = "5f60718293a4b5c6d7e8f90a1b2c3d4e"
	seedNamesMarked = "60718293a4b5c6d7e8f90a1b2c3d4e5f"
	seedNamesSRV    = "718293a4b5c6d7e8f90a1b2c3d4e5f60"
	seedDaveSRV     = "8293a4b5c6d7e8f90a1b2c3d4e5f6071"
	seedMailMarked  = "93a4b5c6d7e8f90a1b2c3d4e5f607182"
)

func TestTheGuardRefusesEveryChangeOutsideItsPatterns(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	s, cf := e.svc, e.cf
	mine := cfRecord{ID: "c0ffee", Type: "A", Name: aliceFQD, Content: aliceV4, Comment: marker("alice")}
	mineSRV := cfRecord{ID: "c0ffef", Type: "SRV", Name: "_minecraft._tcp." + aliceFQD, Data: &cfSRV{Port: 25565, Target: aliceFQD}, Comment: marker("alice")}
	a := func(name string) cfRecord { return cfRecord{Type: "A", Name: name, Content: aliceV4} }
	before := cf.requestCount()
	for _, tc := range []struct {
		name string
		do   func() error
	}{
		{"create at the apex", func() error { return s.createRecord(ctx, "alice", a(testBase)) }},
		{"create www", func() error { return s.createRecord(ctx, "www", a("www.playkeeper.io")) }},
		{"create names", func() error { return s.createRecord(ctx, "names", a("names.playkeeper.io")) }},
		{"create mail", func() error { return s.createRecord(ctx, "mail", a("mail.playkeeper.io")) }},
		{"create under another name", func() error { return s.createRecord(ctx, "alice", a("bob.playkeeper.io")) }},
		{"create for an invalid name", func() error { return s.createRecord(ctx, "Alice", a("Alice.playkeeper.io")) }},
		{"create an MX", func() error {
			return s.createRecord(ctx, "alice", cfRecord{Type: "MX", Name: aliceFQD, Content: "mail.example.com"})
		}},
		{"create a CNAME", func() error {
			return s.createRecord(ctx, "alice", cfRecord{Type: "CNAME", Name: aliceFQD, Content: "example.com"})
		}},
		{"create a TXT outside _acme-challenge", func() error {
			return s.createRecord(ctx, "alice", cfRecord{Type: "TXT", Name: aliceFQD, Content: "v=spf1 -all"})
		}},
		{"update a record made by hand", func() error {
			return s.updateRecord(ctx, "carol", cf.seedRecord(seedCarolA), a("carol.playkeeper.io"))
		}},
		{"update the names record", func() error { return s.updateRecord(ctx, "names", cf.seedRecord(seedNamesA), a("names.playkeeper.io")) }},
		{"update into another name", func() error { return s.updateRecord(ctx, "alice", mine, a("www.playkeeper.io")) }},
		{"update into another type", func() error {
			return s.updateRecord(ctx, "alice", mine, cfRecord{Type: "AAAA", Name: aliceFQD, Content: aliceV6})
		}},
		{"update into another of its own names", func() error {
			moved := mineSRV
			moved.Name = "_minecraft._tcp.survival." + aliceFQD
			return s.updateRecord(ctx, "alice", mineSRV, moved)
		}},
		{"update without an id", func() error {
			noID := mine
			noID.ID = ""
			return s.updateRecord(ctx, "alice", noID, a(aliceFQD))
		}},
		{"delete the apex", func() error { return s.deleteRecord(ctx, "alice", cf.seedRecord(seedApexA)) }},
		{"delete an MX", func() error { return s.deleteRecord(ctx, "alice", cf.seedRecord(seedMX1)) }},
		{"delete SPF", func() error { return s.deleteRecord(ctx, "alice", cf.seedRecord(seedSPF)) }},
		{"delete a marked apex record", func() error { return s.deleteRecord(ctx, "playkeeper", cf.seedRecord(seedApexMarked)) }},
		{"delete a marked www record", func() error { return s.deleteRecord(ctx, "www", cf.seedRecord(seedWWWMarked)) }},
		{"delete a marked names record", func() error { return s.deleteRecord(ctx, "names", cf.seedRecord(seedNamesMarked)) }},
		{"delete a marked names SRV", func() error { return s.deleteRecord(ctx, "names", cf.seedRecord(seedNamesSRV)) }},
		{"delete a marked mail record", func() error { return s.deleteRecord(ctx, "mail", cf.seedRecord(seedMailMarked)) }},
		{"delete a record with another name's marker", func() error { return s.deleteRecord(ctx, "dave", cf.seedRecord(seedDaveSRV)) }},
		{"delete a record made by hand", func() error { return s.deleteRecord(ctx, "carol", cf.seedRecord(seedCarolA)) }},
		{"delete without an id", func() error {
			noID := mine
			noID.ID = ""
			return s.deleteRecord(ctx, "alice", noID)
		}},
	} {
		if err := tc.do(); !errors.Is(err, errRefused) {
			t.Errorf("%s: got %v, want the guard to refuse", tc.name, err)
		}
	}
	if n := cf.requestCount() - before; n != 0 {
		t.Errorf("the guard let %d requests reach Cloudflare", n)
	}
	if got := strings.Count(e.log.String(), "Refused to touch a DNS record"); got != 26 {
		t.Errorf("logged %d refusals, want 26", got)
	}
	cf.checkUntouched(t)
}

func TestRecordsTheServiceDoesNotManageAreNeverTouched(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	cf := e.cf
	m := newMachine(aliceV4, aliceV6)
	alice := e.claimed("alice", "alice", m)
	value := acmeValue("alice-token.thumbprint")
	if _, err := alice.SetServer(ctx, "", 25565); err != nil {
		t.Fatal(err)
	}
	if _, err := alice.SetServer(ctx, "survival", 25566); err != nil {
		t.Fatal(err)
	}
	if err := alice.SetTXT(ctx, names.ChallengeFQDN("alice", testBase), value); err != nil {
		t.Fatal(err)
	}
	if err := alice.ClearTXT(ctx, names.ChallengeFQDN("alice", testBase), value); err != nil {
		t.Fatal(err)
	}

	intruder := e.install("mallory", newMachine("5.75.161.10", ""))
	for name, code := range map[string]string{
		"carol": names.CodeNameInUse, "dave": names.CodeNameInUse,
		"www": names.CodeNameReserved, "names": names.CodeNameReserved, "mail": names.CodeNameReserved,
		"wiki": names.CodeNameReserved, "playkeeper": names.CodeNameReserved, "dmarc": names.CodeNameReserved,
	} {
		if _, err := intruder.Claim(ctx, name); codeOf(err) != code {
			t.Errorf("claim %s: got %v, want %s", name, err, code)
		}
	}

	// The owner adds records under the claimed name by hand.
	handTXT := cf.addByHand("TXT", aliceFQD, `"google-site-verification=6Hc1nq2X"`, "")
	handSRV := cf.addByHand("SRV", "_minecraft._tcp.creative."+aliceFQD, "0 0 25570 other.example.net", "set up by hand")
	m.set("5.75.160.100", aliceV6)
	if n, err := alice.Refresh(ctx); err != nil || n.DNS != names.DNSOK {
		t.Fatalf("refresh with hand-made TXT and SRV next to the records: %+v, %v", n, err)
	}
	if got := cf.get("A", aliceFQD); len(got) != 1 || got[0].Content != "5.75.160.100" {
		t.Errorf("A records %+v, want the new address", got)
	}
	// Hand-made records where one of the name's records goes: the service
	// leaves that address alone instead of answering next to it.
	creative := "_minecraft._tcp.creative." + aliceFQD
	if sv, err := alice.SetServer(ctx, "creative", 25571); err != nil || sv.DNS != names.DNSPending {
		t.Errorf("a server address where an SRV record was made by hand: %+v, %v; want it pending", sv, err)
	}
	if got := cf.get("SRV", creative); len(got) != 1 || got[0].ID != handSRV {
		t.Errorf("SRV records at %s: %+v; want only the one made by hand", creative, got)
	}
	if _, err := alice.SetServer(ctx, "survival", 25567); err != nil {
		t.Fatal(err)
	}
	if got := cf.get("SRV", "_minecraft._tcp.survival."+aliceFQD); len(got) != 1 || got[0].Data.Port != 25567 {
		t.Errorf("another server address next to a hand-made SRV did not change: %+v", got)
	}
	handA := cf.addByHand("A", aliceFQD, "5.75.160.77", "playkeeper-names bob")
	m.set("5.75.160.101", aliceV6)
	n, err := alice.Refresh(ctx)
	if err != nil || n.DNS != names.DNSPending || n.IPv4 != "5.75.160.101" {
		t.Errorf("refresh next to a hand-made A: %+v, %v; want the new address pending", n, err)
	}
	if !strings.Contains(e.log.String(), aliceFQD+" has a hand-made A record") || !strings.Contains(e.log.String(), creative+" has a hand-made SRV record") {
		t.Error("the conflicts are not logged")
	}
	if got := cf.get("A", aliceFQD); len(got) != 2 {
		t.Errorf("A records next to a hand-made one: %+v", got)
	}
	cf.checkUntouched(t)

	if _, err := alice.Release(ctx); err != nil {
		t.Fatal(err)
	}
	left := map[string]bool{}
	for _, r := range cf.under(aliceFQD) {
		left[r.ID] = true
	}
	if len(left) != 3 || !left[handTXT] || !left[handSRV] || !left[handA] {
		t.Errorf("after the release, records under the name: %+v; want only the three made by hand", cf.under(aliceFQD))
	}
	for _, w := range cf.writeLog() {
		if strings.Contains(w, "google-site-verification") || strings.Contains(w, "other.example.net") || strings.Contains(w, "5.75.160.77") {
			t.Errorf("the service wrote %q", w)
		}
	}
	cf.checkUntouched(t)
}
