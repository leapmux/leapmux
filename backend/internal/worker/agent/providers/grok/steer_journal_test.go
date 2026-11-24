package grok

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

func grokConsumptionRow(sessionID string, sequence, when int, text string) string {
	return fmt.Sprintf(`{"method":"session/update","params":{"sessionId":%q,"_meta":{"eventId":%q,"agentTimestampMs":%d},"update":{"sessionUpdate":"user_message_chunk","_meta":{"interjection":true},"content":{"type":"text","_meta":{"displayText":%q}}}}}`+"\n",
		sessionID, fmt.Sprintf("%s-%d", sessionID, sequence), when, text)
}

func TestGrokConsumptionReadsOnlyTheUnchangedCompleteSuffix(t *testing.T) {
	t.Parallel()
	const session = "native-session"
	old := grokConsumptionRow(session, 1, 10, "a prior interjection")
	newRow := grokConsumptionRow(session, 4, 20, "a new interjection")
	baseline := grokBaseline([]byte(old))
	rows, ok := readGrokConsumptions(baseline, []byte(old+newRow+newRow[:17]), grokJournalScope{sessionID: session})
	require.True(t, ok)
	assert.Equal(t, []grokConsumption{{payload: grokInterjectionPayload{text: "a new interjection"}, eventTime: 20, eventSeq: 4}}, rows)

	for name, data := range map[string]string{
		"rewritten prefix":   newRow,
		"old event repeated": old + newRow + newRow,
		"other session":      old + grokConsumptionRow("wrong", 5, 20, "a new interjection"),
		"reordered time":     old + newRow + grokConsumptionRow(session, 5, 19, "a new interjection"),
	} {
		t.Run(name, func(t *testing.T) {
			_, ok := readGrokConsumptions(baseline, []byte(data), grokJournalScope{sessionID: session})
			assert.False(t, ok)
		})
	}
}

func TestGrokSteerProofRequiresTheCompleteNativeBatch(t *testing.T) {
	t.Parallel()
	inputs := []grokSteerAdmission{
		{id: "first", payload: grokInterjectionPayload{text: "same input"}, echoed: true, afterStream: 10},
		{id: "second", payload: grokInterjectionPayload{text: "same input"}, echoed: true, afterStream: 10},
	}
	streams := []grokSteerStream{
		{start: 10, eventSeq: 2, keys: []string{"old-text"}},
		{start: 20, eventSeq: 10},
		{start: 30, eventSeq: 20, keys: []string{"new-reply"}},
	}
	rows := []grokConsumption{
		{payload: grokInterjectionPayload{text: "same input"}, eventTime: 19, eventSeq: 7},
		{payload: grokInterjectionPayload{text: "same input"}, eventTime: 19, eventSeq: 8},
	}
	proof, err := matchGrokConsumptions(inputs, streams, rows[:1])
	require.ErrorIs(t, err, errGrokConsumptionIncomplete)
	assert.Nil(t, proof)

	proof, err = matchGrokConsumptions(inputs, streams, rows)
	require.NoError(t, err)
	assert.Equal(t, map[string]int{"first": 1, "second": 1}, proof)
	reply := grokCanonicalSteerReply(
		grokSteerReply{sessionID: "session", turnID: "turn", inputs: inputs, streams: streams},
		proof, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_COMPLETED,
	)
	assert.Equal(t, []agent.NativeReplyBoundary{
		{InputID: "first", AfterOrdinal: 2}, {InputID: "second", AfterOrdinal: 2},
	}, reply.Inputs)
	assert.Equal(t, []agent.NativeReplyItem{
		{Ordinal: 2, MessageKey: "old-text"}, {Ordinal: 3, MessageKey: "new-reply"},
	}, reply.Items)

	rows[1].eventSeq = 11
	proof, err = matchGrokConsumptions(inputs, streams, rows)
	require.ErrorIs(t, err, errGrokConsumptionIncomplete)
	assert.Nil(t, proof, "a later journal record cannot complete an earlier cohort")
}

