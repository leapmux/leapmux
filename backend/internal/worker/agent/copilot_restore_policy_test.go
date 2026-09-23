//go:build unix

package agent

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// An axis the Copilot runtime refused fails the restore only when it changes what
// the session DOES. Effort does not, and treating it as fatal aborted a goal clear
// the user had asked for: the runtime reports effort only through
// `model.getCurrent`, and an empty `reasoningEffort` there deletes the option, so a
// replaced session reads back "" for a tier nobody chose in that moment.
func TestCopilotRestoreIsFatal(t *testing.T) {
	t.Parallel()

	assert.False(t, copilotRestoreIsFatal(OptionIDEffort), "effort is a quality dial, not a capability")

	for _, option := range []string{OptionIDModel, OptionIDPermissionMode, copilotOptionSessionMode} {
		assert.True(t, copilotRestoreIsFatal(option),
			"%s changes what the session does, so a refused value must not survive", option)
	}
}
