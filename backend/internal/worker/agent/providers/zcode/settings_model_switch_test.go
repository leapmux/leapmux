package zcode

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRefreshZCodeUsageFromSession_PublishesChangedSettings(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	stdin := &zcodeRecordedStdin{}
	a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(sink), stdin)
	answerZCodeRequest(t, a, stdin, MethodSessionRead, `{"settings":{"model":{"current":{"providerId":"p","modelId":"new"}}}}`)
	a.refreshZCodeUsageFromSession()
	require.Equal(t, 1, sink.SettingsRefreshCount())
	assert.Equal(t, "p/new", sink.LastSettingsRefresh().Model)
}

func TestRefreshZCodeUsageFromSession_PublishesNothingForUnchangedSettings(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	stdin := &zcodeRecordedStdin{}
	a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(sink), stdin)
	a.model = "p/current"
	answerZCodeRequest(t, a, stdin, MethodSessionRead, `{"settings":{"model":{"current":{"providerId":"p","modelId":"current"}}}}`)
	a.refreshZCodeUsageFromSession()
	assert.Zero(t, sink.SettingsRefreshCount())
	assert.Equal(t, "p/current", a.model)
}

// zcodeWideAndNarrowSnapshot is a live catalog of two models that share the levels low and high.
// The wide model also offers max. Both default to high. The session runs on the wide model at the
// level that the format verb states.
const zcodeWideAndNarrowSnapshot = `{
  "model": {
    "current": {"providerId":"account:p","modelId":"wide"},
    "available": [
      {"ref":{"providerId":"account:p","modelId":"wide"},"label":"Wide","providerLabel":"P",
       "contextWindow":200000,
       "reasoning":{"levels":[{"value":"low"},{"value":"high"},{"value":"max"}],"defaultLevel":"high"},
       "properties":{"inputFormat":{"supportsText":true}}},
      {"ref":{"providerId":"account:p","modelId":"narrow"},"label":"Narrow","providerLabel":"P",
       "contextWindow":128000,
       "reasoning":{"levels":[{"value":"low"},{"value":"high"}],"defaultLevel":"high"},
       "properties":{"inputFormat":{"supportsText":true}}}
    ]
  },
  "thoughtLevel": {"enabled": true, "current": %q, "available": [{"value":"low"},{"value":"high"},{"value":"max"}]}
}`

// zcodeAgentOnTheWideModel returns an agent whose session runs on the wide model at level.
func zcodeAgentOnTheWideModel(t *testing.T, level string) (*Agent, *zcodeRecordedStdin) {
	t.Helper()
	return zcodeAgentOnSnapshot(t, zcodeWideAndNarrowSnapshot, level)
}

// zcodeAgentOnSnapshot returns an agent whose session runs the live settings
// snapshot, with the level that the format verb states as the current one.
func zcodeAgentOnSnapshot(t *testing.T, snapshot, level string) (*Agent, *zcodeRecordedStdin) {
	t.Helper()
	stdin := &zcodeRecordedStdin{}
	a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(&agenttest.ControlSink{}), stdin)
	a.Mu.Lock()
	a.account = &zcodeAccountState{}
	a.Mu.Unlock()
	zcodeApplySettings(t, a, fmt.Sprintf(snapshot, level))
	a.Mu.Lock()
	defer a.Mu.Unlock()
	require.Equal(t, level, a.thoughtLevel)
	return a, stdin
}

// answerZCodeNarrowSetModel answers a session/setModel request the way ZCode 0.16.9 does. The
// app-server refuses a selection whose `options.reasoningLevel` the model does not list. Otherwise
// it reports the level of the selection as the current one. The function returns the level that
// the request carried.
func answerZCodeNarrowSetModel(t *testing.T, a *Agent, stdin *zcodeRecordedStdin) string {
	t.Helper()
	req := waitZCodeRequest(t, stdin, MethodSetModel)
	var params struct {
		Model struct {
			ModelID string `json:"modelId"`
			Options struct {
				ReasoningLevel string `json:"reasoningLevel"`
			} `json:"options"`
		} `json:"model"`
	}
	require.NoError(t, json.Unmarshal(req.Params, &params))
	require.Equal(t, "narrow", params.Model.ModelID)
	level := params.Model.Options.ReasoningLevel
	if level != "low" && level != "high" {
		a.HandleOutput(zcodeErrorReplyLine(t, zcodeSentRequestID(t, req), -32602,
			fmt.Sprintf("Reasoning effort %q is not supported by account:p/narrow", level)))
		return level
	}
	a.HandleOutput(zcodeReplyLine(t, zcodeSentRequestID(t, req), json.RawMessage(fmt.Sprintf(`{"settings":{
      "model":{"current":{"providerId":"account:p","modelId":"narrow"}},
      "thoughtLevel":{"enabled":true,"current":%q,"available":[{"value":"low"},{"value":"high"}]}}}`, level))))
	return level
}

