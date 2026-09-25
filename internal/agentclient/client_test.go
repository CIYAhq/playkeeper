package agentclient

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

type linkErr struct{ code string }

func (e *linkErr) Error() string { return e.code }

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func reply(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(body))}
}

func TestViaKeepsTheTransportsErrorInTheChain(t *testing.T) {
	c := Via(roundTrip(func(*http.Request) (*http.Response, error) {
		return nil, &linkErr{code: "machine_not_connected"}
	}))
	_, err := c.Do(context.Background(), "GET", "/v1/servers", nil, nil, nil)
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err = %v, want ErrUnavailable", err)
	}
	var le *linkErr
	if !errors.As(err, &le) || le.code != "machine_not_connected" {
		t.Fatalf("the link's error is lost: %v", err)
	}
}

func TestViaSendsAgentRequestsAndReadsAgentErrors(t *testing.T) {
	var got []string
	c := Via(roundTrip(func(r *http.Request) (*http.Response, error) {
		got = append(got, r.Method+" "+r.URL.Path+" "+r.URL.RawQuery)
		if r.URL.Path == "/v1/servers/x" {
			return reply(http.StatusNotFound, `{"error":"Server not found.","code":"not_found"}`), nil
		}
		return reply(http.StatusOK, `[{"id":"a"}]`), nil
	}))
	var out []map[string]string
	if status, err := c.Do(context.Background(), "GET", "/v1/servers", map[string][]string{"limit": {"5"}}, nil, &out); err != nil || status != http.StatusOK {
		t.Fatalf("status %d, err %v", status, err)
	}
	if len(out) != 1 || out[0]["id"] != "a" {
		t.Fatalf("decoded %v", out)
	}
	_, err := c.Do(context.Background(), "GET", "/v1/servers/x", nil, nil, nil)
	var ae *Error
	if !errors.As(err, &ae) || ae.Status != http.StatusNotFound || ae.Body.Code != "not_found" {
		t.Fatalf("err = %v, want the agent's not_found", err)
	}
	if want := []string{"GET /v1/servers limit=5", "GET /v1/servers/x "}; strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("requests %q, want %q", got, want)
	}
}

func TestAnswersNoAgentGivesAreRefused(t *testing.T) {
	for _, tc := range []struct {
		status int
		body   string
	}{
		{http.StatusMultipleChoices, `{}`},
		{http.StatusOK, `<html>`},
		{http.StatusOK, ``},
		{http.StatusCreated, `{"id":`},
	} {
		c := Via(roundTrip(func(*http.Request) (*http.Response, error) { return reply(tc.status, tc.body), nil }))
		var out map[string]any
		if _, err := c.Do(context.Background(), "GET", "/v1/servers/x", nil, nil, &out); !errors.Is(err, ErrBadAnswer) {
			t.Errorf("%d %q: err = %v, want ErrBadAnswer", tc.status, tc.body, err)
		}
	}
	c := Via(roundTrip(func(*http.Request) (*http.Response, error) { return reply(http.StatusNoContent, ``), nil }))
	if status, err := c.Do(context.Background(), "DELETE", "/v1/servers/x/whitelist/a", nil, nil, &map[string]any{}); err != nil || status != http.StatusNoContent {
		t.Fatalf("no content: %d %v", status, err)
	}
}
