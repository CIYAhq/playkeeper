package addons

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
)

// serveAs serves data with a Content-Type that the library must not trust.
func serveAs(contentType, data string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", contentType)
		w.Write([]byte(data))
	}
}

func TestFetchIconSniffsTheImage(t *testing.T) {
	f := newFakes(t)
	l := f.library()
	for _, tc := range []struct{ name, data, want string }{
		{"png", "\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR\x00\x00\x00\x60", "image/png"},
		{"jpeg", "\xff\xd8\xff\xe0\x00\x10JFIF\x00\x01", "image/jpeg"},
		{"gif89a", "GIF89a\x01\x00\x01\x00\x80\x00\x00", "image/gif"},
		{"gif87a", "GIF87a\x01\x00\x01\x00\x80\x00\x00", "image/gif"},
		{"webp", "RIFF\x24\x00\x00\x00WEBPVP8L\x18\x00\x00\x00", "image/webp"},
	} {
		path := "/data/icons/" + tc.name
		f.hook(path, serveAs("text/html", tc.data))
		icon, err := l.FetchIcon(context.Background(), f.cdn.URL+path)
		if err != nil {
			t.Errorf("%s: %v", tc.name, err)
			continue
		}
		if icon.ContentType != tc.want || string(icon.Data) != tc.data {
			t.Errorf("%s: %s, %q", tc.name, icon.ContentType, icon.Data)
		}
	}
	for _, r := range f.sent("cdn") {
		if r.userAgent != wantUserAgent {
			t.Errorf("icon request with User-Agent %q", r.userAgent)
		}
	}
}

func TestFetchIconRefusesWhatIsNotAnImage(t *testing.T) {
	f := newFakes(t)
	l := f.library()
	for i, data := range []string{
		`<svg xmlns="http://www.w3.org/2000/svg" onload="alert(1)"><script>alert(2)</script></svg>`,
		`<?xml version="1.0" encoding="UTF-8"?><svg xmlns="http://www.w3.org/2000/svg"/>`,
		"<!DOCTYPE html><html><body><script>alert(3)</script></body></html>",
		"RIFF\x24\x00\x00\x00WAVEfmt ",
		" \x89PNG\r\n\x1a\n",
		"",
	} {
		path := "/data/icons/" + string(rune('a'+i))
		f.hook(path, serveAs("image/png", data))
		_, err := l.FetchIcon(context.Background(), f.cdn.URL+path)
		if e := wantKind(t, err, KindIconRefused); e.Msg != "The icon is not a PNG, JPEG, WebP or GIF image, so Playkeeper will not show it." {
			t.Errorf("message %q", e.Msg)
		}
	}
}

func TestFetchIconRefusals(t *testing.T) {
	f := newFakes(t)
	l := f.library()
	l.MaxIconSize = 1024
	png := "\x89PNG\r\n\x1a\n" + strings.Repeat("\x00", 2048)
	f.hook("/big.png", serveBytes([]byte(png)))
	f.hook("/endless.png", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(png[:512]))
		w.(http.Flusher).Flush()
		w.Write([]byte(png[512:]))
	})
	f.hook("/moved.png", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, f.evil.URL+"/icon.png", http.StatusFound)
	})
	f.hook("/broken.png", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusInternalServerError) })
	cdnHost := f.cdn.Listener.Addr().String()

	for _, tc := range []struct {
		url  string
		kind Kind
		msg  string
	}{
		{"https://example.com/icon.png", KindHostNotAllowed, "Playkeeper only loads icons from Modrinth's and Hangar's file hosts, not from example.com."},
		{f.evil.URL + "/icon.png", KindHostNotAllowed, "Playkeeper only loads icons from Modrinth's and Hangar's file hosts, not from 127.0.0.1."},
		{"https://playkeeper:secret@" + cdnHost + "/icon.png", KindHostNotAllowed, "Playkeeper only loads icons from Modrinth's and Hangar's file hosts, not from 127.0.0.1."},
		{"::not a url", KindHostNotAllowed, "Playkeeper only loads icons from Modrinth's and Hangar's file hosts, not from an invalid address."},
		{"http://" + cdnHost + "/icon.png", KindNotHTTPS, "Playkeeper only loads icons over HTTPS."},
		{f.cdn.URL + "/big.png", KindTooLarge, "The icon is larger than the 1 KiB Playkeeper accepts."},
		{f.cdn.URL + "/endless.png", KindTooLarge, "The icon is larger than the 1 KiB Playkeeper accepts."},
		{f.cdn.URL + "/moved.png", KindRedirectRefused, "The icon was redirected to 127.0.0.1, which is not one of Modrinth's or Hangar's file hosts."},
		{f.cdn.URL + "/missing.png", KindNotFound, "127.0.0.1 answered the icon request with HTTP 404."},
		{f.cdn.URL + "/broken.png", KindUpstream, "127.0.0.1 answered the icon request with HTTP 500."},
	} {
		icon, err := l.FetchIcon(context.Background(), tc.url)
		var e *Error
		if icon != nil || !errors.As(err, &e) || e.Kind != tc.kind {
			t.Errorf("%s: icon %v, error %v (kind %q), want kind %q", tc.url, icon != nil, err, KindOf(err), tc.kind)
			continue
		}
		inner := ""
		if e.Err != nil {
			inner = e.Err.Error()
		}
		if e.Msg != tc.msg || strings.Contains(e.Msg+e.Hint+inner, "secret") {
			t.Errorf("%s: message %q", tc.url, e.Msg)
		}
	}
	if s := f.strays(); len(s) != 0 {
		t.Errorf("requests went to %v", s)
	}
	if got := len(f.sent("cdn")); got != 5 {
		t.Errorf("%d requests reached the CDN, want 5", got)
	}
}
