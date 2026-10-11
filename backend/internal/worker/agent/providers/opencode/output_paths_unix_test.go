//go:build unix

package opencode

import (
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent/providers/opencode/opencodetest"
)

func TestOpenCodeNativeOutputPathPreservesPreviewWithoutSavedText(t *testing.T) {
	opencodetest.AssertNativeOutputPathDoesNotSaveText(t, "opencode", Start)
}

func TestHelperProcessCompleteToolOutputFile(*testing.T) {
	opencodetest.ServeCompleteToolOutputFileRPC()
}