// updateZCodeSettings runs UpdateSettings on its own goroutine, because the setters block until
// the test answers their requests.
func updateZCodeSettings(a *Agent, options map[string]string) <-chan agent.SettingsApplyResult {
	done := make(chan agent.SettingsApplyResult, 1)
	go func() { done <- a.UpdateSettings(options) }()
	return done
}

func receiveZCodeResult(t *testing.T, done <-chan agent.SettingsApplyResult) agent.SettingsApplyResult {
	t.Helper()
	select {
	case result := <-done:
		return result
	case <-time.After(30 * time.Second):
		t.Fatal("UpdateSettings did not return")
		return agent.SettingsApplyResult{}
	}
}

func requireNoZCodeThoughtLevelRequest(t *testing.T, stdin *zcodeRecordedStdin, msgAndArgs ...any) {
	t.Helper()
	for _, req := range stdin.Requests(t) {
		assert.NotEqual(t, MethodSetThoughtLevel, req.Method, msgAndArgs...)
	}
}

// A model switch must carry a thought level that the NEW model offers. The Worker sends the stored
// level with a model-only edit, because ZCode takes no part in the shared effort reset. ZCode
// 0.16.9 refuses a selection whose level the model does not list. A level kept from the old model
// made the live switch fail. The restart that followed sent the same level and failed again, so
// the session stayed on the old model.
func TestZCodeUpdateSettings_AModelSwitchCarriesTheDefaultLevelWhenTheNewModelDoesNotOfferTheCurrentOne(t *testing.T) {
	t.Parallel()

	a, stdin := zcodeAgentOnTheWideModel(t, "max")
	done := updateZCodeSettings(a, map[string]string{agent.OptionIDModel: "account:p/narrow", agent.OptionIDEffort: "max"})

	carried := answerZCodeNarrowSetModel(t, a, stdin)

	assert.Equal(t, "high", carried, "the narrow model does not offer max, so the switch carries the default level of that model")
	result := receiveZCodeResult(t, done)
	require.True(t, result.AppliedLive, "the live switch settles without a restart")
	assert.Equal(t, "account:p/narrow", result.ConfirmedOptions()[agent.OptionIDModel])
	assert.Equal(t, "high", result.ConfirmedOptions()[agent.OptionIDEffort])
	requireNoZCodeThoughtLevelRequest(t, stdin)
}

// A level that the new model offers survives the switch, and the model request carries it.
func TestZCodeUpdateSettings_AModelSwitchKeepsALevelTheNewModelOffers(t *testing.T) {
	t.Parallel()

	a, stdin := zcodeAgentOnTheWideModel(t, "low")
	done := updateZCodeSettings(a, map[string]string{agent.OptionIDModel: "account:p/narrow", agent.OptionIDEffort: "low"})

	carried := answerZCodeNarrowSetModel(t, a, stdin)

	assert.Equal(t, "low", carried)
	result := receiveZCodeResult(t, done)
	require.True(t, result.AppliedLive)
	assert.Equal(t, "account:p/narrow", result.ConfirmedOptions()[agent.OptionIDModel])
	assert.Equal(t, "low", result.ConfirmedOptions()[agent.OptionIDEffort])
	requireNoZCodeThoughtLevelRequest(t, stdin)
}

// An edit that states both a model and a level, as `agent set --model B --effort Y` does, ends on
// that model at that level without a restart. The model request carries the REQUESTED level, not
// the one the session runs, so the pair lands in one request and no second setter follows.
func TestZCodeUpdateSettings_AModelAndLevelEditEndsOnTheRequestedPair(t *testing.T) {
	t.Parallel()

	a, stdin := zcodeAgentOnTheWideModel(t, "max")
	done := updateZCodeSettings(a, map[string]string{agent.OptionIDModel: "account:p/narrow", agent.OptionIDEffort: "low"})

	carried := answerZCodeNarrowSetModel(t, a, stdin)

	assert.Equal(t, "low", carried, "the combined edit starts the ladder from the level it requests")
	result := receiveZCodeResult(t, done)
	require.True(t, result.AppliedLive, "no setter was refused, so no restart is needed")
	assert.Equal(t, "account:p/narrow", result.ConfirmedOptions()[agent.OptionIDModel])
	assert.Equal(t, "low", result.ConfirmedOptions()[agent.OptionIDEffort])
	requireNoZCodeThoughtLevelRequest(t, stdin)
}

