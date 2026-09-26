package certs

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/acme"
)

var b64 = base64.RawURLEncoding

// fakeCA is a small ACME server (RFC 8555) for Issue's tests. It checks
// nonces, URLs and signatures, and validates a challenge as soon as it is
// accepted, so the client never has to wait for a poll.
type fakeCA struct {
	t   *testing.T
	ca  *testCA
	srv *httptest.Server

	// Settings a test changes before calling Issue.
	terms        string
	eabRequired  bool
	offer        []string                   // challenge types; nil means http-01 and dns-01
	validAuthz   bool                       // authorizations start out valid
	httpPort     int                        // where http-01 is checked, on 127.0.0.1
	txt          func(fqdn string) []string // what dns-01 checks find
	chain        func(csr *x509.CertificateRequest) [][]byte
	fail         map[string]caFailure // by step
	directory    func(w http.ResponseWriter) bool
	rejectNonces int
	// finalizeAnswer is how finalize requests are answered: "processing"
	// like Pebble (issuance still running, no Location header); "lost" and
	// "lost-early" hang up after or before issuing; "slow" and "slow-early"
	// never answer, after or before issuing.
	finalizeAnswer string
	// processing is how many looks at a finalized order find it still
	// processing, as does the finalize answer, with a Retry-After of
	// retryAfter; -1 means every look. unanswered is how many looks at a
	// finalized order get no answer at all; -1 means every look.
	processing int
	retryAfter string
	unanswered int

	mu       sync.Mutex
	nonces   map[string]bool
	accounts map[string]*caAccount // by URL
	orders   map[string]*caOrder
	last     *caOrder // the order made last
	authzs   map[string]*caAuthz
	seq      int
	steps    []string
	looked   []time.Time // when finalized orders were looked at
}

// caFailure is the answer to a step instead of the normal one; for the
// "validate" step, Problem becomes the challenge's error.
type caFailure struct {
	Status  int               `json:"status"`
	Headers map[string]string `json:"headers"`
	Problem json.RawMessage   `json:"problem"`
}

type caAccount struct {
	url     string
	key     *ecdsa.PublicKey
	contact []string
	agreed  bool
}

type caOrder struct {
	id, account string
	names       []string
	authzs      []string
	valid       bool
	invalid     bool // given up on, as when it expires
	expires     time.Time
	cert        []byte
	looks       int // since it was finalized
}

type caAuthz struct {
	id, name, status, account string
	chals                     []*caChallenge
}

type caChallenge struct {
	Type   string          `json:"type"`
	URL    string          `json:"url"`
	Token  string          `json:"token"`
	Status string          `json:"status"`
	Error  json.RawMessage `json:"error,omitempty"`
}

func newFakeCA(t *testing.T) *fakeCA {
	f := &fakeCA{
		t: t, ca: newTestCA(t), fail: map[string]caFailure{},
		nonces: map[string]bool{}, accounts: map[string]*caAccount{},
		orders: map[string]*caOrder{}, authzs: map[string]*caAuthz{},
	}
	f.srv = httptest.NewTLSServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeCA) url(path string) string { return f.srv.URL + path }

// issuer returns an Issuer for the fake CA whose account key is created in
// dir.
func (f *fakeCA) issuer(dir string) *Issuer {
	return &Issuer{
		DirectoryURL:   f.url("/dir"),
		AccountKeyFile: filepath.Join(dir, "account.key"),
		AgreedTerms:    f.terms,
		Client:         f.srv.Client(),
	}
}

// responder returns an HTTP-01 responder on a free port, where the fake CA
// checks http-01 challenges.
func (f *fakeCA) responder(t *testing.T) *HTTP01Responder {
	f.httpPort = freePort(t)
	return &HTTP01Responder{Addr: "127.0.0.1:" + strconv.Itoa(f.httpPort)}
}

func (f *fakeCA) count(step string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, s := range f.steps {
		if s == step {
			n++
		}
	}
	return n
}

func (f *fakeCA) accountList() []*caAccount {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Collect(maps.Values(f.accounts))
}

