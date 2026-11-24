package junie

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	junieControlTaskLine = `{"kind":"TaskStartedEvent","taskId":"task-261123-071734-r1h9"}` + "\n"
	junieControlInputs   = `{"kind":"UserMessagesCommittedToHistory","userMessageIds":["first","second"]}` + "\n"
	junieLaterInputs     = `{"kind":"UserMessagesCommittedToHistory","userMessageIds":["first","second","later"]}` + "\n"
	juniePermissionStep  = "97ee4c57-9e79-4be4-ba33-34c875a1256d"
	juniePermissionEvent = `{"kind":"SessionA2uxEvent","taskId":"task-261123-071734-r1h9","event":{"state":"INPUT_REQUIRED","agentEvent":{"kind":"TerminalBlockUpdatedEvent","agent":{"kind":"MainAgent","id":"main"},"stepId":"97ee4c57-9e79-4be4-ba33-34c875a1256d","status":"IN_PROGRESS"}}}` + "\n"
)

func TestJuniePermissionUsesTheInputsCommittedWhenItOpened(t *testing.T) {
	t.Parallel()
	selector := junieControlSelector{toolCallID: juniePermissionStep}
	journal := junieControlTaskLine + junieControlInputs + juniePermissionEvent + junieLaterInputs
	task, ok := readJunieControlTask(junieCursor(nil), []byte(journal), selector)
	require.True(t, ok)
	assert.Equal(t, "task-261123-071734-r1h9", task.id)
	assert.Equal(t, []string{"first", "second"}, task.inputIDs)

	for name, wrong := range map[string]string{
		"other step":     strings.ReplaceAll(journal, juniePermissionStep, "415ce89c-f0d3-4638-b58e-0b59935b420f"),
		"other actor":    strings.ReplaceAll(journal, "MainAgent", "CustomAgent"),
		"retired task":   journal + `{"kind":"TaskState","state":"COMPLETED"}` + "\n",
		"answered tool":  journal + strings.ReplaceAll(juniePermissionEvent, "IN_PROGRESS", "COMPLETED"),
		"later task":     journal + strings.ReplaceAll(junieControlTaskLine, "r1h9", "next"),
		"no consumption": junieControlTaskLine + juniePermissionEvent,
	} {
		t.Run(name, func(t *testing.T) {
			_, ok := readJunieControlTask(junieCursor(nil), []byte(wrong), selector)
			assert.False(t, ok)
		})
	}
}

func TestJunieControlTaskMatchesTheWholeNativeForm(t *testing.T) {
	t.Parallel()
	const size = `{"kind":"SessionA2uxEvent","taskId":"task-261123-071734-r1h9","event":{"state":"INPUT_REQUIRED","agentEvent":{"kind":"AskAsyncRequestUpdatedEvent","agent":{"kind":"MainAgent","id":"main"},"stepId":"cf973294-b449-4b34-9b06-abce5ef4d9aa","status":"IN_PROGRESS","request":{"id":"001a5155-a567-49b8-bec5-f51495f9a9e9","name":"Size","question":"Choose native sizes.","options":[{"id":"Small","title":"Small","description":"Use small."}],"allowMultiple":true}}}}` + "\n"
	const color = `{"kind":"SessionA2uxEvent","taskId":"task-261123-071734-r1h9","event":{"state":"INPUT_REQUIRED","agentEvent":{"kind":"AskAsyncRequestUpdatedEvent","agent":{"kind":"MainAgent","id":"main"},"stepId":"415ce89c-f0d3-4638-b58e-0b59935b420f","status":"IN_PROGRESS","request":{"id":"e6360d92-2006-47fb-a253-4d86b66be754","name":"Color","question":"Choose a native color.","options":[{"id":"Green","title":"Green","description":"Use green."}]}}}}` + "\n"
	const rawForm = `{"mode":"form","message":"Please answer the following questions.","requestedSchema":{"type":"object","title":"Questions","properties":{"question_0":{"type":"array","title":"Size","description":"Choose native sizes.","items":{"anyOf":[{"const":"Small","title":"Small","description":"Use small."}]}},"question_0_custom":{"type":"string","_meta":{"_askUserQuestionCustomAnswer":true}},"question_1":{"type":"string","title":"Color","description":"Choose a native color.","oneOf":[{"const":"Green","title":"Green","description":"Use green."}]}},"required":[]}}`
	selector, ok := junieControlSelectorOf(&providerkit.ParsedLine{Method: "elicitation/create", Params: json.RawMessage(rawForm)})
	require.True(t, ok)
	journal := junieControlTaskLine + junieControlInputs + size + color + junieLaterInputs
	task, ok := readJunieControlTask(junieCursor(nil), []byte(journal), selector)
	require.True(t, ok)
	assert.Equal(t, "task-261123-071734-r1h9", task.id)
	assert.Equal(t, []string{"first", "second"}, task.inputIDs)

	for name, wrong := range map[string]string{
		"missing field":    junieControlTaskLine + junieControlInputs + size,
		"different choice": strings.ReplaceAll(journal, "Use green.", "Use blue."),
		"opposite order":   junieControlTaskLine + junieControlInputs + color + size,
		"retired form":     journal + strings.ReplaceAll(color, "IN_PROGRESS", "COMPLETED"),
		"unrelated":        strings.ReplaceAll(journal, "Choose a native color.", "Choose a different color."),
		"different inputs": junieControlTaskLine + junieControlInputs + size + junieLaterInputs + color,
	} {
		t.Run(name, func(t *testing.T) {
			_, ok := readJunieControlTask(junieCursor(nil), []byte(wrong), selector)
			assert.False(t, ok)
		})
	}
}
