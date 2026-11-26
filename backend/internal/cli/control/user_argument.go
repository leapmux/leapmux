package control

import (
	"strings"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/cli"
)

// UserArgumentHelp is the help text of a <user> argument.
const UserArgumentHelp = `A username, "id:<id>" for a user id, or "username:<name>".`

// ParseUserArgument reads the <user> argument of a command that addresses ONE
// user: a username, "id:<id>" for a user id, or "username:<name>". A username
// cannot hold a colon, so the prefixes cannot collide with one. The online
// `leapmux admin` commands and the offline `leapmux recover` commands both read
// it here, so one argument means the same user on both.
//
// It refuses an empty argument before anything opens a connection or a
// database.
func ParseUserArgument(raw string) (*leapmuxv1.UserSelector, error) {
	raw = strings.TrimSpace(raw)
	if id, byID := strings.CutPrefix(raw, "id:"); byID {
		id = strings.TrimSpace(id)
		if id == "" {
			return nil, cli.Usagef("<user> %q has no id after the \"id:\" prefix", raw)
		}
		return &leapmuxv1.UserSelector{By: &leapmuxv1.UserSelector_Id{Id: id}}, nil
	}
	if name, byName := strings.CutPrefix(raw, "username:"); byName {
		name = strings.TrimSpace(name)
		if name == "" {
			return nil, cli.Usagef("<user> %q has no username after the \"username:\" prefix", raw)
		}
		return &leapmuxv1.UserSelector{By: &leapmuxv1.UserSelector_Username{Username: name}}, nil
	}
	if raw == "" {
		return nil, cli.Usagef("<user> is empty").WithHint(`give a username, "id:<id>", or "username:<name>"`)
	}
	return &leapmuxv1.UserSelector{By: &leapmuxv1.UserSelector_Username{Username: raw}}, nil
}
