package addons

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"

	"github.com/CIYAhq/playkeeper/internal/addons/fetch"
)

// Icon is a project icon that is known to be a PNG, JPEG, WebP or GIF image.
type Icon struct {
	// ContentType comes from the bytes, not from the upstream's header.
	ContentType string
	Data        []byte
}

// FetchIcon downloads a project icon (a Card's or Installed's IconURL) for
// the panel to serve from its own origin. Only the sources' CDN hosts are
// asked, the size is bounded, and the bytes must be a PNG, JPEG, WebP or GIF
// image. SVG and everything else is refused because it can carry scripts.
func (l *Library) FetchIcon(ctx context.Context, rawURL string) (*Icon, error) {
	hosts := l.iconHosts()
	u, err := hosts.Check(rawURL)
	if err != nil {
		host, scheme := "an invalid address", ""
		if pu, perr := url.Parse(rawURL); perr == nil && pu.Host != "" {
			host, scheme = printable(pu.Hostname()), pu.Scheme
		}
		if scheme != "" && scheme != "https" {
			return nil, &Error{Notice: notice(KindNotHTTPS, kv("host", host), "Playkeeper only loads icons over HTTPS.", ""), Err: err}
		}
		return nil, &Error{Notice: notice(KindHostNotAllowed, kv("host", host),
			"Playkeeper only loads icons from Modrinth's and Hangar's file hosts, not from "+host+".", ""), Err: err}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", l.userAgent())
	resp, err := fetch.Guard(l.HTTP, hosts).Do(req)
	if err != nil {
		var re *fetch.RedirectError
		if errors.As(err, &re) {
			return nil, &Error{Notice: notice(KindRedirectRefused, kv("host", re.To),
				"The icon was redirected to "+re.To+", which is not one of Modrinth's or Hangar's file hosts.", ""), Err: err}
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, &Error{Notice: notice(KindUnreachable, kv("source", u.Hostname()), "Playkeeper could not reach "+u.Hostname()+".",
			"Check that this machine can reach the internet, then try again."), Err: err}
	}
	defer func() {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
		resp.Body.Close()
	}()
	if resp.StatusCode != http.StatusOK {
		k := KindUpstream
		if resp.StatusCode == http.StatusNotFound {
			k = KindNotFound
		}
		return nil, fail(k, kv("source", u.Hostname(), "status", strconv.Itoa(resp.StatusCode)),
			u.Hostname()+" answered the icon request with HTTP "+strconv.Itoa(resp.StatusCode)+".", "")
	}
	max := l.maxIconSize()
	tooBig := fail(KindTooLarge, kv("limit", fetch.Size(max)), "The icon is larger than the "+fetch.Size(max)+" Playkeeper accepts.", "")
	if resp.ContentLength > max {
		return nil, tooBig
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, max+1))
	if err != nil {
		return nil, &Error{Notice: notice(KindUnreachable, kv("source", u.Hostname()), "The icon download from "+u.Hostname()+" broke off.", "Try again."), Err: err}
	}
	if int64(len(b)) > max {
		return nil, tooBig
	}
	ct := sniffImage(b)
	if ct == "" {
		return nil, fail(KindIconRefused, nil, "The icon is not a PNG, JPEG, WebP or GIF image, so Playkeeper will not show it.", "")
	}
	return &Icon{ContentType: ct, Data: b}, nil
}

func sniffImage(b []byte) string {
	switch {
	case bytes.HasPrefix(b, []byte("\x89PNG\r\n\x1a\n")):
		return "image/png"
	case bytes.HasPrefix(b, []byte("\xff\xd8\xff")):
		return "image/jpeg"
	case bytes.HasPrefix(b, []byte("GIF87a")), bytes.HasPrefix(b, []byte("GIF89a")):
		return "image/gif"
	case len(b) >= 12 && string(b[:4]) == "RIFF" && string(b[8:12]) == "WEBP":
		return "image/webp"
	}
	return ""
}
