package auth

import (
	"encoding/json"
	"fmt"
	"os/user"
	"slices"
)

// Role is what a user may do. Higher roles include the lower ones.
type Role int

const (
	NoAccess Role = iota
	// Viewer sees everything (machines, metrics, XML) but changes nothing.
	Viewer
	// Operator runs machines day to day: power, console, media, snapshots.
	Operator
	// Admin may change everything: create, delete, edit, storage, networks, users' tokens.
	Admin
)

var roleNames = map[Role]string{NoAccess: "none", Viewer: "viewer", Operator: "operator", Admin: "admin"}

func (r Role) String() string { return roleNames[r] }

func ParseRole(s string) (Role, error) {
	for r, n := range roleNames {
		if n == s && r != NoAccess {
			return r, nil
		}
	}
	return NoAccess, fmt.Errorf("unknown role %q (viewer, operator or admin)", s)
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

// Groups maps Linux groups to roles. Root is always Admin. An empty group name is unused.
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
	in := func(group string) bool {
		if group == "" {
			return false
		}
		gr, err := user.LookupGroup(group)
		return err == nil && slices.Contains(ids, gr.Gid)
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
