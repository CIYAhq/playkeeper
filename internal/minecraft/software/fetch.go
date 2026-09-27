package software

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
)

const userAgent = "playkeeper (https://github.com/CIYAhq/playkeeper)"

// Size limits for what Playkeeper reads from the network. The largest real
// metadata is Quilt's loader list for one game version (about 0.7 MiB); the
// largest artifact is a Purpur jar (about 70 MiB).
const (
	maxMetadata = 8 << 20
	maxChecksum = 1 << 10
	maxArtifact = 256 << 20
)

// upstream reads one project's metadata and files over HTTPS, from that
// project's own hosts only. Redirects to any other host are refused.
type upstream struct {
	name  string
	hosts []string
	hc    *http.Client
}

func mojangUpstream(hc *http.Client) upstream {
	return upstream{name: "Mojang", hosts: []string{"piston-meta.mojang.com", "piston-data.mojang.com"}, hc: hc}
}

func (u upstream) params(host string, kv ...string) map[string]string {
	p := map[string]string{"upstream": u.name}
	if host != "" {
		p["host"] = host
	}
	for i := 0; i+1 < len(kv); i += 2 {
		p[kv[i]] = kv[i+1]
	}
	return p
}

// checkURL refuses anything but an HTTPS URL on one of the upstream's hosts.
func (u upstream) checkURL(raw, what string) (*url.URL, error) {
	p, err := url.Parse(raw)
	if err != nil || p.Host == "" {
		return nil, &Error{Kind: KindMalformed, Msg: fmt.Sprintf("%s gave an address for %s that is not a valid URL.", u.name, what),
			Hint:   "Try again later. If it keeps happening, " + u.name + " may have changed its API and Playkeeper needs an update.",
			Params: u.params("", "what", what)}
	}
	if p.Scheme != "https" || p.User != nil || p.Port() != "" || !slices.Contains(u.hosts, p.Hostname()) {
		return nil, &Error{Kind: KindHostNotAllowed,
			Msg: fmt.Sprintf("%s pointed Playkeeper to %s://%s for %s. Playkeeper only downloads from %s over HTTPS, so it refused.",
				u.name, p.Scheme, p.Host, what, strings.Join(u.hosts, ", ")),
			Hint:   "Nothing was downloaded. If this keeps happening, " + u.name + " may have moved its downloads and Playkeeper needs an update.",
			Params: u.params(p.Hostname(), "scheme", p.Scheme, "what", what)}
	}
	return p, nil
}

// client is the caller's client with a redirect policy that keeps every hop
// on the upstream's hosts. The caller's client itself is not changed.
func (u upstream) client(what string) *http.Client {
	base := u.hc
	if base == nil {
		base = http.DefaultClient
	}
	c := *base
	c.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return &Error{Kind: KindUpstreamStatus, Msg: fmt.Sprintf("%s redirected Playkeeper too many times for %s.", u.name, what),
				Hint: "Try again in a few minutes.", Params: u.params(req.URL.Hostname(), "what", what)}
		}
		_, err := u.checkURL(req.URL.String(), what)
		return err
	}
	return &c
}

// NeoForge's Maven now and then answers 404 for files it has (about one
// request in five at times), so a 404 from it is asked again before it
// counts.
var (
	flaky404Hosts = []string{"maven.neoforged.net"}
	flakyWait     = 400 * time.Millisecond
)

const flakyRetries = 2

// open sends a GET and returns the response only when it is 200 OK. what
// names the thing asked for, as it reads in a sentence: "its version list".
func (u upstream) open(ctx context.Context, raw, what string) (*http.Response, error) {
	p, err := u.checkURL(raw, what)
	if err != nil {
		return nil, err
	}
	resp, err := u.get(ctx, p, what)
	for try := 1; err == nil && resp.StatusCode == http.StatusNotFound && try <= flakyRetries && slices.Contains(flaky404Hosts, p.Hostname()); try++ {
		resp.Body.Close()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(time.Duration(try) * flakyWait):
		}
		resp, err = u.get(ctx, p, what)
	}
	if err != nil {
		return nil, err
	}
	status := strconv.Itoa(resp.StatusCode)
	switch {
	case resp.StatusCode == http.StatusOK:
		return resp, nil
	case resp.StatusCode == http.StatusNotFound:
		resp.Body.Close()
		return nil, &Error{Kind: KindNotFound, Msg: fmt.Sprintf("%s does not have %s.", u.name, what),
			Hint: "Choose another version.", Params: u.params(p.Hostname(), "status", status, "what", what)}
	case resp.StatusCode == http.StatusTooManyRequests:
		resp.Body.Close()
		return nil, &Error{Kind: KindRateLimited, Msg: fmt.Sprintf("%s is limiting requests from this host, so Playkeeper could not load %s (HTTP 429).", u.name, what),
			Hint: "Wait a few minutes, then try again.", Params: u.params(p.Hostname(), "status", status, "what", what)}
	default:
		resp.Body.Close()
		return nil, &Error{Kind: KindUpstreamStatus, Msg: fmt.Sprintf("%s answered HTTP %d when Playkeeper asked for %s.", u.name, resp.StatusCode, what),
			Hint: u.name + " may be having problems. Try again in a few minutes.", Params: u.params(p.Hostname(), "status", status, "what", what)}
	}
}

