package service

import (
	"encoding/json"
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type reductionTestProvider struct {
	agent.ProviderDefaults
}

func (provider reductionTestProvider) Classify(raw json.RawMessage) agent.NotificationClassification {
	var value struct {
		Group *string `json:"group"`
		Kind  string  `json:"kind"`
	}
	_ = json.Unmarshal(raw, &value)
	if value.Group != nil {
		return agent.ProviderScopedNotification(*value.Group)
	}
	switch value.Kind {
	case "status":
		return agent.StatusNotification()
	case "retry":
		return agent.APIRetryNotification()
	case "boundary":
		return agent.CompactionBoundaryNotification()
	default:
		return agent.NotificationClassification{}
	}
}

func reduceNotificationPrefixes(t *testing.T, inputs []json.RawMessage, provider agent.Provider) ([]json.RawMessage, agent.NotificationReductionState) {
	t.Helper()
	var messages []json.RawMessage
	var state agent.NotificationReductionState
	for index, incoming := range inputs {
		var err error
		messages, state, err = reduceNotificationThread(messages, state, incoming, provider)
		require.NoError(t, err, "prefix %d", index+1)
		assert.Equal(t, consolidateNotificationThread(inputs[:index+1], provider), messages, "prefix %d", index+1)
		encoded, err := agent.WithNotificationReduction(nil, state, len(messages))
		require.NoError(t, err)
		state, err = agent.DecodeNotificationReduction(encoded, len(messages))
		require.NoError(t, err)
	}
	return messages, state
}

func TestNotificationReductionMatchesBatchConsolidation(t *testing.T) {
	t.Parallel()
	for _, provider := range []leapmuxv1.AgentProvider{leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE, leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX} {
		t.Run(provider.String(), func(t *testing.T) {
			inputs := []json.RawMessage{
				raw(t, settingsChanged("A", "B")), raw(t, codexStartupStatus("one", "starting", nil)),
				[]byte(`{"type":"system","subtype":"api_retry","attempt":1}`), []byte(`{"type":"context_cleared"}`),
				raw(t, settingsChanged("B", "A")), []byte(`{"type":"plan_updated","path":"first"}`),
				raw(t, codexStartupStatus("two", "ready", nil)), []byte(`{"type":"interrupted"}`),
				[]byte(`{"type":"rate_limit","rate_limit_info":{"rateLimitType":"five_hour"}}`),
				[]byte(`{"type":"rate_limit","rate_limit_info":{"rateLimitType":"seven_day"}}`),
				[]byte(`{"type":"plan_updated","path":"second"}`), raw(t, settingsChanged("unexpected-old", "C")),
				raw(t, codexStartupStatus("one", "ready", nil)), []byte(`{"type":"stop_ignored"}`),
				[]byte(`{"type":"plan_execution"}`), []byte(`{"type":"goal_updated","transition":"set"}`),
			}
			reduceNotificationPrefixes(t, inputs, testRegistry.Plugin(provider))
		})
	}
}

func TestNotificationReductionRetainsCanceledSettingsBaseline(t *testing.T) {
	t.Parallel()
	inputs := []json.RawMessage{
		raw(t, settingsChanged("A", "B")), raw(t, settingsChanged("B", "A")),
		[]byte(`{"type":"settings_changed","changes":{"model":{"old":"different","new":"C"},"effort":{"old":"","new":"high"}}}`),
	}
	messages, state := reduceNotificationPrefixes(t, inputs, nil)
	require.Len(t, messages, 1)
	assert.JSONEq(t, `{"type":"settings_changed","changes":{"model":{"old":"A","new":"C"},"effort":{"old":"","new":"high"}}}`, string(messages[0]))
	assert.Empty(t, state.CancelledSettings)
}

func TestNotificationReductionPreservesVisibleSettingsFieldsAtEveryPrefix(t *testing.T) {
	t.Parallel()
	inputs := []json.RawMessage{
		[]byte(`{"type":"settings_changed","contextCleared":true,"extra":{"n":1.00e+0},"changes":{"model":{"old":"A","new":"B","old_label":"Alpha","new_label":"Beta","label":"Model","extra":null},"effort":{"old":"","new":"high","old_label":"","new_label":"High","label":"Effort"}}}`),
		[]byte(`{"type":"settings_changed","extra":{"n":9007199254740993},"changes":{"model":{"old":"B","new":"C","old_label":"Beta","new_label":"Gamma","label":"Current model","extra":{"kept":true}}}}`),
		[]byte(`{"type":"settings_changed","changes":{"model":{"old":"C","new":"A","old_label":"Gamma","new_label":"Alpha","label":"Model"}}}`),
		[]byte(`{"type":"settings_changed","changes":{"model":{"old":"different","new":"D","old_label":"Different","new_label":"Delta","label":"Model","extra":false}}}`),
	}
	want := []string{
		`{"type":"settings_changed","extra":{"n":1.00e+0},"changes":{"model":{"old":"A","new":"B","old_label":"Alpha","new_label":"Beta","label":"Model","extra":null},"effort":{"old":"","new":"high","old_label":"","new_label":"High","label":"Effort"}}}`,
		`{"type":"settings_changed","extra":{"n":9007199254740993},"changes":{"model":{"old":"A","new":"C","old_label":"Alpha","new_label":"Gamma","label":"Current model","extra":{"kept":true}},"effort":{"old":"","new":"high","old_label":"","new_label":"High","label":"Effort"}}}`,
		`{"type":"settings_changed","changes":{"effort":{"old":"","new":"high","old_label":"","new_label":"High","label":"Effort"}}}`,
		`{"type":"settings_changed","changes":{"model":{"old":"A","new":"D","new_label":"Delta","label":"Model","extra":false},"effort":{"old":"","new":"high","old_label":"","new_label":"High","label":"Effort"}}}`,
	}
	before := make([]json.RawMessage, len(inputs))
	for index, input := range inputs {
		before[index] = append(json.RawMessage(nil), input...)
	}
	var messages []json.RawMessage
	var state agent.NotificationReductionState
	for index, input := range inputs {
		var err error
		messages, state, err = reduceNotificationThread(messages, state, input, nil)
		require.NoError(t, err)
		require.Len(t, messages, 1)
		assert.JSONEq(t, want[index], string(messages[0]), "prefix %d", index+1)
		batch := consolidateNotificationThread(inputs[:index+1], nil)
		require.Len(t, batch, 1)
		assert.JSONEq(t, want[index], string(batch[0]), "independent batch prefix %d", index+1)
		encoded, err := agent.WithNotificationReduction(nil, state, len(messages))
		require.NoError(t, err)
		state, err = agent.DecodeNotificationReduction(encoded, len(messages))
		require.NoError(t, err)
		assert.NotContains(t, state.CancelledSettings, "old_label")
	}
	assert.Equal(t, before, inputs)
}

func TestNotificationReductionDoesNotRestoreCanceledSettingsOnNonSettingsAppend(t *testing.T) {
	t.Parallel()
	inputs := []json.RawMessage{raw(t, settingsChanged("", "B")), raw(t, settingsChanged("B", "")), []byte(`{"type":"system","text":"later"}`)}
	messages, state := reduceNotificationPrefixes(t, inputs, nil)
	require.Len(t, messages, 1)
	assert.JSONEq(t, string(inputs[2]), string(messages[0]))
	assert.Equal(t, map[string]string{"model": ""}, state.CancelledSettings)
}

func TestNotificationReductionRebuildsProviderIndicesAfterRemoval(t *testing.T) {
	t.Parallel()
	inputs := []json.RawMessage{
		[]byte(`{"kind":"status"}`), []byte(`{"group":"a","value":1}`), []byte(`{"group":"b","value":2}`),
		[]byte(`{"kind":"boundary"}`), []byte(`{"type":"context_cleared"}`), []byte(`{"group":"a","value":3}`),
	}
	messages, state := reduceNotificationPrefixes(t, inputs, reductionTestProvider{})
	require.Len(t, messages, 3)
	assert.Equal(t, map[string]int{"b": 1, "a": 3}, state.ProviderSlots)
	assert.JSONEq(t, string(inputs[2]), string(messages[0]))
	assert.JSONEq(t, string(inputs[5]), string(messages[2]))
}

func TestNotificationReductionPreservesCompactionAndContextOrdering(t *testing.T) {
	t.Parallel()
	for _, inputs := range [][]json.RawMessage{
		{[]byte(`{"kind":"status"}`), []byte(`{"kind":"boundary"}`), []byte(`{"type":"context_cleared"}`)},
		{[]byte(`{"type":"context_cleared"}`), []byte(`{"kind":"retry"}`), []byte(`{"kind":"boundary"}`)},
		{[]byte(`{"kind":"boundary"}`), []byte(`{"kind":"status"}`), []byte(`{"kind":"retry"}`), []byte(`{"kind":"status","value":2}`)},
	} {
		reduceNotificationPrefixes(t, inputs, reductionTestProvider{})
	}
}

func TestNotificationReductionKeepsTheLatestProviderScopedEntry(t *testing.T) {
	t.Parallel()
	inputs := []json.RawMessage{
		[]byte(`{"group":"one","value":1}`),
		[]byte(`{"group":"two","value":2}`),
		[]byte(`{"group":"one","value":3}`),
	}
	messages, state := reduceNotificationPrefixes(t, inputs, reductionTestProvider{})
	assert.Equal(t, inputs[1:], messages)
	assert.Equal(t, map[string]int{"two": 1, "one": 2}, state.ProviderSlots)
}

func TestNotificationReductionProviderScopedNoticeSurvivesACompaction(t *testing.T) {
	t.Parallel()
	inputs := []json.RawMessage{
		[]byte(`{"group":"notice","text":"keep"}`),
		[]byte(`{"kind":"status","text":"running"}`),
		[]byte(`{"kind":"boundary"}`),
	}
	messages, state := reduceNotificationPrefixes(t, inputs, reductionTestProvider{})
	assert.Equal(t, []json.RawMessage{inputs[0], inputs[2]}, messages)
	assert.Equal(t, map[string]int{"notice": 1}, state.ProviderSlots)
}

func TestNotificationReductionKeepsUnclassifiedEmptyProviderKeys(t *testing.T) {
	t.Parallel()
	inputs := []json.RawMessage{[]byte(`{"group":"","value":1}`), []byte(`{"group":"","value":2}`)}
	messages, state := reduceNotificationPrefixes(t, inputs, reductionTestProvider{})
	assert.Equal(t, inputs, messages)
	assert.Empty(t, state.ProviderSlots)
}

func TestNotificationReductionApiRetryResolvedReplacesTheRetry(t *testing.T) {
	t.Parallel()
	inputs := []json.RawMessage{[]byte(`{"kind":"retry","attempt":2}`), []byte(`{"type":"api_retry_resolved"}`)}
	messages, _ := reduceNotificationPrefixes(t, inputs, reductionTestProvider{})
	assert.Equal(t, inputs[1:], messages)
}

func TestNotificationReductionApiRetryEnvelopeFolds(t *testing.T) {
	t.Parallel()
	inputs := []json.RawMessage{[]byte(`{"type":"api_retry","count":1}`), []byte(`{"type":"api_retry","count":3}`)}
	messages, _ := reduceNotificationPrefixes(t, inputs, reductionTestProvider{})
	assert.Equal(t, inputs[1:], messages)
}

func TestNotificationReductionClaudeNotificationsOfOneKeyFold(t *testing.T) {
	t.Parallel()
	plugin := testRegistry.Plugin(leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE)
	inputs := []json.RawMessage{
		[]byte(`{"type":"system","subtype":"notification","key":"stop-hook-error","message":"first error"}`),
		[]byte(`{"type":"system","subtype":"notification","key":"another-key","message":"keep this"}`),
		[]byte(`{"type":"system","subtype":"notification","key":"stop-hook-error","message":"latest error"}`),
	}
	messages, _ := reduceNotificationPrefixes(t, inputs, plugin)
	assert.Equal(t, inputs[1:], messages)
}

func TestNotificationReductionCompactionEndedEndsTheStatus(t *testing.T) {
	t.Parallel()
	inputs := []json.RawMessage{[]byte(`{"kind":"status"}`), []byte(`{"type":"compaction_ended","outcome":"failed"}`)}
	messages, _ := reduceNotificationPrefixes(t, inputs, reductionTestProvider{})
	assert.Equal(t, inputs[1:], messages)
}

func TestNotificationReductionKeepsEveryGoalTransition(t *testing.T) {
	t.Parallel()
	inputs := []json.RawMessage{[]byte(`{"type":"goal_updated","transition":"set"}`), []byte(`{"type":"goal_updated","transition":"pause"}`), []byte(`{"type":"goal_updated","transition":"resume"}`), []byte(`{"type":"goal_updated","transition":"restart"}`), []byte(`{"type":"goal_cleared"}`)}
	messages, _ := reduceNotificationPrefixes(t, inputs, nil)
	assert.Equal(t, inputs, messages)
}

func TestNotificationReductionRejectsCorruptState(t *testing.T) {
	t.Parallel()
	messages := []json.RawMessage{raw(t, settingsChanged("A", "B"))}
	state := agent.NotificationReductionState{CancelledSettings: map[string]string{"model": "A"}}
	result, _, err := reduceNotificationThread(messages, state, []byte(`{"type":"system"}`), nil)
	require.ErrorContains(t, err, "both visible and canceled")
	assert.Nil(t, result)
	assert.Equal(t, []json.RawMessage{raw(t, settingsChanged("A", "B"))}, messages)
	assert.Equal(t, map[string]string{"model": "A"}, state.CancelledSettings)
}

func TestClineStatusNoticeSurvivesACompaction(t *testing.T) {
	t.Parallel()
	status := json.RawMessage(`{"event":"session.notice","payload":{"message":"The context exceeded the window.","metadata":{"kind":"context_overflow_recovery","phase":"started"}}}`)
	ended := json.RawMessage(`{"event":"session.notice","payload":{"message":"The context is compacted.","metadata":{"kind":"auto_compaction","phase":"completed"}}}`)
	rows, _ := reduceNotificationPrefixes(t, []json.RawMessage{status, ended}, testRegistry.Plugin(leapmuxv1.AgentProvider_AGENT_PROVIDER_CLINE))
	require.Len(t, rows, 2)
	assert.JSONEq(t, string(status), string(rows[0]))
	assert.JSONEq(t, string(ended), string(rows[1]))
}
