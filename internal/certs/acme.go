package certs

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/mail"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/acme"
)

// maxACMEResponse bounds every response from the certificate authority.
const maxACMEResponse = 1 << 20

// validationWait bounds the wait for one check of a name, and for the order
// to become ready afterwards.
const validationWait = 2 * time.Minute

// Issuer gets certificates from an ACME certificate authority, Let's Encrypt
// unless DirectoryURL says otherwise.
type Issuer struct {
	// DirectoryURL is the CA's ACME directory, an https:// URL; empty means
	// Let's Encrypt's production directory. Only the directory's host is
	// ever contacted, and redirects are refused.
	DirectoryURL string
	// AccountKeyFile holds the account's private key. It is created with a
	// new ECDSA P-256 key (mode 0600) when missing.
	AccountKeyFile string
	// Email is an optional contact address for a new account.
	Email string
	// AgreedTerms is the URL of the terms of service the admin accepted
	// (see Terms). A new account is only created when it is the CA's
	// current terms; an existing account keeps working.
	AgreedTerms string
	// Client makes the requests; nil means one with a 30-second timeout.
	Client *http.Client
	// Now is the clock; nil means time.Now.
	Now func() time.Time

	keyMu sync.Mutex
}

// Request says what certificate to get and how to prove control of it.
type Request struct {
	// Names are the DNS names the certificate covers; the first one names
	// the saved file.
	Names []string
	// HTTP01 and DNS01 are the ways to prove control of the names. Set at
	// least one; where both are set and offered, DNS-01 is used.
	HTTP01 *HTTP01Responder
	DNS01  *DNS01
	// Dir is where the certificate is saved, with its key, as
	// "<first name>.pem". It is created when missing; its parent's path
	// must not be writable by less trusted users (see openDir).
	Dir string
	// Owner, when set, gets the saved file (mode 0640) and Dir (mode 0750),
	// so that it can read them; otherwise they are private to this process
	// (0600 and 0700).
	Owner *Owner
}

func (is *Issuer) now() time.Time {
	if is.Now != nil {
		return is.Now()
	}
	return time.Now()
}

// Terms returns the URL of the CA's current terms of service, which the
// admin accepts by setting AgreedTerms to it. It is empty when the CA has
// none.
func (is *Issuer) Terms(ctx context.Context) (string, error) {
	c, s, err := is.client(false)
	if err != nil {
		return "", err
	}
	d, err := c.Discover(ctx)
	if err != nil {
		return "", explain(err, s, is.now())
	}
	return d.Terms, nil
}

// Issue gets a certificate for req.Names and saves it in req.Dir. Errors are
// *Problems.
func (is *Issuer) Issue(ctx context.Context, req Request) (*Certificate, error) {
	names, err := normalizeNames(req.Names)
	if err != nil {
		return nil, err
	}
	if req.HTTP01 == nil && (req.DNS01 == nil || req.DNS01.Challenger == nil) {
		return nil, newProblem(nil, CodeConfig, map[string]string{"kind": "no_challenge"})
	}
	if req.Dir == "" {
		return nil, newProblem(nil, CodeConfig, map[string]string{"kind": "no_dir"})
	}
	if is.Email != "" {
		if err := checkEmail(is.Email); err != nil {
			return nil, err
		}
	}
	dir, err := openDir(req.Dir, req.Owner)
	if err != nil {
		return nil, newProblem(err, CodeSaveFailed, nil)
	}
	defer dir.Close()

	c, s, err := is.client(true)
	if err != nil {
		return nil, err
	}
	if len(names) == 1 {
		s.name = names[0]
	}
	d, err := c.Discover(ctx)
	if err != nil {
		return nil, explain(err, s, is.now())
	}
	if d.ExternalAccountRequired {
		return nil, newProblem(nil, CodeCANeedsAccount, nil)
	}
	if err := is.account(ctx, c, d, s); err != nil {
		return nil, err
	}
	order, err := c.AuthorizeOrder(ctx, acme.DomainIDs(names...))
	if err != nil {
		return nil, explain(err, s, is.now())
	}
	for _, u := range order.AuthzURLs {
		if err := is.authorize(ctx, c, u, req, s); err != nil {
			return nil, err
		}
	}
	wctx, cancel := context.WithTimeout(ctx, validationWait)
	order, err = c.WaitOrder(wctx, order.URI)
	cancel()
	if err != nil {
		return nil, explain(err, s, is.now())
	}

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, newProblem(err, CodeFailed, nil)
	}
	tmpl := &x509.CertificateRequest{DNSNames: names}
	if len(names[0]) <= 64 {
		tmpl.Subject = pkix.Name{CommonName: names[0]}
	}
	csr, err := x509.CreateCertificateRequest(rand.Reader, tmpl, key)
	if err != nil {
		return nil, newProblem(err, CodeFailed, nil)
	}
	der, _, err := c.CreateOrderCert(ctx, order.FinalizeURL, csr, true)
	if err != nil {
		return nil, explain(err, s, is.now())
	}
	leaf, err := checkChain(der, key, names, is.now())
	if err != nil {
		return nil, newProblem(err, CodeBadCertificate, nil)
	}
	data, err := encodeBundle(der, key)
	if err != nil {
		return nil, newProblem(err, CodeFailed, nil)
	}
	file := names[0] + ".pem"
	mode := os.FileMode(0o600)
	if req.Owner != nil {
		mode = 0o640
	}
	if err := writeFile(dir, file, data, mode, req.Owner); err != nil {
		return nil, newProblem(err, CodeSaveFailed, nil)
	}
	info := certificateInfo(filepath.Join(filepath.Clean(req.Dir), file), leaf)
	return &info, nil
}

