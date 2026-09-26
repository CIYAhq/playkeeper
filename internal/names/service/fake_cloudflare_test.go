package service

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
)

const (
	testToken  = "cF7tQ2wX9zL4mN8pR1sV5yB3dG6hJ0kE-u_A2cFt"
	testZoneID = "023e105f4ecef8ad9ca31a8372d0c353"
	testBase   = "playkeeper.io"
)

// fakeCloudflare stands in for the parts of Cloudflare's API v4 the service
// uses. It starts from the zone in testdata/cloudflare, checks writes the
// way Cloudflare does (content, TTL, comment length, proxying, CNAME
// conflicts, identical records, the record quota), answers with Cloudflare's
// envelopes and error bodies, and logs every change. On top of that it
// fails the test if the service ever writes a record without its marker.
type fakeCloudflare struct {
	t      *testing.T
	srv    *httptest.Server
	token  string
	zoneID string

	mu        sync.Mutex
	zone      map[string]any
	records   []fakeRecord
	protected []fakeRecord
	nextID    int
	quota     *int
	requests  int
	writes    []string
	failures  []*fakeFailure
}

// fakeRecord is a DNS record as Cloudflare lists it.
type fakeRecord struct {
	ID                string         `json:"id"`
	Name              string         `json:"name"`
	Type              string         `json:"type"`
	Content           string         `json:"content"`
	Priority          *int           `json:"priority,omitempty"`
	Proxiable         bool           `json:"proxiable"`
	Proxied           bool           `json:"proxied"`
	TTL               int            `json:"ttl"`
	Data              *cfSRV         `json:"data,omitempty"`
	Settings          map[string]any `json:"settings"`
	Meta              map[string]any `json:"meta"`
	Comment           *string        `json:"comment"`
	Tags              []string       `json:"tags"`
	CreatedOn         string         `json:"created_on"`
	ModifiedOn        string         `json:"modified_on"`
	CommentModifiedOn string         `json:"comment_modified_on,omitempty"`
}

func (r fakeRecord) comment() string {
	if r.Comment == nil {
		return ""
	}
	return *r.Comment
}

// cf is r as the service's client decodes it.
func (r fakeRecord) cf() cfRecord {
	proxied := r.Proxied
	return cfRecord{ID: r.ID, Type: r.Type, Name: r.Name, Content: r.Content, Data: r.Data, TTL: r.TTL, Proxied: &proxied, Comment: r.comment()}
}

// fakeWrite is the body of a create or update.
type fakeWrite struct {
	ID       *string  `json:"id"`
	Type     *string  `json:"type"`
	Name     *string  `json:"name"`
	Content  *string  `json:"content"`
	Data     *cfSRV   `json:"data"`
	TTL      *int     `json:"ttl"`
	Proxied  *bool    `json:"proxied"`
	Comment  *string  `json:"comment"`
	Priority *int     `json:"priority"`
	Tags     []string `json:"tags"`
}

// fakeFailure makes matching requests fail with a Cloudflare error body.
type fakeFailure struct {
	method     string
	status     int
	fixture    string
	body       string
	retryAfter string
	left       int
}