// An edit whose level the model switch already settled on sends no second request.
func TestZCodeUpdateSettings_AModelSwitchThatSettlesTheRequestedLevelSendsNoSetter(t *testing.T) {
	t.Parallel()

	a, stdin := zcodeAgentOnTheWideModel(t, "max")
	done := updateZCodeSettings(a, map[string]string{agent.OptionIDModel: "account:p/narrow", agent.OptionIDEffort: "high"})

	answerZCodeNarrowSetModel(t, a, stdin)

	result := receiveZCodeResult(t, done)
	require.True(t, result.AppliedLive)
	assert.Equal(t, "high", result.ConfirmedOptions()[agent.OptionIDEffort])
	requireNoZCodeThoughtLevelRequest(t, stdin)
}

// A restart replays the stored pair as launch options. The level that the launch model does not
// offer must not block the model: the model request carries the default level, and the refused
// level setter leaves the session on the launch model.
func TestApplyStartupSettings_ALevelTheLaunchModelDoesNotOfferDoesNotBlockTheModel(t *testing.T) {
	t.Parallel()

	a, stdin := zcodeAgentOnTheWideModel(t, "max")
	done := make(chan struct{})
	go func() {
		defer close(done)
		a.applyStartupSettings(zcodeSettingsRequest{Model: "account:p/narrow", ThoughtLevel: "max"}, 5*time.Second)
	}()

	carried := answerZCodeNarrowSetModel(t, a, stdin)
	setter := waitZCodeRequest(t, stdin, MethodSetThoughtLevel)
	a.HandleOutput(zcodeErrorReplyLine(t, zcodeSentRequestID(t, setter), -32603, "Unsupported reasoning effort: max"))
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("applyStartupSettings did not return")
	}

	assert.Equal(t, "high", carried)
	a.Mu.Lock()
	defer a.Mu.Unlock()
	assert.Equal(t, "account:p/narrow", a.model, "the app-server accepted the launch model")
	assert.Equal(t, "high", a.thoughtLevel, "the session runs at the default level of the launch model")
}

// config.json lists only part of what a current app-server offers, such as the models
// that a Start Plan grants. A launch model that the catalog cannot resolve reaches the
// startup setter, which resolves it against the live list. The switch settles the
// level the launch asked for, so no second request carries it.
func TestApplyStartupSettings_ResolvesALaunchModelAgainstTheLiveList(t *testing.T) {
	t.Parallel()

	a, stdin := zcodeAgentOnTheWideModel(t, "high")
	done := make(chan struct{})
	go func() {
		defer close(done)
		a.applyStartupSettings(zcodeSettingsRequest{Model: a.catalog.launchModel("narrow")}, 5*time.Second)
	}()

	answerZCodeNarrowSetModel(t, a, stdin)
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("applyStartupSettings did not return")
	}
	a.Mu.Lock()
	defer a.Mu.Unlock()
	assert.Equal(t, "account:p/narrow", a.model)
}

// A launch model that the app-server does not offer leaves the session on the model the
// app-server chose, and the agent reports that model, not the refused request.
func TestApplyStartupSettings_KeepsTheObservedModelWhenTheAppServerOffersNoSuchModel(t *testing.T) {
	t.Parallel()

	a, stdin := zcodeAgentOnTheWideModel(t, "high")
	a.applyStartupSettings(zcodeSettingsRequest{Model: a.catalog.launchModel("GLM-9")}, 5*time.Second)

	for _, req := range stdin.Requests(t) {
		assert.NotEqual(t, MethodSetModel, req.Method, "a model that no list holds is never sent")
	}
	a.Mu.Lock()
	defer a.Mu.Unlock()
	assert.Equal(t, "account:p/wide", a.model)
}

func TestApplyStartupSettings_KeepsObservedSettingsAfterRejection(t *testing.T) {
	t.Parallel()
	a, stdin := zcodeAgentOnTheWideModel(t, "high")
	a.Mu.Lock()
	a.mode = "build"
	a.modeObserved = true
	a.planObserved = true
	a.Mu.Unlock()
	refuseZCodeRequest(t, a, stdin, MethodSetThoughtLevel, -32602, "invalid level")
	refuseZCodeRequest(t, a, stdin, MethodSetMode, -32602, "invalid mode")
	a.applyStartupSettings(zcodeSettingsRequest{Model: "missing", ThoughtLevel: "max", Mode: "yolo"}, 30*time.Second)
	a.Mu.Lock()
	defer a.Mu.Unlock()
	assert.Equal(t, "account:p/wide", a.model)
	assert.Equal(t, "high", a.thoughtLevel)
	assert.Equal(t, "build", a.permissionModeLocked())
	assert.True(t, a.modeObserved)
	assert.True(t, a.planObserved)
}

