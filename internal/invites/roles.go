package invites

// Install roles (users.role) and project roles (project_members.role), as
// in decision 0004. Admin is the only project role in 0.3.0; moderator and
// viewer come with co-admins.
const (
	InstallOwner  = "owner"
	InstallMember = "member"

	RoleAdmin     = "admin"
	RoleModerator = "moderator"
	RoleViewer    = "viewer"
)

// ProjectRoles lists the project roles, most trusted first.
func ProjectRoles() []string { return []string{RoleAdmin, RoleModerator, RoleViewer} }

// rank orders project roles; 0 is not a project role.
func rank(role string) int {
	switch role {
	case RoleAdmin:
		return 3
	case RoleModerator:
		return 2
	case RoleViewer:
		return 1
	default:
		return 0
	}
}

// Inviter is the account behind a member invite as it stands now: its
// install role, and its role in the invite's project ("" if none). A
// deleted account is the zero Inviter.
type Inviter struct {
	UserID      int64
	InstallRole string
	ProjectRole string
}

// CanGrant reports whether inviter may give role. The install's owner may
// give any project role, and a project admin the roles below admin, so an
// admin can't make another admin. Nobody can be invited as the owner.
func CanGrant(inviter Inviter, role string) error {
	if role == InstallOwner {
		return roleNotAllowed(role)
	}
	r := rank(role)
	if r == 0 {
		return roleUnknown()
	}
	switch inviter.InstallRole {
	case InstallOwner:
		return nil
	case InstallMember:
		if rank(inviter.ProjectRole) == rank(RoleAdmin) && r < rank(RoleAdmin) {
			return nil
		}
	}
	return roleNotAllowed(role)
}

// GrantableRoles lists the project roles inviter may give, for the role
// picker.
func GrantableRoles(inviter Inviter) []string {
	var out []string
	for _, r := range ProjectRoles() {
		if CanGrant(inviter, r) == nil {
			out = append(out, r)
		}
	}
	return out
}
