package update

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

// BuildManifest describes a release tarball: its size and SHA-256 and the
// SHA-256 of the playkeeper binary inside it. The result is what the release
// workflow signs.
func BuildManifest(version, date, notes, tarball string) ([]byte, error) {
	if _, err := ParseVersion(version); err != nil {
		return nil, err
	}
	sum, err := FileSHA256(tarball)
	if err != nil {
		return nil, err
	}
	st, err := os.Stat(tarball)
	if err != nil {
		return nil, err
	}
	binary := "playkeeper-" + version + "-linux-amd64/playkeeper"
	binSum, err := entrySHA256(tarball, binary)
	if err != nil {
		return nil, err
	}
	m := Manifest{Schema: 1, Version: version, Date: date, Notes: strings.TrimSpace(notes), Assets: []Asset{{
		Platform: Platform, File: TarballFile, SHA256: sum, Size: st.Size(), Binary: binary, BinarySHA256: binSum,
	}}}
	if err := m.validate(); err != nil {
		return nil, err
	}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

func entrySHA256(tarball, name string) (string, error) {
	f, err := os.Open(tarball)
	if err != nil {
		return "", err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return "", err
	}
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return "", fmt.Errorf("%s does not contain %s", tarball, name)
		}
		if err != nil {
			return "", err
		}
		if hdr.Name == name && hdr.Typeflag == tar.TypeReg {
			h := sha256.New()
			if _, err := io.Copy(h, tr); err != nil {
				return "", err
			}
			return hex.EncodeToString(h.Sum(nil)), nil
		}
	}
}

// ChangelogSection returns the text under the "## <version>" heading of a
// changelog, up to the next "## " heading.
func ChangelogSection(changelog []byte, version string) (string, bool) {
	var out []string
	in := false
	for _, line := range strings.Split(string(changelog), "\n") {
		if strings.HasPrefix(line, "## ") {
			if in {
				break
			}
			head := strings.Fields(strings.TrimPrefix(line, "## "))
			in = len(head) > 0 && strings.Trim(head[0], "[]") == strings.TrimPrefix(version, "v")
			continue
		}
		if in {
			out = append(out, line)
		}
	}
	text := strings.TrimSpace(strings.Join(out, "\n"))
	return text, text != ""
}