func TestApplyStartupSettings_KeepsModelObservationAfterRequestFailure(t *testing.T) {
	t.Parallel()
	a, stdin := zcodeAgentOnTheWideModel(t, "high")
	done := make(chan struct{})
	go func() {
		defer close(done)
		a.applyStartupSettings(zcodeSettingsRequest{Model: "account:p/narrow"}, 30*time.Second)
	}()
	req := waitZCodeRequest(t, stdin, MethodSetModel)
	a.handleZCodeStateUpdated(json.RawMessage(`{"scope":"session","patch":{"model":{"current":{"providerId":"account:p","modelId":"narrow"}}}}`))
	a.HandleOutput(zcodeErrorReplyLine(t, zcodeSentRequestID(t, req), -32603, "reply failed after model change"))
	answerZCodeRequest(t, a, stdin, MethodSetThoughtLevel, `{"settings":{"thoughtLevel":{"enabled":true,"current":"high"}}}`)
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("startup settings did not finish")
	}
	a.Mu.Lock()
	defer a.Mu.Unlock()
	assert.Equal(t, "account:p/narrow", a.model)
}

// --- the reasoning-level ladder ---

// zcodeSetModelAttempt is one session/setModel request as it sits on the wire.
type zcodeSetModelAttempt struct {
	ProviderID                 string
	ModelID                    string
	ReasoningLevel             string
	HasReasoningLevel          bool
	PersistAsWorkspaceLastUsed *bool
}

// zcodeSetModelAttempts decodes every session/setModel request the agent wrote.
func zcodeSetModelAttempts(t *testing.T, stdin *zcodeRecordedStdin) []zcodeSetModelAttempt {
	t.Helper()
	out := []zcodeSetModelAttempt{}
	for _, req := range stdin.Requests(t) {
		if req.Method != MethodSetModel {
			continue
		}
		var params struct {
			Model struct {
				zcodeModelRef
				Options map[string]json.RawMessage `json:"options"`
			} `json:"model"`
			PersistAsWorkspaceLastUsed *bool `json:"persistAsWorkspaceLastUsed"`
		}
		require.NoError(t, json.Unmarshal(req.Params, &params))
		attempt := zcodeSetModelAttempt{
			ProviderID:                 params.Model.ProviderID,
			ModelID:                    params.Model.ModelID,
			PersistAsWorkspaceLastUsed: params.PersistAsWorkspaceLastUsed,
		}
		if raw, ok := params.Model.Options["reasoningLevel"]; ok {
			require.NoError(t, json.Unmarshal(raw, &attempt.ReasoningLevel))
			attempt.HasReasoningLevel = true
		}
		out = append(out, attempt)
	}
	return out
}

// zcodeModelLevelFailure is the verified reasoning-level rejection: data.code
// "invalid_model_request" with one of the two messages the bundle sends.
func zcodeModelLevelFailure(id int64, message string) []byte {
	line, _ := json.Marshal(map[string]any{
		"id": id,
		"error": map[string]any{
			"code":    -32602,
			"message": message,
			"data":    map[string]any{"code": "invalid_model_request"},
		},
	})
	return line
}

// zcodeSetModelRejectLevel rejects a session/setModel request with the verified
// "not supported" class, naming the level the request carried.
func zcodeSetModelRejectLevel(providerModel string) func(zcodeSentRequest) []byte {
	return func(req zcodeSentRequest) []byte {
		return zcodeModelLevelFailure(zcodeRequestID(req),
			fmt.Sprintf("Reasoning effort %q is not supported by %s", zcodeRequestLevel(req), providerModel))
	}
}

// zcodeSetModelRejectMissingLevel rejects a session/setModel request with the
// verified "required" class.
func zcodeSetModelRejectMissingLevel(providerModel string) func(zcodeSentRequest) []byte {
	return func(req zcodeSentRequest) []byte {
		return zcodeModelLevelFailure(zcodeRequestID(req), "Reasoning level is required for "+providerModel)
	}
}

