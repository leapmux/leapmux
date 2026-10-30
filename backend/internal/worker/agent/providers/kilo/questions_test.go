package kilo

import (
	"context"
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/kilo/kilotest"
)

func TestKiloAnswersAQuestionThroughTheBridge(t *testing.T) {
	t.Parallel()
	a := &Agent{}
	kilotest.AssertAnswersAQuestionThroughTheBridge(t, a, EventRoute, QuestionRoot,
		func(ctx context.Context, sink agent.ControlServices, baseURL string) {
			a.SetContextForTest(ctx)
			a.BeginQuestionsForTest(ctx, sink, "agent-1", baseURL)
		})
}

func TestKiloRcMarkerNeverReachesTheDaemon(t *testing.T) {
	t.Parallel()
	kilotest.AssertRcMarkerNeverReachesTheDaemon(t, "kilo", "KILO_CLIENT", kiloQuestionToolEnv, ACPArgs())
}
