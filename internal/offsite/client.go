package offsite

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/xml"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
)

const (
	defaultPartSize = 64 << 20
	minPartSize     = 5 << 20
	maxParts        = 10000
	maxObjectSize   = 5 << 40
	maxXMLBody      = 4 << 20
	maxErrorBody    = 64 << 10
	metaSHA256      = "X-Amz-Meta-Playkeeper-Sha256"
)

// Client copies one server's backups to one bucket and prefix. It is safe
// for concurrent use.
type Client struct {
	cfg    Config
	host   string
	prefix string
	hc     *http.Client
	now    func() time.Time
	creds  credentials

	partSize int64
	minPart  int64
	attempts int
	stall    time.Duration
	sleep    func(context.Context, time.Duration) error

	// noChecksums is set once the service refuses x-amz-checksum headers.
	noChecksums atomic.Bool
}

// New checks cfg and returns a client for it. Requests go out with hc, or
// NewHTTPClient() if hc is nil; hc should have no overall Timeout, since
// a large part takes a while. Redirects are never followed, whatever hc
// says. Requests are signed with the time from now (time.Now if nil).
func New(cfg Config, hc *http.Client, now func() time.Time) (*Client, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if hc == nil {
		hc = NewHTTPClient()
	}
	h := *hc
	h.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	h.Jar = nil
	if now == nil {
		now = time.Now
	}
	u, _ := url.Parse(cfg.Endpoint)
	prefix := cfg.Prefix
	if prefix != "" && !strings.HasSuffix(prefix, "/") {
		prefix += "/"
	}
	return &Client{
		cfg: cfg, host: strings.ToLower(u.Host), prefix: prefix, hc: &h, now: now,
		creds:    credentials{accessKeyID: cfg.AccessKeyID, secret: cfg.SecretKey, region: cfg.Region, service: "s3"},
		partSize: defaultPartSize, minPart: minPartSize, attempts: 4, stall: 2 * time.Minute, sleep: sleepCtx,
	}, nil
}

// NewHTTPClient returns the HTTP client New uses by default: TLS 1.2 or
// later, no proxy, bounded waits for connecting, the handshake and the
// response headers, and no connections to link-local, multicast or
// unspecified addresses (such as a cloud's metadata service at
// 169.254.169.254), whatever the host name resolves to.
func NewHTTPClient() *http.Client {
	d := &net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second, Control: refuseAddr}
	return &http.Client{Transport: &http.Transport{
		DialContext:           d.DialContext,
		TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12},
		TLSHandshakeTimeout:   15 * time.Second,
		ResponseHeaderTimeout: 2 * time.Minute,
		IdleConnTimeout:       90 * time.Second,
		MaxIdleConnsPerHost:   4,
	}}
}

func refuseAddr(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	if ip := net.ParseIP(host); ip == nil || refusedIP(ip) {
		return &refusedAddrError{ip: host}
	}
	return nil
}