func TestGrokSteerProofRejectsDifferentInputAndReorderedStreams(t *testing.T) {
	t.Parallel()
	inputs := []grokSteerAdmission{{id: "accepted", payload: grokInterjectionPayload{text: "sent"}, echoed: true, afterStream: 10}}
	rows := []grokConsumption{{payload: grokInterjectionPayload{text: "interleaved"}, eventTime: 19, eventSeq: 5}}
	streams := []grokSteerStream{{start: 10, eventSeq: 2}, {start: 20, eventSeq: 10}}
	proof, err := matchGrokConsumptions(inputs, streams, rows)
	require.ErrorContains(t, err, "journal and steer batch differ")
	assert.Nil(t, proof)

	rows[0].payload.text = "sent"
	proof, err = matchGrokConsumptions(inputs, []grokSteerStream{streams[1], streams[0]}, rows)
	require.ErrorContains(t, err, "model boundaries are out of order")
	assert.Nil(t, proof)
}

func grokImageConsumptionRow(sessionID string, seq, when int, uri, mime, encoded string) string {
	return fmt.Sprintf(`{"method":"session/update","params":{"sessionId":%q,"_meta":{"eventId":%q,"agentTimestampMs":%d},"update":{"sessionUpdate":"user_message_chunk","_meta":{"interjection":true},"content":{"type":"image","uri":%q,"mimeType":%q,"data":%q}}}}`+"\n",
		sessionID, fmt.Sprintf("%s-%d", sessionID, seq), when, uri, mime, encoded)
}

func TestGrokSteerJournalKeepsAnImageOnlyEventTogether(t *testing.T) {
	t.Parallel()
	const session = "session-with-images"
	text := grokConsumptionRow(session, 2, 9, "")
	first := grokImageConsumptionRow(session, 2, 9, "first.png", "image/png", "AQI=")
	second := grokImageConsumptionRow(session, 2, 9, "second.jpeg", "image/jpeg", "AwQ=")
	input := grokSteerAdmission{
		id: "image-only", echoed: true,
		payload: grokInterjectionPayload{images: []grokInterjectionImage{
			grokImageFingerprint("image/png", "first.png", []byte{1, 2}),
			grokImageFingerprint("image/jpeg", "second.jpeg", []byte{3, 4}),
		}},
	}
	receiving := []grokSteerStream{{start: 10, eventSeq: 3}}
	baseline := grokBaseline(nil)
	rows, ok := readGrokConsumptions(baseline, []byte(text+first+second[:len(second)-1]), grokJournalScope{sessionID: session})
	require.True(t, ok)
	require.Len(t, rows, 1)
	_, err := matchGrokConsumptions([]grokSteerAdmission{input}, receiving, rows)
	require.ErrorIs(t, err, errGrokConsumptionIncomplete)

	rows, ok = readGrokConsumptions(baseline, []byte(text+first+second), grokJournalScope{sessionID: session})
	require.True(t, ok)
	require.Len(t, rows, 1)
	assert.Equal(t, input.payload, rows[0].payload)
	proof, err := matchGrokConsumptions([]grokSteerAdmission{input}, receiving, rows)
	require.NoError(t, err)
	assert.Equal(t, map[string]int{"image-only": 0}, proof)
}

func TestGrokSteerJournalRejectsImagesOutsideTheirEvent(t *testing.T) {
	t.Parallel()
	const session = "session-with-images"
	text := grokConsumptionRow(session, 2, 9, "caption")
	for name, data := range map[string]string{
		"orphan":        grokImageConsumptionRow(session, 2, 9, "chart.png", "image/png", "AQI="),
		"wrong event":   text + grokImageConsumptionRow(session, 3, 9, "chart.png", "image/png", "AQI="),
		"wrong time":    text + grokImageConsumptionRow(session, 2, 10, "chart.png", "image/png", "AQI="),
		"invalid bytes": text + grokImageConsumptionRow(session, 2, 9, "chart.png", "image/png", "@@"),
		"missing bytes": text + grokImageConsumptionRow(session, 2, 9, "chart.png", "image/png", ""),
	} {
		t.Run(name, func(t *testing.T) {
			_, ok := readGrokConsumptions(grokBaseline(nil), []byte(data), grokJournalScope{sessionID: session})
			assert.False(t, ok)
		})
	}
}

