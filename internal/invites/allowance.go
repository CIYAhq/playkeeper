package invites

import (
	"fmt"
	"net/http"
	"strconv"
)

// Allowance is what a creator may make on a shared machine, as in the
// managed beta: up to Servers servers with MemoryMB of memory between them,
// and the disk of DiskBytes. A creator invite gives Admin with no servers
// and an allowance; the creator is then Admin of each server they create.
// Only the owner may give one, since a creator's servers use the machine's
// memory and disk.
type Allowance struct {
	Servers  int `json:"servers"`
	MemoryMB int `json:"memoryMB"`
	// DiskGB is the disk their servers may take between them, or 0 for the
	// default from the memory.
	DiskGB int `json:"diskGB,omitempty"`
}

// Bounds of an allowance.
const (
	MaxAllowanceServers  = 10
	MinAllowanceMemoryMB = 1024
	MaxAllowanceMemoryMB = 65536
	MaxAllowanceDiskGB   = 4096
)

// IsZero reports whether al allows nothing: the account isn't a creator.
func (al Allowance) IsZero() bool { return al == Allowance{} }

// DiskBytes is the disk al's servers may take between them: DiskGB, or by
// default 7.5 GB for each GB of memory, as Playkeeper Cloud's plans give.
// web/src/lib/access.ts shows the same default.
func (al Allowance) DiskBytes() int64 {
	if al.DiskGB > 0 {
		return int64(al.DiskGB) << 30
	}
	return int64(al.MemoryMB) * 15 << 19
}

func (al Allowance) check() error {
	switch {
	case al.Servers < 1 || al.Servers > MaxAllowanceServers:
		return badOptions("allowance", fmt.Sprintf("Allow 1 to %d servers.", MaxAllowanceServers), "max", strconv.Itoa(MaxAllowanceServers))
	case al.MemoryMB < MinAllowanceMemoryMB || al.MemoryMB > MaxAllowanceMemoryMB || al.MemoryMB%512 != 0:
		return badOptions("allowance", "Allow 1 to 64 GB of memory, in steps of 0.5 GB.")
	case al.DiskGB < 0 || al.DiskGB > MaxAllowanceDiskGB:
		return badOptions("allowance", fmt.Sprintf("Allow up to %d GB of disk, or leave it to the default from the memory.", MaxAllowanceDiskGB))
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
