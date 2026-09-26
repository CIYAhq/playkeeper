package panel

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
)

func TestWorldUploadStreamsToTheAgent(t *testing.T) {
	var mu sync.Mutex
	var got []byte
	e, agent := newScriptedEnv(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == "PUT":
			b, _ := io.ReadAll(r.Body)
			mu.Lock()
			got = b
			mu.Unlock()
			if r.URL.Query().Get("offset") != "1024" {
				w.WriteHeader(http.StatusConflict)
				io.WriteString(w, `{"error":"The upload carries on from byte 1024.","code":"conflict"}`)
				return
			}
			io.WriteString(w, `{"id":"imp","files":[{"index":0,"received":`+jsonNumber(1024+len(b))+`}]}`)
		case strings.HasSuffix(r.URL.Path, "/inspect"):
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			if body["actor"] != "admin" {
				w.WriteHeader(http.StatusBadRequest)
				io.WriteString(w, `{"error":"actor is required","code":"invalid"}`)
				return
			}
			w.WriteHeader(http.StatusConflict)
			io.WriteString(w, `{"error":"\"world.zip\" hasn't finished uploading.","code":"conflict"}`)
		default:
			io.WriteString(w, `{"ok":true}`)
		}
	})
	cookie, csrf := e.setup(t)
	machines, err := e.srv.machines()
	if err != nil || len(machines) != 1 {
		t.Fatalf("machines: %v %v", machines, err)
	}
	base := "/api/machines/" + machines[0].ID + "/world-imports/0123456789abcdef"
	h := auth(cookie, csrf)
	h["Origin"] = e.ts.URL

	payload := bytes.Repeat([]byte("world-bytes-"), 300_000)
	r, body := e.stream(t, "PUT", base+"/files/0?offset=1024", bytes.NewReader(payload), h)
	if r.StatusCode != 200 || r.Header.Get("Cache-Control") != "no-store" || !bytes.Contains(body, []byte(jsonNumber(1024+len(payload)))) {
		t.Fatalf("upload: %d %s", r.StatusCode, body)
	}
	mu.Lock()
	same := sha256.Sum256(got) == sha256.Sum256(payload)
	mu.Unlock()
	if !same {
		t.Fatal("the agent didn't receive the bytes as sent")
	}
	hits := agent.seen()
	if len(hits) != 1 || hits[0].URL.Path != "/v1/world-imports/0123456789abcdef/files/0" || hits[0].Header.Get("X-Playkeeper-Actor") != "admin" || hits[0].Header.Get("Cookie") != "" {
		t.Fatalf("agent saw %v", hits)
	}
	if r, body := e.stream(t, "PUT", base+"/files/0?offset=0", strings.NewReader("x"), h); r.StatusCode != 409 || !bytes.Contains(body, []byte("carries on from byte 1024")) {
		t.Fatalf("wrong offset: %d %s", r.StatusCode, body)
	}

	agent.forget()
	for _, p := range []string{"/files/0", "/files/0?offset=-1", "/files/0?offset=01", "/files/0?offset=abc", "/files/0?offset=1e3", "/files/x?offset=0", "/files/123?offset=0"} {
		if r, body := e.stream(t, "PUT", base+p, strings.NewReader("x"), h); r.StatusCode != 400 {
			t.Fatalf("PUT %s: %d %s", p, r.StatusCode, body)
		}
	}
	if hits := agent.seen(); len(hits) != 0 {
		t.Fatalf("refused uploads reached the agent: %v", hits)
	}

	if res := e.do(t, "POST", base+"/inspect", `{}`, auth(cookie, csrf)); res.status != 409 || !strings.Contains(res.body["error"].(string), "hasn't finished uploading") {
		t.Fatalf("inspect: %d %v", res.status, res.body)
	}
	if res := e.do(t, "POST", base+"/inspect", `[1]`, auth(cookie, csrf)); res.status != 400 {
		t.Fatalf("inspect with a list: %d %v", res.status, res.body)
	}
}

func jsonNumber(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}