const fakeTime = "2026-09-25T12:00:00.000000Z"

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "cloudflare", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func newFakeCloudflare(t *testing.T) *fakeCloudflare {
	f := &fakeCloudflare{t: t, token: testToken, zoneID: testZoneID, nextID: 0xc0ffee000}
	var zone struct {
		Result map[string]any `json:"result"`
	}
	if err := json.Unmarshal(readFixture(t, "zone.json"), &zone); err != nil {
		t.Fatal(err)
	}
	f.zone = zone.Result
	var list struct {
		Result []fakeRecord `json:"result"`
	}
	if err := json.Unmarshal(readFixture(t, "dns_records.json"), &list); err != nil {
		t.Fatal(err)
	}
	f.records = list.Result
	f.protected = slices.Clone(list.Result)
	quota := 200
	f.quota = &quota
	f.srv = httptest.NewServer(f)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeCloudflare) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests++
	if r.Header.Get("Authorization") != "Bearer "+f.token {
		f.fixture(w, http.StatusBadRequest, "error_auth.json")
		return
	}
	for _, fl := range f.failures {
		if fl.left != 0 && (fl.method == "" || fl.method == r.Method) {
			fl.left--
			if fl.retryAfter != "" {
				w.Header().Set("Retry-After", fl.retryAfter)
			}
			if fl.body != "" {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(fl.status)
				_, _ = w.Write([]byte(fl.body))
				return
			}
			f.fixture(w, fl.status, fl.fixture)
			return
		}
	}
	rest, _ := strings.CutPrefix(r.URL.Path, "/client/v4/zones/")
	parts := strings.Split(rest, "/")
	if parts[0] != f.zoneID || !strings.HasPrefix(r.URL.Path, "/client/v4/zones/") {
		f.badRoute(w, r)
		return
	}
	switch {
	case len(parts) == 1 && r.Method == http.MethodGet:
		f.result(w, f.zone)
	case len(parts) == 2 && parts[1] == "dns_records" && r.Method == http.MethodGet:
		f.list(w, r)
	case len(parts) == 2 && parts[1] == "dns_records" && r.Method == http.MethodPost:
		f.create(w, r)
	case len(parts) == 3 && parts[1] == "dns_records" && parts[2] == "usage" && r.Method == http.MethodGet:
		f.result(w, map[string]any{"record_quota": f.quota, "record_usage": len(f.records)})
	case len(parts) == 3 && parts[1] == "dns_records" && r.Method == http.MethodPatch:
		f.update(w, r, parts[2])
	case len(parts) == 3 && parts[1] == "dns_records" && r.Method == http.MethodDelete:
		f.delete(w, parts[2])
	default:
		f.badRoute(w, r)
	}
}

func (f *fakeCloudflare) fixture(w http.ResponseWriter, status int, name string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(readFixture(f.t, name))
}

func (f *fakeCloudflare) badRoute(w http.ResponseWriter, r *http.Request) {
	body := bytes.ReplaceAll(readFixture(f.t, "error_bad_route.json"), []byte("/client/v4/zones/not-a-zone/dns_records"), []byte(r.URL.Path))
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusNotFound)
	_, _ = w.Write(body)
}

func (f *fakeCloudflare) result(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"result": v, "success": true, "errors": []any{}, "messages": []any{}})
}

// invalid answers the way Cloudflare refuses a record it cannot accept.
func (f *fakeCloudflare) invalid(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusBadRequest)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"result": nil, "success": false, "messages": []any{},
		"errors": []any{map[string]any{"code": 1004, "message": "DNS Validation Error", "error_chain": []any{map[string]any{"code": code, "message": msg}}}},
	})
}

func (f *fakeCloudflare) list(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	for k := range q {
		if k != "name.exact" && k != "name.endswith" && k != "per_page" && k != "page" {
			f.t.Errorf("the service listed records with an unexpected parameter %q", k)
		}
	}
	exact, ends := strings.ToLower(q.Get("name.exact")), strings.ToLower(q.Get("name.endswith"))
	perPage, err1 := strconv.Atoi(q.Get("per_page"))
	page, err2 := strconv.Atoi(q.Get("page"))
	if err1 != nil || err2 != nil || perPage < 1 || page < 1 {
		f.invalid(w, 9000, "Invalid pagination parameters.")
		return
	}
	var match []fakeRecord
	for _, rec := range f.records {
		if (exact == "" || rec.Name == exact) && (ends == "" || strings.HasSuffix(rec.Name, ends)) {
			match = append(match, rec)
		}
	}
	total := len(match)
	pages := (total + perPage - 1) / perPage
	lo, hi := min((page-1)*perPage, total), min(page*perPage, total)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"result": append([]fakeRecord{}, match[lo:hi]...), "success": true, "errors": []any{}, "messages": []any{},
		"result_info": map[string]any{"page": page, "per_page": perPage, "count": hi - lo, "total_count": total, "total_pages": pages},
	})
}