// zcodeSetModelRejectOther rejects a session/setModel request with an error the
// ladder must NOT retry.
func zcodeSetModelRejectOther(code int, message string) func(zcodeSentRequest) []byte {
	return func(req zcodeSentRequest) []byte {
		line, _ := json.Marshal(map[string]any{
			"id":    zcodeRequestID(req),
			"error": map[string]any{"code": code, "message": message},
		})
		return line
	}
}

// zcodeSetModelRejectUnverifiedMessage rejects with data.code
// "invalid_model_request" but a message that is not one of the two verified
// classes. The ladder must NOT retry it either.
func zcodeSetModelRejectUnverifiedMessage(message string) func(zcodeSentRequest) []byte {
	return func(req zcodeSentRequest) []byte {
		line, _ := json.Marshal(map[string]any{
			"id": zcodeRequestID(req),
			"error": map[string]any{
				"code":    -32602,
				"message": message,
				"data":    map[string]any{"code": "invalid_model_request"},
			},
		})
		return line
	}
}

// zcodeSetModelAccept settles a session/setModel request with result.
func zcodeSetModelAccept(result string) func(zcodeSentRequest) []byte {
	return func(req zcodeSentRequest) []byte {
		line, _ := json.Marshal(map[string]any{"id": zcodeRequestID(req), "result": json.RawMessage(result)})
		return line
	}
}

func zcodeRequestID(req zcodeSentRequest) int64 {
	var id int64
	_ = json.Unmarshal(req.ID, &id)
	return id
}

// zcodeRequestLevel is the options.reasoningLevel a session/setModel request
// carried, or empty when it omitted one.
func zcodeRequestLevel(req zcodeSentRequest) string {
	var params struct {
		Model struct {
			Options struct {
				ReasoningLevel string `json:"reasoningLevel"`
			} `json:"options"`
		} `json:"model"`
	}
	_ = json.Unmarshal(req.Params, &params)
	return params.Model.Options.ReasoningLevel
}

// runZCodeSetModelScript answers session/setModel requests in order: step i answers
// the (i+1)th request. The goroutine stops when every step answered, and fails
// nothing on its own -- a request beyond the script is left to the RPC's own
// timeout, and the test asserts the requests and the outcome.
func runZCodeSetModelScript(a *Agent, stdin *zcodeRecordedStdin, steps ...func(zcodeSentRequest) []byte) {
	go func() {
		for i, step := range steps {
			req, ok := zcodeAwaitNthRequest(stdin, MethodSetModel, i+1)
			if !ok {
				return
			}
			if line := step(req); line != nil {
				a.HandleOutput(line)
			}
		}
	}()
}

