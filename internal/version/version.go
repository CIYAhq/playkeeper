// Package version exposes build metadata injected with -ldflags at release time.
package version

// Values are overwritten by the release build (see scripts/package.sh).
var (
	Version = "dev"
	Commit  = "unknown"
	Date    = "unknown"
)

// String returns a single human-readable version line.
func String() string {
	return "playkeeper " + Version + " (" + Commit + ", " + Date + ")"
}
