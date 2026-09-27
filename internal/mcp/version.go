package mcp

// Protocol revisions. Modern revisions carry the version in every request's
// _meta; legacy revisions negotiate it once with initialize.
const (
	version20241105 = "2024-11-05"
	version20250326 = "2025-03-26"
	version20250618 = "2025-06-18"
	version20251125 = "2025-11-25"
	version20260728 = "2026-07-28"
)

// supportedVersions lists every revision the server speaks, newest first.
// Legacy revisions are included so that a client whose modern revision is not
// supported can still fall back to initialize.
func supportedVersions() []string {
	return []string{version20260728, version20251125, version20250618, version20250326, version20241105}
}

func isModern(v string) bool { return v == version20260728 }

func isLegacy(v string) bool {
	switch v {
	case version20251125, version20250618, version20250326, version20241105:
		return true
	}
	return false
}

// negotiate picks the legacy revision to answer an initialize request with:
// the requested one when supported, otherwise the newest legacy revision.
func negotiate(requested string) string {
	if isLegacy(requested) {
		return requested
	}
	return version20251125
}

// allowsBatch reports whether JSON-RPC batches are part of revision v.
// 2025-03-26 requires servers to accept them and 2025-06-18 removed them;
// 2024-11-05 inherits them from JSON-RPC 2.0.
func allowsBatch(v string) bool { return v == version20250326 || v == version20241105 }

// atLeast compares revisions, which are ISO dates.
func atLeast(v, min string) bool { return v >= min }
