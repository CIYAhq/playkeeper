package panel

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/CIYAhq/playkeeper/internal/invites"
)

// customerEnv is a dashboard whose own machine has room for customers.
func customerEnv(t *testing.T) (joinEnv, member, customerCore) {
	t.Helper()
	e := newJoinEnv(t)
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
	alex := Customer{Provider: whopProvider, Subject: "user_alex", Handle: "AlexPlays"}
	got, err := core.StartCustomer(ctx, alex, starter)
	if err != nil || got.Account != "alexplays" {
		t.Fatalf("starting alex: %+v, %v", got, err)
	}
	info, ok, err := core.CustomerAccount(ctx, whopProvider, "user_alex")
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

// A customer whose handle is the owner's username, as when the owner
// test-buys on their own machine, gets a separate account, and Sign in
// with Whop opens that one and never the owner's.
func TestACustomerNamedLikeTheOwnerGetsAnAccountOfTheirOwn(t *testing.T) {
	f, e, own := connectedWhop(t)
	if r := e.do(t, "PUT", "/api/whop/signin", `{"clientId":"`+whopTestApp+`"}`, own.auth()); r.status != http.StatusOK {
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

	info, ok, err := customerCore{s: e.srv}.CustomerAccount(context.Background(), whopProvider, "user_own")
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
		cust := Customer{Provider: whopProvider, Subject: "user_" + string(rune('a'+i)), Handle: tc.handle}
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
	got, err := core.StartCustomer(context.Background(), Customer{Provider: whopProvider, Subject: "user_alex", Handle: "alex"}, starter)
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
	alex := Customer{Provider: whopProvider, Subject: "user_alex", Handle: "alex"}
	for name, c := range map[string]struct {
		cust Customer
		plan CustomerPlan
	}{
		"no provider":       {Customer{Subject: "user_alex", Handle: "alex"}, starter},
		"no subject":        {Customer{Provider: whopProvider, Handle: "alex"}, starter},
		"a control code":    {Customer{Provider: whopProvider, Subject: "user_alex\n", Handle: "alex"}, starter},
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
