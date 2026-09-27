package templates

import (
	"github.com/CIYAhq/playkeeper/internal/addons"
)

// Notice, Kind and Error are the add-on library's, so the dashboard shows
// and translates template messages the way it does add-on ones.
type (
	Notice = addons.Notice
	Kind   = addons.Kind
	Error  = addons.Error
)

// Reading a template.
const (
	KindNotTemplate    Kind = "not_a_template"
	KindNewer          Kind = "newer_template"
	KindFileDamaged    Kind = "template_file_damaged"
	KindLinkIncomplete Kind = "link_incomplete"
	KindLinkDamaged    Kind = "link_damaged"
	KindTooLarge       Kind = "template_too_large"
	KindUnknownField   Kind = "template_unknown_field"
	KindForbiddenField Kind = "template_forbidden_field"
	KindInvalid        Kind = "template_invalid"
)

// Writing one.
const (
	KindLinkTooLong   Kind = "link_too_long"
	KindLinkLong      Kind = "link_long"
	KindExportInvalid Kind = "export_invalid"

	// What an export left out.
	KindLeftOutUpload      Kind = "left_out_addon_upload"
	KindLeftOutMissing     Kind = "left_out_addon_missing"
	KindLeftOutAddon       Kind = "left_out_addon_invalid"
	KindLeftOutAddonsLimit Kind = "left_out_addons_limit"
	KindLeftOutAddonsSize  Kind = "left_out_addons_size"
	KindLeftOutModpack     Kind = "left_out_modpack"
	KindLeftOutPackUpload  Kind = "left_out_pack_upload"
	KindLeftOutPackAddress Kind = "left_out_pack_address"
	KindLeftOutPackHash    Kind = "left_out_pack_hash"
	KindLeftOutPacksLimit  Kind = "left_out_packs_limit"
	KindLeftOutSetting     Kind = "left_out_setting"
	KindLeftOutFormatting  Kind = "left_out_formatting"
	KindLeftOutBuild       Kind = "left_out_build"
	KindLeftOutIcon        Kind = "left_out_icon"

	// What never travels, and what the template holds differently from
	// the server.
	KindNoteWorld         Kind = "note_world"
	KindNotePlayers       Kind = "note_players"
	KindNoteAddonConfig   Kind = "note_addon_config"
	KindNoteChanged       Kind = "note_addon_changed"
	KindNoteIdentified    Kind = "note_addon_identified"
	KindNoteLatest        Kind = "note_latest"
	KindNoteModpackAddons Kind = "note_modpack_addons"
)

// Planning an import.
const (
	KindTypeUnknown         Kind = "type_unknown"
	KindTypeUnavailable     Kind = "type_unavailable"
	KindTypeSubstituted     Kind = "type_substituted"
	KindVersionsUnavailable Kind = "versions_unavailable"
	KindVersionSubstituted  Kind = "version_substituted"
	KindVersionExperimental Kind = "version_experimental"
	KindAddonsUnpinned      Kind = "addons_unpinned"
	KindAddonUnsupported    Kind = "addon_unsupported"
	KindModpackUnavailable  Kind = "modpack_unavailable"
	KindMemoryReduced       Kind = "memory_reduced"
	KindNoMemory            Kind = "no_memory"
	KindDataPacks           Kind = "data_packs_need_trust"
	KindResourcePack        Kind = "resource_pack_host"
)

// Installing an add-on of a confirmed import.
const (
	KindPinMismatch     Kind = "pin_mismatch"
	KindProjectMismatch Kind = "project_mismatch"
)

func notice(k Kind, params map[string]string, msg, hint string) Notice {
	return Notice{Kind: k, Params: params, Msg: msg, Hint: hint}
}

func fail(k Kind, params map[string]string, msg, hint string) *Error {
	return &Error{Notice: notice(k, params, msg, hint)}
}

func kv(pairs ...string) map[string]string {
	m := make(map[string]string, len(pairs)/2)
	for i := 0; i+1 < len(pairs); i += 2 {
		m[pairs[i]] = pairs[i+1]
	}
	return m
}

const askAgain = "Ask whoever shared it to export it again from their Playkeeper."

// invalid refuses a template for a problem with one field.
func invalid(field, problem, msg string) *Error {
	return fail(KindInvalid, kv("field", field, "problem", problem), msg, askAgain)
}

// printable shortens s and replaces control characters, for messages.
func printable(s string) string {
	r := []rune(s)
	if len(r) > 40 {
		r = append(r[:40], '…')
	}
	for i, c := range r {
		if c < 0x20 || c == 0x7f || isFormat(c) {
			r[i] = '?'
		}
	}
	return string(r)
}
