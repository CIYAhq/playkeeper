package panel

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/CIYAhq/playkeeper/internal/agentclient"
	"github.com/CIYAhq/playkeeper/internal/machinelink"
)

// shortWaits shortens the agent client's minute, and the machine links'
// minute to wait for an answer, to d for the test. The dashboard takes both
// when it's made, so a test calls it first.
func shortWaits(t *testing.T, d time.Duration) {
	t.Helper()
	client, link := agentclient.Timeout, linkRequestTimeout
	agentclient.Timeout, linkRequestTimeout = d, d
	t.Cleanup(func() { agentclient.Timeout, linkRequestTimeout = client, link })
}

const slowCopy = "survival-1.tar.zst.age"

// slowAnswer answers with body after wait, as an agent waiting on a slow
// store does, and notes in cancelled, if set, a request cancelled before
// then.
func slowAnswer(wait time.Duration, body string, cancelled *atomic.Bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(wait):
		case <-r.Context().Done():
			if cancelled != nil {
				cancelled.Store(true)
			}
			return
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, body)
	}
}

const deleted = `{"deleted":"` + slowCopy + `"}`

func TestASlowCopyDeleteOnAJoinedMachineWaitsForItsStore(t *testing.T) {
	shortWaits(t, 500*time.Millisecond)
	e := newEnvConfig(t, withDomain, nil)
	cookie, csrf := e.setup(t)
	e.reply("GET", "/v1/servers", `[{"id":"abcdefghjk","name":"Survival","phase":"online"}]`)
	ra := newRemoteAgent()
	key := "DELETE /v1/servers/rstuvwxyzq/offsite/copies/" + slowCopy
	var cancelled atomic.Bool
	ra.handle(key, slowAnswer(1500*time.Millisecond, deleted, &cancelled))
	mid, _ := e.joinMachine(t, cookie, csrf, ra)
	var servers []map[string]any
	e.get(t, "/api/servers", cookie, &servers)
	if len(servers) != 2 || servers[1]["id"] != "rstuvwxyzq" || servers[1]["machineId"] != mid {
		t.Fatalf("servers: %v", servers)
	}

	r := e.do(t, "DELETE", "/api/servers/rstuvwxyzq/offsite/copies/"+slowCopy, "", auth(cookie, csrf))
	if r.status != http.StatusOK || r.body["deleted"] != slowCopy {
		t.Fatalf("a deletion its store took three times the link's and the agent client's wait for: %d %v", r.status, r.body)
	}
	if cancelled.Load() {
		t.Fatal("the dashboard cancelled the machine's deletion")
	}
	if actor, _ := ra.saw(key); actor != "admin" {
		t.Fatalf("the deletion reached the machine as %q", actor)
	}
}

func TestASlowCopyDeleteOnTheDashboardsMachineWaitsForItsStore(t *testing.T) {
	shortWaits(t, 500*time.Millisecond)
	e := newEnv(t)
	cookie, csrf := e.setup(t)
	key := "DELETE /v1/servers/" + sampleServer + "/offsite/copies/" + slowCopy
	var cancelled atomic.Bool
	e.agent.mu.Lock()
	e.agent.answers[key] = slowAnswer(1500*time.Millisecond, deleted, &cancelled)
	e.agent.mu.Unlock()

	r := e.do(t, "DELETE", "/api/servers/"+sampleServer+"/offsite/copies/"+slowCopy, "", auth(cookie, csrf))
	if r.status != http.StatusOK || r.body["deleted"] != slowCopy {
		t.Fatalf("a deletion its store took three times the agent client's wait for: %d %v", r.status, r.body)
	}
	if cancelled.Load() {
		t.Fatal("the dashboard cancelled the agent's deletion")
	}
	e.agent.mu.Lock()
	defer e.agent.mu.Unlock()
	last := e.agent.reqs[len(e.agent.reqs)-1]
	if last.method+" "+last.path != key || last.query.Get("actor") != "admin" {
		t.Fatalf("the agent got %s %s with %v", last.method, last.path, last.query)
	}
}

// The dashboard keeps resource packs on its own machine (hResourcePackUpload),
// so it sends none over a link; one sent anyway has the link's limits, as
// every request that isn't a stream does, and one too large never reaches the
// machine's agent.
func TestAResourcePackUploadOverALinkHasTheLinksLimits(t *testing.T) {
	shortWaits(t, 500*time.Millisecond)
	e := newEnvConfig(t, withDomain, nil)
	cookie, csrf := e.setup(t)
	ra := newRemoteAgent()
	var dataErr error
	var mu sync.Mutex
	pack, data := "/v1/servers/rstuvwxyzq/resourcepack", "/v1/servers/rstuvwxyzq/datapacks"
	ra.handle("POST "+data, func(w http.ResponseWriter, r *http.Request) {
		_, err := io.Copy(io.Discard, r.Body)
		mu.Lock()
		dataErr = err
		mu.Unlock()
		io.WriteString(w, `{"ok":true}`)
	})
	mid, _ := e.joinMachine(t, cookie, csrf, ra)
	send := func(path string, body []byte) (*http.Response, error) {
		req, err := http.NewRequest("POST", "http://machine"+path, bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set(machinelink.ActorHeader, "admin")
		return e.srv.machineTransport(mid).RoundTrip(req)
	}
	big := bytes.Repeat([]byte("p"), 2<<20)

	resp, err := send(data, big)
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("2 MiB of a data pack, a stream: %v %v", resp, err)
	}
	resp.Body.Close()
	mu.Lock()
	err = dataErr
	mu.Unlock()
	if err != nil {
		t.Fatalf("the machine couldn't read the data pack whole: %v", err)
	}

	resp, err = send(pack, big)
	if err != nil {
		t.Fatalf("2 MiB of a resource pack: %v", err)
	}
	var refusal struct {
		Code   string            `json:"code"`
		Params map[string]string `json:"params"`
	}
	json.NewDecoder(resp.Body).Decode(&refusal)
	resp.Body.Close()
	if resp.StatusCode != http.StatusRequestEntityTooLarge || refusal.Code != machinelink.CodeTooLarge || refusal.Params["limit"] != "1 MiB" {
		t.Fatalf("2 MiB of a resource pack: %d %+v", resp.StatusCode, refusal)
	}
	if _, ok := ra.saw("POST " + pack); ok {
		t.Fatal("2 MiB of a resource pack reached the machine's agent")
	}

	ra.handle("POST "+pack, slowAnswer(1500*time.Millisecond, `{"ok":true}`, nil))
	_, err = send(pack, []byte("PK"))
	var le *machinelink.Error
	if !errors.As(err, &le) || le.Code != machinelink.CodeTimeout {
		t.Fatalf("a resource pack's upload waited past the link's wait for its answer: %v", err)
	}
}