func (f *fakeCloudflare) decode(w http.ResponseWriter, r *http.Request, in *fakeWrite) bool {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(in); err != nil {
		f.t.Errorf("the service sent a record Cloudflare does not understand: %v", err)
		f.invalid(w, 9207, "Request body is invalid.")
		return false
	}
	if in.ID != nil {
		f.t.Errorf("the service sent a record id in the body")
	}
	return true
}

func (f *fakeCloudflare) apply(rec *fakeRecord, in fakeWrite) {
	if in.Type != nil {
		rec.Type = strings.ToUpper(*in.Type)
	}
	if in.Name != nil {
		rec.Name = strings.TrimSuffix(strings.ToLower(*in.Name), ".")
	}
	if in.Content != nil {
		rec.Content = *in.Content
	}
	if in.Data != nil {
		d := *in.Data
		rec.Data = &d
	}
	if in.TTL != nil {
		rec.TTL = *in.TTL
	}
	if in.Proxied != nil {
		rec.Proxied = *in.Proxied
	}
	if in.Comment != nil {
		c := *in.Comment
		rec.Comment = &c
		rec.CommentModifiedOn = fakeTime
	}
	if in.Priority != nil {
		p := *in.Priority
		rec.Priority = &p
	}
	if in.Tags != nil {
		rec.Tags = in.Tags
	}
	if rec.TTL == 0 {
		rec.TTL = 1
	}
	rec.Proxiable = rec.Type == "A" || rec.Type == "AAAA" || rec.Type == "CNAME"
	switch rec.Type {
	case "SRV":
		if rec.Data != nil {
			rec.Content = fmt.Sprintf("%d %d %s", rec.Data.Weight, rec.Data.Port, rec.Data.Target)
			p := rec.Data.Priority
			rec.Priority = &p
		}
	case "TXT":
		if txtValue(rec.Content) == rec.Content {
			rec.Content = `"` + rec.Content + `"`
		}
	}
}

// valid checks rec the way Cloudflare checks a record before saving it and
// answers the refusal if it is not valid. self is the id of the record
// being updated.
func (f *fakeCloudflare) valid(w http.ResponseWriter, rec fakeRecord, self string) bool {
	zone := f.zone["name"].(string)
	if rec.Name != zone && !strings.HasSuffix(rec.Name, "."+zone) {
		f.t.Errorf("the service wrote a record outside the zone: %s", rec.Name)
		f.invalid(w, 9007, "Record name is not in the zone.")
		return false
	}
	addr, addrErr := netip.ParseAddr(rec.Content)
	switch rec.Type {
	case "A":
		if addrErr != nil || !addr.Is4() {
			f.fixture(w, http.StatusBadRequest, "error_validation.json")
			return false
		}
	case "AAAA":
		if addrErr != nil || !addr.Is6() || addr.Is4In6() {
			f.invalid(w, 9006, "Content for AAAA record must be a valid IPv6 address.")
			return false
		}
	case "TXT":
		if v := txtValue(rec.Content); v == "" || len(v) > 2048 {
			f.invalid(w, 9100, "Content for TXT record is invalid.")
			return false
		}
	case "SRV":
		if rec.Data == nil || rec.Data.Port < 0 || rec.Data.Port > 65535 || rec.Data.Target == "" || !strings.HasPrefix(rec.Name, "_") {
			f.invalid(w, 9101, "SRV record data is invalid.")
			return false
		}
	case "CNAME", "MX":
	default:
		f.invalid(w, 9000, "Invalid DNS record type.")
		return false
	}
	if rec.TTL != 1 && (rec.TTL < 60 || rec.TTL > 86400) {
		f.invalid(w, 9021, "TTL must be between 60 and 86400 seconds, or 1 for Automatic.")
		return false
	}
	if rec.Proxied && !rec.Proxiable {
		f.invalid(w, 9004, "This record type cannot be proxied.")
		return false
	}
	if len(rec.comment()) > 100 {
		f.invalid(w, 9102, "DNS record comment cannot be longer than 100 characters on this plan.")
		return false
	}
	if len(rec.Tags) > 0 {
		f.invalid(w, 9103, "DNS record tags are not available on this plan.")
		return false
	}
	for _, o := range f.records {
		if o.ID == self || o.Name != rec.Name {
			continue
		}
		addrType := func(t string) bool { return t == "A" || t == "AAAA" || t == "CNAME" }
		if addrType(o.Type) && addrType(rec.Type) && (o.Type == "CNAME" || rec.Type == "CNAME") {
			f.fixture(w, http.StatusBadRequest, "error_cname_conflict.json")
			return false
		}
		if o.Type == rec.Type && sameContent(o, rec) {
			f.fixture(w, http.StatusBadRequest, "error_identical_record.json")
			return false
		}
	}
	return true
}

