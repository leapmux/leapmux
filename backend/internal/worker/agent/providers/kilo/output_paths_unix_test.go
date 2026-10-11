//go:build unix

package kilo

import (
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent/providers/opencode/opencodetest"
)

func TestKiloNativeOutputPathPreservesPreviewWithoutSavedText(t *testing.T) {
	opencodetest.AssertNativeOutputPathDoesNotSaveText(t, "kilo", Start)
}

func TestHelperProcessCompleteToolOutputFile(*testing.T) {
	opencodetest.ServeCompleteToolOutputFileRPC()
}
