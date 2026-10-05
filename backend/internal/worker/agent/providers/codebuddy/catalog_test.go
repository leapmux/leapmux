package codebuddy

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/util/testutil"
)

// TestLoadModelCatalogWaitsAsLongAsTheStartupHandshake pins the wait
// of the catalog request. CodeBuddy answers get_available_models from its own
// process, so a late answer means a slow start, not a missing catalog. A cold
// start of the CLI on a loaded machine took longer than five seconds, and the
// agent then kept an empty catalog until its next restart. The request waits as
// long as the startup handshake of the agent allows.
func TestLoadModelCatalogWaitsAsLongAsTheStartupHandshake(t *testing.T) {
	t.Parallel()
	clock := testutil.NewQuartzMock(t)
	requestIDs := make(chan string, 1)
	a, _ := newCodebuddySteerAgent(t, clock, func(requestID string) string {
		requestIDs <- requestID
		return ""
	})
	a.opts.StartupTimeout = time.Minute
	// The traps come after the agent, so that they close before Process.Stop
	// arms its own timer in the cleanup.
	newTimer := clock.Trap().NewTimer()
	stopTimer := clock.Trap().TimerStop()
	t.Cleanup(func() {
		newTimer.Close()
		stopTimer.Close()
	})
	ctx := testutil.DeadlineContext(t)

	result := make(chan error, 1)
	go func() { result <- a.loadModelCatalog() }()

	delay := testutil.WaitForTimer(t, ctx, newTimer)
	require.Equal(t, a.opts.EffectiveStartupTimeout(), delay,
		"the catalog request waits as long as the startup handshake of the agent")

	// The answer arrives long after the old fixed window of five seconds.
	clock.Advance(30 * time.Second).MustWait(ctx)
	requestID := <-requestIDs
	a.HandleOutput([]byte(codebuddyControlAnswer(`{"availableModels":[{"modelId":"custom-local:late","name":"Late"}]}`)(requestID)))
	stopTimer.MustWait(ctx).MustRelease(ctx)

	select {
	case err := <-result:
		require.NoError(t, err)
	case <-ctx.Done():
		t.Fatal("loadModelCatalog did not return after the answer")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	assert.Equal(t, []codebuddyModelInfo{{ID: "custom-local:late", Name: "Late"}}, a.models)
}

// TestLoadModelCatalogKeepsTheCatalogEmptyWhenCodeBuddyCannotAnswer pins the
// two answers that leave the agent with no live catalog. Start tolerates both,
// and the model group then offers the current model alone.
func TestLoadModelCatalogKeepsTheCatalogEmptyWhenCodeBuddyCannotAnswer(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		answer  func(string) string
		message string
	}{
		{
			name:    "an error answer",
			answer:  codebuddyControlFailure("Unknown control request subtype"),
			message: "Unknown control request subtype",
		},
		{
			name:    "an answer with no catalog",
			answer:  codebuddyControlAnswer(`{}`),
			message: "CodeBuddy returned no availableModels list",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a, _ := newCodebuddySteerAgent(t, nil, tc.answer)
			err := a.loadModelCatalog()
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.message)
			a.mu.Lock()
			defer a.mu.Unlock()
			assert.Empty(t, a.models)
		})
	}
}