func TestGrokSteerProofMatchesImagesInOrderWithinAMixedBatch(t *testing.T) {
	t.Parallel()
	first := grokImageFingerprint("image/png", "first.png", []byte{1, 2})
	second := grokImageFingerprint("image/png", "second.png", []byte{3, 4})
	inputs := []grokSteerAdmission{
		{id: "with-images", echoed: true, payload: grokInterjectionPayload{text: "caption", images: []grokInterjectionImage{first, second}}},
		{id: "text-only", echoed: true, payload: grokInterjectionPayload{text: "caption"}},
	}
	receiving := []grokSteerStream{{start: 10, eventSeq: 4}}
	rows := []grokConsumption{
		{payload: inputs[0].payload, eventTime: 8, eventSeq: 2},
		{payload: inputs[1].payload, eventTime: 8, eventSeq: 3},
	}
	proof, err := matchGrokConsumptions(inputs, receiving, rows)
	require.NoError(t, err)
	assert.Equal(t, map[string]int{"with-images": 0, "text-only": 0}, proof)

	for name, images := range map[string][]grokInterjectionImage{
		"skipped image": {first},
		"swapped":       {second, first},
		"different digest": {
			grokImageFingerprint("image/png", "first.png", []byte{9, 2}), second,
		},
		"different mime": {
			grokImageFingerprint("image/webp", "first.png", []byte{1, 2}), second,
		},
		"different name": {
			grokImageFingerprint("image/png", "other.png", []byte{1, 2}), second,
		},
	} {
		t.Run(name, func(t *testing.T) {
			rows[0].payload.images = images
			proof, err := matchGrokConsumptions(inputs, receiving, rows)
			require.Error(t, err)
			assert.NotErrorIs(t, err, errGrokConsumptionIncomplete)
			assert.Nil(t, proof)
		})
	}
}

func grokOrdinaryUserJournalRow(sessionID string, sequence, when, promptIndex int, content string) string {
	return fmt.Sprintf(`{"method":"session/update","params":{"sessionId":%q,"_meta":{"eventId":%q,"agentTimestampMs":%d},"update":{"sessionUpdate":"user_message_chunk","_meta":{"promptIndex":%d},"content":%s}}}`+"\n",
		sessionID, fmt.Sprintf("%s-%d", sessionID, sequence), when, promptIndex, content)
}

func grokEndJournalRow(sessionID string, sequence int, promptID string) string {
	return fmt.Sprintf(`{"method":"_x.ai/session/update","params":{"sessionId":%q,"_meta":{"eventId":%q},"update":{"sessionUpdate":"turn_completed","prompt_id":%q}}}`+"\n",
		sessionID, fmt.Sprintf("%s-%d", sessionID, sequence), promptID)
}

func grokChildMessageJournalRow(sessionID string, sequence, when int, text string) string {
	return fmt.Sprintf(`{"method":"session/update","params":{"sessionId":%q,"_meta":{"eventId":%q,"agentTimestampMs":%d},"update":{"sessionUpdate":"user_message_chunk","_meta":{"modelId":"grok"},"content":{"type":"text","text":%q}}}}`+"\n",
		sessionID, fmt.Sprintf("%s-%d", sessionID, sequence), when, text)
}

