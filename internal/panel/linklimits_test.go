package panel

import (
	"bytes"
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

// The dashboard keeps resource packs on its own machine (hResourcePackUpload),
// so it sends none over a link; one sent anyway has the link's limits, as
// every request that isn't a stream does.
func TestAResourcePackUploadOverALinkHasTheLinksLimits(t *testing.T) {
	shortWaits(t, 500*time.Millisecond)
	e := newEnvConfig(t, withDomain, nil)
	cookie, csrf := e.setup(t)
	ra := newRemoteAgent()
	var mu sync.Mutex
	readErr := map[string]error{}
	read := func(w http.ResponseWriter, r *http.Request) {
		_, err := io.Copy(io.Discard, r.Body)
		mu.Lock()
		readErr[r.URL.Path] = err
		mu.Unlock()
		if err != nil {
			w.WriteHeader(http.StatusRequestEntityTooLarge)
			return
		}
		io.WriteString(w, `{"ok":true}`)
	}
	pack, data := "/v1/servers/rstuvwxyzq/resourcepack", "/v1/servers/rstuvwxyzq/datapacks"
	ra.handle("POST "+pack, read)
	ra.handle("POST "+data, read)
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
	resp, err = send(pack, big)
	if err != nil {
		t.Fatalf("2 MiB of a resource pack: %v", err)
	}
	resp.Body.Close()
	mu.Lock()
	dataErr, packErr := readErr[data], readErr[pack]
	mu.Unlock()
	if dataErr != nil {
		t.Fatalf("the machine couldn't read the data pack whole: %v", dataErr)
	}
	var tooBig *http.MaxBytesError
	if resp.StatusCode != http.StatusRequestEntityTooLarge || !errors.As(packErr, &tooBig) || tooBig.Limit != 1<<20 {
		t.Fatalf("2 MiB of a resource pack reached the machine: %d, read error %v", resp.StatusCode, packErr)
	}

	ra.handle("POST "+pack, slowAnswer(1500*time.Millisecond, `{"ok":true}`, nil))
	_, err = send(pack, []byte("PK"))
	var le *machinelink.Error
	if !errors.As(err, &le) || le.Code != machinelink.CodeTimeout {
		t.Fatalf("a resource pack's upload waited past the link's wait for its answer: %v", err)
	}
}
