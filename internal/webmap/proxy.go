package webmap

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/CIYAhq/playkeeper/internal/api"
)

// Limits on what squaremap may answer. A 512×512 tile is about 100 KB and
// at most about 1 MB even for noise.
const (
	maxTileBytes = 2 << 20
	maxJSONBytes = 1 << 20
	maxWorlds    = 16
	maxPlayers   = 500
)

const userAgent = "playkeeper (https://github.com/CIYAhq/playkeeper)"

// tileCache lets a browser reuse a tile for one background-render interval
// before asking again with its ETag.
const tileCache = "private, max-age=30"

var (
	reWorld      = regexp.MustCompile(`^[a-z0-9_][a-z0-9_.-]{0,99}$`)
	reTileFile   = regexp.MustCompile(`^(-?[0-9]{1,7})_(-?[0-9]{1,7})\.png$`)
	pngSignature = []byte("\x89PNG\r\n\x1a\n")
)

// validWorld accepts squaremap's name for a world: its dimension id with ':'
// replaced by '_', without folders or dot segments.
func validWorld(s string) bool { return reWorld.MatchString(s) && !strings.Contains(s, "..") }

// ServeHTTP answers the map page for one server. Mount it with
// http.StripPrefix; it serves only
//
//	/worlds                            the worlds, their spawn and zoom levels
//	/players                           who is where, in Playkeeper's shape
//	/tiles/{world}/{zoom}/{x}_{z}.png  one tile
//
// to GET and HEAD. It asks squaremap for exactly those paths, rebuilt from
// validated parts, and passes on nothing from the viewer's request but the
// conditional headers of a tile request: no cookies, credentials or
// addresses. Tiles must be PNG images and other answers JSON, within size
// limits; JSON is rebuilt from the fields Playkeeper uses.
func (m Map) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		writeError(w, r, http.StatusMethodNotAllowed, fail(KindMethod, nil, "The map only answers GET and HEAD requests.", ""))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), m.timeout())
	defer cancel()
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/"), "/")
	switch {
	case len(parts) == 1 && parts[0] == "worlds":
		ws, err := m.worlds(ctx)
		if err != nil {
			writeError(w, r, statusFor(err), err)
			return
		}
		writeJSON(w, r, Worlds{Worlds: ws, TileSize: TileSize})
	case len(parts) == 1 && parts[0] == "players":
		ps, err := m.players(ctx)
		if err != nil {
			writeError(w, r, statusFor(err), err)
			return
		}
		writeJSON(w, r, ps)
	case len(parts) == 4 && parts[0] == "tiles":
		m.serveTile(ctx, w, r, parts[1], parts[2], parts[3])
	default:
		writeError(w, r, http.StatusNotFound, notFound())
	}
}

func (m Map) serveTile(ctx context.Context, w http.ResponseWriter, r *http.Request, world, zoom, file string) {
	z, err := strconv.Atoi(zoom)
	xz := reTileFile.FindStringSubmatch(file)
	if !validWorld(world) || err != nil || strconv.Itoa(z) != zoom || z < 0 || z > ZoomMax ||
		xz == nil || !canonicalInt(xz[1]) || !canonicalInt(xz[2]) {
		writeError(w, r, http.StatusNotFound, notFound())
		return
	}
	cond := http.Header{}
	for _, k := range []string{"If-None-Match", "If-Modified-Since"} {
		if v := r.Header.Get(k); validHeader(v) {
			cond.Set(k, v)
		}
	}
	a, err := m.get(ctx, "/tiles/"+world+"/"+zoom+"/"+xz[1]+"_"+xz[2]+".png", "image/png", maxTileBytes, cond)
	if err != nil {
		writeError(w, r, statusFor(err), err)
		return
	}
	h := w.Header()
	switch {
	case a.status == http.StatusNotModified:
		copyValidators(h, a.header)
		h.Set("Cache-Control", tileCache)
		w.WriteHeader(http.StatusNotModified)
		return
	// squaremap answers 200 with no body for a tile it has not drawn.
	case a.status == http.StatusNotFound || a.status == http.StatusOK && len(a.body) == 0:
		h.Set("Cache-Control", tileCache)
		writeErrorCached(w, r, http.StatusNotFound, fail(KindNotDrawn, nil, "This part of the map is not drawn yet.", ""))
		return
	case a.status != http.StatusOK:
		writeError(w, r, http.StatusBadGateway, badAnswer(fmt.Sprintf("HTTP %d", a.status)))
		return
	case !isType(a.header, "image/png") || !bytes.HasPrefix(a.body, pngSignature):
		writeError(w, r, http.StatusBadGateway, badAnswer("a tile that is not a PNG image"))
		return
	}
	copyValidators(h, a.header)
	h.Set("Content-Type", "image/png")
	h.Set("Content-Length", strconv.Itoa(len(a.body)))
	h.Set("Cache-Control", tileCache)
	h.Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		w.Write(a.body)
	}
}

func canonicalInt(s string) bool {
	n, err := strconv.Atoi(s)
	return err == nil && strconv.Itoa(n) == s
}