// get sends one GET, whatever its status.
func (u upstream) get(ctx context.Context, p *url.URL, what string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := u.client(what).Do(req)
	if err != nil {
		var e *Error
		if errors.As(err, &e) {
			return nil, e
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, &Error{Kind: KindUnreachable, Msg: fmt.Sprintf("Playkeeper could not reach %s to load %s (%v).", u.name, what, unwrapURLError(err)),
			Hint: "Check that this host can reach " + p.Hostname() + ", then try again.", Params: u.params(p.Hostname(), "what", what), Err: err}
	}
	return resp, nil
}

// read returns the whole body, refusing more than limit bytes.
func (u upstream) read(ctx context.Context, raw, what string, limit int64) ([]byte, error) {
	resp, err := u.open(ctx, raw, what)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.ContentLength > limit {
		return nil, u.tooLarge(resp.Request.URL.Hostname(), what, limit)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, &Error{Kind: KindUnreachable, Msg: fmt.Sprintf("The connection to %s broke while Playkeeper was loading %s (%v).", u.name, what, err),
			Hint: "Try again.", Params: u.params(resp.Request.URL.Hostname(), "what", what), Err: err}
	}
	if int64(len(b)) > limit {
		return nil, u.tooLarge(resp.Request.URL.Hostname(), what, limit)
	}
	return b, nil
}

func (u upstream) tooLarge(host, what string, limit int64) error {
	return &Error{Kind: KindTooLarge, Msg: fmt.Sprintf("%s sent more than %s for %s, far more than expected, so Playkeeper stopped reading.", u.name, size(limit), what),
		Hint:   "Try again later. If it keeps happening, " + u.name + " may have changed its API and Playkeeper needs an update.",
		Params: u.params(host, "what", what, "limit", strconv.FormatInt(limit, 10))}
}

func (u upstream) getJSON(ctx context.Context, raw, what string, v any) error {
	b, err := u.read(ctx, raw, what, maxMetadata)
	if err != nil {
		return err
	}
	return u.decode(b, what, v)
}

func (u upstream) decode(b []byte, what string, v any) error {
	if err := json.Unmarshal(b, v); err != nil {
		return u.malformed(what, err)
	}
	return nil
}

func (u upstream) malformed(what string, cause error) error {
	msg := fmt.Sprintf("%s sent %s in a form Playkeeper could not read.", u.name, what)
	if cause != nil {
		msg = fmt.Sprintf("%s sent %s in a form Playkeeper could not read (%v).", u.name, what, cause)
	}
	return &Error{Kind: KindMalformed, Msg: msg,
		Hint:   "Try again later. If it keeps happening, " + u.name + " may have changed its API and Playkeeper needs an update.",
		Params: u.params("", "what", what), Err: cause}
}

// checksum reads a Maven-style checksum file: the hex digest, optionally
// followed by the file name.
func (u upstream) checksum(ctx context.Context, fileURL string, a Algorithm, file string) (Hash, error) {
	what := "the " + a.label() + " of " + file
	b, err := u.read(ctx, fileURL+"."+string(a), what, maxChecksum)
	if err != nil {
		return Hash{}, err
	}
	fields := strings.Fields(string(b))
	if len(fields) == 0 {
		return Hash{}, u.malformed(what, nil)
	}
	h, ok := parseHash(a, fields[0])
	if !ok {
		return Hash{}, u.malformed(what, nil)
	}
	return h, nil
}

func unwrapURLError(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Err
	}
	return err
}

func size(n int64) string {
	switch {
	case n >= 1<<20 && n%(1<<20) == 0:
		return fmt.Sprintf("%d MiB", n>>20)
	case n >= 1<<10 && n%(1<<10) == 0:
		return fmt.Sprintf("%d KiB", n>>10)
	}
	return fmt.Sprintf("%d bytes", n)
}
