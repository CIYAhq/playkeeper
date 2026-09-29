package invites

import (
	"fmt"
	"net/http"
	"strconv"
)

// Allowance is what a creator may make on a shared machine, as in the
// managed beta: up to Servers servers with MemoryMB of memory between them.
// A creator invite gives Admin with no servers and an allowance; the
// creator is then Admin of each server they create. Only the owner may give
// one, since a creator's servers use the machine's memory and disk.
type Allowance struct {
	Servers  int `json:"servers"`
	MemoryMB int `json:"memoryMB"`
}

// Bounds of an allowance.
const (
	MaxAllowanceServers  = 10
	MinAllowanceMemoryMB = 1024
	MaxAllowanceMemoryMB = 65536
)

// IsZero reports whether al allows nothing: the account isn't a creator.
func (al Allowance) IsZero() bool { return al == Allowance{} }

func (al Allowance) check() error {
	switch {
	case al.Servers < 1 || al.Servers > MaxAllowanceServers:
		return badOptions("allowance", fmt.Sprintf("Allow 1 to %d servers.", MaxAllowanceServers), "max", strconv.Itoa(MaxAllowanceServers))
	case al.MemoryMB < MinAllowanceMemoryMB || al.MemoryMB > MaxAllowanceMemoryMB || al.MemoryMB%512 != 0:
		return badOptions("allowance", "Allow 1 to 64 GB of memory, in steps of 0.5 GB.")
	}
	return nil
}

// Check reports whether al is within an allowance's bounds, for allowances
// that come from elsewhere, such as a plan sold on Whop.
func (al Allowance) Check() error { return al.check() }

// CanGrantAllowance reports whether a may invite a creator with al.
func CanGrantAllowance(a Account, al Allowance) error {
	if a.InstallRole != InstallOwner {
		return allowanceNotAllowed()
	}
	return al.check()
}

func allowanceNotAllowed() *Error {
	return &Error{Code: CodeRoleNotAllowed, Status: http.StatusForbidden, Msg: "Only the owner can invite creators.", Hint: "Ask the owner of this Playkeeper to send the invite."}
}