// client returns an ACME client for the directory; withKey loads (or
// creates) the account key, which discovery alone does not need.
func (is *Issuer) client(withKey bool) (*acme.Client, situation, error) {
	raw := is.DirectoryURL
	if raw == "" {
		raw = acme.LetsEncryptURL
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
		return nil, situation{}, newProblem(nil, CodeConfig, map[string]string{"kind": "directory_url", "url": displayName(raw)})
	}
	s := situation{host: u.Hostname()}
	c := &acme.Client{
		DirectoryURL: u.String(),
		HTTPClient:   guardedClient(is.Client, u),
		RetryBackoff: retryBackoff,
		UserAgent:    userAgent(),
	}
	if withKey {
		is.keyMu.Lock()
		key, err := loadAccountKey(is.AccountKeyFile)
		is.keyMu.Unlock()
		if err != nil {
			return nil, s, err
		}
		c.Key = key
	}
	return c, s, nil
}

// account makes sure the account key is registered, creating the account
// when the admin has accepted the current terms.
func (is *Issuer) account(ctx context.Context, c *acme.Client, d acme.Directory, s situation) error {
	_, err := c.GetReg(ctx, "")
	if err == nil {
		return nil
	}
	if !errors.Is(err, acme.ErrNoAccount) {
		return explain(err, s, is.now())
	}
	if d.Terms != "" && d.Terms != is.AgreedTerms {
		return newProblem(nil, CodeTermsNotAccepted, map[string]string{"url": safeURL(d.Terms)})
	}
	acct := &acme.Account{}
	if is.Email != "" {
		acct.Contact = []string{"mailto:" + is.Email}
	}
	_, err = c.Register(ctx, acct, func(tos string) bool { return tos == is.AgreedTerms })
	if err != nil && !errors.Is(err, acme.ErrAccountAlreadyExists) {
		return explain(err, s, is.now())
	}
	return nil
}

// authorize proves control of the name of one authorization.
func (is *Issuer) authorize(ctx context.Context, c *acme.Client, authzURL string, req Request, s situation) error {
	z, err := c.GetAuthorization(ctx, authzURL)
	if err != nil {
		return explain(err, s, is.now())
	}
	s.name = z.Identifier.Value
	switch z.Status {
	case acme.StatusValid:
		return nil
	case acme.StatusPending:
	default:
		return newProblem(fmt.Errorf("the authorization for %s is %s", s.name, z.Status), CodeCAError, map[string]string{"name": s.name})
	}
	var chal *acme.Challenge
	var release func()
	dns01 := offered(z, "dns-01")
	http01 := offered(z, "http-01")
	switch {
	case req.DNS01 != nil && req.DNS01.Challenger != nil && dns01 != nil:
		chal, s.challenge = dns01, "dns-01"
		value, err := c.DNS01ChallengeRecord(chal.Token)
		if err != nil {
			return explain(err, s, is.now())
		}
		if release, err = req.DNS01.present(ctx, s.name, value); err != nil {
			return explain(err, s, is.now())
		}
	case req.HTTP01 != nil && http01 != nil:
		chal, s.challenge = http01, "http-01"
		keyAuth, err := c.HTTP01ChallengeResponse(chal.Token)
		if err != nil {
			return explain(err, s, is.now())
		}
		if release, err = req.HTTP01.Present(chal.Token, keyAuth); err != nil {
			return explain(err, s, is.now())
		}
	default:
		challenge := "http-01"
		if req.HTTP01 == nil {
			challenge = "dns-01"
		}
		return newProblem(nil, CodeChallengeNotOffered, map[string]string{"name": s.name, "challenge": challenge})
	}
	defer release()
	if _, err := c.Accept(ctx, chal); err != nil {
		return explain(err, s, is.now())
	}
	wctx, cancel := context.WithTimeout(ctx, validationWait)
	defer cancel()
	if _, err := c.WaitAuthorization(wctx, z.URI); err != nil {
		if ctx.Err() == nil && errors.Is(wctx.Err(), context.DeadlineExceeded) {
			return newProblem(err, CodeValidationTimeout, map[string]string{"name": s.name})
		}
		return explain(err, s, is.now())
	}
	return nil
}

func offered(z *acme.Authorization, typ string) *acme.Challenge {
	for _, c := range z.Challenges {
		if c.Type == typ {
			return c
		}
	}
	return nil
}