func sameContent(a, b fakeRecord) bool {
	switch {
	case a.Type == "SRV":
		return a.Data != nil && b.Data != nil && *a.Data == *b.Data
	case a.Type == "TXT":
		return txtValue(a.Content) == txtValue(b.Content)
	case a.Type == "A" || a.Type == "AAAA":
		return sameAddr(a.Content, b.Content)
	}
	return a.Content == b.Content
}

func (f *fakeCloudflare) mustBeMarked(op string, rec fakeRecord) {
	if !strings.HasPrefix(rec.comment(), "playkeeper-names ") {
		f.t.Errorf("the service %s a record without its marker: %s %s %q (comment %q)", op, rec.Type, rec.Name, rec.Content, rec.comment())
	}
	for _, p := range f.protected {
		if p.ID == rec.ID {
			f.t.Errorf("the service %s a record it does not manage: %s %s (%s)", op, rec.Type, rec.Name, rec.ID)
		}
	}
}

func (f *fakeCloudflare) create(w http.ResponseWriter, r *http.Request) {
	var in fakeWrite
	if !f.decode(w, r, &in) {
		return
	}
	rec := fakeRecord{Settings: map[string]any{}, Meta: map[string]any{}, Tags: []string{}, CreatedOn: fakeTime, ModifiedOn: fakeTime}
	f.apply(&rec, in)
	if !f.valid(w, rec, "") {
		return
	}
	if f.quota != nil && len(f.records) >= *f.quota {
		f.fixture(w, http.StatusBadRequest, "error_quota.json")
		return
	}
	f.nextID++
	rec.ID = fmt.Sprintf("%032x", f.nextID)
	f.mustBeMarked("created", rec)
	f.records = append(f.records, rec)
	f.writes = append(f.writes, "create "+rec.Type+" "+rec.Name+" "+rec.Content)
	f.result(w, rec)
}

func (f *fakeCloudflare) index(id string) int {
	return slices.IndexFunc(f.records, func(r fakeRecord) bool { return r.ID == id })
}

func (f *fakeCloudflare) update(w http.ResponseWriter, r *http.Request, id string) {
	i := f.index(id)
	if i < 0 {
		f.fixture(w, http.StatusNotFound, "error_record_missing.json")
		return
	}
	var in fakeWrite
	if !f.decode(w, r, &in) {
		return
	}
	f.mustBeMarked("changed", f.records[i])
	rec := f.records[i]
	f.apply(&rec, in)
	rec.ModifiedOn = fakeTime
	if !f.valid(w, rec, id) {
		return
	}
	f.mustBeMarked("changed", rec)
	f.records[i] = rec
	f.writes = append(f.writes, "update "+rec.Type+" "+rec.Name+" "+rec.Content)
	f.result(w, rec)
}