func (f *fakeCA) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	w.Header().Set("Replay-Nonce", f.newNonce())
	w.Header().Set("Cache-Control", "no-store")
	switch {
	case r.URL.Path == "/dir" && r.Method == http.MethodGet:
		f.steps = append(f.steps, "directory")
		if f.failed(w, "directory") || (f.directory != nil && f.directory(w)) {
			return
		}
		meta := map[string]any{}
		if f.terms != "" {
			meta["termsOfService"] = f.terms
		}
		if f.eabRequired {
			meta["externalAccountRequired"] = true
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"newNonce": f.url("/nonce"), "newAccount": f.url("/new-acct"), "newOrder": f.url("/new-order"),
			"revokeCert": f.url("/revoke"), "keyChange": f.url("/key-change"), "meta": meta,
		})
	case r.URL.Path == "/nonce" && r.Method == http.MethodHead:
		w.WriteHeader(http.StatusOK)
	case r.Method == http.MethodPost:
		f.post(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (f *fakeCA) post(w http.ResponseWriter, r *http.Request) {
	var jws struct{ Protected, Payload, Signature string }
	var hdr struct {
		Alg, Nonce, URL, KID string
		JWK                  *struct{ Kty, Crv, X, Y string }
	}
	if r.Header.Get("Content-Type") != "application/jose+json" {
		f.malformed(w, "content type "+r.Header.Get("Content-Type"))
		return
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&jws); err != nil {
		f.malformed(w, "not a JWS: "+err.Error())
		return
	}
	prot, err := b64.DecodeString(jws.Protected)
	if err == nil {
		err = json.Unmarshal(prot, &hdr)
	}
	if err != nil || hdr.URL != f.url(r.URL.Path) {
		f.malformed(w, fmt.Sprintf("protected header %s for %s", prot, r.URL.Path))
		return
	}
	if !f.nonces[hdr.Nonce] || f.rejectNonces > 0 {
		f.rejectNonces = max(f.rejectNonces-1, 0)
		f.problem(w, http.StatusBadRequest, "badNonce", "JWS has an invalid anti-replay nonce")
		return
	}
	delete(f.nonces, hdr.Nonce)
	var key *ecdsa.PublicKey
	var acct *caAccount
	switch {
	case hdr.JWK != nil && hdr.KID == "":
		if key, err = parseJWK(hdr.JWK.Kty, hdr.JWK.Crv, hdr.JWK.X, hdr.JWK.Y); err != nil {
			f.malformed(w, err.Error())
			return
		}
	case hdr.KID != "" && hdr.JWK == nil:
		if acct = f.accounts[hdr.KID]; acct == nil {
			f.problem(w, http.StatusBadRequest, "accountDoesNotExist", "unknown account "+hdr.KID)
			return
		}
		key = acct.key
	default:
		f.malformed(w, "a request needs exactly one of jwk and kid")
		return
	}
	if hdr.Alg != "ES256" || !verifyES256(key, jws.Protected+"."+jws.Payload, jws.Signature) {
		f.malformed(w, "bad signature")
		return
	}
	payload, err := b64.DecodeString(jws.Payload)
	if err != nil {
		f.malformed(w, "bad payload")
		return
	}
	path := r.URL.Path
	id := path[strings.LastIndexByte(path, '/')+1:]
	switch {
	case path == "/new-acct":
		f.newAccount(w, key, payload)
	case acct == nil:
		f.malformed(w, "only newAccount is signed with a jwk")
	case path == "/new-order":
		f.newOrder(w, acct, payload)
	case strings.HasPrefix(path, "/authz/"):
		f.steps = append(f.steps, "authz")
		if z := f.authzs[id]; z != nil && z.account == acct.url {
			f.writeAuthz(w, z)
		} else {
			f.problem(w, http.StatusNotFound, "malformed", "no such authorization")
		}
	case strings.HasPrefix(path, "/chal/"):
		f.challenge(w, acct, strings.TrimPrefix(path, "/chal/"))
	case strings.HasPrefix(path, "/order/"):
		f.steps = append(f.steps, "order")
		if f.failed(w, "order") {
			return
		}
		o := f.orders[id]
		if o == nil || o.account != acct.url {
			f.problem(w, http.StatusNotFound, "malformed", "no such order")
			return
		}
		if o.valid {
			o.looks++
			f.looked = append(f.looked, time.Now())
			if f.unanswered < 0 || o.looks <= f.unanswered {
				f.ignore(w)
				return
			}
		}
		f.writeOrder(w, http.StatusOK, o)
	case strings.HasPrefix(path, "/finalize/"):
		f.finalize(w, acct, id, payload)
	case strings.HasPrefix(path, "/cert/"):
		f.steps = append(f.steps, "cert")
		o := f.orders[id]
		if o == nil || o.account != acct.url || o.cert == nil {
			f.problem(w, http.StatusNotFound, "malformed", "no such certificate")
			return
		}
		w.Header().Set("Content-Type", "application/pem-certificate-chain")
		w.Write(o.cert)
	default:
		f.problem(w, http.StatusNotFound, "malformed", "no such resource")
	}
}

func (f *fakeCA) newAccount(w http.ResponseWriter, key *ecdsa.PublicKey, payload []byte) {
	var req struct {
		Contact            []string `json:"contact"`
		TermsAgreed        bool     `json:"termsOfServiceAgreed"`
		OnlyReturnExisting bool     `json:"onlyReturnExisting"`
	}
	if err := json.Unmarshal(payload, &req); err != nil {
		f.malformed(w, "bad account request")
		return
	}
	var existing *caAccount
	for _, a := range f.accounts {
		if a.key.Equal(key) {
			existing = a
		}
	}
	if req.OnlyReturnExisting {
		f.steps = append(f.steps, "lookup-account")
		if f.failed(w, "lookup-account") {
			return
		}
		if existing == nil {
			f.problem(w, http.StatusBadRequest, "accountDoesNotExist", "No account exists with the provided key")
			return
		}
		f.writeAccount(w, http.StatusOK, existing)
		return
	}
	f.steps = append(f.steps, "new-acct")
	if f.failed(w, "new-acct") {
		return
	}
	if existing != nil {
		f.writeAccount(w, http.StatusOK, existing)
		return
	}
	if f.terms != "" && !req.TermsAgreed {
		w.Header().Set("Link", "<"+f.terms+`>;rel="terms-of-service"`)
		f.problem(w, http.StatusForbidden, "userActionRequired", "Must agree to the terms of service")
		f.t.Error("fake CA: an account was requested without agreeing to the terms")
		return
	}
	f.seq++
	a := &caAccount{url: f.url("/acct/" + strconv.Itoa(f.seq)), key: key, contact: req.Contact, agreed: req.TermsAgreed}
	f.accounts[a.url] = a
	f.writeAccount(w, http.StatusCreated, a)
}

func (f *fakeCA) writeAccount(w http.ResponseWriter, status int, a *caAccount) {
	w.Header().Set("Location", a.url)
	writeJSON(w, status, map[string]any{"status": "valid", "contact": a.contact, "orders": a.url + "/orders"})
}

func (f *fakeCA) newOrder(w http.ResponseWriter, a *caAccount, payload []byte) {
	f.steps = append(f.steps, "new-order")
	if f.failed(w, "new-order") {
		return
	}
	var req struct {
		Identifiers []struct{ Type, Value string }
	}
	if err := json.Unmarshal(payload, &req); err != nil || len(req.Identifiers) == 0 {
		f.malformed(w, "bad order request")
		return
	}
	offer := f.offer
	if offer == nil {
		offer = []string{"http-01", "dns-01"}
	}
	f.seq++
	o := &caOrder{id: strconv.Itoa(f.seq), account: a.url, expires: time.Now().Add(7 * 24 * time.Hour)}
	for _, ident := range req.Identifiers {
		if ident.Type != "dns" {
			f.malformed(w, "identifier type "+ident.Type)
			return
		}
		f.seq++
		z := &caAuthz{id: strconv.Itoa(f.seq), name: ident.Value, status: "pending", account: a.url}
		if f.validAuthz {
			z.status = "valid"
		}
		for _, typ := range offer {
			z.chals = append(z.chals, &caChallenge{Type: typ, URL: f.url("/chal/" + z.id + "/" + typ), Token: randomToken(), Status: "pending"})
		}
		f.authzs[z.id] = z
		o.names = append(o.names, ident.Value)
		o.authzs = append(o.authzs, z.id)
	}
	f.orders[o.id], f.last = o, o
	w.Header().Set("Location", f.url("/order/"+o.id))
	f.writeOrder(w, http.StatusCreated, o)
}

func (f *fakeCA) orderStatus(o *caOrder) string {
	if o.invalid {
		return "invalid"
	}
	if o.valid && (f.processing < 0 || (f.processing > 0 && o.looks <= f.processing)) {
		return "processing"
	}
	if o.valid {
		return "valid"
	}
	status := "ready"
	for _, id := range o.authzs {
		switch f.authzs[id].status {
		case "invalid":
			return "invalid"
		case "pending":
			status = "pending"
		}
	}
	return status
}

func (f *fakeCA) writeOrder(w http.ResponseWriter, status int, o *caOrder) {
	var ids []map[string]string
	var authzs []string
	for i, n := range o.names {
		ids = append(ids, map[string]string{"type": "dns", "value": n})
		authzs = append(authzs, f.url("/authz/"+o.authzs[i]))
	}
	s := f.orderStatus(o)
	v := map[string]any{"status": s, "expires": o.expires.Format(time.RFC3339), "identifiers": ids, "authorizations": authzs, "finalize": f.url("/finalize/" + o.id)}
	if s == "valid" {
		v["certificate"] = f.url("/cert/" + o.id)
	}
	if s == "processing" && f.retryAfter != "" {
		w.Header().Set("Retry-After", f.retryAfter)
	}
	writeJSON(w, status, v)
}

func (f *fakeCA) writeAuthz(w http.ResponseWriter, z *caAuthz) {
	writeJSON(w, http.StatusOK, map[string]any{"status": z.status, "identifier": map[string]string{"type": "dns", "value": z.name}, "challenges": z.chals})
}

func (f *fakeCA) challenge(w http.ResponseWriter, a *caAccount, rest string) {
	id, typ, _ := strings.Cut(rest, "/")
	z := f.authzs[id]
	if z == nil || z.account != a.url {
		f.problem(w, http.StatusNotFound, "malformed", "no such challenge")
		return
	}
	i := slices.IndexFunc(z.chals, func(c *caChallenge) bool { return c.Type == typ })
	if i < 0 {
		f.problem(w, http.StatusNotFound, "malformed", "no such challenge")
		return
	}
	c := z.chals[i]
	f.steps = append(f.steps, "validate "+typ)
	var prob []byte
	if fl, ok := f.fail["validate"]; ok {
		prob = fl.Problem
	} else {
		prob = f.validate(a, z, c)
	}
	if prob != nil {
		c.Status, c.Error, z.status = "invalid", prob, "invalid"
	} else {
		c.Status, z.status = "valid", "valid"
	}
	writeJSON(w, http.StatusOK, c)
}

// validate checks a challenge the way Let's Encrypt does, answering with
// problems in its format.
func (f *fakeCA) validate(a *caAccount, z *caAuthz, c *caChallenge) []byte {
	thumb, err := acme.JWKThumbprint(a.key)
	if err != nil {
		return problemJSON("serverInternal", err.Error())
	}
	keyAuth := c.Token + "." + thumb
	switch c.Type {
	case "http-01":
		shown := "http://" + z.name + challengePath + c.Token
		req, _ := http.NewRequest(http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d%s%s", f.httpPort, challengePath, c.Token), nil)
		req.Host = z.name
		client := &http.Client{Transport: &http.Transport{DisableKeepAlives: true}, Timeout: 5 * time.Second}
		resp, err := client.Do(req)
		if err != nil {
			return problemJSON("connection", "127.0.0.1: Fetching "+shown+": Connection refused")
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		if resp.StatusCode != http.StatusOK {
			return problemJSON("unauthorized", fmt.Sprintf("127.0.0.1: Invalid response from %s: %d", shown, resp.StatusCode))
		}
		if strings.TrimSpace(string(body)) != keyAuth {
			return problemJSON("unauthorized", fmt.Sprintf("127.0.0.1: The key authorization file from the server did not match this challenge. Expected %q (got %q)", keyAuth, body))
		}
	case "dns-01":
		sum := sha256.Sum256([]byte(keyAuth))
		fqdn := "_acme-challenge." + z.name
		var found []string
		if f.txt != nil {
			found = f.txt(fqdn)
		}
		if len(found) == 0 {
			return problemJSON("unauthorized", "No TXT record found at "+fqdn)
		}
		if !slices.Contains(found, b64.EncodeToString(sum[:])) {
			return problemJSON("unauthorized", fmt.Sprintf("Incorrect TXT record %q found at %s", found[0], fqdn))
		}
	default:
		return problemJSON("malformed", "cannot validate "+c.Type)
	}
	return nil
}

func (f *fakeCA) finalize(w http.ResponseWriter, a *caAccount, id string, payload []byte) {
	f.steps = append(f.steps, "finalize")
	if f.failed(w, "finalize") {
		return
	}
	o := f.orders[id]
	if o == nil || o.account != a.url {
		f.problem(w, http.StatusNotFound, "malformed", "no such order")
		return
	}
	if f.orderStatus(o) != "ready" {
		f.problem(w, http.StatusForbidden, "orderNotReady", "Order's status is "+f.orderStatus(o))
		return
	}
	csr, err := parseCSR(payload)
	if err == nil && !slices.Equal(slices.Sorted(slices.Values(csr.DNSNames)), slices.Sorted(slices.Values(o.names))) {
		err = fmt.Errorf("the CSR is for %q, the order for %q", csr.DNSNames, o.names)
	}
	if err != nil {
		f.problem(w, http.StatusBadRequest, "badCSR", err.Error())
		f.t.Errorf("fake CA: %v", err)
		return
	}
	switch f.finalizeAnswer {
	case "lost-early":
		hangUp(w)
		return
	case "slow-early":
		f.ignore(w)
		return
	}
	var chain [][]byte
	if f.chain != nil {
		chain = f.chain(csr)
	} else {
		now := time.Now()
		chain = f.ca.issue(f.t, csr.PublicKey, csr.DNSNames, now.Add(-time.Hour), now.Add(90*24*time.Hour))
	}
	var buf bytes.Buffer
	for _, c := range chain {
		pem.Encode(&buf, &pem.Block{Type: "CERTIFICATE", Bytes: c})
	}
	o.cert, o.valid = buf.Bytes(), true
	switch f.finalizeAnswer {
	case "processing":
		writeJSON(w, http.StatusOK, map[string]any{"status": "processing", "finalize": f.url("/finalize/" + o.id)})
		return
	case "lost":
		hangUp(w)
		return
	case "slow":
		f.ignore(w)
		return
	}
	w.Header().Set("Location", f.url("/order/"+o.id))
	f.writeOrder(w, http.StatusOK, o)
}

// hangUp closes the connection without answering.
func hangUp(w http.ResponseWriter) {
	if conn, _, err := w.(http.Hijacker).Hijack(); err == nil {
		conn.Close()
	}
}

// ignore leaves a request unanswered, its connection open until the test
// ends, as Pebble v2.10.1 does when it deadlocks.
func (f *fakeCA) ignore(w http.ResponseWriter) {
	conn, _, err := w.(http.Hijacker).Hijack()
	if err != nil {
		f.t.Errorf("fake CA: %v", err)
		return
	}
	f.t.Cleanup(func() { conn.Close() })
}

// failed answers with the failure set for step, if there is one.
func (f *fakeCA) failed(w http.ResponseWriter, step string) bool {
	fl, ok := f.fail[step]
	if !ok {
		return false
	}
	for k, v := range fl.Headers {
		w.Header().Set(k, v)
	}
	var text string
	if json.Unmarshal(fl.Problem, &text) == nil {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(fl.Status)
		io.WriteString(w, text)
		return true
	}
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(fl.Status)
	w.Write(fl.Problem)
	return true
}

func (f *fakeCA) problem(w http.ResponseWriter, status int, typ, detail string) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	w.Write(problemJSON(typ, detail))
}

// malformed rejects a request Playkeeper should never send.
func (f *fakeCA) malformed(w http.ResponseWriter, detail string) {
	f.t.Errorf("fake CA: %s", detail)
	f.problem(w, http.StatusBadRequest, "malformed", detail)
}

func (f *fakeCA) newNonce() string {
	n := randomToken()
	f.nonces[n] = true
	return n
}

func problemJSON(typ, detail string) []byte {
	b, _ := json.Marshal(map[string]string{"type": "urn:ietf:params:acme:error:" + typ, "detail": detail})
	return b
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func randomToken() string {
	var b [32]byte
	rand.Read(b[:])
	return b64.EncodeToString(b[:])
}

func parseCSR(payload []byte) (*x509.CertificateRequest, error) {
	var req struct{ CSR string }
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, err
	}
	der, err := b64.DecodeString(req.CSR)
	if err != nil {
		return nil, err
	}
	csr, err := x509.ParseCertificateRequest(der)
	if err != nil {
		return nil, err
	}
	return csr, csr.CheckSignature()
}

func parseJWK(kty, crv, x, y string) (*ecdsa.PublicKey, error) {
	xb, errX := b64.DecodeString(x)
	yb, errY := b64.DecodeString(y)
	if kty != "EC" || crv != "P-256" || errX != nil || errY != nil || len(xb) != 32 || len(yb) != 32 {
		return nil, fmt.Errorf("not a P-256 JWK: %s %s", kty, crv)
	}
	return ecdsa.ParseUncompressedPublicKey(elliptic.P256(), slices.Concat([]byte{4}, xb, yb))
}

func verifyES256(pub *ecdsa.PublicKey, signed, sig string) bool {
	s, err := b64.DecodeString(sig)
	if err != nil || len(s) != 64 {
		return false
	}
	h := sha256.Sum256([]byte(signed))
	return ecdsa.Verify(pub, h[:], new(big.Int).SetBytes(s[:32]), new(big.Int).SetBytes(s[32:]))
}

func TestIssueHTTP01(t *testing.T) {
	f := newFakeCA(t)
	f.terms = f.url("/terms")
	base := t.TempDir()
	is := f.issuer(base)
	is.Email = "admin@example.com"
	h := f.responder(t)
	dir := filepath.Join(base, "certs")
	c, err := is.Issue(t.Context(), Request{Names: []string{"MC.Example.com"}, HTTP01: h, Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "mc.example.com.pem")
	if c.File != file || !slices.Equal(c.Names, []string{"mc.example.com"}) || c.Issuer != "Playkeeper Test T1" ||
		!c.RenewAt.Equal(RenewAt(c.NotBefore, c.NotAfter)) {
		t.Errorf("Issue = %+v", c)
	}
	wantMode(t, file, 0o600)
	wantMode(t, dir, 0o700)
	wantMode(t, is.AccountKeyFile, 0o600)
	if read, err := ReadCertificate(file); err != nil || !reflect.DeepEqual(read, c) {
		t.Errorf("ReadCertificate = %+v, %v; Issue returned %+v", read, err, c)
	}
	accounts := f.accountList()
	if len(accounts) != 1 || !slices.Equal(accounts[0].contact, []string{"mailto:admin@example.com"}) || !accounts[0].agreed {
		t.Errorf("accounts = %+v", accounts)
	}
	if f.count("validate http-01") != 1 || f.count("lookup-account") != 1 {
		t.Errorf("steps = %q", f.steps)
	}
	if h.pending != 0 || h.srv != nil || len(h.tokens) != 0 {
		t.Error("the responder still has a check pending")
	}
	st, err := NewStore(StoreOptions{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if cert, err := st.GetCertificate(hello("mc.example.com")); err != nil || cert.Leaf.DNSNames[0] != "mc.example.com" {
		t.Errorf("the store does not serve the new certificate: %v", err)
	}

	// Renewal reuses the account, looking it up only once.
	f.mu.Lock()
	f.steps = nil
	f.mu.Unlock()
	c2, err := is.Issue(t.Context(), Request{Names: []string{"mc.example.com"}, HTTP01: h, Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if n := len(f.accountList()); n != 1 || f.count("new-acct") != 0 || f.count("lookup-account") != 1 {
		t.Errorf("%d accounts; steps %q", n, f.steps)
	}
	if read, err := ReadCertificate(file); err != nil || read.Serial != c2.Serial || c2.Serial == c.Serial {
		t.Errorf("the renewed certificate was not saved: %v", err)
	}
}

func TestIssueDNS01(t *testing.T) {
	f := newFakeCA(t)
	ch := newChallenger(t)
	f.txt = ch.txt
	base := t.TempDir()
	h := &HTTP01Responder{}
	req := Request{
		Names:  []string{"alex.playkeeper.io"},
		HTTP01: h,
		DNS01:  &DNS01{Challenger: ch, LookupTXT: ch.lookup, Interval: time.Millisecond},
		Dir:    filepath.Join(base, "certs"),
	}
	c, err := f.issuer(base).Issue(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(c.Names, []string{"alex.playkeeper.io"}) {
		t.Errorf("names = %q", c.Names)
	}
	if f.count("validate dns-01") != 1 || f.count("validate http-01") != 0 {
		t.Errorf("DNS-01 was not preferred: %q", f.steps)
	}
	log := ch.log()
	if len(log) != 2 || !strings.HasPrefix(log[0], "set "+alexTXT+" ") || !strings.HasPrefix(log[1], "clear "+alexTXT+" ") || log[0][4:] != log[1][6:] {
		t.Errorf("challenger calls = %q", log)
	}
	if got := ch.txt(alexTXT); len(got) != 0 {
		t.Errorf("records left behind: %q", got)
	}
}

func TestIssueOwnerAndNames(t *testing.T) {
	f := newFakeCA(t)
	base := t.TempDir()
	own := &Owner{UID: os.Getuid(), GID: os.Getgid()}
	dir := filepath.Join(base, "certs")
	c, err := f.issuer(base).Issue(t.Context(), Request{Names: []string{"mc.example.com", "www.example.com"}, HTTP01: f.responder(t), Dir: dir, Owner: own})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(c.Names, []string{"mc.example.com", "www.example.com"}) || f.count("validate http-01") != 2 {
		t.Errorf("names %q, steps %q", c.Names, f.steps)
	}
	wantMode(t, c.File, 0o640)
	wantMode(t, dir, 0o750)
}

func TestIssueValidAuthorizations(t *testing.T) {
	f := newFakeCA(t)
	f.validAuthz = true
	base := t.TempDir()
	if _, err := f.issuer(base).Issue(t.Context(), Request{Names: []string{"mc.example.com"}, HTTP01: &HTTP01Responder{}, Dir: filepath.Join(base, "certs")}); err != nil {
		t.Fatal(err)
	}
	if f.count("validate http-01") != 0 {
		t.Errorf("an authorization that is already valid was checked again: %q", f.steps)
	}
}

func TestIssueBadNonce(t *testing.T) {
	f := newFakeCA(t)
	f.rejectNonces = 2
	base := t.TempDir()
	if _, err := f.issuer(base).Issue(t.Context(), Request{Names: []string{"mc.example.com"}, HTTP01: f.responder(t), Dir: filepath.Join(base, "certs")}); err != nil {
		t.Fatalf("Issue after two rejected nonces = %v", err)
	}
}

func TestIssueFinalizeAnswers(t *testing.T) {
	cases := []struct {
		answer string
		issued bool
		files  []string
	}{
		{"processing", true, []string{"mc.example.com.pem"}},
		{"lost", true, []string{"mc.example.com.pem"}},
		{"lost-early", false, []string{"mc.example.com.order"}},
	}
	for _, c := range cases {
		t.Run(c.answer, func(t *testing.T) {
			f := newFakeCA(t)
			f.finalizeAnswer = c.answer
			base := t.TempDir()
			dir := filepath.Join(base, "certs")
			got, err := f.issuer(base).Issue(t.Context(), Request{Names: []string{"mc.example.com"}, HTTP01: f.responder(t), Dir: dir})
			if c.issued {
				if err != nil {
					t.Fatal(err)
				}
				if read, err := ReadCertificate(got.File); err != nil || read.Serial != got.Serial {
					t.Errorf("the certificate was not saved: %v", err)
				}
			} else {
				wantProblem(t, err, CodeCAUnreachable, "")
			}
			if names := dirNames(t, dir); !slices.Equal(names, c.files) {
				t.Errorf("files %q, want %q", names, c.files)
			}
			if f.count("finalize") != 1 || f.count("order") != 2 || (f.count("cert") == 1) != c.issued {
				t.Errorf("steps = %q", f.steps)
			}
		})
	}
}

// shortWaits shortens validationWait, issuedWait and maxPollWait for a test.
func shortWaits(t *testing.T, validation, issued, poll time.Duration) {
	v, i, p := validationWait, issuedWait, maxPollWait
	validationWait, issuedWait, maxPollWait = validation, issued, poll
	t.Cleanup(func() { validationWait, issuedWait, maxPollWait = v, i, p })
}

// issueWaiting issues a certificate from f, whose finalized order answers
// "processing" with a Retry-After of an hour, with requests that time out
// after timeout. Like an address operation, Issue has a deadline of its own,
// later than the waits for the certificate.
func issueWaiting(t *testing.T, f *fakeCA, timeout time.Duration) (*Certificate, error) {
	f.validAuthz, f.retryAfter = true, "3600"
	base := t.TempDir()
	is := f.issuer(base)
	is.Client = &http.Client{Timeout: timeout, Transport: f.srv.Client().Transport}
	ctx, cancel := context.WithTimeout(t.Context(), validationWait+issuedWait+5*time.Second)
	defer cancel()
	return is.Issue(ctx, Request{Names: []string{"mc.example.com"}, HTTP01: &HTTP01Responder{}, Dir: filepath.Join(base, "certs")})
}

func (f *fakeCA) looks() []time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.looked)
}

// TestIssueWaitsForTheCertificate: once the order is finalized, it is looked
// at again within maxPollWait whatever Retry-After the certificate authority
// asks for, and a look that gets no answer is tried again.
func TestIssueWaitsForTheCertificate(t *testing.T) {
	shortWaits(t, 4*time.Second, 4*time.Second, time.Second)
	const timeout = time.Second
	cases := []struct {
		name       string
		finalize   string
		processing int
		unanswered int
		wait       time.Duration // between the two looks
	}{
		{"one more look finds it processing, like Pebble", "processing", 1, 0, maxPollWait},
		{"one more look finds it processing, like Let's Encrypt", "", 1, 0, maxPollWait},
		{"a look gets no answer", "processing", 0, 1, timeout + maxPollWait},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFakeCA(t)
			f.finalizeAnswer, f.processing, f.unanswered = c.finalize, c.processing, c.unanswered
			got, err := issueWaiting(t, f, timeout)
			if err != nil {
				t.Fatal(err)
			}
			if read, err := ReadCertificate(got.File); err != nil || read.Serial != got.Serial {
				t.Errorf("the certificate was not saved: %v", err)
			}
			looked := f.looks()
			if len(looked) != 2 || f.count("finalize") != 1 || f.count("cert") != 1 {
				t.Fatalf("%d looks at the finalized order; steps %q", len(looked), f.steps)
			}
			if gap := looked[1].Sub(looked[0]); gap > c.wait+time.Second {
				t.Errorf("%s between the looks, want at most %s", gap.Round(time.Millisecond), c.wait)
			}
		})
	}
}

// TestIssueTimesOutWaitingForTheCertificate: a certificate that is not issued
// by the end of the waits for it is a timeout, not a refusal by the
// certificate authority, and no other order is made. The finalize request
// waits validationWait at most, and the looks at the order after it fails
// issuedWait.
func TestIssueTimesOutWaitingForTheCertificate(t *testing.T) {
	shortWaits(t, 2*time.Second, 2*time.Second, time.Second)
	cases := []struct {
		name       string
		finalize   string
		processing int
		unanswered int
		want       time.Duration // until it gives up
	}{
		{"still processing, like Let's Encrypt", "", -1, 0, validationWait + issuedWait},
		{"no answers, like a deadlocked Pebble", "processing", 0, -1, issuedWait},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFakeCA(t)
			f.finalizeAnswer, f.processing, f.unanswered = c.finalize, c.processing, c.unanswered
			start := time.Now()
			_, err := issueWaiting(t, f, 500*time.Millisecond)
			took := time.Since(start)
			p := wantProblem(t, err, CodeIssuanceTimeout, "")
			if p.NeedsAction || !p.RetryAt.IsZero() || p.Detail == "" {
				t.Errorf("problem = %+v", p)
			}
			if took < c.want || took > c.want+time.Second {
				t.Errorf("gave up after %s, want %s", took.Round(time.Millisecond), c.want)
			}
			if n := len(f.looks()); n < 2 || f.count("new-order") != 1 || f.count("finalize") != 1 {
				t.Errorf("%d looks at the finalized order; steps %q", n, f.steps)
			}
		})
	}
}

// TestIssueKeepsTheOrder: a certificate issued just after the finalize
// request timed out is fetched with a wait of its own, and an attempt that
// fails between the finalize request and saving the certificate leaves the
// next one the order, so that it gets the certificate without a new order.
// An order that can give no certificate for the names is dropped for a new
// one; when the certificate authority can't be asked about it now, it is
// kept.
func TestIssueKeepsTheOrder(t *testing.T) {
	shortWaits(t, 1500*time.Millisecond, 2*time.Second, time.Second)
	const pemName, orderName = "mc.example.com.pem", "mc.example.com.order"
	type attempt struct {
		before func(t *testing.T, f *fakeCA, dir string) // with f.mu held
		names  []string                                  // nil means mc.example.com
		later  time.Duration                             // how far ahead the Issuer's clock is
		code   string                                    // of the Problem; "" means a certificate
		files  []string                                  // in the directory afterwards
	}
	answer := func(a string) func(*testing.T, *fakeCA, string) {
		return func(_ *testing.T, f *fakeCA, _ string) { f.finalizeAnswer = a }
	}
	looksFail := func(fl caFailure) func(*testing.T, *fakeCA, string) {
		return func(_ *testing.T, f *fakeCA, _ string) { f.finalizeAnswer, f.fail["order"] = "", fl }
	}
	looksWork := func(_ *testing.T, f *fakeCA, _ string) { delete(f.fail, "order") }
	plant := func(content []byte) func(*testing.T, *fakeCA, string) {
		return func(t *testing.T, _ *fakeCA, dir string) {
			if err := os.MkdirAll(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, orderName), content, 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	block := func(file string) func(*testing.T, *fakeCA, string) {
		return func(t *testing.T, _ *fakeCA, dir string) {
			if err := os.MkdirAll(filepath.Join(dir, file, "x"), 0o700); err != nil {
				t.Fatal(err)
			}
		}
	}
	unblock := func(file string) func(*testing.T, *fakeCA, string) {
		return func(t *testing.T, _ *fakeCA, dir string) {
			if err := os.RemoveAll(filepath.Join(dir, file)); err != nil {
				t.Fatal(err)
			}
		}
	}
	key, err := x509.MarshalECPrivateKey(newKey(t))
	if err != nil {
		t.Fatal(err)
	}
	elsewhere, err := json.Marshal(orderFile{URL: "https://acme.other.example/order/1", Expires: time.Now().Add(24 * time.Hour), Key: key})
	if err != nil {
		t.Fatal(err)
	}
	unavailable := caFailure{Status: 503, Headers: map[string]string{"Retry-After": "300"}, Problem: json.RawMessage(`"<h1>Service Unavailable</h1>"`)}
	limited := caFailure{Status: 429, Headers: map[string]string{"Retry-After": "3600"}, Problem: problemJSON("rateLimited", "too many requests")}
	cases := []struct {
		name                       string
		ca                         func(f *fakeCA)
		attempts                   []attempt
		orders, finalized, fetched int
	}{
		{
			name:     "a slow finalize answer: the certificate issued meanwhile is fetched",
			ca:       func(f *fakeCA) { f.finalizeAnswer = "slow" },
			attempts: []attempt{{files: []string{pemName}}},
			orders:   1, finalized: 1, fetched: 1,
		},
		{
			name:     "the wait of the finalize request runs out: the certificate issued meanwhile is fetched",
			ca:       func(f *fakeCA) { f.processing = 2 },
			attempts: []attempt{{files: []string{pemName}}},
			orders:   1, finalized: 1, fetched: 1,
		},
		{
			name: "a slow finalize answer and a certificate issued later: the next attempt fetches it",
			ca:   func(f *fakeCA) { f.finalizeAnswer, f.processing = "slow", -1 },
			attempts: []attempt{
				{code: CodeIssuanceTimeout, files: []string{orderName}},
				{before: func(_ *testing.T, f *fakeCA, _ string) { f.processing = 0 }, files: []string{pemName}},
			},
			orders: 1, finalized: 1, fetched: 1,
		},
		{
			name: "the finalize answer is lost before issuing: the next attempt finalizes the same order",
			ca:   func(f *fakeCA) { f.finalizeAnswer = "lost-early" },
			attempts: []attempt{
				{code: CodeCAUnreachable, files: []string{orderName}},
				{before: answer(""), files: []string{pemName}},
			},
			orders: 1, finalized: 2, fetched: 1,
		},
		{
			name: "a slow finalize request that is never carried out: the next attempt finalizes the same order",
			ca:   func(f *fakeCA) { f.finalizeAnswer = "slow-early" },
			attempts: []attempt{
				{code: CodeIssuanceTimeout, files: []string{orderName}},
				{before: answer(""), files: []string{pemName}},
			},
			orders: 1, finalized: 2, fetched: 1,
		},
		{
			name: "saving the certificate fails: the next attempt fetches it again",
			attempts: []attempt{
				{before: block(pemName), code: CodeSaveFailed, files: []string{orderName, pemName}},
				{before: unblock(pemName), files: []string{pemName}},
			},
			orders: 1, finalized: 1, fetched: 2,
		},
		{
			name: "the kept order turned invalid: the next attempt makes a new one",
			ca:   func(f *fakeCA) { f.finalizeAnswer = "lost-early" },
			attempts: []attempt{
				{code: CodeCAUnreachable, files: []string{orderName}},
				{before: func(_ *testing.T, f *fakeCA, _ string) { f.finalizeAnswer, f.last.invalid = "", true }, files: []string{pemName}},
			},
			orders: 2, finalized: 2, fetched: 1,
		},
		{
			name: "the kept order expired: the next attempt makes a new one",
			ca:   func(f *fakeCA) { f.finalizeAnswer = "lost-early" },
			attempts: []attempt{
				{code: CodeCAUnreachable, files: []string{orderName}},
				{before: answer(""), later: 8 * 24 * time.Hour, files: []string{pemName}},
			},
			orders: 2, finalized: 2, fetched: 1,
		},
		{
			name: "the certificate authority no longer knows the kept order: the next attempt makes a new one",
			ca:   func(f *fakeCA) { f.finalizeAnswer = "lost-early" },
			attempts: []attempt{
				{code: CodeCAUnreachable, files: []string{orderName}},
				{before: func(_ *testing.T, f *fakeCA, _ string) { f.finalizeAnswer = ""; delete(f.orders, f.last.id) }, files: []string{pemName}},
			},
			orders: 2, finalized: 2, fetched: 1,
		},
		{
			name: "the kept order is for other names: the next attempt makes a new one",
			ca:   func(f *fakeCA) { f.finalizeAnswer = "lost-early" },
			attempts: []attempt{
				{names: []string{"mc.example.com", "www.example.com"}, code: CodeCAUnreachable, files: []string{orderName}},
				{before: answer(""), files: []string{pemName}},
			},
			orders: 2, finalized: 2, fetched: 1,
		},
		{
			name:     "the kept order is at another certificate authority: a new one is made",
			attempts: []attempt{{before: plant(elsewhere), files: []string{pemName}}},
			orders:   1, finalized: 1, fetched: 1,
		},
		{
			name:     "the file of the kept order is damaged: a new one is made",
			attempts: []attempt{{before: plant([]byte("not an order\n")), files: []string{pemName}}},
			orders:   1, finalized: 1, fetched: 1,
		},
		{
			name: "the certificate authority is unavailable: the order is kept for the attempt after",
			ca:   func(f *fakeCA) { f.finalizeAnswer = "lost-early" },
			attempts: []attempt{
				{code: CodeCAUnreachable, files: []string{orderName}},
				{before: looksFail(unavailable), code: CodeCAUnavailable, files: []string{orderName}},
				{before: looksWork, files: []string{pemName}},
			},
			orders: 1, finalized: 2, fetched: 1,
		},
		{
			name: "a rate limit at the certificate authority: the order is kept for the attempt after",
			ca:   func(f *fakeCA) { f.finalizeAnswer = "lost-early" },
			attempts: []attempt{
				{code: CodeCAUnreachable, files: []string{orderName}},
				{before: looksFail(limited), code: CodeRateLimited, files: []string{orderName}},
				{before: looksWork, files: []string{pemName}},
			},
			orders: 1, finalized: 2, fetched: 1,
		},
		{
			name: "the certificate authority refuses the finalize request: the next attempt makes a new order",
			ca: func(f *fakeCA) {
				f.fail["finalize"] = caFailure{Status: 400, Problem: problemJSON("badCSR", "Error finalizing order :: invalid public key in CSR")}
			},
			attempts: []attempt{
				{code: CodeCAError},
				{before: func(_ *testing.T, f *fakeCA, _ string) { delete(f.fail, "finalize") }, files: []string{pemName}},
			},
			orders: 2, finalized: 2, fetched: 1,
		},
		{
			name:     "keeping the order fails: it is not finalized",
			attempts: []attempt{{before: block(orderName), code: CodeSaveFailed, files: []string{orderName}}},
			orders:   1, finalized: 0, fetched: 0,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFakeCA(t)
			f.validAuthz = true
			if c.ca != nil {
				c.ca(f)
			}
			base := t.TempDir()
			dir := filepath.Join(base, "certs")
			for i, a := range c.attempts {
				if a.before != nil {
					func() {
						f.mu.Lock()
						defer f.mu.Unlock()
						a.before(t, f, dir)
					}()
				}
				is := f.issuer(base)
				is.Now = func() time.Time { return time.Now().Add(a.later) }
				names := a.names
				if names == nil {
					names = []string{"mc.example.com"}
				}
				got, err := is.Issue(t.Context(), Request{Names: names, HTTP01: &HTTP01Responder{}, Dir: dir})
				var p *Problem
				switch {
				case a.code == "" && err != nil:
					t.Fatalf("attempt %d: %v", i+1, err)
				case a.code == "":
					if read, err := ReadCertificate(got.File); err != nil || read.Serial != got.Serial {
						t.Errorf("attempt %d: the certificate was not saved: %v", i+1, err)
					}
				case !errors.As(err, &p) || p.Code != a.code:
					t.Fatalf("attempt %d: %#v, want a %s problem", i+1, err, a.code)
				}
				if files := dirNames(t, dir); !slices.Equal(files, a.files) {
					t.Errorf("after attempt %d: files %q, want %q", i+1, files, a.files)
				}
			}
			if f.count("new-order") != c.orders || f.count("finalize") != c.finalized || f.count("cert") != c.fetched {
				t.Errorf("steps = %q", f.steps)
			}
		})
	}
}

func TestIssueRefusesEarly(t *testing.T) {
	f := newFakeCA(t)
	base := t.TempDir()
	dir := filepath.Join(base, "certs")
	cases := []struct {
		name  string
		edit  func(*Issuer, *Request)
		code  string
		kind  string
		steps int
	}{
		{"invalid name", func(_ *Issuer, r *Request) { r.Names = []string{"minecraft.local"} }, CodeInvalidName, "reserved_tld", 0},
		{"no challenge", func(_ *Issuer, r *Request) { r.HTTP01 = nil; r.DNS01 = &DNS01{} }, CodeConfig, "no_challenge", 0},
		{"no directory", func(_ *Issuer, r *Request) { r.Dir = "" }, CodeConfig, "no_dir", 0},
		{"invalid email", func(is *Issuer, _ *Request) { is.Email = "Admin <admin@example.com>" }, CodeInvalidEmail, "", 0},
		{"http directory", func(is *Issuer, _ *Request) { is.DirectoryURL = "http://" + f.srv.Listener.Addr().String() + "/dir" }, CodeConfig, "directory_url", 0},
		{"credentials in the directory URL", func(is *Issuer, _ *Request) {
			is.DirectoryURL = strings.Replace(f.url("/dir"), "https://", "https://user:secret@", 1)
		}, CodeConfig, "directory_url", 0},
		{"no account key file", func(is *Issuer, _ *Request) { is.AccountKeyFile = "" }, CodeConfig, "no_account_key", 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f.mu.Lock()
			f.steps = nil
			f.mu.Unlock()
			is := f.issuer(base)
			req := Request{Names: []string{"mc.example.com"}, HTTP01: &HTTP01Responder{}, Dir: dir}
			c.edit(is, &req)
			_, err := is.Issue(t.Context(), req)
			p := wantProblem(t, err, c.code, c.kind)
			if strings.Contains(fmt.Sprint(p.Message, p.Detail, p.Params), "secret") {
				t.Errorf("the problem shows a password: %+v", p)
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			if len(f.steps) != c.steps {
				t.Errorf("requests were made: %q", f.steps)
			}
		})
	}
}

func TestIssueTermsNotAccepted(t *testing.T) {
	f := newFakeCA(t)
	f.terms = f.url("/terms-v2")
	base := t.TempDir()
	is := f.issuer(base)
	is.AgreedTerms = f.url("/terms-v1")
	_, err := is.Issue(t.Context(), Request{Names: []string{"mc.example.com"}, HTTP01: &HTTP01Responder{}, Dir: filepath.Join(base, "certs")})
	p := wantProblem(t, err, CodeTermsNotAccepted, "")
	if p.Params["url"] != f.terms || !p.NeedsAction {
		t.Errorf("problem = %+v", p)
	}
	if f.count("new-acct") != 0 || f.count("new-order") != 0 {
		t.Errorf("steps = %q", f.steps)
	}
	if terms, err := is.Terms(t.Context()); err != nil || terms != f.terms {
		t.Errorf("Terms = %q, %v", terms, err)
	}
}

func TestIssueExternalAccountRequired(t *testing.T) {
	f := newFakeCA(t)
	f.eabRequired = true
	base := t.TempDir()
	_, err := f.issuer(base).Issue(t.Context(), Request{Names: []string{"mc.example.com"}, HTTP01: &HTTP01Responder{}, Dir: filepath.Join(base, "certs")})
	wantProblem(t, err, CodeCANeedsAccount, "")
	if f.count("lookup-account") != 0 {
		t.Errorf("steps = %q", f.steps)
	}
}

func TestIssuePort80Busy(t *testing.T) {
	f := newFakeCA(t)
	base := t.TempDir()
	h := f.responder(t)
	ln, err := net.Listen("tcp", h.Addr)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	_, err = f.issuer(base).Issue(t.Context(), Request{Names: []string{"mc.example.com"}, HTTP01: h, Dir: filepath.Join(base, "certs")})
	wantProblem(t, err, CodePort80Busy, "")
	if f.count("validate http-01") != 0 {
		t.Errorf("the challenge was accepted: %q", f.steps)
	}
}

func TestIssueChallengeNotOffered(t *testing.T) {
	f := newFakeCA(t)
	base := t.TempDir()
	ch := newChallenger(t)
	cases := []struct {
		offer     []string
		req       Request
		challenge string
	}{
		{[]string{"tls-alpn-01"}, Request{HTTP01: &HTTP01Responder{}}, "http-01"},
		{[]string{"dns-01"}, Request{HTTP01: &HTTP01Responder{}}, "http-01"},
		{[]string{"http-01"}, Request{DNS01: &DNS01{Challenger: ch}}, "dns-01"},
	}
	for _, c := range cases {
		f.mu.Lock()
		f.offer = c.offer
		f.mu.Unlock()
		c.req.Names, c.req.Dir = []string{"mc.example.com"}, filepath.Join(base, "certs")
		_, err := f.issuer(base).Issue(t.Context(), c.req)
		p := wantProblem(t, err, CodeChallengeNotOffered, "")
		if p.Params["challenge"] != c.challenge || p.Params["name"] != "mc.example.com" {
			t.Errorf("offering %q: params %v", c.offer, p.Params)
		}
	}
	if len(ch.log()) != 0 {
		t.Errorf("records were published: %q", ch.log())
	}
}

func TestIssueBadChain(t *testing.T) {
	f := newFakeCA(t)
	other := newTestCA(t)
	now := time.Now()
	day := 24 * time.Hour
	cases := map[string]func(csr *x509.CertificateRequest) [][]byte{
		"other key": func(csr *x509.CertificateRequest) [][]byte {
			return f.ca.issue(t, &newKey(t).PublicKey, csr.DNSNames, now.Add(-time.Hour), now.Add(90*day))
		},
		"other name": func(csr *x509.CertificateRequest) [][]byte {
			return f.ca.issue(t, csr.PublicKey, []string{"other.example.com"}, now.Add(-time.Hour), now.Add(90*day))
		},
		"expired": func(csr *x509.CertificateRequest) [][]byte {
			return f.ca.issue(t, csr.PublicKey, csr.DNSNames, now.Add(-100*day), now.Add(-10*day))
		},
		"not yet valid": func(csr *x509.CertificateRequest) [][]byte {
			return f.ca.issue(t, csr.PublicKey, csr.DNSNames, now.Add(day), now.Add(90*day))
		},
		"wrong intermediate": func(csr *x509.CertificateRequest) [][]byte {
			chain := f.ca.issue(t, csr.PublicKey, csr.DNSNames, now.Add(-time.Hour), now.Add(90*day))
			return [][]byte{chain[0], other.inter.Raw}
		},
		"garbage": func(*x509.CertificateRequest) [][]byte { return [][]byte{[]byte("not a certificate")} },
	}
	for name, chain := range cases {
		t.Run(name, func(t *testing.T) {
			f.mu.Lock()
			f.chain = chain
			f.mu.Unlock()
			base := t.TempDir()
			dir := filepath.Join(base, "certs")
			_, err := f.issuer(base).Issue(t.Context(), Request{Names: []string{"mc.example.com"}, HTTP01: f.responder(t), Dir: dir})
			wantProblem(t, err, CodeBadCertificate, "")
			if names := dirNames(t, dir); len(names) != 0 {
				t.Errorf("saved %q", names)
			}
		})
	}
}

func TestIssueCanceled(t *testing.T) {
	f := newFakeCA(t)
	base := t.TempDir()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := f.issuer(base).Issue(ctx, Request{Names: []string{"mc.example.com"}, HTTP01: &HTTP01Responder{}, Dir: filepath.Join(base, "certs")})
	wantProblem(t, err, CodeCanceled, "")
}

func TestGuardedClient(t *testing.T) {
	f := newFakeCA(t)
	huge := strings.Repeat("a", maxACMEResponse+1)
	cases := map[string]func(w http.ResponseWriter){
		"redirect": func(w http.ResponseWriter) {
			w.Header().Set("Location", f.url("/elsewhere"))
			w.WriteHeader(http.StatusFound)
		},
		"too large": func(w http.ResponseWriter) {
			w.Header().Set("Content-Length", strconv.Itoa(len(huge)+2))
			io.WriteString(w, `"`+huge+`"`)
		},
		"too large, chunked": func(w http.ResponseWriter) {
			io.WriteString(w, `{"newNonce": "`)
			w.(http.Flusher).Flush()
			io.WriteString(w, huge+`"}`)
		},
		"another host": func(w http.ResponseWriter) {
			writeJSON(w, http.StatusOK, map[string]any{"newNonce": "https://acme.other.example/nonce", "newAccount": "https://acme.other.example/new-acct", "newOrder": "https://acme.other.example/new-order"})
		},
		"plain http": func(w http.ResponseWriter) {
			u := strings.Replace(f.url(""), "https://", "http://", 1)
			writeJSON(w, http.StatusOK, map[string]any{"newNonce": u + "/nonce", "newAccount": u + "/new-acct", "newOrder": u + "/new-order"})
		},
	}
	for name, dir := range cases {
		t.Run(name, func(t *testing.T) {
			f.mu.Lock()
			f.directory = func(w http.ResponseWriter) bool {
				w.Header().Del("Replay-Nonce")
				dir(w)
				return true
			}
			f.steps = nil
			f.mu.Unlock()
			base := t.TempDir()
			_, err := f.issuer(base).Issue(t.Context(), Request{Names: []string{"mc.example.com"}, HTTP01: &HTTP01Responder{}, Dir: filepath.Join(base, "certs")})
			p := wantProblem(t, err, CodeCAError, "")
			var refused *refusedError
			if !errors.As(err, &refused) {
				t.Errorf("not refused by the guard: %v", err)
			}
			if f.count("lookup-account") != 0 {
				t.Errorf("steps = %q", f.steps)
			}
			if len(p.Detail) > 450 {
				t.Errorf("detail of %d bytes", len(p.Detail))
			}
		})
	}
}

func TestIssuerDefaults(t *testing.T) {
	c, s, err := (&Issuer{}).client(false)
	if err != nil {
		t.Fatal(err)
	}
	if c.DirectoryURL != acme.LetsEncryptURL || s.host != "acme-v02.api.letsencrypt.org" || c.Key != nil {
		t.Errorf("client = %s, host %s", c.DirectoryURL, s.host)
	}
	if c.HTTPClient.Timeout != 30*time.Second || c.HTTPClient.CheckRedirect == nil {
		t.Errorf("HTTP client = %+v", c.HTTPClient)
	}
	f := newFakeCA(t)
	dir := t.TempDir()
	is := f.issuer(dir)
	if terms, err := is.Terms(t.Context()); err != nil || terms != "" {
		t.Errorf("Terms without terms = %q, %v", terms, err)
	}
	if _, err := os.Stat(is.AccountKeyFile); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("Terms created an account key: %v", err)
	}
}

func TestRetryBackoff(t *testing.T) {
	resp := func(code int, retryAfter string) *http.Response {
		r := &http.Response{StatusCode: code, Header: http.Header{}}
		if retryAfter != "" {
			r.Header.Set("Retry-After", retryAfter)
		}
		return r
	}
	cases := []struct {
		n    int
		resp *http.Response
		want time.Duration
	}{
		{1, nil, 0},
		{1, resp(429, ""), 0},
		{1, resp(429, "1"), 0},
		{1, resp(400, ""), 100 * time.Millisecond},
		{1, resp(503, ""), time.Second},
		{2, resp(503, ""), 2 * time.Second},
		{3, resp(500, ""), 4 * time.Second},
		{4, resp(503, ""), 0},
		{1, resp(503, "5"), 5 * time.Second},
		{1, resp(503, "120"), 0},
		{1, resp(503, "soon"), time.Second},
	}
	for _, c := range cases {
		if got := retryBackoff(c.n, nil, c.resp); got != c.want {
			t.Errorf("retryBackoff(%d, %v) = %s, want %s", c.n, c.resp, got, c.want)
		}
	}
}

func TestCheckEmail(t *testing.T) {
	for _, e := range []string{"admin@example.com", "a.b+mc@sub.example.co.uk"} {
		if err := checkEmail(e); err != nil {
			t.Errorf("checkEmail(%q) = %v", e, err)
		}
	}
	bad := []string{
		"not an email", "admin", "admin@", "@example.com", "Admin <admin@example.com>", "<admin@example.com>",
		"admin@example.com, other@example.com", "admin@example.com;other@example.com", `"a b"@example.com`,
		strings.Repeat("a", 250) + "@example.com", "admin@example.com\n",
	}
	for _, e := range bad {
		wantProblem(t, checkEmail(e), CodeInvalidEmail, "")
	}
}

func TestCheckChain(t *testing.T) {
	ca := newTestCA(t)
	key := newKey(t)
	now := time.Now()
	chain := ca.issue(t, &key.PublicKey, []string{"mc.example.com"}, now.Add(-time.Hour), now.Add(90*24*time.Hour))
	if leaf, err := checkChain(chain, key, []string{"mc.example.com"}, now); err != nil || leaf.DNSNames[0] != "mc.example.com" {
		t.Errorf("checkChain = %v", err)
	}
	if _, err := checkChain(chain[:1], key, []string{"mc.example.com"}, now); err != nil {
		t.Errorf("a chain without its intermediate = %v", err)
	}
	if _, err := checkChain(nil, key, []string{"mc.example.com"}, now); err == nil {
		t.Error("an empty chain was accepted")
	}
	if _, err := checkChain(chain, key, []string{"mc.example.com"}, now.Add(-2*time.Hour)); err == nil {
		t.Error("a certificate from the future was accepted")
	}
	if _, err := checkChain(chain, key, []string{"mc.example.com"}, now.Add(-time.Hour-4*time.Minute)); err != nil {
		t.Errorf("a clock a few minutes behind = %v", err)
	}
}

// problemFixtures are problem documents in the format Let's Encrypt sends,
// each answered at one step of issuing a certificate.
type problemFixtures struct {
	Note  string    `json:"note"`
	Now   time.Time `json:"now"`
	Cases []struct {
		Name      string   `json:"name"`
		Step      string   `json:"step"`
		Challenge string   `json:"challenge"`
		Names     []string `json:"names"`
		Email     string   `json:"email"`
		caFailure
		Want struct {
			Code        string            `json:"code"`
			Params      map[string]string `json:"params"`
			RetryAt     time.Time         `json:"retryAt"`
			NeedsAction bool              `json:"needsAction"`
			Detail      string            `json:"detail"`
		} `json:"want"`
	} `json:"cases"`
}

func TestIssueProblems(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("testdata", "acme", "problems.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fx problemFixtures
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&fx); err != nil {
		t.Fatal(err)
	}
	for _, c := range fx.Cases {
		t.Run(c.Name, func(t *testing.T) {
			f := newFakeCA(t)
			f.fail[c.Step] = c.caFailure
			ch := newChallenger(t)
			f.txt = ch.txt
			base := t.TempDir()
			is := f.issuer(base)
			is.Now = func() time.Time { return fx.Now }
			is.Email = c.Email
			req := Request{Names: c.Names, Dir: filepath.Join(base, "certs")}
			if req.Names == nil {
				req.Names = []string{"mc.example.com"}
			}
			if c.Challenge == "dns-01" {
				req.DNS01 = &DNS01{Challenger: ch, LookupTXT: ch.lookup, Interval: time.Millisecond}
			} else {
				req.HTTP01 = f.responder(t)
			}
			_, err := is.Issue(t.Context(), req)
			p := problemOf(t, err)
			w := c.Want
			if p.Code != w.Code || !maps.Equal(p.Params, w.Params) || !p.RetryAt.Equal(w.RetryAt) || p.NeedsAction != w.NeedsAction {
				t.Errorf("got  %s %v retry %v action %v\nwant %s %v retry %v action %v\nmessage %q, detail %q",
					p.Code, p.Params, p.RetryAt, p.NeedsAction, w.Code, w.Params, w.RetryAt, w.NeedsAction, p.Message, p.Detail)
			}
			if w.Detail != "" && p.Detail != w.Detail {
				t.Errorf("detail = %q, want %q", p.Detail, w.Detail)
			}
			if strings.ContainsAny(p.Message+p.Hint, "{}") || p.Message == "" {
				t.Errorf("text %q / %q", p.Message, p.Hint)
			}
			if f.count(c.Step) == 0 && !strings.HasPrefix(c.Step, "validate") {
				t.Errorf("step %s never ran: %q", c.Step, f.steps)
			}
			if got := ch.txt("_acme-challenge." + req.Names[0]); len(got) != 0 {
				t.Errorf("records left behind: %q", got)
			}
		})
	}
}
