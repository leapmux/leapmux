//go:build unix

// The spawn test below installs a fake CLI on PATH through the shared unix-only
// helpers, so it lives beside them rather than in questions_test.go.

package opencode

import (
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent/providers/opencode/opencodetest"
)

func TestOpenCodeFamilyPinsTheQuestionToolFlag(t *testing.T) {
	opencodetest.AssertPinsTheQuestionToolFlag(t, openCodeQuestionToolEnv,
		func(t *testing.T, envFile string) { installFakeOpenCodeACP(t, "", envFile) },
		Start, "opencode-question-flag")
}