// checkChain checks what the CA sent before it is saved: the leaf is for
// key and names and valid now, and each certificate is signed by the next.
func checkChain(der [][]byte, key *ecdsa.PrivateKey, names []string, now time.Time) (*x509.Certificate, error) {
	if len(der) == 0 {
		return nil, errors.New("the certificate authority sent no certificate")
	}
	chain := make([]*x509.Certificate, len(der))
	for i, d := range der {
		c, err := x509.ParseCertificate(d)
		if err != nil {
			return nil, fmt.Errorf("certificate %d of the chain: %w", i+1, err)
		}
		chain[i] = c
	}
	leaf := chain[0]
	if pub, ok := leaf.PublicKey.(*ecdsa.PublicKey); !ok || !pub.Equal(&key.PublicKey) {
		return nil, errors.New("the certificate is not for the key Playkeeper sent")
	}
	for _, n := range names {
		if err := leaf.VerifyHostname(n); err != nil {
			return nil, fmt.Errorf("the certificate does not cover %s", n)
		}
	}
	if now.Add(5*time.Minute).Before(leaf.NotBefore) || !now.Before(leaf.NotAfter) {
		return nil, fmt.Errorf("the certificate is valid from %s to %s, not now", leaf.NotBefore.UTC().Format(time.RFC3339), leaf.NotAfter.UTC().Format(time.RFC3339))
	}
	for i := 0; i+1 < len(chain); i++ {
		if err := chain[i].CheckSignatureFrom(chain[i+1]); err != nil {
			return nil, fmt.Errorf("certificate %d of the chain is not signed by the next one: %w", i+1, err)
		}
	}
	return leaf, nil
}

func checkEmail(e string) error {
	a, err := mail.ParseAddress(e)
	if err != nil || a.Name != "" || a.Address != e || len(e) > 254 || strings.ContainsAny(e, " ,;<>\"") {
		return newProblem(nil, CodeInvalidEmail, map[string]string{"email": displayName(e)})
	}
	return nil
}

// retryBackoff replaces the ACME client's default, which waits out any
// Retry-After (hours, for a rate limit) and retries forever. Rate limits
// and long waits are returned at once, so that they can be explained;
// other failures are retried three times.
func retryBackoff(n int, _ *http.Request, resp *http.Response) time.Duration {
	if resp == nil || n > 3 || resp.StatusCode == http.StatusTooManyRequests {
		return 0
	}
	if resp.StatusCode == http.StatusBadRequest {
		return 100 * time.Millisecond
	}
	if d := retryAfter(resp.Header.Get("Retry-After"), time.Now()); d > 10*time.Second {
		return 0
	} else if d > 0 {
		return d
	}
	return time.Duration(1<<(n-1)) * time.Second
}

// refusedError is a request or response the guarded client refused.
type refusedError struct{ msg string }

func (e *refusedError) Error() string { return e.msg }

// guardedClient only talks HTTPS to allowed's host, refuses redirects and
// bounds response sizes; the ACME client itself reads bodies unbounded.
func guardedClient(base *http.Client, allowed *url.URL) *http.Client {
	c := &http.Client{Timeout: 30 * time.Second}
	next := http.DefaultTransport
	if base != nil {
		if base.Timeout > 0 {
			c.Timeout = base.Timeout
		}
		if base.Transport != nil {
			next = base.Transport
		}
	}
	c.Transport = &guardTransport{host: canonicalHost(allowed), next: next}
	c.CheckRedirect = func(req *http.Request, _ []*http.Request) error {
		return &refusedError{"refused a redirect to " + req.URL.Redacted()}
	}
	return c
}

type guardTransport struct {
	host string
	next http.RoundTripper
}

func (g *guardTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Scheme != "https" || canonicalHost(req.URL) != g.host {
		if req.Body != nil {
			req.Body.Close()
		}
		return nil, &refusedError{fmt.Sprintf("refused to contact %s: only https://%s is allowed", req.URL.Redacted(), g.host)}
	}
	resp, err := g.next.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	if resp.ContentLength > maxACMEResponse {
		resp.Body.Close()
		return nil, &refusedError{fmt.Sprintf("the answer from %s is larger than %d bytes", g.host, maxACMEResponse)}
	}
	resp.Body = &limitedBody{rc: resp.Body, left: maxACMEResponse}
	return resp, nil
}

func canonicalHost(u *url.URL) string {
	port := u.Port()
	if port == "" {
		port = "443"
	}
	return net.JoinHostPort(strings.ToLower(u.Hostname()), port)
}

// limitedBody fails, rather than silently truncating, past its limit.
type limitedBody struct {
	rc   io.ReadCloser
	left int64
}

func (b *limitedBody) Read(p []byte) (int, error) {
	if int64(len(p)) > b.left+1 {
		p = p[:b.left+1]
	}
	n, err := b.rc.Read(p)
	if int64(n) > b.left {
		return int(b.left), &refusedError{fmt.Sprintf("an answer was larger than %d bytes", maxACMEResponse)}
	}
	b.left -= int64(n)
	return n, err
}

func (b *limitedBody) Close() error { return b.rc.Close() }
