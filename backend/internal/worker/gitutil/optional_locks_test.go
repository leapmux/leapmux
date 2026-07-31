package gitutil

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestNewGitCmd_DeclinesOptionalLocks pins the env var that keeps read-only
// probes (git status) from taking .git/index.lock and colliding with a
// concurrent checkout/worktree-add in the same repo.
func TestNewGitCmd_DeclinesOptionalLocks(t *testing.T) {
	t.Parallel()
	cmd := NewGitCmd(context.Background(), "status", "--porcelain")

	var values []string
	for _, kv := range cmd.Env {
		if v, ok := strings.CutPrefix(kv, "GIT_OPTIONAL_LOCKS="); ok {
			values = append(values, v)
		}
	}
	require.Len(t, values, 1, "every git command must decline optional locks exactly once; see NewGitCmd")
	assert.Equal(t, "0", values[0])
}
