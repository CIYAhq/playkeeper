package panel

import (
	"net/http"
	"time"
)

// Playkeeper Cloud's plans, for its page on playkeeper.io (the fleet plan's
// step 5): the paid plans of the dashboard's own store, the one Settings ›
// Sell on Whop connects with its key, hidden ones too, since an invite-only
// store sells through links, each saying only whether it's available or
// sold out, never how many are left. Whop enforces each plan's stock at
// checkout, which follows the machines' room (whop_stock.go); this lets the
// page say Sold out too. A free plan, such as the creators', isn't sold, and
// a business selling through the app sells its own plans, so neither is
// here. Any site may ask: nothing in it is private.

const cloudPlansPath = "/api/public/cloud/plans"

// cloudPlansLimits let a page's visitors behind one address ask now and
// then; caches keep the answer a minute (cloudPlansCache).
var cloudPlansLimits = publicLimits{perMinute: 30, open: 2, read: 5 * time.Second, write: 10 * time.Second}

const cloudPlansCache = "public, max-age=60"

type cloudPlansView struct {
	Plans []cloudPlanView `json:"plans"`
}

type cloudPlanView struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	MemoryMB  int    `json:"memoryMB"`
	Available bool   `json:"available"`
}

// hCloudPlans is cloudPlansView, from the plans and stock the dashboard last
// read from or wrote to Whop, so no request asks Whop or a machine.
func (s *Server) hCloudPlans(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != cloudPlansPath || r.Method != http.MethodGet {
		http.NotFound(w, r)
		return
	}
	rows, err := s.db.QueryContext(r.Context(), `SELECT p.plan_id, p.title, p.allowance_memory_mb, p.unlimited_stock OR p.stock > 0 FROM whop_plans p
		JOIN whop_stores st ON st.store_id = p.store_id
		WHERE st.via = ? AND st.taken_over_by = '' AND p.free = 0 AND p.visibility != 'archived' AND p.allowance_from != '' ORDER BY p.position`, whopViaKey)
	if err != nil {
		http.Error(w, "Database error.", http.StatusInternalServerError)
		return
	}
	defer rows.Close()
	v := cloudPlansView{Plans: []cloudPlanView{}}
	for rows.Next() {
		var p cloudPlanView
		var title string
		if err := rows.Scan(&p.ID, &title, &p.MemoryMB, &p.Available); err != nil {
			http.Error(w, "Database error.", http.StatusInternalServerError)
			return
		}
		p.Name = cmpOr(title, p.ID)
		v.Plans = append(v.Plans, p)
	}
	if rows.Err() != nil {
		http.Error(w, "Database error.", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Access-Control-Allow-Origin", "*")
	writeJSON(w, http.StatusOK, v)
}
