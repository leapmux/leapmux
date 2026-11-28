package opencode

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/quartz"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const openCodeCatalogModelsFixture = `[
 {"id":"model","providerID":"provider","name":"Reasoning model","enabled":true,"capabilities":{"input":["text","image"]},"variants":[{"id":"low"},{"id":"high"},{"id":"auto"},{"id":"default"},{"id":"variant:custom"}],"limit":{"context":128000}},
 {"id":"model/special","providerID":"other","name":"Audio model","enabled":true,"capabilities":{"input":["text","audio"]},"variants":[],"limit":{"context":64000}},
 {"id":"disabled","providerID":"provider","name":"Disabled model","enabled":false,"variants":[]}
]`

const openCodeCatalogAgentsFixture = `[
 {"id":"build","name":"Build","mode":"primary"},
 {"id":"plan","name":"Plan","mode":"primary"},
 {"id":"custom","name":"Custom agent","mode":"all"},
 {"id":"general","name":"General","mode":"subagent"},
 {"id":"hidden","name":"Hidden","mode":"primary","hidden":true}
]`

const openCodePluginPendingFixture = `{"_tag":"ServiceUnavailableError","message":"Location plugins did not finish activating","service":"plugin"}`

func openCodeCatalogFixture(t *testing.T) openCodeCatalog {
	t.Helper()
	var catalog openCodeCatalog
	require.NoError(t, json.Unmarshal([]byte(openCodeCatalogModelsFixture), &catalog.models))
	require.NoError(t, json.Unmarshal([]byte(openCodeCatalogAgentsFixture), &catalog.agents))
	catalog.defaultModel = openCodeModelRef{ProviderID: "provider", ID: "model"}
	catalog.defaultAgent = "build"
	return catalog
}

func TestOpenCodeCatalogProjectsNativeModelsAndPrimaryAgents(t *testing.T) {
	t.Parallel()
	catalog := openCodeCatalogFixture(t)
	require.NoError(t, catalog.validate())
	models := catalog.modelOptions()
	require.Len(t, models, 2)
	assert.Equal(t, "provider/model", models[0].Id)
	assert.True(t, models[0].IsDefault)
	assert.EqualValues(t, 128000, models[0].ContextWindow)
	assert.False(t, models[0].SupportsAudioInput)
	assert.True(t, models[1].SupportsAudioInput)
	assert.Empty(t, models[1].SupportedEfforts)
	efforts := map[string]string{}
	for _, effort := range models[0].SupportedEfforts {
		efforts[effort.Id] = effort.Name
	}
	assert.Equal(t, map[string]string{
		"auto": "Model default", "variant:low": "Low", "variant:high": "High",
		"variant:auto": "Auto", "variant:variant:custom": "Variant:custom",
	}, efforts)
	primary := catalog.primaryAgents()
	require.Len(t, primary, 3)
	assert.Equal(t, "build", primary[0].Id)
	assert.True(t, primary[0].Default)
	assert.True(t, catalog.offersAgent("custom"))
	assert.False(t, catalog.offersAgent("general"))
	assert.False(t, catalog.offersAgent("hidden"))
	ref, found := catalog.modelRef("other/model/special")
	require.True(t, found)
	assert.Equal(t, openCodeModelRef{ProviderID: "other", ID: "model/special"}, ref)
	_, found = catalog.modelRef("provider/disabled")
	assert.False(t, found)
	models[0].SupportedEfforts[0].Name = "changed outside the catalog"
	assert.Equal(t, "Model default", catalog.modelOptions()[0].SupportedEfforts[0].Name)
}

func TestOpenCodeVariantOptionsHaveNoDefaultCollision(t *testing.T) {
	t.Parallel()
	for _, native := range []string{"low", "auto", "default", "variant:low", ":", "思考"} {
		option := openCodeVariantOption(native)
		assert.NotEqual(t, agent.EffortAuto, option)
		decoded, valid := openCodeVariantFromOption(option)
		require.True(t, valid)
		assert.Equal(t, native, decoded)
	}
	assert.Equal(t, agent.EffortAuto, openCodeVariantOption(""))
	decoded, valid := openCodeVariantFromOption(agent.EffortAuto)
	assert.True(t, valid)
	assert.Empty(t, decoded)
	for _, option := range []string{"", "variant:", "low", "prefix:low"} {
		_, valid := openCodeVariantFromOption(option)
		assert.False(t, valid, "%q", option)
	}
}

func TestOpenCodeCatalogRejectsAmbiguousOrUnusableEntries(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, reason string
		change       func(*openCodeCatalog)
	}{
		{"missing model ID", "without an ID", func(c *openCodeCatalog) { c.models[0].ID = "" }},
		{"duplicate model", "repeats model", func(c *openCodeCatalog) { c.models = append(c.models, c.models[0]) }},
		{"missing variant", "variant without an ID", func(c *openCodeCatalog) { c.models[0].Variants[0].ID = "" }},
		{"duplicate variant", "repeats variant", func(c *openCodeCatalog) { c.models[0].Variants[0].ID = c.models[0].Variants[1].ID }},
		{"disabled default", "default model", func(c *openCodeCatalog) { c.models[0].Enabled = false }},
		{"absent default", "default model", func(c *openCodeCatalog) { c.defaultModel.ID = "missing" }},
		{"missing agent ID", "agent without an ID", func(c *openCodeCatalog) { c.agents[0].ID = "" }},
		{"duplicate agent", "repeats agent", func(c *openCodeCatalog) { c.agents = append(c.agents, c.agents[0]) }},
		{"no primary agent", "no visible primary", func(c *openCodeCatalog) { c.agents = nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			catalog := openCodeCatalogFixture(t)
			tc.change(&catalog)
			require.ErrorContains(t, catalog.validate(), tc.reason)
		})
	}
}

func TestOpenCodeCatalogSettlesOnlyVariantsOfTheSelectedModel(t *testing.T) {
	t.Parallel()
	catalog := openCodeCatalogFixture(t)
	for _, native := range []string{"", "low", "auto"} {
		ref := openCodeModelRef{ProviderID: "provider", ID: "model", Variant: native}
		actual, err := catalog.settleVariant(ref)
		require.NoError(t, err)
		assert.Equal(t, ref, actual)
	}
	ref, err := catalog.settleVariant(openCodeModelRef{ProviderID: "other", ID: "model/special", Variant: "high"})
	require.NoError(t, err)
	assert.Empty(t, ref.Variant)
	_, err = catalog.settleVariant(openCodeModelRef{ProviderID: "provider", ID: "missing"})
	require.ErrorContains(t, err, "absent or disabled")
}

func TestOpenCodeCatalogWaitsForActivationBeforeConcurrentCatalogReads(t *testing.T) {
	t.Parallel()
	ctx := testutil.DeadlineContext(t)
	entered := make(chan string, 3)
	release := make(chan struct{})
	var activated atomic.Bool
	rpc := openCodeTestRPC(t, func(w http.ResponseWriter, r *http.Request) {
		entered <- r.URL.Path
		if r.URL.Path == "/api/model/default" {
			activated.Store(true)
			_, _ = fmt.Fprint(w, `{"data":{"id":"model","providerID":"provider"}}`)
			return
		}
		assert.True(t, activated.Load(), "the default-model request must activate plugins before the agent list is read")
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		switch r.URL.Path {
		case "/api/model":
			_, _ = fmt.Fprintf(w, `{"data":%s}`, openCodeCatalogModelsFixture)
		case "/api/agent":
			_, _ = fmt.Fprintf(w, `{"data":%s}`, openCodeCatalogAgentsFixture)
		default:
			t.Errorf("unexpected catalog route: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	})
	finished := make(chan error, 1)
	go func() { _, err := loadOpenCodeCatalog(ctx, rpc); finished <- err }()
	select {
	case first := <-entered:
		assert.Equal(t, "/api/model/default", first)
	case <-ctx.Done():
		t.Fatal("the default-model request did not start")
	}
	for range 2 {
		select {
		case <-entered:
		case <-ctx.Done():
			t.Fatal("the catalog requests did not overlap")
		}
	}
	close(release)
	require.NoError(t, <-finished)
}

func TestOpenCodeCatalogWaitsWhenPluginActivationIsStillPending(t *testing.T) {
	t.Parallel()
	var defaultReads atomic.Int32
	rpc := openCodeTestRPC(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/model/default":
			if defaultReads.Add(1) == 1 {
				w.WriteHeader(http.StatusServiceUnavailable)
				_, _ = fmt.Fprint(w, openCodePluginPendingFixture)
				return
			}
			_, _ = fmt.Fprint(w, `{"data":{"id":"model","providerID":"provider"}}`)
		case "/api/model":
			assert.EqualValues(t, 2, defaultReads.Load())
			_, _ = fmt.Fprintf(w, `{"data":%s}`, openCodeCatalogModelsFixture)
		case "/api/agent":
			assert.EqualValues(t, 2, defaultReads.Load())
			_, _ = fmt.Fprintf(w, `{"data":%s}`, openCodeCatalogAgentsFixture)
		default:
			t.Errorf("unexpected catalog route: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	})
	ctx := testutil.DeadlineContext(t)
	clock := testutil.NewQuartzMock(t)
	trap := clock.Trap().NewTimer(openCodeCatalogActivationPollTag)
	t.Cleanup(trap.Close)
	type readResult struct {
		catalog openCodeCatalog
		err     error
	}
	finished := make(chan readResult, 1)
	go func() {
		catalog, err := awaitOpenCodeCatalog(ctx, rpc, clock, time.Minute)
		finished <- readResult{catalog, err}
	}()
	delay := testutil.WaitForTimer(t, ctx, trap)
	assert.EqualValues(t, 1, defaultReads.Load())
	clock.Advance(delay).MustWait(ctx)
	select {
	case result := <-finished:
		require.NoError(t, result.err)
		assert.Equal(t, openCodeModelRef{ProviderID: "provider", ID: "model"}, result.catalog.defaultModel)
		assert.Equal(t, "build", result.catalog.defaultAgent)
	case <-ctx.Done():
		t.Fatal("the catalog read did not finish after activation")
	}
	assert.EqualValues(t, 2, defaultReads.Load())
}

func TestOpenCodeCatalogWaitsWhenDefaultModelDataIsNull(t *testing.T) {
	t.Parallel()
	var defaultReads atomic.Int32
	rpc := openCodeTestRPC(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/model/default":
			if defaultReads.Add(1) == 1 {
				_, _ = fmt.Fprint(w, `{"data":null}`)
				return
			}
			_, _ = fmt.Fprint(w, `{"data":{"id":"model","providerID":"provider"}}`)
		case "/api/model":
			_, _ = fmt.Fprintf(w, `{"data":%s}`, openCodeCatalogModelsFixture)
		case "/api/agent":
			_, _ = fmt.Fprintf(w, `{"data":%s}`, openCodeCatalogAgentsFixture)
		default:
			t.Errorf("unexpected catalog route: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	})
	ctx := testutil.DeadlineContext(t)
	clock := testutil.NewQuartzMock(t)
	trap := clock.Trap().NewTimer(openCodeCatalogActivationPollTag)
	t.Cleanup(trap.Close)
	type readResult struct {
		catalog openCodeCatalog
		err     error
	}
	finished := make(chan readResult, 1)
	go func() {
		catalog, err := awaitOpenCodeCatalog(ctx, rpc, clock, time.Minute)
		finished <- readResult{catalog, err}
	}()
	delay := testutil.WaitForTimer(t, ctx, trap)
	assert.EqualValues(t, 1, defaultReads.Load())
	clock.Advance(delay).MustWait(ctx)
	select {
	case result := <-finished:
		require.NoError(t, result.err)
		assert.Equal(t, openCodeModelRef{ProviderID: "provider", ID: "model"}, result.catalog.defaultModel)
	case <-ctx.Done():
		t.Fatal("the catalog read did not finish after default model became available")
	}
	assert.EqualValues(t, 2, defaultReads.Load())
}

func TestOpenCodeCatalogWaitsForRequestedCustomModelDuringWarmup(t *testing.T) {
	t.Parallel()

	t.Run("polls past transient built-in catalog until requested custom model appears", func(t *testing.T) {
		t.Parallel()
		ctx := testutil.DeadlineContext(t)
		clock := testutil.NewQuartzMock(t)
		start := clock.Now()
		const resolveAfter = 120 * time.Millisecond
		var modelReads atomic.Int32
		rpc := openCodeTestRPC(t, func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/api/model/default":
				if clock.Now().Sub(start) < resolveAfter {
					_, _ = fmt.Fprint(w, `{"data":{"id":"model","providerID":"provider"}}`)
					return
				}
				_, _ = fmt.Fprint(w, `{"data":{"id":"custom-model","providerID":"custom"}}`)
			case "/api/model":
				modelReads.Add(1)
				if clock.Now().Sub(start) < resolveAfter {
					_, _ = fmt.Fprintf(w, `{"data":%s}`, openCodeCatalogModelsFixture)
					return
				}
				_, _ = fmt.Fprint(w, `{"data":[{"id":"custom-model","providerID":"custom","name":"Custom","enabled":true,"variants":[]}]}`)
			case "/api/agent":
				_, _ = fmt.Fprintf(w, `{"data":%s}`, openCodeCatalogAgentsFixture)
			default:
				t.Errorf("unexpected catalog route: %s", r.URL.Path)
				w.WriteHeader(http.StatusNotFound)
			}
		})
		trap := clock.Trap().NewTimer(openCodeCatalogActivationPollTag)
		t.Cleanup(trap.Close)

		done := make(chan struct{})
		var (
			gotCatalog openCodeCatalog
			gotErr     error
		)
		go func() {
			defer close(done)
			gotCatalog, gotErr = awaitOpenCodeCatalogWithModel(ctx, rpc, clock, time.Minute, "custom/custom-model")
		}()

		driveOpenCodeMockTimers(t, ctx, clock, trap, done)
		<-done
		require.NoError(t, gotErr)
		require.NotNil(t, gotCatalog.findModel("custom/custom-model"))
		assert.Equal(t, openCodeModelRef{ProviderID: "custom", ID: "custom-model"}, gotCatalog.defaultModel)
		assert.GreaterOrEqual(t, modelReads.Load(), int32(2))
		assert.Less(t, clock.Now().Sub(start), openCodeRequestedModelWarmupBudget)
	})

	t.Run("returns catalog after 3s budget when requested model never appears", func(t *testing.T) {
		t.Parallel()
		ctx := testutil.DeadlineContext(t)
		clock := testutil.NewQuartzMock(t)
		start := clock.Now()
		var modelReads atomic.Int32
		rpc := openCodeTestRPC(t, func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/api/model/default":
				_, _ = fmt.Fprint(w, `{"data":{"id":"model","providerID":"provider"}}`)
			case "/api/model":
				modelReads.Add(1)
				_, _ = fmt.Fprintf(w, `{"data":%s}`, openCodeCatalogModelsFixture)
			case "/api/agent":
				_, _ = fmt.Fprintf(w, `{"data":%s}`, openCodeCatalogAgentsFixture)
			default:
				t.Errorf("unexpected catalog route: %s", r.URL.Path)
				w.WriteHeader(http.StatusNotFound)
			}
		})
		trap := clock.Trap().NewTimer(openCodeCatalogActivationPollTag)
		t.Cleanup(trap.Close)

		done := make(chan struct{})
		var (
			gotCatalog openCodeCatalog
			gotErr     error
		)
		go func() {
			defer close(done)
			gotCatalog, gotErr = awaitOpenCodeCatalogWithModel(ctx, rpc, clock, time.Minute, "custom/nonexistent")
		}()

		driveOpenCodeMockTimers(t, ctx, clock, trap, done)
		<-done
		require.NoError(t, gotErr)
		assert.Nil(t, gotCatalog.findModel("custom/nonexistent"))
		assert.Equal(t, openCodeRequestedModelWarmupBudget, clock.Now().Sub(start))
		assert.Greater(t, modelReads.Load(), int32(1))
	})
}

func TestOpenCodeCatalogUsesCappedExponentialBackoff(t *testing.T) {
	t.Parallel()
	const pendingAttempts = 7
	var defaultReads atomic.Int32
	rpc := openCodeTestRPC(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/model/default":
			if defaultReads.Add(1) <= pendingAttempts {
				_, _ = fmt.Fprint(w, `{"data":null}`)
				return
			}
			_, _ = fmt.Fprint(w, `{"data":{"id":"model","providerID":"provider"}}`)
		case "/api/model":
			_, _ = fmt.Fprintf(w, `{"data":%s}`, openCodeCatalogModelsFixture)
		case "/api/agent":
			_, _ = fmt.Fprintf(w, `{"data":%s}`, openCodeCatalogAgentsFixture)
		default:
			t.Errorf("unexpected catalog route: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	})
	ctx := testutil.DeadlineContext(t)
	clock := testutil.NewQuartzMock(t)
	trap := clock.Trap().NewTimer(openCodeCatalogActivationPollTag)
	t.Cleanup(trap.Close)
	type readResult struct {
		catalog openCodeCatalog
		err     error
	}
	finished := make(chan readResult, 1)
	go func() {
		catalog, err := awaitOpenCodeCatalog(ctx, rpc, clock, time.Minute)
		finished <- readResult{catalog, err}
	}()
	var delays []time.Duration
	for range pendingAttempts {
		delay := testutil.WaitForTimer(t, ctx, trap)
		delays = append(delays, delay)
		clock.Advance(delay).MustWait(ctx)
	}
	select {
	case result := <-finished:
		require.NoError(t, result.err)
		assert.Equal(t, openCodeModelRef{ProviderID: "provider", ID: "model"}, result.catalog.defaultModel)
	case <-ctx.Done():
		t.Fatal("the catalog read did not finish after backoff retries")
	}
	assert.Equal(t, []time.Duration{
		50 * time.Millisecond,
		100 * time.Millisecond,
		200 * time.Millisecond,
		400 * time.Millisecond,
		800 * time.Millisecond,
		time.Second,
		time.Second,
	}, delays)
}

func driveOpenCodeMockTimers(t *testing.T, ctx context.Context, clock *quartz.Mock, trap *quartz.Trap, done <-chan struct{}) {
	t.Helper()
	waitCtx, cancelWait := context.WithCancel(ctx)
	defer cancelWait()
	go func() {
		select {
		case <-done:
			cancelWait()
		case <-ctx.Done():
			cancelWait()
		}
	}()
	for {
		call, err := trap.Wait(waitCtx)
		if err != nil {
			if ctx.Err() != nil {
				t.Fatalf("timed out driving mock timers: %v", ctx.Err())
			}
			return
		}
		call.MustRelease(ctx)
		clock.Advance(call.Duration).MustWait(ctx)
	}
}

func TestOpenCodeCatalogDefaultModelWarmupBudget(t *testing.T) {
	t.Parallel()

	t.Run("fails fast after 15s when /api/model/default stays null", func(t *testing.T) {
		t.Parallel()
		var defaultReads atomic.Int32
		rpc := openCodeTestRPC(t, func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, "/api/model/default", r.URL.Path)
			defaultReads.Add(1)
			_, _ = fmt.Fprint(w, `{"data":null}`)
		})
		ctx := testutil.DeadlineContext(t)
		clock := testutil.NewQuartzMock(t)
		start := clock.Now()
		trap := clock.Trap().NewTimer(openCodeCatalogActivationPollTag)
		t.Cleanup(trap.Close)

		done := make(chan struct{})
		var gotErr error
		go func() {
			defer close(done)
			_, gotErr = awaitOpenCodeCatalog(ctx, rpc, clock, 5*time.Minute)
		}()

		driveOpenCodeMockTimers(t, ctx, clock, trap, done)
		<-done
		require.ErrorIs(t, gotErr, errOpenCodeNoDefaultModel)
		assert.Equal(t, openCodeDefaultModelWarmupBudget, clock.Now().Sub(start))
		assert.Greater(t, defaultReads.Load(), int32(1))
	})

	t.Run("HTTP 503 plugin activation continues retrying past 15s until resolved", func(t *testing.T) {
		t.Parallel()
		ctx := testutil.DeadlineContext(t)
		clock := testutil.NewQuartzMock(t)
		start := clock.Now()
		const resolveAfter = 25 * time.Second
		var defaultReads atomic.Int32
		rpc := openCodeTestRPC(t, func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/api/model/default":
				defaultReads.Add(1)
				if clock.Now().Sub(start) < resolveAfter {
					w.WriteHeader(http.StatusServiceUnavailable)
					_, _ = fmt.Fprint(w, openCodePluginPendingFixture)
					return
				}
				_, _ = fmt.Fprint(w, `{"data":{"id":"model","providerID":"provider"}}`)
			case "/api/model":
				_, _ = fmt.Fprintf(w, `{"data":%s}`, openCodeCatalogModelsFixture)
			case "/api/agent":
				_, _ = fmt.Fprintf(w, `{"data":%s}`, openCodeCatalogAgentsFixture)
			default:
				t.Errorf("unexpected catalog route: %s", r.URL.Path)
				w.WriteHeader(http.StatusNotFound)
			}
		})
		trap := clock.Trap().NewTimer(openCodeCatalogActivationPollTag)
		t.Cleanup(trap.Close)

		done := make(chan struct{})
		var (
			gotCatalog openCodeCatalog
			gotErr     error
		)
		go func() {
			defer close(done)
			gotCatalog, gotErr = awaitOpenCodeCatalog(ctx, rpc, clock, 5*time.Minute)
		}()

		driveOpenCodeMockTimers(t, ctx, clock, trap, done)
		<-done
		require.NoError(t, gotErr)
		assert.Equal(t, openCodeModelRef{ProviderID: "provider", ID: "model"}, gotCatalog.defaultModel)
		assert.GreaterOrEqual(t, clock.Now().Sub(start), resolveAfter)
		assert.Less(t, clock.Now().Sub(start), 5*time.Minute)
	})

	t.Run("data null that resolves within 15s succeeds", func(t *testing.T) {
		t.Parallel()
		ctx := testutil.DeadlineContext(t)
		clock := testutil.NewQuartzMock(t)
		start := clock.Now()
		const resolveAfter = 10 * time.Second
		rpc := openCodeTestRPC(t, func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/api/model/default":
				if clock.Now().Sub(start) < resolveAfter {
					_, _ = fmt.Fprint(w, `{"data":null}`)
					return
				}
				_, _ = fmt.Fprint(w, `{"data":{"id":"model","providerID":"provider"}}`)
			case "/api/model":
				_, _ = fmt.Fprintf(w, `{"data":%s}`, openCodeCatalogModelsFixture)
			case "/api/agent":
				_, _ = fmt.Fprintf(w, `{"data":%s}`, openCodeCatalogAgentsFixture)
			default:
				t.Errorf("unexpected catalog route: %s", r.URL.Path)
				w.WriteHeader(http.StatusNotFound)
			}
		})
		trap := clock.Trap().NewTimer(openCodeCatalogActivationPollTag)
		t.Cleanup(trap.Close)

		done := make(chan struct{})
		var (
			gotCatalog openCodeCatalog
			gotErr     error
		)
		go func() {
			defer close(done)
			gotCatalog, gotErr = awaitOpenCodeCatalog(ctx, rpc, clock, 5*time.Minute)
		}()

		driveOpenCodeMockTimers(t, ctx, clock, trap, done)
		<-done
		require.NoError(t, gotErr)
		assert.Equal(t, openCodeModelRef{ProviderID: "provider", ID: "model"}, gotCatalog.defaultModel)
		assert.GreaterOrEqual(t, clock.Now().Sub(start), resolveAfter)
		assert.Less(t, clock.Now().Sub(start), openCodeDefaultModelWarmupBudget)
	})

	t.Run("default-model 15s budget starts on first null after HTTP 503 plugin activation", func(t *testing.T) {
		t.Parallel()
		ctx := testutil.DeadlineContext(t)
		clock := testutil.NewQuartzMock(t)
		start := clock.Now()
		rpc := openCodeTestRPC(t, func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/api/model/default":
				elapsed := clock.Now().Sub(start)
				if elapsed < 20*time.Second {
					w.WriteHeader(http.StatusServiceUnavailable)
					_, _ = fmt.Fprint(w, openCodePluginPendingFixture)
					return
				}
				if elapsed < 25*time.Second {
					_, _ = fmt.Fprint(w, `{"data":null}`)
					return
				}
				_, _ = fmt.Fprint(w, `{"data":{"id":"model","providerID":"provider"}}`)
			case "/api/model":
				_, _ = fmt.Fprintf(w, `{"data":%s}`, openCodeCatalogModelsFixture)
			case "/api/agent":
				_, _ = fmt.Fprintf(w, `{"data":%s}`, openCodeCatalogAgentsFixture)
			default:
				t.Errorf("unexpected catalog route: %s", r.URL.Path)
				w.WriteHeader(http.StatusNotFound)
			}
		})
		trap := clock.Trap().NewTimer(openCodeCatalogActivationPollTag)
		t.Cleanup(trap.Close)

		done := make(chan struct{})
		var (
			gotCatalog openCodeCatalog
			gotErr     error
		)
		go func() {
			defer close(done)
			gotCatalog, gotErr = awaitOpenCodeCatalog(ctx, rpc, clock, 5*time.Minute)
		}()

		driveOpenCodeMockTimers(t, ctx, clock, trap, done)
		<-done
		require.NoError(t, gotErr)
		assert.Equal(t, openCodeModelRef{ProviderID: "provider", ID: "model"}, gotCatalog.defaultModel)
	})
}

func TestOpenCodeCatalogFailsImmediatelyWhenModelOrAgentDataIsNull(t *testing.T) {
	t.Parallel()
	for _, nullPath := range []string{"/api/model", "/api/agent"} {
		t.Run(nullPath, func(t *testing.T) {
			t.Parallel()
			var defaultReads, targetReads atomic.Int32
			rpc := openCodeTestRPC(t, func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/model/default":
					defaultReads.Add(1)
					_, _ = fmt.Fprint(w, `{"data":{"id":"model","providerID":"provider"}}`)
				case "/api/model":
					if nullPath == "/api/model" {
						targetReads.Add(1)
						_, _ = fmt.Fprint(w, `{"data":null}`)
						return
					}
					_, _ = fmt.Fprintf(w, `{"data":%s}`, openCodeCatalogModelsFixture)
				case "/api/agent":
					if nullPath == "/api/agent" {
						targetReads.Add(1)
						_, _ = fmt.Fprint(w, `{"data":null}`)
						return
					}
					_, _ = fmt.Fprintf(w, `{"data":%s}`, openCodeCatalogAgentsFixture)
				default:
					t.Errorf("unexpected catalog route: %s", r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
				}
			})
			// Passing an un-advanced mock clock proves no activation poll timer is waited on.
			_, err := awaitOpenCodeCatalog(testutil.DeadlineContext(t), rpc, testutil.NewQuartzMock(t), time.Minute)
			require.ErrorIs(t, err, errOpenCodeNoData)
			assert.NotErrorIs(t, err, errOpenCodeDefaultModelPending)
			assert.EqualValues(t, 1, defaultReads.Load())
			assert.EqualValues(t, 1, targetReads.Load())
		})
	}
}

func TestOpenCodeCatalogStopsWaitingWhenStartupIsCanceled(t *testing.T) {
	t.Parallel()
	var reads atomic.Int32
	rpc := openCodeTestRPC(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/model/default", r.URL.Path)
		reads.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = fmt.Fprint(w, openCodePluginPendingFixture)
	})
	ctx := testutil.DeadlineContext(t)
	startup, cancel := context.WithCancel(ctx)
	defer cancel()
	clock := testutil.NewQuartzMock(t)
	trap := clock.Trap().NewTimer(openCodeCatalogActivationPollTag)
	t.Cleanup(trap.Close)
	finished := make(chan error, 1)
	go func() {
		_, err := awaitOpenCodeCatalog(startup, rpc, clock, time.Minute)
		finished <- err
	}()
	testutil.WaitForTimer(t, ctx, trap)
	cancel()
	select {
	case err := <-finished:
		require.ErrorIs(t, err, context.Canceled)
		require.ErrorContains(t, err, "Location plugins did not finish activating")
	case <-ctx.Done():
		t.Fatal("canceling startup did not stop the catalog wait")
	}
	assert.EqualValues(t, 1, reads.Load())
}

func TestOpenCodeCatalogDoesNotWaitOnUnrelatedAvailabilityErrors(t *testing.T) {
	t.Parallel()
	for _, body := range []string{
		`{"_tag":"ServiceUnavailableError","service":"database","message":"Database unavailable"}`,
		`{"_tag":"ServiceUnavailableError","service":"plugin","message":"Plugin loading is disabled"}`,
		"service unavailable",
	} {
		t.Run(body, func(t *testing.T) {
			t.Parallel()
			var reads atomic.Int32
			rpc := openCodeTestRPC(t, func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, "/api/model/default", r.URL.Path)
				reads.Add(1)
				w.WriteHeader(http.StatusServiceUnavailable)
				_, _ = fmt.Fprint(w, body)
			})
			_, err := awaitOpenCodeCatalog(testutil.DeadlineContext(t), rpc, testutil.NewQuartzMock(t), time.Minute)
			var status *providerkit.HTTPStatusError
			require.ErrorAs(t, err, &status)
			assert.Equal(t, http.StatusServiceUnavailable, status.StatusCode)
			assert.Equal(t, body, status.Body)
			assert.EqualValues(t, 1, reads.Load())
		})
	}
}

func TestOpenCodeCatalogResolvesOnlyTheNativeDefaultOrder(t *testing.T) {
	t.Parallel()
	catalog := openCodeCatalogFixture(t)
	catalog.defaultAgent = "plan"
	agentModel := openCodeModelRef{ProviderID: "other", ID: "model/special"}
	catalog.agents[1].Model = &agentModel
	actual, err := catalog.effectiveModel(nil, "")
	require.NoError(t, err)
	assert.Equal(t, agentModel, actual)
	explicit := openCodeModelRef{ProviderID: "provider", ID: "model", Variant: "high"}
	actual, err = catalog.effectiveModel(&explicit, "plan")
	require.NoError(t, err)
	assert.Equal(t, explicit, actual)
	actual, err = catalog.effectiveModel(nil, "build")
	require.NoError(t, err)
	assert.Equal(t, catalog.defaultModel, actual)
	missing := openCodeModelRef{ProviderID: "provider", ID: "missing"}
	_, err = catalog.effectiveModel(&missing, "plan")
	require.Error(t, err)
	catalog.agents[1].Model = &missing
	actual, err = catalog.effectiveModel(nil, "plan")
	require.NoError(t, err)
	assert.Equal(t, catalog.defaultModel, actual)
	actual, err = catalog.settleVariant(openCodeModelRef{ProviderID: "provider", ID: "model", Variant: "default"})
	require.NoError(t, err)
	assert.Empty(t, actual.Variant)
}

func TestOpenCodeCatalogUsesTheNativeResolvedDefaultAgent(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		configured, resolved string
		withoutBuild         bool
	}{
		{"missing", "build", false}, {"hidden", "build", false}, {"general", "build", false}, {"", "build", false},
		{"plan", "plan", false}, {"missing", "custom", true}, {"custom", "custom", false},
	} {
		t.Run(fmt.Sprintf("%s/default=%s", tc.configured, tc.resolved), func(t *testing.T) {
			t.Parallel()
			fixture := openCodeCatalogFixture(t)
			effective := openCodeModelRef{ProviderID: "other", ID: "model/special"}
			ordered := []openCodeAgentInfo{}
			for _, entry := range fixture.agents {
				if entry.ID == tc.resolved {
					entry.Model = &effective
					ordered = append(ordered, entry)
				}
			}
			for _, entry := range fixture.agents {
				if entry.ID == tc.resolved || tc.withoutBuild && (entry.ID == "build" || entry.ID == "plan") {
					continue
				}
				if entry.Hidden || entry.Mode == "subagent" {
					model := fixture.defaultModel
					entry.Model = &model
				}
				ordered = append(ordered, entry)
			}
			agents, err := json.Marshal(ordered)
			require.NoError(t, err)
			rpc := openCodeTestRPC(t, func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/model":
					_, _ = fmt.Fprintf(w, `{"data":%s}`, openCodeCatalogModelsFixture)
				case "/api/model/default":
					_, _ = fmt.Fprint(w, `{"data":{"id":"model","providerID":"provider"}}`)
				case "/api/agent":
					_, _ = fmt.Fprintf(w, `{"data":%s}`, agents)
				case "/api/config":
					_, _ = fmt.Fprintf(w, `[{"type":"document","info":{"default_agent":%q}}]`, tc.configured)
				default:
					t.Errorf("unexpected route %s", r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
				}
			})
			catalog, err := loadOpenCodeCatalog(t.Context(), rpc)
			require.NoError(t, err)
			assert.Equal(t, tc.resolved, catalog.defaultAgent)
			model, err := catalog.effectiveModel(nil, "")
			require.NoError(t, err)
			assert.Equal(t, effective, model)
		})
	}
}
