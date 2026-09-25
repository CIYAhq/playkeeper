package software

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"
)

const maxZipEntries = 200_000

var (
	reJavaClass = regexp.MustCompile(`^[A-Za-z_$][A-Za-z0-9_$]*(\.[A-Za-z_$][A-Za-z0-9_$]*)+$`)
	reMavenPart = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]*$`)
)

func jarError(rel, why string, cause error) error {
	return &Error{Kind: KindMalformed, Msg: fmt.Sprintf("Playkeeper could not read %s: %s.", rel, why),
		Hint:   "Reinstall the server software. If it keeps happening, the upstream may have changed its files and Playkeeper needs an update.",
		Params: map[string]string{"file": rel}, Err: cause}
}

// readZipEntry reads one file out of a jar inside dataDir without extracting
// anything to disk. Oversized entries, duplicated names and archives with an
// absurd number of entries are refused.
func readZipEntry(dataDir, rel, name string, limit int64) ([]byte, error) {
	p, err := within(dataDir, rel)
	if err != nil {
		return nil, err
	}
	r, err := zip.OpenReader(p)
	if err != nil {
		return nil, jarError(rel, "it is not a readable jar", err)
	}
	defer r.Close()
	if len(r.File) > maxZipEntries {
		return nil, jarError(rel, fmt.Sprintf("it has more than %d entries", maxZipEntries), nil)
	}
	var found *zip.File
	for _, f := range r.File {
		if f.Name != name {
			continue
		}
		if found != nil {
			return nil, jarError(rel, "it contains "+name+" twice", nil)
		}
		found = f
	}
	if found == nil {
		return nil, jarError(rel, "it has no "+name, nil)
	}
	if found.UncompressedSize64 > uint64(limit) {
		return nil, jarError(rel, fmt.Sprintf("its %s is larger than %s", name, size(limit)), nil)
	}
	rc, err := found.Open()
	if err != nil {
		return nil, jarError(rel, "its "+name+" cannot be opened", err)
	}
	defer rc.Close()
	b, err := io.ReadAll(io.LimitReader(rc, limit+1))
	if err != nil {
		return nil, jarError(rel, "its "+name+" cannot be read", err)
	}
	if int64(len(b)) > limit {
		return nil, jarError(rel, fmt.Sprintf("its %s is larger than %s", name, size(limit)), nil)
	}
	return b, nil
}

// manifestAttribute returns an attribute of a jar manifest's main section.
func manifestAttribute(manifest []byte, name string) string {
	var lines []string
	for _, l := range strings.Split(string(manifest), "\n") {
		l = strings.TrimSuffix(l, "\r")
		if l == "" {
			break
		}
		if strings.HasPrefix(l, " ") && len(lines) > 0 {
			lines[len(lines)-1] += l[1:]
			continue
		}
		lines = append(lines, l)
	}
	for _, l := range lines {
		if k, v, ok := strings.Cut(l, ":"); ok && strings.EqualFold(k, name) {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// manifestAttr writes one manifest line, wrapped like java.util.jar does:
// at most 72 bytes per line, continuation lines starting with a space.
func manifestAttr(b *strings.Builder, name, value string) {
	line := name + ": " + value
	for len(line) > 72 {
		b.WriteString(line[:72])
		b.WriteString("\r\n")
		line = " " + line[72:]
	}
	b.WriteString(line)
	b.WriteString("\r\n")
}

// launchJarTime keeps launch jars byte-for-byte reproducible.
var launchJarTime = time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)

// launchJar builds a jar with no code in it, like the one the official
// Fabric and Quilt installers write for servers: a manifest naming the
// loader's launcher class and the libraries it runs with and, for Fabric,
// one properties file naming the class the launcher starts.
func launchJar(mainClass string, classPath []string, propsName, props string) ([]byte, error) {
	var m strings.Builder
	manifestAttr(&m, "Manifest-Version", "1.0")
	manifestAttr(&m, "Main-Class", mainClass)
	manifestAttr(&m, "Class-Path", strings.Join(classPath, " "))
	m.WriteString("\r\n")
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	add := func(name, body string) error {
		w, err := zw.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Deflate, Modified: launchJarTime})
		if err != nil {
			return err
		}
		_, err = io.WriteString(w, body)
		return err
	}
	if err := add("META-INF/MANIFEST.MF", m.String()); err != nil {
		return nil, err
	}
	if propsName != "" {
		if err := add(propsName, props); err != nil {
			return nil, err
		}
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

type bundled struct {
	sha256, id, path string
}

// bundlerList reads a list inside Mojang's server jar (META-INF/libraries.list
// or versions.list): one "sha256<TAB>id<TAB>path" line per bundled file.
func bundlerList(dataDir, jar, entry string) ([]bundled, error) {
	b, err := readZipEntry(dataDir, jar, entry, 1<<20)
	if err != nil {
		return nil, err
	}
	var out []bundled
	for _, l := range strings.Split(string(b), "\n") {
		l = strings.TrimSuffix(l, "\r")
		if l == "" {
			continue
		}
		f := strings.Split(l, "\t")
		if len(f) != 3 {
			return nil, jarError(jar, "a line of its "+entry+" does not have three tab-separated fields", nil)
		}
		h, ok := parseHash(SHA256, f[0])
		if !ok || !cleanRel(f[2], ".jar") {
			return nil, jarError(jar, fmt.Sprintf("its %s lists %q with an invalid hash or path", entry, f[2]), nil)
		}
		out = append(out, bundled{sha256: h.Value, id: f[1], path: f[2]})
		if len(out) > 10_000 {
			return nil, jarError(jar, "its "+entry+" lists more than 10000 files", nil)
		}
	}
	return out, nil
}

// mavenPath turns a Maven coordinate, "group:artifact:version[:classifier]"
// with an optional "@extension", into the file's path in a repository.
func mavenPath(coord string) (string, bool) {
	ext := "jar"
	if c, e, ok := strings.Cut(coord, "@"); ok {
		coord, ext = c, e
	}
	parts := strings.Split(coord, ":")
	if len(parts) < 3 || len(parts) > 4 {
		return "", false
	}
	for _, p := range append(parts, ext) {
		if !reMavenPart.MatchString(p) {
			return "", false
		}
	}
	file := parts[1] + "-" + parts[2]
	if len(parts) == 4 {
		file += "-" + parts[3]
	}
	p := strings.ReplaceAll(parts[0], ".", "/") + "/" + parts[1] + "/" + parts[2] + "/" + file + "." + ext
	return p, cleanRel(p)
}
