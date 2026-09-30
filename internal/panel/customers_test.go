package panel

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/CIYAhq/playkeeper/internal/invites"
	"github.com/CIYAhq/playkeeper/internal/store"
)

// testStore is the store the tests' customers bought from: the business the
// fake Whop has.
const testStore = "biz_pip"

// customerEnv is a dashboard whose own machine has room for customers. What
// the core tells them is kept, not sent: no store is connected.
func customerEnv(t *testing.T) (joinEnv, member, customerCore) {
	t.Helper()
	e := newJoinEnv(t)
	e.srv.notifier = &recordingNotifier{}
	own := owner(t, e.env)
	e.reply("GET", "/v1/machine", liveMachine(30000, true))
	e.reply("GET", "/v1/servers", `[]`)
	return e, own, customerCore{s: e.srv}
}

// A customer's first start makes an account of their own: a member with no
// password, Admin of no servers yet, with their plan's allowance and a home
// machine. They're Admin of what they create with no two-factor sign-in of
// ours, and never of anything machine-wide. Starting them again changes
// nothing, and a new plan changes their allowance.
func TestACustomerGetsAnAccountOfTheirOwn(t *testing.T) {
	e, own, core := customerEnv(t)
	ctx := context.Background()
	alex := Customer{Provider: whopProvider, Store: testStore, Subject: "user_alex", Handle: "AlexPlays"}
	got, err := core.StartCustomer(ctx, alex, starter)
	if err != nil || got.Account != "alexplays" {
		t.Fatalf("starting alex: %+v, %v", got, err)
	}
	info, ok, err := core.CustomerAccount(ctx, whopProvider, testStore, "user_alex")
	if err != nil || !ok || info.Username != "alexplays" || info.State != CustomerActive || !info.SignIn {
		t.Fatalf("alex's account: %+v, %v, %v", info, ok, err)
	}
	var role, hash, projectRole, servers string
	var al invites.Allowance
	if err := e.srv.db.QueryRow(`SELECT u.role, u.password_hash, m.role, m.servers, m.allowance_servers, m.allowance_memory_mb, m.allowance_disk_gb
		FROM users u JOIN project_members m ON m.user_id = u.id WHERE u.id = ?`, info.UserID).Scan(&role, &hash, &projectRole, &servers, &al.Servers, &al.MemoryMB, &al.DiskGB); err != nil {
		t.Fatal(err)
	}
	if role != roleMember || hash != "" || projectRole != invites.RoleAdmin || servers != "" || al != fourGB {
		t.Fatalf("alex stored as %s with password %q, %s of %q, allowance %+v", role, hash, projectRole, servers, al)
	}
	a, err := e.srv.access(user{ID: info.UserID, Username: info.Username, Role: roleMember})
	if err != nil || a.Customer != CustomerActive || !a.TwoFactor || permit(a, actCreateOwnServers, "") != nil {
		t.Fatalf("alex may not create their own servers: %+v, %v", a, err)
	}
	for _, act := range []action{actCreateServers, actManageMachine, actViewAuditTrail} {
		if permit(a, act, "") == nil {
			t.Errorf("alex may %v", act)
		}
	}
	if home, ok, err := e.srv.homeMachine(ctx, info.UserID); err != nil || !ok || home != machineID(t, e.env) {
		t.Fatalf("alex's home machine: %q, %v, %v", home, ok, err)
	}
	var team teamBody
	e.get(t, "/api/team", own.cookie, &team)
	for _, m := range team.Members {
		if customer := m.Username == "alexplays"; customer != (m.Customer == whopProvider) || customer && m.Handle != "AlexPlays" {
			t.Fatalf("%s on the Team page: customer %q, handle %q", m.Username, m.Customer, m.Handle)
		}
	}

	if again, err := core.StartCustomer(ctx, alex, starter); err != nil || again.Account != "alexplays" {
		t.Fatalf("starting alex again: %+v, %v", again, err)
	}
	plus := CustomerPlan{ID: "plan_plus", Name: "Plus", Servers: 2, MemoryMB: 8192, DiskGB: 100}
	if err := core.ChangeCustomerPlan(ctx, alex, plus); err != nil {
		t.Fatal(err)
	}
	e.srv.db.QueryRow(`SELECT allowance_servers, allowance_memory_mb, allowance_disk_gb FROM project_members WHERE user_id = ?`, info.UserID).Scan(&al.Servers, &al.MemoryMB, &al.DiskGB)
	if al != (invites.Allowance{Servers: 2, MemoryMB: 8192, DiskGB: 100}) {
		t.Fatalf("alex's allowance on Plus: %+v", al)
	}
	var accounts int
	e.srv.db.QueryRow(`SELECT COUNT(*) FROM users WHERE username LIKE 'alexplays%'`).Scan(&accounts)
	if starts, plans := e.auditRows(t, "customer.start"), e.auditRows(t, "customer.plan"); accounts != 1 || len(starts) != 1 || len(plans) != 1 {
		t.Fatalf("%d accounts, starts %v, plan changes %v", accounts, starts, plans)
	}
}

