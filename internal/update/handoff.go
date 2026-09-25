package update

import "time"

// Files in the update directory that the agent and the updater share. The
// agent (sandboxed, it cannot replace the binary) downloads and verifies a
// release into StagedDir and writes RequestFile; systemd then starts the
// updater, which renames the request to ApplyingFile while it works and
// leaves a ResultFile for the agent to report.
const (
	StagedDir    = "staged"
	RequestFile  = "request.json"
	ApplyingFile = "applying.json"
	ResultFile   = "result.json"
	BinaryFile   = "playkeeper"
)

// Request asks the updater to install a staged release.
type Request struct {
	OpID        string    `json:"opId"`
	Version     string    `json:"version"`
	From        string    `json:"from"`
	Actor       string    `json:"actor"`
	RequestedAt time.Time `json:"requestedAt"`
}

// StaleAfter is how long a request stays valid; an older one (for example
// found after a long power outage) is discarded instead of installed.
const StaleAfter = time.Hour

// Result is what the updater did with a request.
type Result struct {
	OpID       string    `json:"opId"`
	From       string    `json:"from"`
	To         string    `json:"to"`
	Outcome    string    `json:"outcome"`
	Error      string    `json:"error,omitempty"`
	Actor      string    `json:"actor"`
	FinishedAt time.Time `json:"finishedAt"`
}

const (
	// OutcomeUpdated: the new version is installed and healthy.
	OutcomeUpdated = "updated"
	// OutcomeRolledBack: the new version was unhealthy; the previous one was
	// put back and is running.
	OutcomeRolledBack = "rolled_back"
	// OutcomeRefused: nothing was changed (the staged release failed a check,
	// or the request was stale).
	OutcomeRefused = "refused"
	// OutcomeFailed: the update and putting the previous version back both
	// failed; the host needs a manual fix.
	OutcomeFailed = "failed"
)
