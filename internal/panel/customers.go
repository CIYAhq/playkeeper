package panel

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/CIYAhq/playkeeper/internal/api"
	"github.com/CIYAhq/playkeeper/internal/invites"
)

// The hosting core's customer accounts (hostingCore, in hosting.go). Each
// customer a billing provider starts gets an account of their own, found
// only by the provider, the store they bought from and the provider's id
// for them, never by name, so a handle that matches an existing account,
// the owner's included, never reaches it, and a customer of one store
// never reaches an account of another's. The account's name is the handle
// in plain lower-case letters, digits and dashes, numbered when that's
// taken, and the handle is kept as a label.
//
// The account has no password, so password sign-in refuses it: it signs in
// through its provider. Its plan, which the provider confirmed, makes it
// Admin of the servers it creates inside the plan's allowance, with no
// two-factor sign-in of ours (decision 2 in the managed-beta plan), and
// nothing machine-wide is ever its, as for any creator.

// customerCore is the hosting core billing providers call.
type customerCore struct{ s *Server }

var (
	// errNoCustomer refuses a change for a customer who has no account yet.
	errNoCustomer = errors.New("That customer has no account here yet.")
	// errCustomerStays refuses removing a customer from the team: their
	// account follows their plan.
	errCustomerStays = &invites.Error{Code: api.CodeConflict, Status: http.StatusConflict,
		Msg: "This is a customer's account, which follows their plan.", Hint: "It pauses when their plan ends."}
)

// reservedNames are names no customer's account takes, so the audit log
// and the Team page never show a customer as the dashboard or its owner.
var reservedNames = map[string]bool{"admin": true, "administrator": true, "owner": true, "root": true, "playkeeper": true, "system": true, "support": true, "whop": true}

// Bounds of a customer as a billing provider names them.
const (
	maxCustomerProvider = 32
	maxCustomerStore    = 64
	maxCustomerSubject  = 128
	maxCustomerHandle   = 64
	maxCustomerBase     = 28
)

// StartCustomer makes the customer's account the first time, and gives it
// the plan when called again. Either way it then asks placement for the
// customer's home machine, and tells an active customer that their server is
// ready to start, or with no room that it's being set up (readyserver.go).
// An error after the account is made leaves the rest for the next call.
func (c customerCore) StartCustomer(ctx context.Context, cust Customer, p CustomerPlan) (StartedCustomer, error) {
	s := c.s
	al, err := customerAllowance(cust, p)
	if err != nil {
		return StartedCustomer{}, err
	}
	s.customersMu.Lock()
	defer s.customersMu.Unlock()
	info, ok, err := c.CustomerAccount(ctx, cust.Provider, cust.Store, cust.Subject)
	switch {
	case err != nil:
		return StartedCustomer{}, err
	case ok:
		err = s.applyCustomerPlan(ctx, cust, info, p, al)
		if err == nil && info.State == CustomerPaused {
			err = s.resumeCustomer(ctx, cust, &info)
		}
	default:
		info, err = s.makeCustomerAccount(ctx, cust, p, al)
	}
	if err != nil {
		return StartedCustomer{}, err
	}
	s.kickDiskLimits()
	_, err = s.placeCustomer(ctx, info.UserID, p)
	placed := err == nil
	if !placed && !errors.Is(err, errNoRoom) {
		return StartedCustomer{}, err
	}
	if info.State == CustomerActive {
		if err := s.tellPlaced(ctx, cust, info.UserID, placed); err != nil {
			return StartedCustomer{}, err
		}
	}
	dash, _ := s.dashboardURL(ctx)
	return StartedCustomer{Account: info.Username, Dashboard: dash}, nil
}

// ChangeCustomerPlan gives the customer's account the plan's allowance.
// Going smaller deletes nothing: what no longer fits is refused from then on.
func (c customerCore) ChangeCustomerPlan(ctx context.Context, cust Customer, p CustomerPlan) error {
	al, err := customerAllowance(cust, p)
	if err != nil {
		return err
	}
	c.s.customersMu.Lock()
	defer c.s.customersMu.Unlock()
	info, ok, err := c.CustomerAccount(ctx, cust.Provider, cust.Store, cust.Subject)
	switch {
	case err != nil:
		return err
	case !ok:
		return errNoCustomer
	}
	return c.s.applyCustomerPlan(ctx, cust, info, p, al)
}