func (f *fakeCloudflare) delete(w http.ResponseWriter, id string) {
	i := f.index(id)
	if i < 0 {
		f.fixture(w, http.StatusNotFound, "error_record_missing.json")
		return
	}
	rec := f.records[i]
	f.mustBeMarked("deleted", rec)
	f.records = slices.Delete(f.records, i, i+1)
	f.writes = append(f.writes, "delete "+rec.Type+" "+rec.Name+" "+rec.Content)
	f.result(w, map[string]string{"id": id})
}

// get returns the records of a type at a name.
func (f *fakeCloudflare) get(typ, name string) []fakeRecord {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []fakeRecord
	for _, r := range f.records {
		if r.Type == typ && r.Name == name {
			out = append(out, r)
		}
	}
	return out
}

// under returns every record at or below fqdn.
func (f *fakeCloudflare) under(fqdn string) []fakeRecord {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []fakeRecord
	for _, r := range f.records {
		if r.Name == fqdn || strings.HasSuffix(r.Name, "."+fqdn) {
			out = append(out, r)
		}
	}
	return out
}

// seedRecord returns a record of the starting zone by id.
func (f *fakeCloudflare) seedRecord(id string) cfRecord {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, r := range f.protected {
		if r.ID == id {
			return r.cf()
		}
	}
	f.t.Fatalf("no seed record %s", id)
	return cfRecord{}
}

// addByHand adds a record the way the owner would in the dashboard. It is
// protected like the starting zone.
func (f *fakeCloudflare) addByHand(typ, name, content, comment string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextID++
	rec := fakeRecord{
		ID: fmt.Sprintf("%032x", f.nextID), Type: typ, Name: name, Content: content, TTL: 1,
		Settings: map[string]any{}, Meta: map[string]any{}, Tags: []string{}, CreatedOn: fakeTime, ModifiedOn: fakeTime,
	}
	if comment != "" {
		rec.Comment = &comment
	}
	if typ == "SRV" {
		var prio, weight, port int
		var target string
		if _, err := fmt.Sscanf(content, "%d %d %d %s", &prio, &weight, &port, &target); err != nil {
			f.t.Fatalf("SRV content %q: %v", content, err)
		}
		rec.Data = &cfSRV{Priority: prio, Weight: weight, Port: port, Target: target}
		rec.Content = fmt.Sprintf("%d %d %s", weight, port, target)
		rec.Priority = &prio
	}
	f.records = append(f.records, rec)
	f.protected = append(f.protected, rec)
	return rec.ID
}

// removeByHand deletes a record the way the owner would in the dashboard.
func (f *fakeCloudflare) removeByHand(id string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.records = slices.DeleteFunc(f.records, func(r fakeRecord) bool { return r.ID == id })
	f.protected = slices.DeleteFunc(f.protected, func(r fakeRecord) bool { return r.ID == id })
}

// checkUntouched fails the test unless every record of the starting zone
// and every record added by hand is still there, unchanged.
func (f *fakeCloudflare) checkUntouched(t *testing.T) {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, p := range f.protected {
		i := slices.IndexFunc(f.records, func(r fakeRecord) bool { return r.ID == p.ID })
		if i < 0 {
			t.Errorf("a record the service does not manage was deleted: %s %s %q", p.Type, p.Name, p.Content)
			continue
		}
		if !reflect.DeepEqual(f.records[i], p) {
			t.Errorf("a record the service does not manage was changed:\n was %+v\n now %+v", p, f.records[i])
		}
	}
}

func (f *fakeCloudflare) fail(fl fakeFailure) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if fl.left == 0 {
		fl.left = -1
	}
	f.failures = append(f.failures, &fl)
}

func (f *fakeCloudflare) stopFailing() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failures = nil
}

func (f *fakeCloudflare) requestCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.requests
}

func (f *fakeCloudflare) writeLog() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.writes)
}

func (f *fakeCloudflare) setZone(name, status string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.zone["name"], f.zone["status"] = name, status
}

func (f *fakeCloudflare) setQuota(q *int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.quota = q
}

func (f *fakeCloudflare) usage() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.records)
}
