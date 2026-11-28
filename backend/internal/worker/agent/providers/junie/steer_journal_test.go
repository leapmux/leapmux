package junie

import (
	"strings"
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/sessionstore"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestJunieSteerAdmissionUsesOneFreshJournalRecord(t *testing.T) {
	t.Parallel()
	const baseline = `{"kind":"TaskStartedEvent","taskId":"task-261123-060030-ah1k"}` + "\n"
	const first = `{"kind":"UserPromptEvent","requestId":"first","prompt":"Check the new shipment.","customAttachments":[{"kind":"RealtimeFollowUpAttachment"}]}` + "\n"
	second := strings.Replace(first, `"first"`, `"second"`, 1)
	withFirst := baseline + first
	withBoth := withFirst + second
	inputs := []junieSteerInput{
		{receipt: agent.NativeInputReceipt{InputID: "leapmux-first"}, text: "Check the new shipment."},
		{receipt: agent.NativeInputReceipt{InputID: "leapmux-second"}, text: "Check the new shipment."},
	}
	matched, ok := readJunieSteerAdmissions(sessionstore.NewJournalCursor([]byte(baseline)), []byte(withBoth), inputs)
	require.True(t, ok)
	assert.Equal(t, inputs[0].receipt, matched["first"])
	assert.Equal(t, inputs[1].receipt, matched["second"])
	_, ok = readJunieSteerAdmissions(sessionstore.NewJournalCursor([]byte(baseline)), []byte(withFirst), inputs)
	assert.False(t, ok, "an acknowledged batch can still be missing an append")

	for name, journal := range map[string]string{
		"unread":         baseline,
		"partial record": strings.TrimSuffix(withFirst, "\n"),
		"two admissions": withBoth,
		"rewritten":      strings.Replace(withFirst, "ah1k", "zzzz", 1),
		"other prompt":   strings.Replace(withFirst, "new shipment", "old shipment", 1),
		"ordinary input": strings.Replace(withFirst, "RealtimeFollowUpAttachment", "ModelForLaunchAttachment", 1),
	} {
		t.Run(name, func(t *testing.T) {
			_, ok := readJunieSteerAdmissions(sessionstore.NewJournalCursor([]byte(baseline)), []byte(journal), inputs[:1])
			assert.False(t, ok)
		})
	}
}

func TestJunieTaskReplyRequiresTheCompletedCurrentTask(t *testing.T) {
	t.Parallel()
	const baseline = `{"kind":"TaskStartedEvent","taskId":"task-261123-060030-ah1k"}` + "\n"
	const task = `{"kind":"TaskStartedEvent","taskId":"task-261123-060032-ae58"}` + "\n"
	const commit = `{"kind":"UserMessagesCommittedToHistory","userMessageIds":["first","second"]}` + "\n"
	const result = `{"kind":"SessionA2uxEvent","taskId":"task-261123-060032-ae58","event":{"state":"COMPLETED","agentEvent":{"kind":"ResultBlockUpdatedEvent","agent":{"kind":"MainAgent","id":"main"},"stepId":"341b2f54-9073-44f4-a776-3fb86f2200ef","cancelled":false,"result":"The shipment is ready."}}}` + "\n"
	cursor := sessionstore.NewJournalCursor([]byte(baseline))
	journal := baseline + task + commit + result
	replies, ok := readJunieTaskReplies(cursor, []byte(journal))
	require.True(t, ok)
	require.Len(t, replies, 1)
	reply := replies[0]
	assert.Equal(t, []string{"first", "second"}, reply.inputIDs)
	assert.Equal(t, "task-261123-060032-ae58", reply.taskID)
	assert.Equal(t, "341b2f54-9073-44f4-a776-3fb86f2200ef", reply.stepID)
	for name, modified := range map[string]string{
		"uncommitted":         baseline + task + result,
		"in progress":         strings.Replace(journal, "COMPLETED", "IN_PROGRESS", 1),
		"cancelled":           strings.Replace(journal, "false", "true", 1),
		"other task":          strings.Replace(journal, "ae58", "abcd", 1),
		"other actor":         strings.Replace(journal, "MainAgent", "CustomAgent", 1),
		"conflicting result":  journal + result,
		"truncated result":    strings.TrimSuffix(journal, "\n"),
		"duplicate task":      journal + task,
		"duplicate input":     strings.Replace(journal, `"first","second"`, `"first","first"`, 1),
		"mismatched ACP text": strings.Replace(journal, "shipment is ready", "shipment is late", 1),
	} {
		t.Run(name, func(t *testing.T) {
			replies, ok := readJunieTaskReplies(cursor, []byte(modified))
			assert.False(t, ok && matchJunieAnswers(replies, []junieACPAnswer{{text: "The shipment is ready."}}))
		})
	}
}

func TestJunieJournalSkipsUnrelatedEventsWithCollidingShapes(t *testing.T) {
	t.Parallel()
	const baseline = `{"kind":"TaskStartedEvent","taskId":"task-261123-060030-ah1k"}` + "\n"
	// An unrelated event mentioning target kind names with an incompatible "event" or "prompt" type.
	const colliding = `{"kind":"LogEvent","prompt":["UserPromptEvent","TaskStartedEvent","ResultBlockUpdatedEvent"],"event":"not-an-object"}` + "\n"
	const first = `{"kind":"UserPromptEvent","requestId":"first","prompt":"Check the new shipment.","customAttachments":[{"kind":"RealtimeFollowUpAttachment"}]}` + "\n"
	const task = `{"kind":"TaskStartedEvent","taskId":"task-261123-060032-ae58"}` + "\n"
	const commit = `{"kind":"UserMessagesCommittedToHistory","userMessageIds":["first"]}` + "\n"
	const result = `{"kind":"SessionA2uxEvent","taskId":"task-261123-060032-ae58","event":{"state":"COMPLETED","agentEvent":{"kind":"ResultBlockUpdatedEvent","agent":{"kind":"MainAgent","id":"main"},"stepId":"341b2f54-9073-44f4-a776-3fb86f2200ef","cancelled":false,"result":"Done."}}}` + "\n"

	cursor := sessionstore.NewJournalCursor([]byte(baseline))
	journal := []byte(baseline + colliding + first + task + commit + result)
	inputs := []junieSteerInput{{receipt: agent.NativeInputReceipt{InputID: "leapmux-first"}, text: "Check the new shipment."}}

	matched, ok := readJunieSteerAdmissions(cursor, journal, inputs)
	require.True(t, ok, "unrelated event with colliding field shape must not abort admission scan")
	assert.Equal(t, inputs[0].receipt, matched["first"])

	replies, ok := readJunieTaskReplies(cursor, journal)
	require.True(t, ok, "unrelated event with colliding field shape must not abort task reply scan")
	require.Len(t, replies, 1)
	assert.Equal(t, "Done.", replies[0].text)
}