// CustomerAccount says which account the store's customer is, found by
// provider, store and subject alone. Any account but a suspended one may
// sign in. No customer is found without a store.
func (c customerCore) CustomerAccount(ctx context.Context, provider, store, subject string) (CustomerAccountInfo, bool, error) {
	if store == "" {
		return CustomerAccountInfo{}, false, nil
	}
	var info CustomerAccountInfo
	var state string
	err := c.s.db.QueryRowContext(ctx, `SELECT c.user_id, u.username, c.state FROM customers c JOIN users u ON u.id = c.user_id
		WHERE c.provider = ? AND c.store = ? AND c.subject = ?`, provider, store, subject).Scan(&info.UserID, &info.Username, &state)
	switch {
	case isNoRows(err):
		return CustomerAccountInfo{}, false, nil
	case err != nil:
		return CustomerAccountInfo{}, false, errDB
	}
	info.State = CustomerState(state)
	info.SignIn = info.State != CustomerSuspended
	return info, true, nil
}

// CustomerStores lists the stores where the provider's subject has an
// account, the oldest first.
func (c customerCore) CustomerStores(ctx context.Context, provider, subject string) ([]string, error) {
	rows, err := c.s.db.QueryContext(ctx, `SELECT store FROM customers WHERE provider = ? AND subject = ? AND store != '' ORDER BY created_at, user_id`, provider, subject)
	if err != nil {
		return nil, errDB
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var store string
		if err := rows.Scan(&store); err != nil {
			return nil, errDB
		}
		out = append(out, store)
	}
	if rows.Err() != nil {
		return nil, errDB
	}
	return out, nil
}

// customerAllowance checks a customer as their billing provider names them,
// and gives their plan as an allowance.
func customerAllowance(cust Customer, p CustomerPlan) (invites.Allowance, error) {
	if cust.Provider == "" || len(cust.Provider) > maxCustomerProvider || cust.Subject == "" || len(cust.Subject) > maxCustomerSubject ||
		cust.Store == "" || len(cust.Store) > maxCustomerStore || len(cust.Handle) > maxCustomerHandle || !printable(cust.Provider+cust.Store+cust.Subject+cust.Handle) {
		return invites.Allowance{}, errors.New("a customer needs a provider, the store they bought from and the provider's id for them")
	}
	al := invites.Allowance{Servers: p.Servers, MemoryMB: p.MemoryMB, DiskGB: p.DiskGB}
	if err := al.Check(); err != nil {
		return invites.Allowance{}, fmt.Errorf("the plan %q allows what no account may have: %w", p.ID, err)
	}
	return al, nil
}

func printable(s string) bool {
	for _, r := range s {
		if !unicode.IsPrint(r) {
			return false
		}
	}
	return true
}

// makeCustomerAccount makes the account: a member with no password, Admin of
// no servers yet, with the plan's allowance.
func (s *Server) makeCustomerAccount(ctx context.Context, cust Customer, p CustomerPlan, al invites.Allowance) (CustomerAccountInfo, error) {
	project := s.projectID(access{})
	now := s.now().UnixMilli()
	var info CustomerAccountInfo
	err := s.immediate(ctx, func(conn *sql.Conn) error {
		name, err := customerName(ctx, conn, customerBase(cust.Handle))
		if err != nil {
			return err
		}
		res, err := conn.ExecContext(ctx, `INSERT INTO users(username, password_hash, role, created_at, password_changed_at) VALUES(?, '', ?, ?, ?)`, name, roleMember, now, now)
		if err != nil {
			return err
		}
		id, err := res.LastInsertId()
		if err != nil {
			return err
		}
		if _, err := conn.ExecContext(ctx, `INSERT INTO project_members(project_id, user_id, role, servers, allowance_servers, allowance_memory_mb, allowance_disk_gb, created_at)
			VALUES(?,?,?,?,?,?,?,?)`, project, id, invites.RoleAdmin, "", al.Servers, al.MemoryMB, al.DiskGB, now); err != nil {
			return err
		}
		if _, err := conn.ExecContext(ctx, `INSERT INTO customers(user_id, provider, store, subject, handle, plan_id, state, created_at, updated_at) VALUES(?,?,?,?,?,?,?,?,?)`,
			id, cust.Provider, cust.Store, cust.Subject, cust.Handle, p.ID, string(CustomerActive), now, now); err != nil {
			return err
		}
		info = CustomerAccountInfo{UserID: id, Username: name, State: CustomerActive, SignIn: true}
		return nil
	})
	if err != nil {
		return CustomerAccountInfo{}, err
	}
	s.audit(cust.Provider, "customer.start", info.Username, "succeeded", fmt.Sprintf("%s of %s as %s; %s", cust.Subject, cust.Store, cust.Handle, allowanceText(al)))
	return info, nil
}