func refusedIP(ip net.IP) bool {
	return ip.IsUnspecified() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsInterfaceLocalMulticast() || ip.IsMulticast()
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// url is the address of key in the bucket, or of the bucket if key is "".
func (c *Client) url(key string, q url.Values) *url.URL {
	u := &url.URL{Scheme: "https", Host: c.host}
	if c.cfg.PathStyle {
		u.Path = "/" + c.cfg.Bucket
		if key != "" {
			u.Path += "/" + key
		}
	} else {
		u.Host = c.cfg.Bucket + "." + c.host
		u.Path = "/" + key
	}
	if len(q) > 0 {
		u.RawQuery = q.Encode()
	}
	return u
}

// call is one S3 request.
type call struct {
	op, name string
	method   string
	key      string // "" for the bucket itself
	query    url.Values
	header   http.Header
	body     io.ReaderAt // the body is size bytes of body at off
	off      int64
	size     int64
	sha256   string // hex SHA-256 of the body
	// checksum marks a request whose x-amz-checksum headers are dropped,
	// and it is sent again, if the service refuses them.
	checksum bool
	once     bool // no retries
}

// do sends cl, retrying what may work a second time, and passes a
// successful response to handle, which reads what it needs from the body.
// The response it returns has its body closed and is for the headers.
func (c *Client) do(ctx context.Context, cl call, handle func(*http.Response) error) (*http.Response, error) {
	attempts := c.attempts
	if cl.once {
		attempts = 1
	}
	fellBack := false
	var e *Error
	for i := 0; i < attempts; i++ {
		if i > 0 {
			wait := e.wait
			if wait <= 0 {
				wait = time.Second << (i - 1)
			}
			if err := c.sleep(ctx, wait); err != nil {
				return nil, c.transportError(cl.op, cl.name, err)
			}
		}
		var resp *http.Response
		resp, e = c.attempt(ctx, cl, handle)
		if e == nil {
			return resp, nil
		}
		if cl.checksum && !fellBack && !c.noChecksums.Load() && checksumUnsupported(e) {
			c.noChecksums.Store(true)
			fellBack = true
			i--
			continue
		}
		if !e.Retry || ctx.Err() != nil {
			return resp, e
		}
	}
	return nil, e
}

func (c *Client) attempt(ctx context.Context, cl call, handle func(*http.Response) error) (*http.Response, *Error) {
	actx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	timer := time.AfterFunc(c.stall, func() { cancel(errStalled) })
	defer timer.Stop()
	fail := func(err error) *Error {
		if ctx.Err() == nil && errors.Is(context.Cause(actx), errStalled) {
			err = errStalled
		}
		return c.transportError(cl.op, cl.name, err)
	}

	req, err := http.NewRequestWithContext(actx, cl.method, c.url(cl.key, cl.query).String(), nil)
	if err != nil {
		return nil, unexpected(cl.op, cl.name, "Playkeeper couldn't build the request to the storage service.")
	}
	skip := cl.checksum && c.noChecksums.Load()
	for k, vs := range cl.header {
		k = http.CanonicalHeaderKey(k)
		if skip && strings.HasPrefix(k, "X-Amz-Checksum-") {
			continue
		}
		req.Header[k] = slices.Clone(vs)
	}
	req.Header.Set("Accept-Encoding", "identity")
	req.Header.Set("User-Agent", "Playkeeper")
	payload := emptySHA256
	if cl.body != nil && cl.size > 0 {
		payload = cl.sha256
		body := func() (io.ReadCloser, error) {
			return io.NopCloser(&watched{r: io.NewSectionReader(cl.body, cl.off, cl.size), left: cl.size, t: timer, d: c.stall}), nil
		}
		req.Body, _ = body()
		req.GetBody = body
		req.ContentLength = cl.size
	}
	sent := c.now()
	c.creds.sign(req, payload, sent)
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, fail(err)
	}
	defer resp.Body.Close()
	timer.Reset(c.stall)
	resp.Body = &watchedBody{ReadCloser: resp.Body, t: timer, d: c.stall}
	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
		e := c.responseError(cl.op, cl.name, req, resp.StatusCode, resp.Header, body, sent)
		e.wait = retryAfter(resp.Header)
		return resp, e
	}
	if handle != nil {
		if err := handle(resp); err != nil {
			var e *Error
			if errors.As(err, &e) {
				return resp, e
			}
			return resp, fail(err)
		}
	}
	return resp, nil
}

// watched restarts the stall timer whenever the body moves, and stops it
// once the body is sent: the wait for the answer is not a stall.
type watched struct {
	r    io.Reader
	left int64
	t    *time.Timer
	d    time.Duration
}

func (w *watched) Read(p []byte) (int, error) {
	n, err := w.r.Read(p)
	w.left -= int64(n)
	if err != nil || w.left <= 0 {
		w.t.Stop()
	} else {
		w.t.Reset(w.d)
	}
	return n, err
}

type watchedBody struct {
	io.ReadCloser
	t *time.Timer
	d time.Duration
}

func (w *watchedBody) Read(p []byte) (int, error) {
	n, err := w.ReadCloser.Read(p)
	w.t.Reset(w.d)
	return n, err
}

func retryAfter(h http.Header) time.Duration {
	v := h.Get("Retry-After")
	if v == "" {
		return 0
	}
	var d time.Duration
	if n, err := strconv.Atoi(v); err == nil {
		d = time.Duration(n) * time.Second
	} else if t, err := http.ParseTime(v); err == nil {
		d = time.Until(t)
	}
	return min(max(d, 0), time.Minute)
}

// readXML reads a successful response's XML body into v. S3 may answer
// 200 and still send an <Error>, after whitespace to keep the connection
// open, so that is checked first.
func (c *Client) readXML(op, name string, r *http.Response, v any) error {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxXMLBody+1))
	if err != nil {
		return err
	}
	if len(body) > maxXMLBody {
		return unexpected(op, name, "The storage service sent an unexpectedly large answer.")
	}
	body = bytes.TrimLeft(body, " \t\r\n")
	if _, ok := parseS3Error(body); ok {
		return c.responseError(op, name, r.Request, r.StatusCode, r.Header, body, time.Time{})
	}
	if err := xml.Unmarshal(body, v); err != nil {
		return unexpected(op, name, "The storage service sent an answer Playkeeper doesn't understand.")
	}
	return nil
}