// Someone who buys from two stores is two customers: each store's purchase
// makes an account of its own, found only with that store, with its own
// plan. Pausing one leaves the other as it was, and what the core tells
// each goes to the store it's about.
func TestACustomerOfTwoStoresHasTwoAccounts(t *testing.T) {
	e, _, core := customerEnv(t)
	n := e.srv.notifier.(*recordingNotifier)
	ctx := context.Background()
	pip := Customer{Provider: whopProvider, Store: testStore, Subject: "user_alex", Handle: "AlexPlays"}
	other := Customer{Provider: whopProvider, Store: "biz_other", Subject: "user_alex", Handle: "AlexPlays"}
	for _, c := range []Customer{pip, other} {
		if _, err := core.StartCustomer(ctx, c, starter); err != nil {
			t.Fatal(err)
		}
	}
	accounts := func() (CustomerAccountInfo, CustomerAccountInfo) {
		a, _, _ := core.CustomerAccount(ctx, whopProvider, testStore, "user_alex")
		b, _, _ := core.CustomerAccount(ctx, whopProvider, "biz_other", "user_alex")
		return a, b
	}
	a, b := accounts()
	if a.UserID == 0 || b.UserID == 0 || a.UserID == b.UserID || a.Username != "alexplays" || b.Username != "alexplays-2" {
		t.Fatalf("alex's accounts: %+v and %+v", a, b)
	}
	if stores, err := core.CustomerStores(ctx, whopProvider, "user_alex"); err != nil || !slices.Equal(stores, []string{testStore, "biz_other"}) {
		t.Fatalf("alex's stores: %v, %v", stores, err)
	}
	if _, ok, err := core.CustomerAccount(ctx, whopProvider, "", "user_alex"); ok || err != nil {
		t.Fatalf("an account found with no store: %v", err)
	}

	plus := CustomerPlan{ID: "plan_plus", Name: "Plus", Servers: 2, MemoryMB: 8192}
	if err := core.ChangeCustomerPlan(ctx, other, plus); err != nil {
		t.Fatal(err)
	}
	if err := core.PauseCustomer(ctx, pip, "their Whop membership is expired"); err != nil {
		t.Fatal(err)
	}
	allowance := func(id int64) invites.Allowance {
		var al invites.Allowance
		e.srv.db.QueryRow(`SELECT allowance_servers, allowance_memory_mb FROM project_members WHERE user_id = ?`, id).Scan(&al.Servers, &al.MemoryMB)
		return al
	}
	a, b = accounts()
	if a.State != CustomerPaused || b.State != CustomerActive || !b.SignIn || allowance(a.UserID) != fourGB || allowance(b.UserID) != (invites.Allowance{Servers: 2, MemoryMB: 8192}) {
		t.Fatalf("after a new plan at one store and a pause at the other: %+v with %+v, %+v with %+v", a, allowance(a.UserID), b, allowance(b.UserID))
	}
	if got, want := n.told(), []string{"ready " + testStore, "ready biz_other", "paused " + testStore}; !slices.Equal(got, want) {
		t.Fatalf("told %v, want %v", got, want)
	}
}

