package control

import (
	"strings"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/hub/service"
)

// UserArgumentHelp is the help text of a <user> argument.
const UserArgumentHelp = `A username, or "id:<id>" for a user id.`

// ParseUserArgument reads the <user> argument of a command that addresses ONE
// user: a username, or "id:<id>" for a user id. A username cannot hold a
// colon, so the prefix cannot collide with one. The online `leapmux control
// admin` commands and the offline `leapmux recover` commands both read it
// here, so one argument means the same user on both.
//
// It refuses an empty argument before anything opens a connection or a
// database. service.ResolveUserSelector holds the same rule, but a caller
// reaches it only after that work.
func ParseUserArgument(raw string) (*leapmuxv1.UserSelector, error) {
	raw = strings.TrimSpace(raw)
	id, byID := strings.CutPrefix(raw, "id:")
	id = strings.TrimSpace(id)
	switch {
	case byID && id == "":
		return nil, cli.Usagef("<user> %q has no id after the \"id:\" prefix", raw)
	case byID:
		return service.SelectUserByID(id), nil
	case raw == "":
		return nil, cli.Usagef("<user> is empty").WithHint(`give a username, or "id:<id>" for a user id`)
	default:
		return service.SelectUserByUsername(raw), nil
	}
}
