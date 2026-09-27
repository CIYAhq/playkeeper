package invites

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// MaxScopeServers bounds how many servers a scope may list.
const MaxScopeServers = 100

// Scope is the servers a team member can use in a project: all of them,
// including servers made later, or only the listed ones.
type Scope struct {
	All     bool     `json:"all,omitempty"`
	Servers []string `json:"servers,omitempty"`
}

// AllServers is the scope of every server in the project.
func AllServers() Scope { return Scope{All: true} }

// OnlyServers is the scope of the listed servers.
func OnlyServers(ids ...string) Scope { return Scope{Servers: ids} }

// Covers reports whether s includes the server.
func (s Scope) Covers(serverID string) bool {
	return validRef(serverID) && (s.All || slices.Contains(s.Servers, serverID))
}

// Within reports whether every server s includes is also in outer. A list
// is never within another list as "all servers", since all servers takes
// in servers made later.
func (s Scope) Within(outer Scope) bool {
	if outer.All {
		return true
	}
	if s.All {
		return false
	}
	for _, id := range s.Servers {
		if !slices.Contains(outer.Servers, id) {
			return false
		}
	}
	return true
}

// String is s as the servers column stores it: "*" for all servers, else
// the ids in order, separated by commas.
func (s Scope) String() string {
	if s.All {
		return "*"
	}
	return strings.Join(s.sorted().Servers, ",")
}

// ParseScope reads a servers column written by Scope.String. The panel
// should read an error as no servers at all.
func ParseScope(col string) (Scope, error) {
	if col == "*" {
		return AllServers(), nil
	}
	s := OnlyServers(strings.Split(col, ",")...)
	if s.check() != nil {
		return Scope{}, errors.New("the servers column is not a list of server ids")
	}
	return s.sorted(), nil
}

// check refuses a scope that isn't all servers or a list of distinct,
// valid server ids.
func (s Scope) check() error {
	switch {
	case s.All && len(s.Servers) > 0:
		return badOptions("servers", "Choose all servers or some of them, not both.")
	case s.All:
		return nil
	case len(s.Servers) == 0:
		return badOptions("servers", "Choose all servers, or at least one server.")
	case len(s.Servers) > MaxScopeServers:
		return badOptions("servers", fmt.Sprintf("Choose at most %d servers, or all of them.", MaxScopeServers), "max", strconv.Itoa(MaxScopeServers))
	}
	for i, id := range s.Servers {
		if !validRef(id) || slices.Contains(s.Servers[:i], id) {
			return badOptions("servers", "Choose servers from this project, each once.")
		}
	}
	return nil
}

func (s Scope) sorted() Scope {
	if s.All {
		return AllServers()
	}
	ids := slices.Clone(s.Servers)
	slices.Sort(ids)
	return OnlyServers(ids...)
}

// narrow drops the servers that aren't in existing. ok is false when a
// list has none left.
func (s Scope) narrow(existing []string) (Scope, bool) {
	if s.All {
		return AllServers(), true
	}
	var kept []string
	for _, id := range s.Servers {
		if slices.Contains(existing, id) {
			kept = append(kept, id)
		}
	}
	return OnlyServers(kept...), len(kept) > 0
}