// applyCustomerPlan gives the account the plan's allowance, and keeps the
// customer's handle as their provider last had it.
func (s *Server) applyCustomerPlan(ctx context.Context, cust Customer, info CustomerAccountInfo, p CustomerPlan, al invites.Allowance) error {
	res, err := s.db.ExecContext(ctx, `UPDATE project_members SET allowance_servers = ?, allowance_memory_mb = ?, allowance_disk_gb = ?
		WHERE user_id = ? AND (allowance_servers != ? OR allowance_memory_mb != ? OR allowance_disk_gb != ?)`,
		al.Servers, al.MemoryMB, al.DiskGB, info.UserID, al.Servers, al.MemoryMB, al.DiskGB)
	if err != nil {
		return errDB
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE customers SET plan_id = ?, handle = ?, updated_at = ? WHERE user_id = ? AND (plan_id != ? OR handle != ?)`,
		p.ID, cust.Handle, s.now().UnixMilli(), info.UserID, p.ID, cust.Handle); err != nil {
		return errDB
	}
	if n, _ := res.RowsAffected(); n > 0 {
		s.audit(cust.Provider, "customer.plan", info.Username, "succeeded", allowanceText(al))
		s.kickDiskLimits()
		s.kickSaleRoom()
	}
	return nil
}

// customerBase is a handle as a name: plain lower-case letters, digits and
// dashes, at most maxCustomerBase of them so a number still fits, or
// "customer" when too little of the handle is left. A letter from another
// alphabet goes, so a look-alike of another name can't pass for it.
func customerBase(handle string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(handle) {
		switch {
		case r >= 'a' && r <= 'z' || r >= '0' && r <= '9':
			b.WriteRune(r)
		case b.Len() > 0 && !strings.HasSuffix(b.String(), "-"):
			b.WriteByte('-')
		}
	}
	name := b.String()
	if len(name) > maxCustomerBase {
		name = name[:maxCustomerBase]
	}
	name = strings.Trim(name, "-")
	if len(name) < 3 {
		return "customer"
	}
	return name
}

// customerName is a name for an account that no account has in any
// capitalisation, and that isn't reserved: base, or base-2, base-3 and on.
func customerName(ctx context.Context, q querier, base string) (string, error) {
	for i := 1; i <= 1000; i++ {
		name := base
		if i > 1 {
			name += "-" + strconv.Itoa(i)
		}
		if reservedNames[name] {
			continue
		}
		var n int
		if err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM users WHERE username = ? COLLATE NOCASE`, name).Scan(&n); err != nil {
			return "", errDB
		}
		if n == 0 {
			return name, nil
		}
	}
	return "", errors.New("every name for this customer is taken")
}

// customersEvery is how often the core asks placement again for customers
// still waiting for room.
const customersEvery = time.Minute

// runCustomers asks placement again every customersEvery, until ctx ends,
// for each active customer still waiting for room, since their billing
// provider doesn't call again once it started them.
func (s *Server) runCustomers(ctx context.Context) {
	t := time.NewTicker(customersEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.startWaitingCustomers(ctx)
		}
	}
}

// startWaitingCustomers asks placement again for each active customer with
// no home machine yet, and for each one placed but not yet told their server
// is ready, whose message failed.
func (s *Server) startWaitingCustomers(ctx context.Context) {
	rows, err := s.db.QueryContext(ctx, `SELECT c.user_id FROM customers c LEFT JOIN customer_homes h ON h.user_id = c.user_id
		WHERE c.state = ? AND (COALESCE(h.machine_id, '') = '' OR c.told_ready = 0) ORDER BY c.created_at`, string(CustomerActive))
	if err != nil {
		s.log.Error("could not list the customers waiting for room", "err", err)
		return
	}
	var waiting []int64
	for rows.Next() {
		var id int64
		if rows.Scan(&id) == nil {
			waiting = append(waiting, id)
		}
	}
	rows.Close()
	for _, id := range waiting {
		if err := s.startWaitingCustomer(ctx, id); err != nil {
			s.log.Warn("could not place a customer waiting for room", "user", id, "err", err)
		}
	}
}

// isCustomer reports whether the account is a billing provider's customer.
func (s *Server) isCustomer(userID int64) bool {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM customers WHERE user_id = ?`, userID).Scan(&n)
	return err != nil || n > 0
}