// zcodeAwaitNthRequest waits for the nth request of method (1-based). It fails
// nothing: only the test goroutine may report a failure.
func zcodeAwaitNthRequest(stdin *zcodeRecordedStdin, method string, n int) (zcodeSentRequest, bool) {
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		matched := 0
		for _, req := range stdin.RequestsFor(method) {
			matched++
			if matched == n {
				return req, true
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	return zcodeSentRequest{}, false
}

// A model switch must carry a thought level the new model runs, and a level the
// app-server refuses must not end the switch. ZCode 0.16.9 refuses a selection
// whose reasoningLevel the model does not list, with
// data.code "invalid_model_request". The ladder then tries no level at all, and
// that selection settles the switch.
func TestZCodeUpdateSettings_ARejectedLevelRecoversWithNoLevel(t *testing.T) {
	t.Parallel()

	a, stdin := zcodeAgentOnTheWideModel(t, "max")
	runZCodeSetModelScript(a, stdin,
		zcodeSetModelRejectLevel("account:p/narrow"),
		zcodeSetModelAccept(`{"settings":{
      "model":{"current":{"providerId":"account:p","modelId":"narrow"}},
      "thoughtLevel":{"enabled":true,"current":"high","available":[{"value":"low"},{"value":"high"}]}}}`),
	)
	done := updateZCodeSettings(a, map[string]string{agent.OptionIDModel: "account:p/narrow", agent.OptionIDEffort: "low"})
	result := receiveZCodeResult(t, done)

	attempts := zcodeSetModelAttempts(t, stdin)
	require.Len(t, attempts, 2, "the rejected level is followed by one request that carries no level")
	assert.Equal(t, "low", attempts[0].ReasoningLevel)
	assert.False(t, attempts[1].HasReasoningLevel, "the recovery candidate carries no options.reasoningLevel")
	for _, attempt := range attempts {
		assert.Equal(t, "account:p", attempt.ProviderID)
		assert.Equal(t, "narrow", attempt.ModelID)
		require.NotNil(t, attempt.PersistAsWorkspaceLastUsed)
		assert.False(t, *attempt.PersistAsWorkspaceLastUsed, "a model switch must not change the workspace default")
	}
	requireNoZCodeThoughtLevelRequest(t, stdin)

	assert.True(t, result.AppliedLive, "a recovered switch settles without a restart")
	assert.Equal(t, "account:p/narrow", result.ConfirmedOptions()[agent.OptionIDModel])
	assert.Equal(t, agent.OptionSettlementUnresolved, result.Settlements[agent.OptionIDEffort].State,
		"the recovered level differs from the request, so the request stays unresolved")
	a.Mu.Lock()
	defer a.Mu.Unlock()
	assert.Equal(t, "high", a.thoughtLevel, "the observed level of the successful snapshot stands")
}

// A combined model/effort edit whose level the app-server refuses must never
// resend that level. The ladder tries the selection the user asked for, then no
// level, then the remaining levels of the live model record, and the original
// request stays unresolved instead of restarting into the rejected pair.
func TestZCodeUpdateSettings_ACombinedEditRecoversAndNeverResendsTheRejectedLevel(t *testing.T) {
	t.Parallel()

	a, stdin := zcodeAgentOnTheWideModel(t, "max")
	runZCodeSetModelScript(a, stdin,
		zcodeSetModelRejectLevel("account:p/narrow"),
		zcodeSetModelRejectMissingLevel("account:p/narrow"),
		zcodeSetModelAccept(`{"settings":{
      "model":{"current":{"providerId":"account:p","modelId":"narrow"}},
      "thoughtLevel":{"enabled":true,"current":"high","available":[{"value":"low"},{"value":"high"}]}}}`),
	)
	done := updateZCodeSettings(a, map[string]string{agent.OptionIDModel: "account:p/narrow", agent.OptionIDEffort: "low"})
	result := receiveZCodeResult(t, done)

	attempts := zcodeSetModelAttempts(t, stdin)
	require.Len(t, attempts, 3, "the selected level, no level, then the remaining levels")
	assert.Equal(t, "low", attempts[0].ReasoningLevel)
	assert.False(t, attempts[1].HasReasoningLevel)
	assert.Equal(t, "high", attempts[2].ReasoningLevel)
	requireNoZCodeThoughtLevelRequest(t, stdin, "the rejected level is never resent")

	assert.True(t, result.AppliedLive, "the recovered switch returns the observed settings without a restart")
	assert.Equal(t, "account:p/narrow", result.ConfirmedOptions()[agent.OptionIDModel])
	assert.Equal(t, agent.OptionSettlementUnresolved, result.Settlements[agent.OptionIDEffort].State)
	a.Mu.Lock()
	defer a.Mu.Unlock()
	assert.Equal(t, "high", a.thoughtLevel)
}

// Only the two verified reasoning-level classes are worth another selection. A
// provider/model-not-found or an unrelated schema error stops the switch after
// one request, and the ordinary failure path decides what happens next: the
// separate effort setter still runs, because the ladder rejected nothing.
func TestZCodeUpdateSettings_ANonReasoningErrorStopsTheSwitchAfterOneRequest(t *testing.T) {
	t.Parallel()

	for name, step := range map[string]func(zcodeSentRequest) []byte{
		"provider/model-not-found":                    zcodeSetModelRejectOther(-32602, "provider/model-not-found"),
		"an unrelated schema error":                   zcodeSetModelRejectOther(-32600, "Invalid request parameters"),
		"the verified code with an unrelated message": zcodeSetModelRejectUnverifiedMessage("Unsupported reasoning effort: max"),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			a, stdin := zcodeAgentOnTheWideModel(t, "max")
			runZCodeSetModelScript(a, stdin, step)
			answerZCodeRequest(t, a, stdin, MethodSetThoughtLevel,
				`{"settings":{"thoughtLevel":{"enabled":true,"current":"low","available":[{"value":"low"},{"value":"high"}]}}}`)
			done := updateZCodeSettings(a, map[string]string{agent.OptionIDModel: "account:p/narrow", agent.OptionIDEffort: "low"})
			result := receiveZCodeResult(t, done)

			assert.Len(t, zcodeSetModelAttempts(t, stdin), 1, "an unrelated error is not worth a second selection")
			assert.False(t, result.AppliedLive, "an unrelated failure keeps the existing restart behavior")
		})
	}
}

