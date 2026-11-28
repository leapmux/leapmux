package control

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/cli"
)

func TestParseUserArgument(t *testing.T) {
	t.Run("reads a username or an id", func(t *testing.T) {
		for raw, want := range map[string]struct{ id, username string }{
			"alice":            {username: "alice"},
			"  alice  ":        {username: "alice"},
			"username:alice":   {username: "alice"},
			"username: alice ": {username: "alice"},
			" username:alice ": {username: "alice"},
			"id:u-1":           {id: "u-1"},
			"id: u-1 ":         {id: "u-1"},
			" id:u-1":          {id: "u-1"},
			"id:u:1":           {id: "u:1"},
			"ID:u-1":           {username: "ID:u-1"},
			"alice:id:x":       {username: "alice:id:x"},
		} {
			sel, err := ParseUserArgument(raw)
			require.NoError(t, err, "argument %q", raw)
			if want.id != "" {
				_, ok := sel.By.(*leapmuxv1.UserSelector_Id)
				assert.True(t, ok, "argument %q must select by id", raw)
			} else {
				_, ok := sel.By.(*leapmuxv1.UserSelector_Username)
				assert.True(t, ok, "argument %q must select by username", raw)
			}
			assert.Equal(t, want.id, sel.GetId(), "argument %q", raw)
			assert.Equal(t, want.username, sel.GetUsername(), "argument %q", raw)
		}
	})

	t.Run("refuses an argument that selects nobody", func(t *testing.T) {
		for raw, message := range map[string]string{
			"":            "<user> is empty",
			"   ":         "<user> is empty",
			"id:":         `<user> "id:" has no id after the "id:" prefix`,
			"id:  ":       `<user> "id:" has no id after the "id:" prefix`,
			"username:":   `<user> "username:" has no username after the "username:" prefix`,
			"username:  ": `<user> "username:" has no username after the "username:" prefix`,
		} {
			sel, err := ParseUserArgument(raw)
			require.Error(t, err, "argument %q", raw)
			assert.Nil(t, sel)
			classified := cli.FromError(err)
			assert.Equal(t, cli.CodeUsage, classified.Code, "argument %q", raw)
			assert.Contains(t, err.Error(), message, "argument %q", raw)
			if message == "<user> is empty" {
				assert.Contains(t, classified.Hints, `give a username, "id:<id>", or "username:<name>"`, "argument %q", raw)
			}
		}
	})
}
