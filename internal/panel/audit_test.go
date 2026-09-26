package panel

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

// auditRows is n audit rows as an agent sends them, dated at, with the ids id
// gives.
func auditRows(n int, at string, id func(i int) int) string {
	rows := make([]string, n)
	for i := range rows {
		rows[i] = fmt.Sprintf(`{"id":%d,"ts":%q,"actor":"admin","action":"server.start","result":"succeeded"}`, id(i), at)
	}
	return "[" + strings.Join(rows, ",") + "]"
}

// The audit log takes at most 200 rows from each machine, each id once and
// none dated after now, and every row is known by where it came from: a
// machine that sends many rows, dates them in the future or numbers them as
// the dashboard's agent does can't bury or hide the dashboard's own rows.
func TestTheAuditLogKeepsEachMachineToItsShare(t *testing.T) {
	e := newEnvConfig(t, withDomain, nil)
	cookie, csrf := e.setup(t)
	local := e.localMachine(t)
	e.reply("GET", "/v1/audit", `[{"id":1,"ts":"2026-09-24T11:30:00Z","actor":"admin","action":"backup.created","result":"succeeded"}]`)
	ra := newRemoteAgent()
	rid, _ := e.joined(t, cookie, csrf, ra)
	now := e.clock.now()
	for _, tc := range []struct {
		name   string
		rows   string
		joined int // how many of the joined machine's rows the log shows
	}{
		{"500 rows dated 2099", auditRows(500, "2099-01-01T00:00:00Z", func(i int) int { return i + 1 }), maxMachineAudit},
		{"one row sent again and again", auditRows(300, "2026-09-24T11:00:00Z", func(int) int { return 7 }), 1},
		{"a row numbered as the dashboard's agent numbers its first", auditRows(1, "2026-09-24T11:00:00Z", func(int) int { return 1 }), 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ra.reply("GET /v1/audit", tc.rows)
			var got []struct {
				ID        int64     `json:"id"`
				TS        time.Time `json:"ts"`
				Source    string    `json:"source"`
				MachineID string    `json:"machineId"`
			}
			if code := e.get(t, "/api/audit", cookie, &got); code != http.StatusOK {
				t.Fatalf("the audit log: %d", code)
			}
			seen := map[string]bool{}
			var panel, own, joined int
			for _, a := range got {
				key := fmt.Sprintf("%s %s %d", a.Source, a.MachineID, a.ID)
				if seen[key] {
					t.Fatalf("two rows are %s", key)
				}
				seen[key] = true
				if a.TS.After(now) {
					t.Fatalf("row %s is dated %v, after now", key, a.TS)
				}
				switch {
				case a.Source == "panel":
					panel++
				case a.MachineID == local:
					own++
				case a.MachineID == rid:
					joined++
				}
			}
			var kept int
			if err := e.srv.db.QueryRow(`SELECT COUNT(*) FROM audit`).Scan(&kept); err != nil {
				t.Fatal(err)
			}
			if joined != tc.joined || own != 1 || panel != kept {
				t.Fatalf("the log shows %d of the joined machine's rows, want %d; %d of the dashboard's agent's, want 1; %d of its own, want %d", joined, tc.joined, own, panel, kept)
			}
		})
	}
}
