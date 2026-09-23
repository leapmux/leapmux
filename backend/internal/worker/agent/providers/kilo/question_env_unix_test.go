//go:build unix

package kilo

import (
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent/providers/opencode/opencodetest"
)

func TestKiloPinsTheQuestionToolFlag(t *testing.T) {
	opencodetest.AssertPinsTheQuestionToolFlag(t, kiloQuestionToolEnv,
		func(t *testing.T, envFile string) { installFakeKiloACP(t, "", envFile) },
		Start, "kilo-question-flag")
}
