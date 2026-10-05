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

// zcodeWideAndNarrowSnapshot is a live catalog of two models that share the levels low and high.
// The wide model also offers max. Both default to high. The session runs on the wide model at the
// level that the format verb states.
const zcodeWideAndNarrowSnapshot = `{
  "model": {
    "current": {"providerId":"account:p","modelId":"wide"},
    "available": [
      {"ref":{"providerId":"account:p","modelId":"wide"},"label":"Wide","providerLabel":"P",
       "reasoning":{"levels":[{"value":"low"},{"value":"high"},{"value":"max"}],"defaultLevel":"high"},
       "properties":{"inputFormat":{"supportsText":true}}},
      {"ref":{"providerId":"account:p","modelId":"narrow"},"label":"Narrow","providerLabel":"P",
       "reasoning":{"levels":[{"value":"low"},{"value":"high"}],"defaultLevel":"high"},
       "properties":{"inputFormat":{"supportsText":true}}}
    ]
  },
  "thoughtLevel": {"enabled": true, "current": %q, "available": [{"value":"low"},{"value":"high"},{"value":"max"}]}
}`

// zcodeAgentOnTheWideModel returns an agent whose session runs on the wide model at level.
func zcodeAgentOnTheWideModel(t *testing.T, level string) (*Agent, *zcodeRecordedStdin) {
	t.Helper()
	stdin := &zcodeRecordedStdin{}
	a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(&agenttest.ControlSink{}), stdin)
	a.Mu.Lock()
	a.accountProviderConfig = true
	a.Mu.Unlock()
	zcodeApplySettings(t, a, fmt.Sprintf(zcodeWideAndNarrowSnapshot, level))
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
	case <-time.After(5 * time.Second):
		t.Fatal("UpdateSettings did not return")
		return agent.SettingsApplyResult{}
	}
}

func requireNoZCodeThoughtLevelRequest(t *testing.T, stdin *zcodeRecordedStdin) {
	t.Helper()
	for _, req := range stdin.Requests(t) {
		assert.NotEqual(t, MethodSetThoughtLevel, req.Method, "the model request carries the level, so no second setter runs")
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
// that model at that level without a restart, although the session level is not offered by the
// new model.
func TestZCodeUpdateSettings_AModelAndLevelEditEndsOnTheRequestedPair(t *testing.T) {
	t.Parallel()

	a, stdin := zcodeAgentOnTheWideModel(t, "max")
	done := updateZCodeSettings(a, map[string]string{agent.OptionIDModel: "account:p/narrow", agent.OptionIDEffort: "low"})

	answerZCodeNarrowSetModel(t, a, stdin)
	setter := waitZCodeRequest(t, stdin, MethodSetThoughtLevel)
	var params struct {
		ThoughtLevel string `json:"thoughtLevel"`
	}
	require.NoError(t, json.Unmarshal(setter.Params, &params))
	assert.Equal(t, "low", params.ThoughtLevel)
	a.HandleOutput(zcodeReplyLine(t, zcodeSentRequestID(t, setter), json.RawMessage(
		`{"settings":{"thoughtLevel":{"enabled":true,"current":"low","available":[{"value":"low"},{"value":"high"}]}}}`)))

	result := receiveZCodeResult(t, done)
	require.True(t, result.AppliedLive, "no setter was refused, so no restart is needed")
	assert.Equal(t, "account:p/narrow", result.ConfirmedOptions()[agent.OptionIDModel])
	assert.Equal(t, "low", result.ConfirmedOptions()[agent.OptionIDEffort])
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
	case <-time.After(5 * time.Second):
		t.Fatal("applyStartupSettings did not return")
	}

	assert.Equal(t, "high", carried)
	a.Mu.Lock()
	defer a.Mu.Unlock()
	assert.Equal(t, "account:p/narrow", a.model, "the app-server accepted the launch model")
	assert.Equal(t, "high", a.thoughtLevel, "the session runs at the default level of the launch model")
}
