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
	"path/filepath"
	"strings"
)

// BuildManifest describes release tarballs, each named as TarballName names
// its platform's: its size and SHA-256 and the SHA-256 of the playkeeper
// binary inside it, in the order of Platforms. The result is what the release
// workflow signs.
func BuildManifest(version, date, notes string, tarballs ...string) ([]byte, error) {
	if _, err := ParseVersion(version); err != nil {
		return nil, err
	}
	if len(tarballs) == 0 {
		return nil, errors.New("a release needs at least one tarball")
	}
	byPlatform := map[string]string{}
	for _, t := range tarballs {
		platform := ""
		for _, p := range Platforms {
			if filepath.Base(t) == TarballName(p) {
				platform = p
			}
		}
		if platform == "" {
			return nil, fmt.Errorf("%s is not named like a release tarball (%s)", filepath.Base(t), TarballName("linux-<arch>"))
		}
		if byPlatform[platform] != "" {
			return nil, fmt.Errorf("two tarballs for %s", platform)
		}
		byPlatform[platform] = t
	}
	m := Manifest{Schema: 1, Version: version, Date: date, Notes: strings.TrimSpace(notes)}
	for _, platform := range Platforms {
		tarball := byPlatform[platform]
		if tarball == "" {
			continue
		}
		sum, err := FileSHA256(tarball)
		if err != nil {
			return nil, err
		}
		st, err := os.Stat(tarball)
		if err != nil {
			return nil, err
		}
		binary := BinaryPath(version, platform)
		binSum, err := entrySHA256(tarball, binary)
		if err != nil {
			return nil, err
		}
		a := Asset{Platform: platform, File: TarballName(platform), SHA256: sum, Size: st.Size(), Binary: binary, BinarySHA256: binSum}
		if err := m.validateAsset(a, platform); err != nil {
			return nil, err
		}
		m.Assets = append(m.Assets, a)
	}
	if err := m.validateHeader(); err != nil {
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