// The migration gives the dashboard's Whop customers the store it sells
// for, keeping all else about them; with none connected they have no store
// until the next one connected takes them on. From then on a Whop user may
// have an account in another store too, but never two in one.
func TestTheMigrationGivesCustomersTheirStore(t *testing.T) {
	at := slices.IndexFunc(panelMigrations, func(m string) bool { return strings.Contains(m, "CREATE TABLE customers_by_store") })
	if at < 0 {
		t.Fatal("no migration keeps customers per store")
	}
	for name, want := range map[string]string{"selling": testStore, "not selling": ""} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "panel.db")
			db, err := store.Open(path, panelMigrations[:at])
			if err != nil {
				t.Fatal(err)
			}
			exec := func(q string, args ...any) {
				t.Helper()
				if _, err := db.Exec(q, args...); err != nil {
					t.Fatal(err)
				}
			}
			exec(`INSERT INTO users(id, username, password_hash, created_at, password_changed_at, role) VALUES(2, 'alexplays', '', 1, 1, 'member'), (3, 'alexplays-2', '', 1, 1, 'member'), (4, 'alexplays-3', '', 1, 1, 'member')`)
			exec(`INSERT INTO customers(user_id, provider, subject, handle, plan_id, state, created_at, updated_at, told_ready, pause_reason)
				VALUES(2, 'whop', 'user_alex', 'AlexPlays', 'plan_starter', 'paused', 1, 2, 3, 'their Whop membership is expired')`)
			if want != "" {
				exec(`INSERT INTO whop_account(id, account_id, api_key, connected_by, connected_at) VALUES(1, ?, 'apik_pip', 'siya', 1)`, want)
			}
			db.Close()
			if db, err = store.Open(path, panelMigrations); err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			var got, subject, handle, plan, state, reason string
			var created, updated, told int64
			if err := db.QueryRow(`SELECT store, subject, handle, plan_id, state, pause_reason, created_at, updated_at, told_ready FROM customers WHERE user_id = 2`).
				Scan(&got, &subject, &handle, &plan, &state, &reason, &created, &updated, &told); err != nil {
				t.Fatal(err)
			}
			if got != want || subject != "user_alex" || handle != "AlexPlays" || plan != "plan_starter" || state != "paused" || reason != "their Whop membership is expired" ||
				created != 1 || updated != 2 || told != 3 {
				t.Fatalf("alex after the migration: store %q, %s %s %s %s %q %d %d %d", got, subject, handle, plan, state, reason, created, updated, told)
			}
			if _, err := db.Exec(`INSERT INTO customers(user_id, provider, store, subject, created_at, updated_at) VALUES(3, 'whop', 'biz_other', 'user_alex', 5, 5)`); err != nil {
				t.Fatalf("alex's account at another store: %v", err)
			}
			if _, err := db.Exec(`INSERT INTO customers(user_id, provider, store, subject, created_at, updated_at) VALUES(4, 'whop', 'biz_other', 'user_alex', 6, 6)`); err == nil {
				t.Fatal("alex has two accounts at one store")
			}
		})
	}
}

// A customer whose handle is the owner's username, as when the owner
// test-buys on their own machine, gets a separate account, and Sign in
// with Whop opens that one and never the owner's.
func TestACustomerNamedLikeTheOwnerGetsAnAccountOfTheirOwn(t *testing.T) {
	f, e, own := connectedWhop(t)
	if r := e.do(t, "PUT", "/api/whop/signin", `{"clientId":"`+whopTestApp+`","clientSecret":"`+whopTestAppSecret+`"}`, own.auth()); r.status != http.StatusOK {
		t.Fatalf("setting up Sign in with Whop: %d %v", r.status, r.body)
	}
	e.reply("GET", "/v1/machine", liveMachine(30000, true))
	e.reply("GET", "/v1/servers", `[]`)
	const ownName = "siya"
	if _, err := e.srv.db.Exec(`UPDATE users SET username = ? WHERE id = ?`, ownName, own.id); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.users["user_own"] = "Siya"
	f.mu.Unlock()
	f.buy("mem_own1", "user_own", "plan_starter", "active")
	e.reconcile()

	info, ok, err := customerCore{s: e.srv}.CustomerAccount(context.Background(), whopProvider, testStore, "user_own")
	if err != nil || !ok || info.UserID == own.id || info.Username != ownName+"-2" {
		t.Fatalf("the test buyer's account: %+v, %v, %v (the owner is %d)", info, ok, err, own.id)
	}
	b := newBrowser(t, e)
	if to := b.signInWithWhop(f, "user_own"); to != "/" {
		t.Fatalf("signing in with Whop ended at %q", to)
	}
	res, err := b.c.Get(e.ts.URL + "/api/auth/me")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var me struct {
		User struct {
			Username string `json:"username"`
			Role     string `json:"role"`
		} `json:"user"`
	}
	if err := json.NewDecoder(res.Body).Decode(&me); err != nil || me.User.Username != info.Username || me.User.Role == roleOwner {
		t.Fatalf("signed in with Whop as %+v, %v; want %q, never the owner %q", me.User, err, info.Username, ownName)
	}
}

