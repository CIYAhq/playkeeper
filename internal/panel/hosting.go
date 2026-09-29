package panel

import (
	"context"
	"errors"
)

// The interface between the hosting core and a billing provider, both
// inside the panel. A billing provider (Whop, in whop_customers.go) turns
// its customers' purchases into the core's calls, and gives the core a
// notifier for what those customers should be told. The core never talks to
// the provider, and the provider touches accounts, servers and limits only
// through these calls. Each call is idempotent, and only a purchase the
// provider's API confirms leads to one.

// Customer is someone who pays through a billing provider: the provider and
// its id for them, such as "whop" and Whop's "user_…", and a handle, their
// username there, that their account and first server are named after.
type Customer struct {
	Provider string
	Subject  string
	Handle   string
}

// CustomerPlan is what a customer pays for: its id and name, and the servers
// and memory it allows between them, with DiskGB the disk for all of them,
// 0 for the default for the memory.
type CustomerPlan struct {
	ID       string
	Name     string
	Servers  int
	MemoryMB int
	DiskGB   int
}

// StartedCustomer is what StartCustomer made or found: the account's name,
// the dashboard's address, where they sign in, and their first server's.
type StartedCustomer struct {
	Account   string
	Dashboard string
	Server    string
}

// CustomerState is where a customer's account stands.
type CustomerState string

const (
	CustomerActive CustomerState = "active"
	CustomerPaused CustomerState = "paused"
)

// CustomerAccountInfo is which account a customer is, and whether they may
// sign in to it now.
type CustomerAccountInfo struct {
	UserID   int64
	Username string
	State    CustomerState
	SignIn   bool
}

// hostingCore is the core's side of the interface.
type hostingCore interface {
	// StartCustomer makes the customer's account and its ready server the
	// first time, and gives an account that has one the plan, resuming it
	// if its plan had ended.
	StartCustomer(ctx context.Context, c Customer, p CustomerPlan) (StartedCustomer, error)
	// ChangeCustomerPlan gives an account the plan's limits.
	ChangeCustomerPlan(ctx context.Context, c Customer, p CustomerPlan) error
	// PauseCustomer is for a customer whose access ended: their servers
	// stop, starting them is refused, and they're deleted when the grace
	// period ends.
	PauseCustomer(ctx context.Context, c Customer, reason string) error
	// ResumeCustomer brings back a paused customer within the grace period.
	ResumeCustomer(ctx context.Context, c Customer, p CustomerPlan) error
	// CustomerAccount says which account a customer is; ok is false while
	// they have none.
	CustomerAccount(ctx context.Context, provider, subject string) (info CustomerAccountInfo, ok bool, err error)
}

// CustomerMessage is something to tell a customer: what it's about (ready,
// paused, back or deleted) and its words.
type CustomerMessage struct {
	Kind string
	Text string
}

// customerNotifier is the billing provider's side the core calls: it sends
// the message the way that provider reaches its customers.
type customerNotifier interface {
	Notify(ctx context.Context, c Customer, m CustomerMessage) error
}

// errNoHostingCore is the core's answer until it hosts customers.
var errNoHostingCore = errors.New("This dashboard can't host customers yet.")

// noHostingCore stands in for the core until it hosts customers.
type noHostingCore struct{}

func (noHostingCore) StartCustomer(context.Context, Customer, CustomerPlan) (StartedCustomer, error) {
	return StartedCustomer{}, errNoHostingCore
}

func (noHostingCore) ChangeCustomerPlan(context.Context, Customer, CustomerPlan) error {
	return errNoHostingCore
}

func (noHostingCore) PauseCustomer(context.Context, Customer, string) error { return errNoHostingCore }

func (noHostingCore) ResumeCustomer(context.Context, Customer, CustomerPlan) error {
	return errNoHostingCore
}

func (noHostingCore) CustomerAccount(context.Context, string, string) (CustomerAccountInfo, bool, error) {
	return CustomerAccountInfo{}, false, nil
}
