package agentclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/CIYAhq/playkeeper/internal/api"
)

// shortDir is a temporary directory with a path short enough for a Unix
// socket, which t.TempDir's often is not.
func shortDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "pk-ac")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

func serve(t *testing.T, h http.HandlerFunc) *Client {
	t.Helper()
	sock := filepath.Join(shortDir(t), "agent.sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: h}
	go srv.Serve(l)
	t.Cleanup(func() { srv.Close() })
	return New(sock)
}

func TestDoSendsJSONAndDecodesTheReply(t *testing.T) {
	c := serve(t, func(w http.ResponseWriter, r *http.Request) {
		var in map[string]string
		json.NewDecoder(r.Body).Decode(&in)
		json.NewEncoder(w).Encode(map[string]string{"method": r.Method, "path": r.URL.Path, "since": r.URL.Query().Get("since"), "type": r.Header.Get("Content-Type"), "actor": in["actor"]})
	})
	var out map[string]string
	code, err := c.Do(context.Background(), "POST", "/v1/servers/abc/backups", url.Values{"since": {"a b&c"}}, map[string]string{"actor": "siya"}, &out)
	if err != nil || code != http.StatusOK {
		t.Fatalf("%d %v", code, err)
	}
	want := map[string]string{"method": "POST", "path": "/v1/servers/abc/backups", "since": "a b&c", "type": "application/json", "actor": "siya"}
	if !reflect.DeepEqual(out, want) {
		t.Fatalf("the agent saw %v, want %v", out, want)
	}
}

func TestDoReturnsTheAgentsErrorWithItsStatus(t *testing.T) {
	c := serve(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
		json.NewEncoder(w).Encode(api.Error{Error: "Another operation is running.", Code: api.CodeBusy, Hint: "Wait for it to finish."})
	})
	out := map[string]string{"kept": "yes"}
	code, err := c.Do(context.Background(), "POST", "/v1/servers/abc/start", nil, nil, &out)
	var e *Error
	if code != http.StatusConflict || !errors.As(err, &e) || e.Status != http.StatusConflict || e.Body.Code != api.CodeBusy || e.Body.Hint != "Wait for it to finish." {
		t.Fatalf("%d %#v", code, err)
	}
	if err.Error() != "Another operation is running." || out["kept"] != "yes" {
		t.Fatalf("the error reads %q and the reply became %v", err, out)
	}
}

func TestAnErrorWithoutAUsableBodyGetsAPlainOne(t *testing.T) {
	for name, body := range map[string]string{
		"not JSON":   "<html>Bad Gateway</html>",
		"no message": `{"code":"busy"}`,
		"over 1 MiB": `{"error":"` + strings.Repeat("x", 1<<20) + `"}`,
	} {
		t.Run(name, func(t *testing.T) {
			c := serve(t, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusBadGateway)
				io.WriteString(w, body)
			})
			_, err := c.Do(context.Background(), "GET", "/v1/health", nil, nil, nil)
			var e *Error
			if !errors.As(err, &e) || !reflect.DeepEqual(e.Body, api.Error{Error: "agent returned HTTP 502", Code: api.CodeInternal}) {
				t.Fatalf("%#v", err)
			}
		})
	}
}

func TestDoLeavesTheReplyAloneWhenThereIsNoContent(t *testing.T) {
	c := serve(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })
	out := map[string]string{"kept": "yes"}
	if code, err := c.Do(context.Background(), "DELETE", "/v1/servers/abc", nil, nil, &out); err != nil || code != http.StatusNoContent || out["kept"] != "yes" {
		t.Fatalf("%d %v %v", code, err, out)
	}
}

func TestRawStreamsTheBodyBothWays(t *testing.T) {
	c := serve(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", r.Header.Get("Content-Type"))
		io.Copy(w, r.Body)
	})
	upload := bytes.Repeat([]byte("playkeeper"), 300_000)
	resp, err := c.Raw(context.Background(), "POST", "/v1/restore/upload", nil, bytes.NewReader(upload), map[string]string{"Content-Type": "application/gzip"}, true)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	got, err := io.ReadAll(resp.Body)
	if err != nil || !bytes.Equal(got, upload) || resp.Header.Get("Content-Type") != "application/gzip" {
		t.Fatalf("%d of %d bytes back, type %q: %v", len(got), len(upload), resp.Header.Get("Content-Type"), err)
	}
}

func TestAMissingSocketMeansTheAgentIsUnavailable(t *testing.T) {
	c := New(filepath.Join(shortDir(t), "agent.sock"))
	if _, err := c.Do(context.Background(), "GET", "/v1/health", nil, nil, nil); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("want ErrUnavailable, got %v", err)
	}
}
