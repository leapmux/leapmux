package agenttest

import (
	"strings"
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ResumeHandleErr is ResolveResumeHandle's error half, for the cases that ask
// only whether a handle is acceptable. A case that cares about the value the
// caller must SEND calls ResolveResumeHandle directly and reads both returns --
// see TestResolveResumeHandleReturnsWhatReachesArgv.
func ResumeHandleErr(p agent.Provider, handle, homeDir string) error {
	_, err := p.ResolveResumeHandle(handle, homeDir)
	return err
}

// AssertTokenResumeRule pins that plugin keeps the token rule for a resume
// handle, and returns an accepted handle unchanged.
func AssertTokenResumeRule(t *testing.T, plugin agent.Provider) {
	t.Helper()
	const token = "01JAV8Q3ZP9K2M4N6R8T0W2Y4B"
	resolved, err := plugin.ResolveResumeHandle(token, "")
	require.NoError(t, err)
	assert.Equal(t, token, resolved, "the token rule refuses rather than normalizes")
	assert.NoError(t, ResumeHandleErr(plugin, "", ""), "the empty handle means no resume")
	// The guard the token rule exists for: one argv element is enough to reach a
	// permission-skipping flag.
	assert.Error(t, ResumeHandleErr(plugin, "--dangerously-skip-permissions", ""))
	assert.Error(t, ResumeHandleErr(plugin, strings.Repeat("a", 129), ""))
}
