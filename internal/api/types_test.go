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

	"github.com/CIYAhq/playkeeper/internal/certs"
	"github.com/CIYAhq/playkeeper/internal/gamefiles"
	"github.com/CIYAhq/playkeeper/internal/names"
	"github.com/CIYAhq/playkeeper/internal/pregen"
	"github.com/CIYAhq/playkeeper/internal/twofactor"
	"github.com/CIYAhq/playkeeper/internal/worldimport"
)

const webSrc = "../../web/src"

func jsonNames(t reflect.Type) map[string]bool {
	names := map[string]bool{}
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		switch {
		case f.Anonymous && name == "" && f.Type.Kind() == reflect.Struct:
			for n := range jsonNames(f.Type) {
				names[n] = true
			}
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
		"ServerStatus": ServerStatus{}, "ServerType": ServerType{}, "Session": Session{}, "SessionsResponse": SessionsResponse{}, "SetupStatus": SetupStatus{},
		"UpdateInfo": UpdateInfo{}, "UpdateResult": UpdateResult{}, "WhitelistEntry": WhitelistEntry{}, "WorldCopy": WorldCopy{},
		"Addon": Addon{}, "AddonBrowse": AddonBrowse{}, "AddonCard": AddonCard{}, "AddonChecks": AddonChecks{}, "AddonDetails": AddonDetails{},
		"AddonFile": AddonFile{}, "AddonKey": AddonKey{}, "AddonNotice": AddonNotice{}, "AddonPlan": AddonPlan{}, "AddonProgress": AddonProgress{},
		"AddonRemovePreview": AddonRemovePreview{}, "AddonRemoval": AddonRemoval{}, "Addons": Addons{}, "AddonStep": AddonStep{},
		"AddonTarget": AddonTarget{}, "AddonUpdate": AddonUpdate{}, "AddonVersion": AddonVersion{}, "DataPack": DataPack{}, "DataPacks": DataPacks{},
		"Pregen": Pregen{}, "PregenPreset": PregenPreset{}, "ResourcePack": ResourcePack{}, "ResourcePackOffer": ResourcePackOffer{},
		"Address": Address{}, "AddressCheck": AddressCheck{}, "AddressPlan": AddressPlan{}, "AddrRecord": AddrRecord{},
		"CertificateStatus": CertificateStatus{}, "DNSRecord": DNSRecord{}, "FreeAddress": FreeAddress{}, "JoinAddress": JoinAddress{},
		"NameAvailability": NameAvailability{}, "NamesService": NamesService{}, "Note": Note{}, "SRVParts": SRVParts{},
		"NameCheck": NameCheck{}, "RecordCheck": RecordCheck{}, "CertificateProblem": CertificateProblem{},
		"Challenge": twofactor.Challenge{}, "SignInNotice": twofactor.Notice{}, "TwoFactorSetup": twofactor.Setup{}, "TwoFactorStatus": twofactor.Status{},
		"Crash": Crash{}, "CrashLine": CrashLine{}, "DiagnosisAction": DiagnosisAction{}, "DiagnosisEvidence": DiagnosisEvidence{}, "FileRefusal": FileRefusal{},
		"LagCause": LagCause{}, "MemoryAdvice": MemoryAdvice{}, "MemoryDay": MemoryDay{}, "MemoryOption": MemoryOption{}, "Running": Running{},
		"AddonSources": AddonSources{}, "CurseForgeSource": CurseForgeSource{},
		"AddonPort": AddonPort{}, "CuratedAddons": CuratedAddons{}, "CuratedAddon": CuratedAddon{},
		"SoftwarePin": SoftwarePin{}, "SoftwareBuild": SoftwareBuild{}, "SoftwareBuilds": SoftwareBuilds{}, "SoftwareChange": SoftwareChange{},
		"ModpackCard": ModpackCard{}, "ModpackResults": ModpackResults{}, "ModpackVersion": ModpackVersion{}, "ModpackPreview": ModpackPreview{},
		"ModpackRef": ModpackRef{}, "ServerModpack": ServerModpack{}, "ServerTemplate": ServerTemplate{}, "TemplateSettings": TemplateSettings{},
		"TemplateAddon": TemplateAddon{}, "TemplateContents": TemplateContents{}, "TemplateExport": TemplateExport{}, "TemplatePlan": TemplatePlan{},
		"PackShare": PackShare{}, "ModpackDetail": ModpackDetail{},
		"ApiErrorBody": Error{}, "DiscordDelivery": DiscordDelivery{}, "DiscordSettings": DiscordSettings{}, "Phrase": Phrase{},
		"PlayerDay": PlayerDay{}, "PlayerProfile": PlayerProfile{},
		"MapInfo": MapInfo{}, "MapProgress": MapProgress{}, "PublicMap": PublicMap{}, "WorldImport": WorldImport{}, "WorldImportFile": WorldImportFile{},
		"WorldImportPreview": WorldImportPreview{}, "WorldImportVersion": WorldImportVersion{}, "ImportMessage": worldimport.Message{},
		"ImportLevel": worldimport.Level{}, "ImportWorld": worldimport.World{}, "ImportPreview": worldimport.Preview{},
	}
	addedByPanel := map[string]bool{"ServerStatus.machineId": true, "AuditEntry.source": true}
	CheckDashboardFields(t, string(src), sent, addedByPanel)
}

