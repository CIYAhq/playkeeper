package modpacks

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"path"
	"strconv"
	"strings"

	"github.com/CIYAhq/playkeeper/internal/addons"
	"github.com/CIYAhq/playkeeper/internal/modpacks/mrpack"
)

// missingModsList is where Missing Mods Checker reads the mods it asks
// players for.
const missingModsList = "config/missing_mods_checker.json"

// missingMod is one entry of Missing Mods Checker's list: a mod only
// CurseForge offers, at its download page.
type missingMod struct {
	DisplayName string `json:"displayName"`
	URL         string `json:"url"`
	Destination string `json:"destination"`
}

// missingModsChecker handles Missing Mods Checker, which Modrinth packs like
// Better MC 4 and 2 ship to have players download the pack's mods that only
// CurseForge offers: on a server it opens a window, which stops the start.
// It stays off the server, and the mods its list names come from
// CurseForge instead, when CurseForge lets Playkeeper download them; each
// other one gets a step to download it by hand. Mods CurseForge tags for
// players' games stay off too.
func (l *Library) missingModsChecker(ctx context.Context, p *pack, lim Limits) error {
	jar := ""
	for rel, f := range p.files {
		if f.origin == Override && path.Dir(rel) == "mods" && strings.HasPrefix(strings.ToLower(path.Base(rel)), "missingmodschecker") {
			jar = rel
		}
	}
	if jar == "" {
		return nil
	}
	delete(p.files, jar)
	p.skip(jar, addons.KindClientOnly)
	e := p.arch.layer(mrpack.Overrides, mrpack.ServerOverrides)[missingModsList]
	if e == nil {
		return nil
	}
	b, err := p.arch.read(e.Name, lim.Index)
	if err != nil {
		return err
	}
	var list []missingMod
	if json.Unmarshal(b, &list) != nil {
		return nil
	}
	byID := map[int64]missingMod{}
	var ids []int64
	for _, m := range list {
		if id, ok := curseForgeDownload(m.URL); ok && m.Destination == "mods" && byID[id].URL == "" {
			byID[id] = m
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	if l.CurseForge == nil {
		for _, id := range ids {
			m := byID[id]
			name := printable(m.DisplayName)
			p.manual = append(p.manual, addons.ManualStep{Notice: notice(KindNoCurseForge, kv("pack", p.info.Name, "name", name),
				fmt.Sprintf("%s needs %s, which only CurseForge offers, and this Playkeeper has no CurseForge key.", p.info.Name, name),
				"Add a CurseForge key in Settings › Add-on sources and install the pack again, or download it from its page and upload it to the mods folder."),
				URL: m.URL})
		}
		return nil
	}
	files, err := l.CurseForge.Files(ctx, ids)
	if err != nil {
		return Upstream(CurseForge, err)
	}
	for i := range files {
		f := &files[i]
		m, ok := byID[f.ID]
		if !ok {
			continue
		}
		name, target := printable(m.DisplayName), "mods/"+f.FileName
		switch {
		case f.ClientOnly():
			p.skip("mods/"+printable(f.FileName), addons.KindClientOnly)
		case !plainJar(f.FileName) || !f.IsAvailable || f.SHA1() == "" || p.files[target] != nil:
		case f.DownloadURL == "":
			p.manual = append(p.manual, addons.ManualStep{Notice: notice(addons.KindExternal, kv("pack", p.info.Name, "name", name, "file", printable(f.FileName), "folder", "mods"),
				fmt.Sprintf("%s's author only allows downloads through CurseForge's app, so Playkeeper cannot download %s for you.", name, printable(f.FileName)),
				"Download it from that page and upload it to the mods folder."), URL: m.URL})
		default:
			if _, err := l.curseForgeFiles().Check(f.DownloadURL); err != nil {
				continue
			}
			p.files[target] = &packFile{
				path: target, origin: Download, project: strconv.FormatInt(f.ModID, 10), name: name, on: true,
				size: f.FileLength, sums: map[string]string{"sha1": f.SHA1()}, urls: []string{f.DownloadURL}, hosts: l.curseForgeFiles(),
			}
		}
	}
	return nil
}

// curseForgeDownload reads the file id out of a CurseForge download page,
// www.curseforge.com/minecraft/mc-mods/<project>/download/<file>.
func curseForgeDownload(raw string) (int64, bool) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host != "www.curseforge.com" {
		return 0, false
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) != 5 || parts[0] != "minecraft" || parts[3] != "download" && parts[3] != "files" {
		return 0, false
	}
	id, err := strconv.ParseInt(parts[4], 10, 64)
	return id, err == nil && id > 0
}
