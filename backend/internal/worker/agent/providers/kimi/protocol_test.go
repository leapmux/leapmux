package kimi

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestKimiReadyLine(t *testing.T) {
	t.Parallel()

	// The line 2.0.2 prints with `--log-level warn`.
	const line = "Kimi server: http://127.0.0.1:62730/#token=92IY_KGgWZDdCOpz7egMynRjlXDdImfa-znrFmRMXPk"
	match := kimiReadyLine.FindStringSubmatch(line)
	require.NotNil(t, match)
	assert.Equal(t, "http://127.0.0.1:62730", match[1])
	assert.Equal(t, "92IY_KGgWZDdCOpz7egMynRjlXDdImfa-znrFmRMXPk", match[2])

	address := kimiAddressLine.FindStringSubmatch(line)
	require.Len(t, address, 2, "the listen waiter takes a pattern with one group")
	assert.Equal(t, "http://127.0.0.1:62730", address[1])

	for _, other := range []string{
		"",
		`{"level":40,"msg":"Kimi server: http://127.0.0.1:1/#token=x"}`,
		"Kimi server: http://127.0.0.1:62730/",
		"Kimi server: http://127.0.0.1:62730/#token=",
		"Kimi server: https://example.com/#token=abc",
		"  Kimi server: http://127.0.0.1:62730/#token=abc",
	} {
		assert.Nil(t, kimiReadyLine.FindStringSubmatch(other), "%q is not the ready line", other)
		assert.Nil(t, kimiAddressLine.FindStringSubmatch(other), "%q is not the ready line", other)
	}
}

func TestKimiCheckID(t *testing.T) {
	t.Parallel()

	for _, id := range []string{"session_f7cf22a1-3d27-4d41-b8e2-978b1e5fa5a7", "approval_1", "q.0", "task-abc"} {
		assert.NoError(t, kimiCheckID("session", id), id)
	}
	for _, id := range []string{"", "../x", "a/b", "a:abort", "a b", "a?b=c", "a#b", "ä"} {
		err := kimiCheckID("session", id)
		require.Error(t, err, "%q must be refused", id)
		assert.Contains(t, err.Error(), "session id")
	}
}

func TestKimiPaths(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "/api/v1/sessions/session_1/profile", kimiSessionPath("session_1", "/profile"))
	assert.Equal(t, "/api/v1/sessions/session_1:abort", kimiSessionPath("session_1", kimiActionAbort))
	assert.Equal(t, "/api/v1/sessions/session_1/questions/q_1:dismiss", kimiItemPath("session_1", "questions", "q_1", kimiActionDismiss))
	assert.Equal(t, "/api/v1/sessions/session_1/approvals/a_1", kimiItemPath("session_1", "approvals", "a_1", ""))
}