// A customer who started while every machine was full is placed once one
// has room, since their billing provider won't start them again; a paused
// one waits until they're active again.
func TestACustomerWaitingForRoomIsPlacedOnceThereIsRoom(t *testing.T) {
	e := newJoinEnv(t)
	n := &recordingNotifier{}
	e.srv.notifier = n
	owner(t, e.env)
	e.reply("GET", "/v1/machine", liveMachine(0, true))
	e.reply("GET", "/v1/servers", `[]`)
	core := customerCore{s: e.srv}
	ctx := context.Background()
	home := func(id int64) string {
		var m string
		e.srv.db.QueryRow(`SELECT machine_id FROM customer_homes WHERE user_id = ?`, id).Scan(&m)
		return m
	}
	var ids []int64
	for _, subject := range []string{"user_alex", "user_sam"} {
		if _, err := core.StartCustomer(ctx, Customer{Provider: whopProvider, Store: testStore, Subject: subject, Handle: subject[5:]}, starter); err != nil {
			t.Fatal(err)
		}
		info, _, _ := core.CustomerAccount(ctx, whopProvider, testStore, subject)
		if home(info.UserID) != "" {
			t.Fatalf("%s has a home with no room", subject)
		}
		ids = append(ids, info.UserID)
	}
	if _, err := e.srv.db.Exec(`UPDATE customers SET state = 'paused' WHERE user_id = ?`, ids[1]); err != nil {
		t.Fatal(err)
	}
	e.reply("GET", "/v1/machine", liveMachine(30000, true))
	e.srv.startWaitingCustomers(ctx)
	if got, want := home(ids[0]), machineID(t, e.env); got != want {
		t.Fatalf("alex's home once there was room: %q, want %q", got, want)
	}
	if err := e.srv.startWaitingCustomer(ctx, ids[1]); err != nil || home(ids[1]) != "" {
		t.Fatalf("a paused customer was placed: %v", err)
	}
	if got, want := n.told(), []string{messageSettingUp + " " + testStore, messageSettingUp + " " + testStore, messageReady + " " + testStore}; !slices.Equal(got, want) {
		t.Fatalf("told %v, want %v", got, want)
	}
}

// A customer's account follows their plan, so the owner can't remove it
// from the team: their billing provider wouldn't make it again.
func TestTheTeamPageKeepsACustomersAccount(t *testing.T) {
	e, own, core := customerEnv(t)
	ctx := context.Background()
	if _, err := core.StartCustomer(ctx, Customer{Provider: whopProvider, Store: testStore, Subject: "user_alex", Handle: "alex"}, starter); err != nil {
		t.Fatal(err)
	}
	info, _, _ := core.CustomerAccount(ctx, whopProvider, testStore, "user_alex")
	alex := member{id: info.UserID}
	if r := e.do(t, "DELETE", alex.path(), "", own.auth()); r.status != http.StatusConflict {
		t.Fatalf("removing a customer: %d %v", r.status, r.body)
	}
	if _, ok, err := core.CustomerAccount(ctx, whopProvider, testStore, "user_alex"); err != nil || !ok {
		t.Fatalf("alex's account after the refused removal: %v, %v", ok, err)
	}
	var team teamBody
	e.get(t, "/api/team", own.cookie, &team)
	for _, m := range team.Members {
		if m.Username == "alex" && m.CanEdit {
			t.Fatal("the Team page offers to change or remove a customer")
		}
	}
}

