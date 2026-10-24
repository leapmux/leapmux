package codebuddy

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
)

func TestCodebuddyOutputStyleNames(t *testing.T) {
	t.Parallel()
	large := strings.Repeat("長", 1<<16)
	styles := normalizeCodebuddyOutputStyles([]string{
		"", " \t", "Default", "default", "Concise", "CONCISE", "İ", "i", "i\u0307",
		"ΟΣ", "ος", "οσ", "詳細説明", " Framed ", "Framed", large, large,
	})
	assert.Equal(t, []string{"Default", "Concise", "İ", "i", "ΟΣ", "οσ", "詳細説明", " Framed ", "Framed", large}, styles)
	assert.Equal(t, "Default", codebuddyOutputStyleName(styles, "default"))
	assert.Equal(t, "İ", codebuddyOutputStyleName(styles, "i\u0307"))
	assert.Equal(t, "i", codebuddyOutputStyleName(styles, "i"))
	assert.Equal(t, "ΟΣ", codebuddyOutputStyleName(styles, "ος"))
	assert.Equal(t, "οσ", codebuddyOutputStyleName(styles, "οσ"))
	assert.Equal(t, " Framed ", codebuddyOutputStyleName(styles, " Framed "))
	assert.Equal(t, "Configured Custom", codebuddyOutputStyleName(styles, "Configured Custom"))
	assert.Empty(t, codebuddyOutputStyleName(styles, " \t"))
	assert.NotNil(t, normalizeCodebuddyOutputStyles(nil), "an explicit empty catalog must differ from absent metadata")
}

func TestCodebuddyOutputStyleGroupKeepsItsNativeSelection(t *testing.T) {
	t.Parallel()
	assert.Nil(t, codebuddyOutputStyleGroup(nil, "Custom"))
	assert.Nil(t, codebuddyOutputStyleGroup([]string{}, ""))
	for _, styles := range [][]string{{"Default"}, {"Default", "Custom"}, {}} {
		group := codebuddyOutputStyleGroup(styles, "custom")
		require.NotNil(t, group)
		assert.Equal(t, "custom", group.GetCurrentValue())
		assert.Equal(t, "custom", group.GetDefaultValue())
		var selected int
		for _, option := range group.GetOptions() {
			if option.GetId() == "custom" {
				selected++
				if len(styles) == 2 {
					assert.Equal(t, "Custom", option.GetName())
				}
			}
		}
		assert.Equal(t, 1, selected, "catalog enrichment must preserve the selected option ID")
	}
}

func TestCodebuddyOutputStyleRefreshOnlyPublishesChoices(t *testing.T) {
	t.Parallel()
	for _, current := range []string{"Concise", "custom"} {
		t.Run(current, func(t *testing.T) {
			t.Parallel()
			a, peer := newCodebuddySteerAgent(t, nil, codebuddyControlAnswer(`{"output_style":"Explanatory","available_output_styles":["Default","Concise","Custom"]}`))
			sink := &agenttest.Sink{}
			a.sink = agent.NewProviderServices(sink)
			a.outputStyle = current
			a.outputStyles = []string{"Default", "Concise"}
			a.refreshOutputStyleCatalog()
			assert.Equal(t, current, a.SettingsSnapshot().SurfacedOptions[OptionOutputStyle], "native settings metadata does not replace the session's retained choice")
			assert.Equal(t, []string{"Default", "Concise", "Custom"}, a.outputStyles)
			require.Equal(t, 1, sink.SettingsRefreshCount())
			assert.Empty(t, sink.LastSettingsRefresh().Options, "only catalog choices changed")
			assert.Zero(t, sink.StatusActiveCount(), "catalog enrichment cannot reactivate a stopped process")
			assert.Contains(t, peer.String(), `"capabilities":{"askUserQuestion":true}`)
			assert.NotContains(t, peer.String(), "elicitation")
			a.refreshOutputStyleCatalog()
			assert.Equal(t, 1, sink.SettingsRefreshCount(), "an unchanged catalog needs no publication")
		})
	}
}

