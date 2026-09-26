package share

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"hash/crc32"
	"mime"
	"net/http"
	"regexp"
	"time"

	"github.com/CIYAhq/playkeeper/internal/modpacks/mrpack"
)

// ContentType is the media type of .mrpack files.
const ContentType = "application/x-modrinth-modpack+zip"

// dosEpoch is 1980-01-01 in a zip header's date field, the earliest date a
// zip can hold.
const dosEpoch = 0x21

// File returns the friends' .mrpack: a zip holding only modrinth.index.json,
// stored uncompressed and dated 1980-01-01, so the same share gives the same
// bytes whatever the clock or the Go version.
func (s *Share) File() ([]byte, error) {
	ix, err := indexJSON(&s.Index)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.CreateRaw(&zip.FileHeader{
		Name: mrpack.IndexName, Method: zip.Store, CreatorVersion: 20, ReaderVersion: 20, ModifiedDate: dosEpoch,
		CRC32: crc32.ChecksumIEEE(ix), CompressedSize64: uint64(len(ix)), UncompressedSize64: uint64(len(ix)),
	})
	if err != nil {
		return nil, err
	}
	if _, err := w.Write(ix); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func indexJSON(ix *mrpack.Index) ([]byte, error) { return json.MarshalIndent(ix, "", "  ") }

var slugPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,39}$`)

// ValidSlug accepts a server's slug, the name in its public links.
func ValidSlug(s string) bool { return slugPattern.MatchString(s) }

// FileName is the file's name for a server's slug.
func FileName(slug string) string {
	if !ValidSlug(slug) {
		return "server.mrpack"
	}
	return slug + ".mrpack"
}

// ServeFile answers a request for the file with a download named after the
// server's slug. The public route and the signed-in one both use it.
func (s *Share) ServeFile(w http.ResponseWriter, r *http.Request, slug string) {
	b, err := s.File()
	if err != nil {
		http.Error(w, "Playkeeper could not make the file.", http.StatusInternalServerError)
		return
	}
	h := w.Header()
	h.Set("Content-Type", ContentType)
	h.Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": FileName(slug)}))
	h.Set("Cache-Control", "no-store")
	h.Set("X-Content-Type-Options", "nosniff")
	http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(b))
}
