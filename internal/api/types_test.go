package api

import (
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/CIYAhq/playkeeper/internal/gamefiles"
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
		"Crash": Crash{}, "CrashLine": CrashLine{}, "DiagnosisAction": DiagnosisAction{}, "DiagnosisEvidence": DiagnosisEvidence{}, "FileRefusal": FileRefusal{},
		"LagCause": LagCause{}, "MemoryAdvice": MemoryAdvice{}, "MemoryDay": MemoryDay{}, "MemoryOption": MemoryOption{}, "Running": Running{},
	}
	addedByPanel := map[string]bool{"ServerStatus.machineId": true, "AuditEntry.source": true}
	field := regexp.MustCompile(`(?m)^  (\w+)\??:`)
	found := 0
	for _, m := range regexp.MustCompile(`(?ms)^export interface (\w+) \{\n(.*?)^\}`).FindAllStringSubmatch(string(src), -1) {
		v, ok := sent[m[1]]
		if !ok {
			continue
		}
		found++
		names := jsonNames(reflect.TypeOf(v))
		for _, f := range field.FindAllStringSubmatch(m[2], -1) {
			if !names[f[1]] && !addedByPanel[m[1]+"."+f[1]] {
				t.Errorf("web/src/api/types.ts: %s.%s is not a JSON field of api.%s", m[1], f[1], m[1])
			}
		}
	}
	if found != len(sent) {
		t.Fatalf("found %d of the %d interfaces in web/src/api/types.ts", found, len(sent))
	}
}

func TestErrorCodesTheDashboardChecksForExist(t *testing.T) {
	codes := map[string]bool{}
	sent := []string{CodeInvalid, CodeEULARequired, CodeBusy, CodeNotFound, CodeConflict, CodeNotCreated, CodeDockerUnavailable, CodeForbidden, CodeUnauthorized, CodeRateLimited, CodeInternal, CodeAgentUnavailable, CodeInsufficientSpace, CodeIconInvalid, pregen.CodeUnsupportedServer}
	for _, k := range []gamefiles.Kind{gamefiles.KindLink, gamefiles.KindSpecial, gamefiles.KindNotFile, gamefiles.KindNotFolder, gamefiles.KindTooLarge, gamefiles.KindTooMany, gamefiles.KindChanged, gamefiles.KindBadName} {
		sent = append(sent, string(k))
	}
	for _, c := range sent {
		if codes[c] {
			t.Errorf("error code %q is used twice", c)
		}
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
