package invites

// Install roles (users.role) and project roles (project_members.role), as
// in decision 0004 and the team page: a viewer looks around, a moderator
// also runs the servers day to day (players, console, restarts, backups),
// and an admin can do everything, including the team.
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

// Account is a Playkeeper account as it stands now, as far as invites care:
// the creator of an invite, or whoever answers a join request. A deleted
// account is the zero Account.
type Account struct {
	UserID int64
	// Name is the username. The join page shows the creator's ("siya
	// invited you to Survival").
	Name        string
	InstallRole string
	// ProjectRole is the account's role in the invite's project, "" if
	// none.
	ProjectRole string
	// Servers is the account's scope in that project. The owner's is
	// ignored: the owner can use every server.
	Servers Scope
	// TwoFactor says whether two-factor sign-in is on.
	TwoFactor bool
}

// RequiresTwoFactor reports whether an account must have two-factor sign-in
// on to use its role. Project admins must. The owner isn't held to it here,
// so an install set up before two-factor existed keeps working.
func RequiresTwoFactor(installRole, projectRole string) bool {
	return installRole != InstallOwner && projectRole == RoleAdmin
}

// CanGrant reports whether a may invite someone with role for servers. The
// owner may give any project role for any servers. A project admin with
// two-factor on may give the roles below admin, and only for servers they
// can use themselves, so nobody hands out more than they have. Nobody can
// be invited as the owner.
func CanGrant(a Account, role string, servers Scope) error {
	if err := canGive(a, role); err != nil {
		return err
	}
	if err := servers.check(); err != nil {
		return err
	}
	if a.InstallRole != InstallOwner && !servers.Within(a.Servers) {
		return serversNotAllowed()
	}
	return nil
}

func canGive(a Account, role string) error {
	if role == InstallOwner {
		return roleNotAllowed(role)
	}
	r := rank(role)
	switch {
	case r == 0:
		return roleUnknown()
	case a.InstallRole == InstallOwner:
		return nil
	case a.InstallRole != InstallMember || a.ProjectRole != RoleAdmin || r >= rank(RoleAdmin):
		return roleNotAllowed(role)
	case RequiresTwoFactor(a.InstallRole, a.ProjectRole) && !a.TwoFactor:
		return TwoFactorRequired()
	}
	return nil
}

// GrantableRoles lists the project roles a may give, for the role picker.
func GrantableRoles(a Account) []string {
	var out []string
	for _, r := range ProjectRoles() {
		if canGive(a, r) == nil {
			out = append(out, r)
		}
	}
	return out
}

// CanLetPlayersIn reports whether a may let players into a server: make
// friend invites for it and answer its join requests. The owner may, and so
// may a project moderator or admin whose servers include it. Letting
// players in is a moderator's right, so an admin without two-factor keeps
// it.
func CanLetPlayersIn(a Account, serverID string) error {
	switch {
	case a.InstallRole == InstallOwner && validRef(serverID):
		return nil
	case a.InstallRole == InstallMember && rank(a.ProjectRole) >= rank(RoleModerator) && a.Servers.Covers(serverID):
		return nil
	}
	return playersNotAllowed()
}
