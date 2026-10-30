//go:build unix

package kilo

import (
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent/providers/kilo/kilotest"
)

func TestKiloPinsTheQuestionToolFlag(t *testing.T) {
	kilotest.AssertPinsTheQuestionToolFlag(t, kiloQuestionToolEnv,
		func(t *testing.T, envFile string) { installFakeKiloACP(t, "", envFile) },
		Start, "kilo-question-flag")
}
