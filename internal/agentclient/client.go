// Package agentclient talks to an agent: the local one over its Unix socket,
// or a joined machine's over its link. It is used by the web panel (as the
// panel service user) and by root CLI commands.
package agentclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"time"

	"github.com/CIYAhq/playkeeper/internal/api"
)

type Client struct {
	hc     *http.Client
	stream *http.Client
}

func New(socket string) *Client {
	dial := func(ctx context.Context, _, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "unix", socket)
	}
	return &Client{
		hc:     &http.Client{Timeout: 60 * time.Second, Transport: &http.Transport{DialContext: dial, MaxIdleConns: 8}},
		stream: &http.Client{Transport: &http.Transport{DialContext: dial, DisableKeepAlives: true}},
	}
}

// Via returns a client whose requests go through rt, such as a machine
// link's transport. The link's own errors stay in the chain of the
// ErrUnavailable it returns.
func Via(rt http.RoundTripper) *Client {
	return &Client{
		hc:     &http.Client{Timeout: 60 * time.Second, Transport: rt},
		stream: &http.Client{Transport: rt},
	}
}

// Error is a non-2xx agent response, or ErrUnavailable when the socket is down.
type Error struct {
	Status int
	Body   api.Error
}

func (e *Error) Error() string { return e.Body.Error }

var ErrUnavailable = errors.New("the Playkeeper agent is not reachable")

// ErrBadAnswer is an answer no agent gives: a status other than 2xx, 4xx or
// 5xx, or a body that isn't the JSON asked for.
var ErrBadAnswer = errors.New("the agent's answer was not valid")

// Do sends a JSON request and decodes a JSON response into out (if non-nil).
// It returns the HTTP status for successful calls.
func (c *Client) Do(ctx context.Context, method, path string, q url.Values, body, out any) (int, error) {
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, err
		}
		r = bytes.NewReader(b)
	}
	resp, err := c.Raw(ctx, method, path, q, r, map[string]string{"Content-Type": "application/json"}, false)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return resp.StatusCode, DecodeError(resp)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return resp.StatusCode, ErrBadAnswer
	}
	if out != nil && resp.StatusCode != http.StatusNoContent {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			return resp.StatusCode, fmt.Errorf("%w: %w", ErrBadAnswer, err)
		}
	}
	return resp.StatusCode, nil
}

// Raw performs a request and returns the live response (caller closes it).
// stream selects a client without an overall timeout (uploads, downloads).
func (c *Client) Raw(ctx context.Context, method, path string, q url.Values, body io.Reader, headers map[string]string, stream bool) (*http.Response, error) {
	u := "http://agent" + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return nil, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	hc := c.hc
	if stream {
		hc = c.stream
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	return resp, nil
}

// DecodeError reads an error response (status 400 or more) into an *Error.
func DecodeError(resp *http.Response) error {
	e := &Error{Status: resp.StatusCode}
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if json.Unmarshal(b, &e.Body) != nil || e.Body.Error == "" {
		e.Body = api.Error{Error: fmt.Sprintf("agent returned HTTP %d", resp.StatusCode), Code: api.CodeInternal}
	}
	return e
}