// A customer's account is named after their handle in plain lower-case
// letters, digits and dashes, and never takes a name an account has, a
// reserved one, or a look-alike of either.
func TestCustomersNamesNeverTakeAnother(t *testing.T) {
	_, _, core := customerEnv(t)
	ctx := context.Background()
	for i, tc := range []struct{ handle, want string }{
		{"alex", "alex"},
		{"Alex", "alex-2"},
		{"admin", "admin-2"},
		{"PlayKeeper", "playkeeper-2"},
		{"sіуа", "customer"}, // Cyrillic і and у
		{"x", "customer-2"},
		{"Build.With_Kai!!", "build-with-kai"},
		{strings.Repeat("long", 15), strings.Repeat("long", 7)},
	} {
		cust := Customer{Provider: whopProvider, Store: testStore, Subject: "user_" + string(rune('a'+i)), Handle: tc.handle}
		got, err := core.StartCustomer(ctx, cust, starter)
		if err != nil || got.Account != tc.want {
			t.Errorf("a customer with the handle %q got %q, %v; want %q", tc.handle, got.Account, err, tc.want)
		}
	}
}

// A customer's account never signs in with a password, even one somebody
// set for it.
func TestPasswordSignInRefusesCustomers(t *testing.T) {
	e, _, core := customerEnv(t)
	got, err := core.StartCustomer(context.Background(), Customer{Provider: whopProvider, Store: testStore, Subject: "user_alex", Handle: "alex"}, starter)
	if err != nil {
		t.Fatal(err)
	}
	for _, password := range []string{"", "correct horse battery"} {
		if password != "" {
			h, err := hashPassword(password)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := e.srv.db.Exec(`UPDATE users SET password_hash = ? WHERE username = ?`, h, got.Account); err != nil {
				t.Fatal(err)
			}
		}
		body := `{"username":"` + got.Account + `","password":"` + password + `"}`
		if r := e.do(t, "POST", "/api/auth/login", body, map[string]string{"X-Requested-With": "playkeeper"}); r.status != http.StatusUnauthorized {
			t.Fatalf("a customer signs in with the password %q: %d %v", password, r.status, r.body)
		}
	}
}

// What isn't a customer, or a plan no account may have, makes nothing, and
// a customer with no account can't change plans.
func TestTheCoreRefusesWhatIsntACustomer(t *testing.T) {
	e, _, core := customerEnv(t)
	ctx := context.Background()
	alex := Customer{Provider: whopProvider, Store: testStore, Subject: "user_alex", Handle: "alex"}
	for name, c := range map[string]struct {
		cust Customer
		plan CustomerPlan
	}{
		"no provider":       {Customer{Store: testStore, Subject: "user_alex", Handle: "alex"}, starter},
		"no store":          {Customer{Provider: whopProvider, Subject: "user_alex", Handle: "alex"}, starter},
		"no subject":        {Customer{Provider: whopProvider, Store: testStore, Handle: "alex"}, starter},
		"a control code":    {Customer{Provider: whopProvider, Store: testStore, Subject: "user_alex\n", Handle: "alex"}, starter},
		"no servers":        {alex, CustomerPlan{ID: "plan_x", MemoryMB: 4096}},
		"too little memory": {alex, CustomerPlan{ID: "plan_x", Servers: 1, MemoryMB: 512}},
		"too much disk":     {alex, CustomerPlan{ID: "plan_x", Servers: 1, MemoryMB: 4096, DiskGB: invites.MaxAllowanceDiskGB + 1}},
	} {
		if _, err := core.StartCustomer(ctx, c.cust, c.plan); err == nil {
			t.Errorf("starting a customer with %s", name)
		}
	}
	var n int
	e.srv.db.QueryRow(`SELECT COUNT(*) FROM customers`).Scan(&n)
	if n != 0 {
		t.Fatalf("%d customers made from what wasn't one", n)
	}
	if err := core.ChangeCustomerPlan(ctx, alex, starter); !errors.Is(err, errNoCustomer) {
		t.Fatalf("a plan change for a customer with no account: %v", err)
	}
}
