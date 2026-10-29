package providerkit

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSlashCommand(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		content  string
		wantName string
		wantArgs string
		wantOK   bool
	}{
		{name: "a bare command", content: "/compact", wantName: "compact", wantOK: true},
		{name: "a command with arguments", content: "/compact keep the plan", wantName: "compact", wantArgs: "keep the plan", wantOK: true},
		{name: "surrounding white space", content: "  /smol \n", wantName: "smol", wantOK: true},
		{name: "a line break before the arguments", content: "/compact\nkeep the plan", wantName: "compact", wantArgs: "keep the plan", wantOK: true},
		{name: "a message that names the command later", content: "please /compact"},
		{name: "a slash with no name", content: "/ compact"},
		{name: "a lone slash", content: "/"},
		{name: "an empty message", content: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			name, args, ok := SlashCommand(tc.content)
			assert.Equal(t, tc.wantOK, ok)
			assert.Equal(t, tc.wantName, name)
			assert.Equal(t, tc.wantArgs, args)
		})
	}
}