// zcodeLevellessModelSnapshot is a live catalog whose one model explicitly
// declares no reasoning levels: it supplies no alternate levels to the ladder.
const zcodeLevellessModelSnapshot = `{
  "model": {
    "current": {"providerId":"account:p","modelId":"plain"},
    "available": [
      {"ref":{"providerId":"account:p","modelId":"plain"},"label":"Plain","providerLabel":"P",
       "properties":{"inputFormat":{"supportsText":true}}}
    ]
  },
  "thoughtLevel": {"enabled": true, "current": %q, "available": [{"value":"low"},{"value":"high"}]}
}`

// zcodeManyLevelsSnapshot is a live catalog of one model that offers more levels
// than the ladder may try.
const zcodeManyLevelsSnapshot = `{
  "model": {
    "current": {"providerId":"account:p","modelId":"huge"},
    "available": [
      {"ref":{"providerId":"account:p","modelId":"huge"},"label":"Huge","providerLabel":"P",
       "reasoning":{"levels":[{"value":"low"},{"value":"medium"},{"value":"high"},{"value":"max"},{"value":"mega"},{"value":"ultra"}],"defaultLevel":"high"},
       "properties":{"inputFormat":{"supportsText":true}}}
    ]
  },
  "thoughtLevel": {"enabled": true, "current": %q,
   "available": [{"value":"low"},{"value":"medium"},{"value":"high"},{"value":"max"},{"value":"mega"},{"value":"ultra"}]}
}`

// zcodeModelSwitchOutcome is what one applyZCodeModel call returns.
type zcodeModelSwitchOutcome struct {
	res zcodeModelApplyResult
	err error
}

// runZCodeModelSwitch calls applyZCodeModel on its own goroutine, because the
// setter blocks until the test answers its requests.
func runZCodeModelSwitch(a *Agent, modelID, level string) <-chan zcodeModelSwitchOutcome {
	done := make(chan zcodeModelSwitchOutcome, 1)
	go func() {
		res, err := a.applyZCodeModel(modelID, level, zcodeTestRPCTimeout)
		done <- zcodeModelSwitchOutcome{res: res, err: err}
	}()
	return done
}

func receiveZCodeModelSwitch(t *testing.T, done <-chan zcodeModelSwitchOutcome) zcodeModelSwitchOutcome {
	t.Helper()
	select {
	case outcome := <-done:
		return outcome
	case <-time.After(30 * time.Second):
		t.Fatal("applyZCodeModel did not return")
		return zcodeModelSwitchOutcome{}
	}
}

// The ladder recovers from a rejected level with the selection that carries no
// level, and reports the observed level of the successful snapshot together with
// the fact that a different candidate settled the switch.
func TestApplyZCodeModel_TheLadderRecoversWithTheOmittedLevel(t *testing.T) {
	t.Parallel()

	a, stdin := zcodeAgentOnTheWideModel(t, "max")
	runZCodeSetModelScript(a, stdin,
		zcodeSetModelRejectLevel("account:p/narrow"),
		zcodeSetModelAccept(`{"settings":{
      "model":{"current":{"providerId":"account:p","modelId":"narrow"}},
      "thoughtLevel":{"enabled":true,"current":"high","available":[{"value":"low"},{"value":"high"}]}}}`),
	)
	outcome := receiveZCodeModelSwitch(t, runZCodeModelSwitch(a, "account:p/narrow", "low"))

	require.NoError(t, outcome.err)
	assert.True(t, outcome.res.Recovered, "a later candidate settled the switch")
	assert.Equal(t, "high", outcome.res.ThoughtLevel, "the observed level of the snapshot stands")

	attempts := zcodeSetModelAttempts(t, stdin)
	require.Len(t, attempts, 2)
	assert.Equal(t, "low", attempts[0].ReasoningLevel)
	assert.False(t, attempts[1].HasReasoningLevel)
	for _, attempt := range attempts {
		require.NotNil(t, attempt.PersistAsWorkspaceLastUsed)
		assert.False(t, *attempt.PersistAsWorkspaceLastUsed)
	}
}

