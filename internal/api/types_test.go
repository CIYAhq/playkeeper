package api

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/CIYAhq/playkeeper/internal/pregen"
)

const webSrc = "../../web/src"

func jsonNames(t reflect.Type) map[string]bool {
	names := map[string]bool{}
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		switch {
		case !f.IsExported() || name == "-":
		case name == "":
			names[f.Name] = true
		default:
			names[name] = true
		}
	}
	return names
}

// A field the dashboard declares that the API never sends is always
// undefined in the browser.
func TestTheDashboardDeclaresOnlyFieldsTheAPISends(t *testing.T) {
	src, err := os.ReadFile(filepath.Join(webSrc, "api", "types.ts"))
	if err != nil {
		t.Fatal(err)
	}
	sent := map[string]any{
		"Activity": Activity{}, "AuditEntry": AuditEntry{}, "Backup": Backup{}, "Catalog": Catalog{}, "CatalogEntry": CatalogEntry{},
		"DailyActivity": DailyActivity{}, "FirstSteps": FirstSteps{}, "Gameplay": Gameplay{}, "Gap": Gap{}, "LogLine": LogLine{},
		"LogsResponse": LogsResponse{}, "Machine": Machine{}, "ManifestSummary": ManifestSummary{}, "MetricsBucket": MetricsBucket{},
		"MetricsResponse": MetricsResponse{}, "Operation": Operation{}, "OperatorEntry": OperatorEntry{}, "PlayerSnapshot": PlayerSnapshot{},
		"PlayersSummary": PlayersSummary{}, "PlayerStat": PlayerStat{}, "Preflight": Preflight{}, "PreflightCheck": PreflightCheck{},
		"Resources": Resources{}, "RestorePreview": RestorePreview{}, "ServerConfig": ServerConfig{}, "ServerMemory": ServerMemory{},
		"ServerStatus": ServerStatus{}, "ServerType": ServerType{}, "Session": Session{}, "SessionsResponse": SessionsResponse{},
		"UpdateInfo": UpdateInfo{}, "UpdateResult": UpdateResult{}, "WhitelistEntry": WhitelistEntry{}, "WorldCopy": WorldCopy{},
		"Addon": Addon{}, "AddonBrowse": AddonBrowse{}, "AddonCard": AddonCard{}, "AddonChecks": AddonChecks{}, "AddonDetails": AddonDetails{},
		"AddonFile": AddonFile{}, "AddonKey": AddonKey{}, "AddonNotice": AddonNotice{}, "AddonPlan": AddonPlan{}, "AddonProgress": AddonProgress{},
		"AddonRemovePreview": AddonRemovePreview{}, "AddonRemoval": AddonRemoval{}, "Addons": Addons{}, "AddonStep": AddonStep{},
		"AddonTarget": AddonTarget{}, "AddonUpdate": AddonUpdate{}, "AddonVersion": AddonVersion{}, "DataPack": DataPack{}, "DataPacks": DataPacks{},
		"Pregen": Pregen{}, "PregenPreset": PregenPreset{}, "ResourcePack": ResourcePack{}, "ResourcePackOffer": ResourcePackOffer{},
		"ApiErrorBody": Error{}, "DiscordDelivery": DiscordDelivery{}, "DiscordSettings": DiscordSettings{}, "Note": Note{},
		"PlayerDay": PlayerDay{}, "PlayerProfile": PlayerProfile{},
	}
	addedByPanel := map[string]bool{"ServerStatus.machineId": true, "AuditEntry.source": true}
	field := regexp.MustCompile(`(?m)^  (\w+)\??:`)
	// An interface may be declared more than once: TypeScript merges the
	// declarations, and each one's fields are checked.
	found := map[string]bool{}
	for _, m := range regexp.MustCompile(`(?ms)^export interface (\w+) \{\n(.*?)^\}`).FindAllStringSubmatch(string(src), -1) {
		v, ok := sent[m[1]]
		if !ok {
			continue
		}
		found[m[1]] = true
		names := jsonNames(reflect.TypeOf(v))
		for _, f := range field.FindAllStringSubmatch(m[2], -1) {
			if !names[f[1]] && !addedByPanel[m[1]+"."+f[1]] {
				t.Errorf("web/src/api/types.ts: %s.%s is not a JSON field of api.%s", m[1], f[1], m[1])
			}
		}
	}
	if len(found) != len(sent) {
		t.Fatalf("found %d of the %d interfaces in web/src/api/types.ts", len(found), len(sent))
	}
}

func TestErrorCodesTheDashboardChecksForExist(t *testing.T) {
	codes := map[string]bool{}
	for _, c := range []string{CodeInvalid, CodeEULARequired, CodeBusy, CodeNotFound, CodeConflict, CodeNotCreated, CodeDockerUnavailable, CodeForbidden, CodeUnauthorized, CodeRateLimited, CodeInternal, CodeAgentUnavailable, CodeInsufficientSpace, CodeIconInvalid, pregen.CodeUnsupportedServer, CodeAdminUnconfirmed} {
		if codes[c] {
			t.Errorf("error code %q is used twice", c)
		}
		codes[c] = true
	}
	// The invite pages check the invites package's codes. That package
	// imports this one (through internal/minecraft), so its constants are
	// read from its source.
	for _, c := range sourceCodes(t, "../invites") {
		codes[c] = true
	}
	checked := regexp.MustCompile(`\bcode [!=]== '(\w+)'`)
	found := 0
	err := filepath.WalkDir(webSrc, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || strings.Contains(path, ".test.") || !strings.HasSuffix(path, ".ts") && !strings.HasSuffix(path, ".tsx") {
			return err
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, m := range checked.FindAllStringSubmatch(string(b), -1) {
			found++
			if !codes[m[1]] {
				t.Errorf("%s checks for error code %q, which the API does not have", path, m[1])
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if found == 0 {
		t.Fatal("found no error code checks in web/src, so this test no longer looks where the dashboard makes them")
	}
}

// sourceCodes are the string constants named Code… in the Go files of dir.
func sourceCodes(t *testing.T, dir string) []string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	fset := token.NewFileSet()
	for _, path := range files {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range f.Decls {
			g, ok := d.(*ast.GenDecl)
			if !ok || g.Tok != token.CONST {
				continue
			}
			for _, spec := range g.Specs {
				vs := spec.(*ast.ValueSpec)
				for i, name := range vs.Names {
					if !strings.HasPrefix(name.Name, "Code") || i >= len(vs.Values) {
						continue
					}
					if lit, ok := vs.Values[i].(*ast.BasicLit); ok && lit.Kind == token.STRING {
						if v, err := strconv.Unquote(lit.Value); err == nil {
							out = append(out, v)
						}
					}
				}
			}
		}
	}
	if len(out) == 0 {
		t.Fatalf("found no Code constants in %s", dir)
	}
	return out
}