// CheckDashboardFields checks that each field of the interfaces of
// web/src/api/types.ts named in sent is a JSON field of the Go value sent
// has for it, or listed in addedByPanel as "Interface.field". It is also
// used by the external test for types whose packages import this one.
func CheckDashboardFields(t *testing.T, src string, sent map[string]any, addedByPanel map[string]bool) {
	t.Helper()
	field := regexp.MustCompile(`(?m)^  (\w+)\??:`)
	// An interface may be declared more than once: TypeScript merges the
	// declarations, and each one's fields are checked.
	found := map[string]bool{}
	for _, m := range regexp.MustCompile(`(?ms)^export interface (\w+)(?: extends [\w, ]+)? \{\n(.*?)^\}`).FindAllStringSubmatch(string(src), -1) {
		v, ok := sent[m[1]]
		if !ok {
			continue
		}
		found[m[1]] = true
		names := jsonNames(reflect.TypeOf(v))
		for _, f := range field.FindAllStringSubmatch(m[2], -1) {
			if !names[f[1]] && !addedByPanel[m[1]+"."+f[1]] {
				t.Errorf("web/src/api/types.ts: %s.%s is not a JSON field of %s", m[1], f[1], reflect.TypeOf(v))
			}
		}
	}
	if len(found) != len(sent) {
		t.Fatalf("found %d of the %d interfaces in web/src/api/types.ts", len(found), len(sent))
	}
}

func TestErrorCodesTheDashboardChecksForExist(t *testing.T) {
	codes := map[string]bool{}
	sent := []string{CodeInvalid, CodeEULARequired, CodeBusy, CodeNotFound, CodeConflict, CodeNotCreated, CodeDockerUnavailable, CodeForbidden, CodeUnauthorized, CodeRateLimited, CodeInternal, CodeAgentUnavailable, CodeInsufficientSpace, CodeIconInvalid, pregen.CodeUnsupportedServer,
		CodeNamesUnreachable, CodeRetryLater, names.CodeInvalidName, names.CodeNotAnswering, certs.CodePort80Unreachable, certs.CodeCertificateLimit,
		string(twofactor.KindPasswordWrong), CodePlanChanged, CodeKeyRefused, CodeAdminUnconfirmed}
	for _, k := range []gamefiles.Kind{gamefiles.KindLink, gamefiles.KindSpecial, gamefiles.KindNotFile, gamefiles.KindNotFolder, gamefiles.KindTooLarge, gamefiles.KindTooMany, gamefiles.KindChanged, gamefiles.KindBadName} {
		sent = append(sent, string(k))
	}
	for _, c := range sent {
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
