package auth

import (
	"encoding/json"
	"fmt"
	"os/user"
	"slices"
	"strings"
)

// Role is what a user may do. Higher roles include the lower ones.
type Role int

const (
	NoAccess Role = iota
	// Member has no global role, but belongs to one or more projects (ADR-0060).
	Member
	// Viewer sees everything (machines, metrics, XML) but changes nothing.
	Viewer
	// Operator runs machines day to day: power, console, media, snapshots.
	Operator
	// Admin may change everything: create, delete, edit, storage, networks, users' tokens.
	Admin
)

var roleNames = map[Role]string{NoAccess: "none", Member: "member", Viewer: "viewer", Operator: "operator", Admin: "admin"}

func (r Role) String() string { return roleNames[r] }

func ParseRole(s string) (Role, error) {
	for r, n := range roleNames {
		if n == s && r != NoAccess {
			return r, nil
		}
	}
	return NoAccess, fmt.Errorf("unknown role %q (member, viewer, operator or admin)", s)
}

func (r Role) MarshalJSON() ([]byte, error) { return json.Marshal(r.String()) }

func (r *Role) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	v, err := ParseRole(s)
	*r = v
	return err
}

// Identity is a signed-in user (or the owner of an API token) and their role.
type Identity struct {
	Name string `json:"user"`
	Role Role   `json:"role"`
	// Token is the API token name when the request used one.
	Token string `json:"token,omitempty"`
	// Via is the cluster member that forwarded the request (ADR-0020); the user signed in there.
	Via string `json:"via,omitempty"`
}

// Groups maps Linux groups to roles. Root is always Admin. Each field may list several
// groups separated by commas; empty names and groups the host does not have are skipped.
type Groups struct {
	Admin, Operator, Viewer string
}

// RoleOf returns the highest role the user's group memberships give.
func (g Groups) RoleOf(name string) Role {
	u, err := user.Lookup(name)
	if err != nil {
		return NoAccess
	}
	if u.Uid == "0" {
		return Admin
	}
	ids, err := u.GroupIds()
	if err != nil {
		return NoAccess
	}
	in := func(groups string) bool {
		for _, group := range strings.Split(groups, ",") {
			if group = strings.TrimSpace(group); group == "" {
				continue
			}
			if gr, err := user.LookupGroup(group); err == nil && slices.Contains(ids, gr.Gid) {
				return true
			}
		}
		return false
	}
	switch {
	case in(g.Admin):
		return Admin
	case in(g.Operator):
		return Operator
	case in(g.Viewer):
		return Viewer
	}
	return NoAccess
}

// UserGroups lists the names of the Linux groups a user belongs to (quota rules by group).
func UserGroups(name string) []string {
	u, err := user.Lookup(name)
	if err != nil {
		return nil
	}
	ids, err := u.GroupIds()
	if err != nil {
		return nil
	}
	var out []string
	for _, id := range ids {
		if g, err := user.LookupGroupId(id); err == nil {
			out = append(out, g.Name)
		}
	}
	return out
}
