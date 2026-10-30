//go:build unix

package kilo

import (
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent/providers/kilo/kilotest"
)

func TestKiloNativeOutputPathPreservesPreviewWithoutSavedText(t *testing.T) {
	kilotest.AssertNativeOutputPathDoesNotSaveText(t, "kilo", Start)
}

func TestHelperProcessCompleteToolOutputFile(*testing.T) {
	kilotest.ServeCompleteToolOutputFileRPC()
}
