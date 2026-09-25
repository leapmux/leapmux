//go:build unix

package providerkit

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestProcessRunsForTheParentProcess(t *testing.T) {
	t.Parallel()
	assert.True(t, ProcessRuns(os.Getppid()))
}