// A selection that carries no level can draw the "required" rejection. The ladder
// then moves on to the remaining levels of the live model record.
func TestApplyZCodeModel_TheLadderRecoversAfterARequiredLevelError(t *testing.T) {
	t.Parallel()

	a, stdin := zcodeAgentOnTheWideModel(t, "max")
	runZCodeSetModelScript(a, stdin,
		zcodeSetModelRejectLevel("account:p/narrow"),
		zcodeSetModelRejectMissingLevel("account:p/narrow"),
		zcodeSetModelAccept(`{"settings":{
      "model":{"current":{"providerId":"account:p","modelId":"narrow"}},
      "thoughtLevel":{"enabled":true,"current":"high","available":[{"value":"low"},{"value":"high"}]}}}`),
	)
	outcome := receiveZCodeModelSwitch(t, runZCodeModelSwitch(a, "account:p/narrow", "low"))

	require.NoError(t, outcome.err)
	assert.True(t, outcome.res.Recovered)
	assert.Equal(t, "high", outcome.res.ThoughtLevel)

	attempts := zcodeSetModelAttempts(t, stdin)
	require.Len(t, attempts, 3)
	assert.Equal(t, "low", attempts[0].ReasoningLevel)
	assert.False(t, attempts[1].HasReasoningLevel)
	assert.Equal(t, "high", attempts[2].ReasoningLevel, "the remaining levels of the live record follow")
}

// A live model that explicitly declares no levels supplies no alternate levels,
// so the ladder gives up after the selected level and the omission.
func TestApplyZCodeModel_ALevellessModelSuppliesNoAlternateLevels(t *testing.T) {
	t.Parallel()

	a, stdin := zcodeAgentOnSnapshot(t, zcodeLevellessModelSnapshot, "high")
	runZCodeSetModelScript(a, stdin,
		zcodeSetModelRejectLevel("account:p/plain"),
		zcodeSetModelRejectMissingLevel("account:p/plain"),
	)
	outcome := receiveZCodeModelSwitch(t, runZCodeModelSwitch(a, "account:p/plain", "low"))

	require.Error(t, outcome.err)
	assert.True(t, zcodeIsReasoningLevelRejection(outcome.err), "the ladder reports the rejection that exhausted it")
	assert.Empty(t, outcome.res.ThoughtLevel)
	attempts := zcodeSetModelAttempts(t, stdin)
	require.Len(t, attempts, 2, "a model with no levels offers no third selection")
	assert.Equal(t, "low", attempts[0].ReasoningLevel)
	assert.False(t, attempts[1].HasReasoningLevel)
}

// The ladder deduplicates its selections and stops at five attempts even when
// the model offers more levels than that.
func TestApplyZCodeModel_TheLadderDeduplicatesAndStopsAtFiveSelections(t *testing.T) {
	t.Parallel()

	a, stdin := zcodeAgentOnSnapshot(t, zcodeManyLevelsSnapshot, "high")
	runZCodeSetModelScript(a, stdin,
		zcodeSetModelRejectLevel("account:p/huge"),
		zcodeSetModelRejectMissingLevel("account:p/huge"),
		zcodeSetModelRejectLevel("account:p/huge"),
		zcodeSetModelRejectLevel("account:p/huge"),
		zcodeSetModelRejectLevel("account:p/huge"),
	)
	outcome := receiveZCodeModelSwitch(t, runZCodeModelSwitch(a, "account:p/huge", "low"))

	require.Error(t, outcome.err)
	attempts := zcodeSetModelAttempts(t, stdin)
	require.Len(t, attempts, 5, "the ladder tries at most five selections")
	assert.Equal(t, "low", attempts[0].ReasoningLevel, "the selected level goes first")
	assert.False(t, attempts[1].HasReasoningLevel, "the selection without a level goes second")
	seen := map[string]bool{"low": true}
	for _, attempt := range attempts[2:] {
		assert.True(t, attempt.HasReasoningLevel, "the remaining selections carry levels")
		assert.False(t, seen[attempt.ReasoningLevel], "the ladder never repeats a selection")
		seen[attempt.ReasoningLevel] = true
	}
}

// A retry is a request against the session the switch started with. Once a
// context clear replaced that session, the ladder must not send it.
func TestApplyZCodeModel_ARetryStopsWhenTheSessionIsReplaced(t *testing.T) {
	t.Parallel()

	a, stdin := zcodeAgentOnTheWideModel(t, "max")
	go func() {
		req, ok := zcodeAwaitNthRequest(stdin, MethodSetModel, 1)
		if !ok {
			return
		}
		a.Mu.Lock()
		a.sessionID = "sess-2"
		a.Mu.Unlock()
		a.HandleOutput(zcodeModelLevelFailure(zcodeRequestID(req),
			`Reasoning effort "low" is not supported by account:p/narrow`))
	}()
	outcome := receiveZCodeModelSwitch(t, runZCodeModelSwitch(a, "account:p/narrow", "low"))

	require.ErrorIs(t, outcome.err, errNoZCodeSession)
	assert.Len(t, zcodeSetModelAttempts(t, stdin), 1, "the replacement session receives no retry")
}
