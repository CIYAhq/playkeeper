package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/CIYAhq/playkeeper/internal/sizing"
)

const siteDir = "../../site"

func renderSite(t *testing.T) map[string][]byte {
	t.Helper()
	files, err := render(os.DirFS(siteDir))
	if err != nil {
		t.Fatal(err)
	}
	return files
}

func TestCommittedFilesAreUpToDate(t *testing.T) {
	files := renderSite(t)
	if len(files) != 2 {
		t.Fatalf("render wrote %d files, want %s and %s", len(files), pageFile, dataFile)
	}
	for name, want := range files {
		got, err := os.ReadFile(filepath.Join(siteDir, name))
		if err != nil || !bytes.Equal(got, want) {
			t.Errorf("site/%s is out of date; run make sizing (or go run ./cmd/sizing-guide) and commit the result", name)
		}
	}
}

// The site's Content-Security-Policy (site/nginx.conf) allows scripts,
// styles and images only from playkeeper.io itself.
func TestPageNeedsNothingTheSitePolicyBlocks(t *testing.T) {
	page := string(renderSite(t)[pageFile])
	scripts := regexp.MustCompile(`(?s)<script\b([^>]*)>(.*?)</script>`).FindAllStringSubmatch(page, -1)
	if len(scripts) == 0 {
		t.Fatal("the page loads no scripts")
	}
	for _, s := range scripts {
		if !strings.Contains(s[1], ` src="/`) || s[2] != "" {
			t.Errorf("inline or off-site script: %s", s[0])
		}
	}
	for _, bad := range []*regexp.Regexp{
		regexp.MustCompile(`<style\b`),
		regexp.MustCompile(`\sstyle=`),
		regexp.MustCompile(`\son[a-z]+=`),
		regexp.MustCompile(`javascript:`),
		regexp.MustCompile(`\s(?:src|srcset)="(?:https?:)?//`),
	} {
		if m := bad.FindString(page); m != "" {
			t.Errorf("the page has %q, which the site's policy blocks", m)
		}
	}
	for _, link := range regexp.MustCompile(`<link\b[^>]*>`).FindAllString(page, -1) {
		if (strings.Contains(link, `rel="stylesheet"`) || strings.Contains(link, `rel="icon"`)) && !strings.Contains(link, ` href="/`) {
			t.Errorf("off-site stylesheet or icon: %s", link)
		}
	}
	for _, m := range regexp.MustCompile(`\s(?:src|href)="(/[^"#]*)`).FindAllStringSubmatch(page, -1) {
		if m[1] == "/" {
			continue
		}
		if _, err := os.Stat(filepath.Join(siteDir, path.Clean(m[1]))); err != nil {
			t.Errorf("the page uses %s, which is not in site/", m[1])
		}
	}
}

