package lifecycle

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestAccountsHaveDistinctScratchPathsUnderSharedTemporaryParent(t *testing.T) {
	parent := t.TempDir()
	first := accountRuntimePath(parent, "S-1-5-21-1001")
	second := accountRuntimePath(parent, "S-1-5-21-1002")
	assert.NotEqual(t, first, second)
	assert.Equal(t, parent, filepath.Dir(first))
	assert.Equal(t, parent, filepath.Dir(second))
	assert.Equal(t, first, accountRuntimePath(parent, "S-1-5-21-1001"))
}