// validHeader accepts a short printable ASCII header value.
func validHeader(v string) bool {
	if v == "" || len(v) > 200 {
		return false
	}
	for i := 0; i < len(v); i++ {
		if v[i] < 0x20 || v[i] > 0x7e {
			return false
		}
	}
	return true
}

func copyValidators(dst, src http.Header) {
	for _, k := range []string{"ETag", "Last-Modified"} {
		if v := src.Get(k); validHeader(v) {
			dst.Set(k, v)
		}
	}
}

func isType(h http.Header, want string) bool {
	mt, _, err := mime.ParseMediaType(h.Get("Content-Type"))
	return err == nil && mt == want
}

// answer is squaremap's answer to one request. body is read only for 200.
type answer struct {
	status int
	header http.Header
	body   []byte
}

func (m Map) get(ctx context.Context, path, accept string, limit int64, cond http.Header) (answer, error) {
	addr, err := m.addr()
	if err != nil {
		return answer{}, notRunning(err)
	}
	u := url.URL{Scheme: "http", Host: addr, Path: path}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return answer{}, err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", accept)
	for k, v := range cond {
		req.Header[k] = v
	}
	resp, err := m.client().Do(req)
	if err != nil {
		return answer{}, unreachable(ctx, err)
	}
	defer resp.Body.Close()
	a := answer{status: resp.StatusCode, header: resp.Header}
	if resp.StatusCode != http.StatusOK {
		return a, nil
	}
	if resp.ContentLength > limit {
		return answer{}, tooLarge(limit)
	}
	a.body, err = io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return answer{}, unreachable(ctx, err)
	}
	if int64(len(a.body)) > limit {
		return answer{}, tooLarge(limit)
	}
	return a, nil
}

// getJSON decodes squaremap's JSON answer for path into v. It reports false
// when squaremap has no such file yet.
func (m Map) getJSON(ctx context.Context, path string, v any) (bool, error) {
	a, err := m.get(ctx, path, "application/json", maxJSONBytes, nil)
	switch {
	case err != nil:
		return false, err
	case a.status == http.StatusNotFound:
		return false, nil
	case a.status != http.StatusOK:
		return false, badAnswer(fmt.Sprintf("HTTP %d", a.status))
	case !isType(a.header, "application/json"):
		return false, badAnswer("not JSON")
	}
	if err := json.Unmarshal(a.body, v); err != nil {
		return false, badAnswer("unreadable JSON")
	}
	return true, nil
}

func notRunning(err error) *Error {
	e := fail(KindNotRunning, nil, "The map shows while the server is running.", "Start the server to see it.")
	e.Err = err
	return e
}

func unreachable(ctx context.Context, err error) *Error {
	var ne net.Error
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.As(err, &ne) && ne.Timeout() {
		e := fail(KindTooSlow, nil, "The map took too long to answer.", "The server may be busy. Try again in a moment.")
		e.Err = err
		return e
	}
	e := fail(KindNotAnswering, nil, "The map is not answering.",
		"squaremap runs inside the server. Restart the server; if the map still does not answer, look for squaremap errors in the console.")
	e.Err = err
	return e
}

func badAnswer(what string) *Error {
	return fail(KindBadAnswer, kv("reason", what), "The map sent an answer Playkeeper cannot use ("+what+").",
		"Restart the server. If this keeps happening, remove squaremap and turn the map on again.")
}

func tooLarge(limit int64) *Error {
	return badAnswer(fmt.Sprintf("more than %d MB", limit>>20))
}

func notFound() *Error {
	return fail(KindNotFound, nil, "The map has no such page.", "")
}

func statusFor(err error) int {
	switch KindOf(err) {
	case KindNotRunning, KindStarting:
		return http.StatusServiceUnavailable
	case KindTooSlow:
		return http.StatusGatewayTimeout
	}
	return http.StatusBadGateway
}

func writeJSON(w http.ResponseWriter, r *http.Request, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "application/json")
	h.Set("Content-Length", strconv.Itoa(len(b)))
	h.Set("Cache-Control", "no-store")
	h.Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		w.Write(b)
	}
}

func writeError(w http.ResponseWriter, r *http.Request, status int, err error) {
	w.Header().Set("Cache-Control", "no-store")
	writeErrorCached(w, r, status, err)
}

// writeErrorCached writes err in the API's error shape, keeping the
// Cache-Control already set.
func writeErrorCached(w http.ResponseWriter, r *http.Request, status int, err error) {
	body := api.Error{Error: "The map could not answer.", Code: api.CodeInternal}
	var e *Error
	if errors.As(err, &e) {
		body = api.Error{Error: e.Msg, Code: string(e.Kind), Hint: e.Hint}
	}
	b, _ := json.Marshal(body)
	h := w.Header()
	h.Set("Content-Type", "application/json")
	h.Set("Content-Length", strconv.Itoa(len(b)))
	h.Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	if r.Method != http.MethodHead {
		w.Write(b)
	}
}