func TestTableWorksWithoutJavaScript(t *testing.T) {
	page := string(renderSite(t)[pageFile])
	tags := regexp.MustCompile(`<[^>]+>`)
	var cols []string
	for _, m := range regexp.MustCompile(`<th scope="col">(.*?)</th>`).FindAllStringSubmatch(page, -1) {
		cols = append(cols, m[1])
	}
	want := []string{"Friends at once"}
	for _, w := range sizing.Workloads() {
		want = append(want, w.Label())
	}
	if !slices.Equal(cols, want) {
		t.Errorf("columns = %q, want %q", cols, want)
	}

	cells := map[string]string{}
	rows := regexp.MustCompile(`(?s)<tr><th scope="row">(.*?)</th>(.*?)</tr>`).FindAllStringSubmatch(page, -1)
	if len(rows) != len(sizing.Bands()) {
		t.Fatalf("the table has %d rows, want one for each of %d groups", len(rows), len(sizing.Bands()))
	}
	for i, b := range sizing.Bands() {
		if rows[i][1] != b.Label() {
			t.Errorf("row %d is %q, want %q", i+1, rows[i][1], b.Label())
		}
		tds := regexp.MustCompile(`<td id="([^"]+)">(.*?)</td>`).FindAllStringSubmatch(rows[i][2], -1)
		if len(tds) != len(sizing.Workloads()) {
			t.Fatalf("row %q has %d sizes, want %d", b.Label(), len(tds), len(sizing.Workloads()))
		}
		for j, w := range sizing.Workloads() {
			if id := "size-" + b.Key() + "-" + string(w); tds[j][1] != id {
				t.Errorf("row %q column %d is %s, want %s", b.Label(), j+1, tds[j][1], id)
			}
			cells[tds[j][1]] = tags.ReplaceAllString(tds[j][2], "")
		}
	}
	for _, r := range sizing.Table() {
		id := "size-" + r.Band.Key() + "-" + string(r.Workload)
		if want := fmt.Sprintf("%d GB, %d cores, · %d GB disk", r.MemoryGB, r.Cores, r.DiskGB); cells[id] != want {
			t.Errorf("%s shows %q, want %q", id, cells[id], want)
		}
	}

	for _, want := range []string{
		fmt.Sprintf("Playkeeper itself needs at least %d CPU cores, %d GB of memory and %d GB of free disk.", sizing.MinCores, sizing.MinMemoryGB, sizing.MinFreeDiskGB),
		fmt.Sprintf("More than %d friends at once?", sizing.MaxPlayers),
		`<code id="command">` + installCommand + `</code>`,
		`<a href="/#install">`,
	} {
		if !strings.Contains(page, want) {
			t.Errorf("the page lacks %q", want)
		}
	}
	index, err := os.ReadFile(filepath.Join(siteDir, "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(index), installCommand) {
		t.Errorf("site/index.html no longer shows %q; keep the sizing guide's install command the same", installCommand)
	}
	if !strings.Contains(string(index), `id="install"`) {
		t.Errorf(`site/index.html has no id="install", which the sizing guide's links to /#install open at`)
	}
}

func TestDataHasEveryAnswerInThePagesOrder(t *testing.T) {
	files := renderSite(t)
	js := string(files[dataFile])
	for i := 0; i < len(js); i++ {
		if js[i] >= 0x80 {
			t.Fatalf("%s has a byte past ASCII at %d", dataFile, i)
		}
	}
	const prefix = "window.playkeeperSizing = "
	start := strings.Index(js, prefix)
	if start < 0 || !strings.HasSuffix(js, ";\n") {
		t.Fatalf("%s does not set window.playkeeperSizing:\n%s", dataFile, js)
	}
	var data struct {
		Answers map[string]map[string]answer `json:"answers"`
	}
	if err := json.Unmarshal([]byte(js[start+len(prefix):len(js)-2]), &data); err != nil {
		t.Fatal(err)
	}

	// The script fills in each reason's value and text under the label the page shows.
	var labels []string
	for _, m := range regexp.MustCompile(`<dt>(.*?)</dt>`).FindAllStringSubmatch(string(files[pageFile]), -1) {
		labels = append(labels, m[1])
	}
	count := 0
	for _, byWorkload := range data.Answers {
		count += len(byWorkload)
	}
	if count != len(sizing.Table()) {
		t.Errorf("%s has %d answers, want %d", dataFile, count, len(sizing.Table()))
	}
	for _, r := range sizing.Table() {
		a, ok := data.Answers[r.Band.Key()][string(r.Workload)]
		if !ok {
			t.Errorf("no answer for %s friends on %s", r.Band.Key(), r.Workload)
			continue
		}
		if a.Title != r.Title || a.Summary != r.Summary || a.Short != fmt.Sprintf("%d GB of memory", r.MemoryGB) || a.Announce == "" {
			t.Errorf("%s/%s: answer %+v does not match %+v", r.Band.Key(), r.Workload, a, r)
		}
		var got []string
		for i, x := range a.Reasons {
			got = append(got, x.Label)
			if i < len(r.Reasons) && (x.Value != r.Reasons[i].Value || x.Text != r.Reasons[i].Text) {
				t.Errorf("%s/%s: reason %+v, want %+v", r.Band.Key(), r.Workload, x, r.Reasons[i])
			}
		}
		if !slices.Equal(got, labels) {
			t.Errorf("%s/%s: reasons are %q, but the page shows %q", r.Band.Key(), r.Workload, got, labels)
		}
	}
}

func TestMissingTemplateSaysWhereToRunIt(t *testing.T) {
	_, err := render(fstest.MapFS{})
	if err == nil || !strings.Contains(err.Error(), "run it from the repository root or pass -site") {
		t.Errorf("err = %v, want a hint to run it from the repository root", err)
	}
}
