//go:build unix

package opencode

import (
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent/providers/kilo/kilotest"
)

func TestOpenCodeNativeOutputPathPreservesPreviewWithoutSavedText(t *testing.T) {
	kilotest.AssertNativeOutputPathDoesNotSaveText(t, "opencode", Start)
}

func TestHelperProcessCompleteToolOutputFile(*testing.T) {
	kilotest.ServeCompleteToolOutputFileRPC()
}
