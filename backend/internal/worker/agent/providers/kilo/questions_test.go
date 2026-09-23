package kilo

import (
	"context"
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/opencode"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/opencode/opencodetest"
)

func TestKiloAnswersAQuestionThroughTheBridge(t *testing.T) {
	t.Parallel()
	a := &Agent{}
	opencodetest.AssertAnswersAQuestionThroughTheBridge(t, a, opencode.EventRoute, opencode.QuestionRoot,
		func(ctx context.Context, sink agent.ControlServices, baseURL string) {
			a.SetContextForTest(ctx)
			a.BeginQuestionsForTest(ctx, sink, "agent-1", baseURL)
		})
}

func TestKiloRcMarkerNeverReachesTheDaemon(t *testing.T) {
	t.Parallel()
	opencodetest.AssertRcMarkerNeverReachesTheDaemon(t, "kilo", "KILO_CLIENT", kiloQuestionToolEnv, opencode.ACPArgs())
}