func TestGrokChildSteerJournalProvesOnlyTheReceivingPromptBatch(t *testing.T) {
	t.Parallel()
	const session = "child-session"
	const prompt = "child-prompt"
	const text = "same steer"
	old := grokChildMessageJournalRow(session, 1, 8, text)
	initial := grokOrdinaryUserJournalRow(session, 2, 10, 0, `{"type":"text","text":"initial prompt"}`)
	first := grokChildMessageJournalRow(session, 5, 19, text)
	end := grokEndJournalRow(session, 9, prompt)
	later := grokChildMessageJournalRow(session, 10, 29, text)
	baseline := grokBaseline([]byte(old))
	scope := grokJournalScope{sessionID: session, promptID: prompt, childMessages: true}
	inputs := []grokSteerAdmission{
		{id: "first-receipt", payload: grokInterjectionPayload{text: text}, echoed: true},
		{id: "second-receipt", payload: grokInterjectionPayload{text: text}, echoed: true},
	}
	streams := []grokSteerStream{{start: 20, eventSeq: 6}}

	rows, ok := readGrokConsumptions(baseline, []byte(old+initial+first+first[:len(first)-1]), scope)
	require.True(t, ok)
	_, err := matchGrokConsumptions(inputs, streams, rows)
	require.ErrorIs(t, err, errGrokConsumptionIncomplete)

	uncorrelatedEnd := grokEndJournalRow(session, 6, "")
	otherPromptEnd := grokEndJournalRow(session, 7, "other-prompt")
	rows, ok = readGrokConsumptions(baseline, []byte(old+initial+first+uncorrelatedEnd+otherPromptEnd+first+end+later), scope)
	require.True(t, ok)
	require.Len(t, rows, 2)
	assert.Equal(t, rows[0], rows[1])
	proof, err := matchGrokConsumptions(inputs, streams, rows)
	require.NoError(t, err)
	assert.Equal(t, map[string]int{"first-receipt": 0, "second-receipt": 0}, proof)

	for name, second := range map[string]string{
		"different session":        grokChildMessageJournalRow("other-child", 5, 19, text),
		"different event time":     grokChildMessageJournalRow(session, 5, 20, text),
		"invalid event sequence":   grokChildMessageJournalRow(session, 0, 19, text),
		"reordered event sequence": grokChildMessageJournalRow(session, 4, 19, text),
		"absent text":              grokChildMessageJournalRow(session, 5, 19, ""),
	} {
		t.Run(name, func(t *testing.T) {
			_, ok := readGrokConsumptions(baseline, []byte(old+initial+first+second+end), scope)
			assert.False(t, ok)
		})
	}
}

func TestGrokSuccessorProofRequiresThePredecessorEndAndAllInputParts(t *testing.T) {
	t.Parallel()
	const session = "native-session"
	const old = "predecessor"
	const next = "successor"
	baseline := grokBaseline(nil)
	oldInput := grokOrdinaryUserJournalRow(session, 2, 10, 0, `{"type":"text","text":"the preceding prompt"}`)
	end := grokEndJournalRow(session, 4, old)
	text := grokOrdinaryUserJournalRow(session, 5, 19, 1, `{"type":"text","text":""}`)
	image := grokOrdinaryUserJournalRow(session, 6, 19, 1, `{"type":"image","uri":"chart.png","mimeType":"image/png","data":"AQI="}`)
	after := grokOrdinaryUserJournalRow(session, 13, 31, 2, `{"type":"text","text":"unrelated later input"}`)
	want := grokInterjectionPayload{images: []grokInterjectionImage{grokImageFingerprint("image/png", "chart.png", []byte{1, 2})}}
	inputs := []grokSteerAdmission{{id: "late-steer", echoed: true, payload: want}}
	streams := []grokSteerStream{{start: 20, eventSeq: 7}}

	rows, ok := readGrokConsumptions(baseline, []byte(oldInput+text+image), grokJournalScope{sessionID: session, predecessorID: old, promptID: next})
	require.True(t, ok)
	assert.Empty(t, rows, "an absent predecessor end leaves the successor input unproved")

	rows, ok = readGrokConsumptions(baseline, []byte(oldInput+end+text), grokJournalScope{sessionID: session, predecessorID: old, promptID: next})
	require.True(t, ok)
	_, err := matchGrokConsumptions(inputs, streams, rows)
	require.ErrorIs(t, err, errGrokConsumptionIncomplete)

	rows, ok = readGrokConsumptions(baseline, []byte(oldInput+end+text+image+grokEndJournalRow(session, 12, next)+after), grokJournalScope{sessionID: session, predecessorID: old, promptID: next})
	require.True(t, ok)
	require.Len(t, rows, 1)
	assert.Equal(t, grokConsumption{payload: want, eventTime: 19, eventSeq: 6}, rows[0])
	proof, err := matchGrokConsumptions(inputs, streams, rows)
	require.NoError(t, err)
	assert.Equal(t, map[string]int{"late-steer": 0}, proof)

	_, err = matchGrokConsumptions(inputs, []grokSteerStream{{start: 20, eventSeq: 6}}, rows)
	require.ErrorIs(t, err, errGrokConsumptionIncomplete, "all parts must precede the receiving stream")
}