func TestCodebuddyOutputStyleRefreshKeepsValidChoicesOnFailure(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		answer func(string) string
	}{
		{name: "refused", answer: codebuddyControlFailure("No catalog.")},
		{name: "absent", answer: codebuddyControlAnswer(`{"output_style":"Default"}`)},
		{name: "null", answer: codebuddyControlAnswer(`{"available_output_styles":null}`)},
		{name: "malformed", answer: codebuddyControlAnswer(`{"available_output_styles":[false]}`)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a, _ := newCodebuddySteerAgent(t, nil, tc.answer)
			sink := &agenttest.Sink{}
			a.sink = agent.NewProviderServices(sink)
			a.outputStyles = []string{"Default", "Concise"}
			a.outputStyle = "Concise"
			a.refreshOutputStyleCatalog()
			assert.Equal(t, []string{"Default", "Concise"}, a.outputStyles)
			assert.Equal(t, "Concise", a.outputStyle)
			assert.Zero(t, sink.SettingsRefreshCount())
			assert.Zero(t, sink.StatusActiveCount())
		})
	}
}

func TestCodebuddyOutputStyleRefreshTimeout(t *testing.T) {
	t.Parallel()
	clock := testutil.NewQuartzMock(t)
	a, _ := newCodebuddySteerAgent(t, clock, nil)
	a.opts.APITimeout = time.Minute
	a.outputStyles = []string{"Default", "Concise"}
	a.outputStyle = "Concise"
	sink := &agenttest.Sink{}
	a.sink = agent.NewProviderServices(sink)
	newTimer := clock.Trap().NewTimer()
	stopTimer := clock.Trap().TimerStop()
	t.Cleanup(func() { newTimer.Close(); stopTimer.Close() })
	ctx := testutil.DeadlineContext(t)
	done := make(chan struct{})
	go func() { a.refreshOutputStyleCatalog(); close(done) }()
	delay := testutil.WaitForTimer(t, ctx, newTimer)
	assert.Equal(t, time.Minute, delay)
	testutil.AdvanceAndAwaitStop(t, ctx, clock, delay, stopTimer)
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("the style refresh did not end after its timeout")
	}
	assert.Equal(t, []string{"Default", "Concise"}, a.outputStyles)
	assert.Equal(t, "Concise", a.outputStyle)
	assert.Zero(t, sink.SettingsRefreshCount())
	a.pendingControlMu.Lock()
	assert.Empty(t, a.pendingControl)
	a.pendingControlMu.Unlock()
}

func TestCodebuddyOutputStyleRefreshIgnoresAReplyAfterStop(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	processDone := make(chan struct{})
	a := newOfflineAgentWithRuntime(t, sink, nil, processDone)
	t.Cleanup(func() {
		select {
		case <-processDone:
		default:
			close(processDone)
		}
		a.Stop()
	})
	requests := make(chan string, 1)
	peer := &codebuddyControlPeer{answer: func(id string) string { requests <- id; return "" }}
	peer.agent.Store(a)
	a.SetStdinForTest(peer)
	a.outputStyles = []string{"Default", "Concise"}
	a.outputStyle = "Concise"
	done := make(chan struct{})
	go func() { a.refreshOutputStyleCatalog(); close(done) }()
	ctx := testutil.DeadlineContext(t)
	var id string
	select {
	case id = <-requests:
	case <-ctx.Done():
		t.Fatal("the native catalog request did not arrive")
	}
	close(processDone)
	a.Stop()
	a.HandleOutput([]byte(codebuddyControlAnswer(`{"output_style":"Default","available_output_styles":["Default","Custom"]}`)(id)))
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("the style refresh did not end after the process stopped")
	}
	assert.Equal(t, []string{"Default", "Concise"}, a.outputStyles)
	assert.Equal(t, "Concise", a.outputStyle)
	assert.Zero(t, sink.SettingsRefreshCount())
	assert.Zero(t, sink.StatusActiveCount())
}

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
