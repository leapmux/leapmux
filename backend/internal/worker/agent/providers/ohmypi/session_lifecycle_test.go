package ohmypi

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResumeArgs(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	file := filepath.Join(home, ".omp", "agent", "sessions", "-p", "2026-09-23T18-11-57-284Z_01a0cf77.jsonl")

	args, err := resumeArgs("", home)
	require.NoError(t, err)
	assert.Nil(t, args, "no handle, no resume")

	args, err = resumeArgs(file, home)
	require.NoError(t, err)
	assert.Equal(t, []string{"--resume", file}, args)

	args, err = resumeArgs("01a0cf77-9ae4-72d8-9a42-665c431d3beb", home)
	require.NoError(t, err)
	assert.Equal(t, []string{"--resume", "01a0cf77-9ae4-72d8-9a42-665c431d3beb"}, args)

	_, err = resumeArgs("relative/a.jsonl", home)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "send /clear", "the failure tells the reader how to recover")
}

func TestClearContext(t *testing.T) {
	t.Parallel()

	t.Run("starts a new session and reports its file", func(t *testing.T) {
		r := newRig(t)
		r.respond(func(command recordedCommand) *rigReply {
			switch command.Type {
			case CommandNewSession:
				return &rigReply{Data: json.RawMessage(`{"cancelled":false}`)}
			case CommandGetState:
				return &rigReply{Data: json.RawMessage(`{"sessionId":"new-id","sessionFile":"/sessions/new.jsonl","model":{"id":"mock-model","provider":"mock"}}`)}
			}
			return nil
		})
		r.emit(`{"type":"tool_execution_start","toolCallId":"call_1","toolName":"bash","args":{"command":"sleep 9"}}`)

		handle, err := r.agent.ClearContext()
		require.NoError(t, err)
		assert.Equal(t, "/sessions/new.jsonl", handle)
		assert.Equal(t, "/sessions/new.jsonl", r.sink.LastSessionID())
		assert.Equal(t, 1, r.sink.GoalClears(), "the new session has no goal")
		last := r.sink.Messages()[len(r.sink.Messages())-1]
		assert.True(t, last.Closing, "the call the replaced session ran is closed")
		assert.Equal(t, agent.MessageCompletionInterrupted, last.Completion)
	})

	t.Run("a cancelled clear keeps the session", func(t *testing.T) {
		r := newRig(t)
		r.respond(func(command recordedCommand) *rigReply {
			if command.Type == CommandNewSession {
				return &rigReply{Data: json.RawMessage(`{"cancelled":true}`)}
			}
			return nil
		})
		_, err := r.agent.ClearContext()
		assert.ErrorIs(t, err, agent.ErrContextClearCancelled)
		assert.Empty(t, r.commandsOfType(CommandGetState))
	})

	t.Run("a response with no cancellation flag fails", func(t *testing.T) {
		r := newRig(t)
		r.respond(func(command recordedCommand) *rigReply {
			if command.Type == CommandNewSession {
				return &rigReply{Data: json.RawMessage(`{}`)}
			}
			return nil
		})
		_, err := r.agent.ClearContext()
		assert.ErrorContains(t, err, "no cancellation status")
	})

	t.Run("ends everything the replaced session ran", func(t *testing.T) {
		r := newRig(t)
		r.respond(func(command recordedCommand) *rigReply {
			switch command.Type {
			case CommandNewSession:
				return &rigReply{Data: json.RawMessage(`{"cancelled":false}`)}
			case CommandGetState:
				return &rigReply{Data: json.RawMessage(`{"sessionId":"new-id","sessionFile":"/sessions/new.jsonl","model":{"id":"mock-model","provider":"mock"}}`)}
			}
			return nil
		})
		subagentKey := subagentRowKey(r.agent.sessionID, "Probe")
		shellKey := shellRowKey(r.agent.sessionID, "bash-1")
		r.emit(`{"type":"agent_start"}`, frameTaskStart, frameStarted, frameBackgroundBashStart, frameBackgroundBashEnd,
			askCallMessage("call_1", singleAskArgs),
			assistantWithUsage(`{"input":1000,"output":20,"cacheRead":0,"cacheWrite":0,"cost":{"total":0.25}}`))

		_, err := r.agent.ClearContext()
		require.NoError(t, err)

		subagent, ok := r.sink.BackgroundTask(subagentKey)
		require.True(t, ok)
		assert.Equal(t, bgtask.StatusStopped, subagent.Status, "omp ends every subagent with the session")
		shell, ok := r.sink.BackgroundTask(shellKey)
		require.True(t, ok)
		assert.Equal(t, bgtask.StatusStopped, shell.Status, "no async result of the replaced session arrives")
		active, _ := r.sink.LastTurnActive()
		assert.False(t, active)
		r.agent.Mu.Lock()
		defer r.agent.Mu.Unlock()
		assert.Empty(t, r.agent.subagents)
		assert.Empty(t, r.agent.shells)
		assert.Empty(t, r.agent.asks.calls, "a question of the replaced session claims no later dialog")
		assert.Equal(t, usageSnapshot{}, r.agent.snapshotLocked(), "the new session's usage starts over")
		assert.Equal(t, "new-id", r.agent.sessionID)
	})

	t.Run("a refused new_session changes nothing", func(t *testing.T) {
		r := newRig(t)
		r.respond(func(command recordedCommand) *rigReply {
			if command.Type == CommandNewSession {
				return &rigReply{Error: "an extension refused the switch"}
			}
			return nil
		})
		_, err := r.agent.ClearContext()
		var refused *commandError
		require.ErrorAs(t, err, &refused)
		assert.Empty(t, r.commandsOfType(CommandGetState))
		assert.Zero(t, r.sink.SessionIDCount())
	})

	t.Run("a new session whose state cannot be read fails", func(t *testing.T) {
		r := newRig(t)
		r.respond(func(command recordedCommand) *rigReply {
			switch command.Type {
			case CommandNewSession:
				return &rigReply{Data: json.RawMessage(`{"cancelled":false}`)}
			case CommandGetState:
				return &rigReply{Error: "busy"}
			}
			return nil
		})
		_, err := r.agent.ClearContext()
		assert.ErrorContains(t, err, "read the new omp session")
		assert.Zero(t, r.sink.SessionIDCount())
	})

	t.Run("a new session with no handle fails", func(t *testing.T) {
		r := newRig(t)
		r.agent.Mu.Lock()
		r.agent.sessionID, r.agent.sessionFile = "", ""
		r.agent.Mu.Unlock()
		r.respond(func(command recordedCommand) *rigReply {
			if command.Type == CommandNewSession {
				return &rigReply{Data: json.RawMessage(`{"cancelled":false}`)}
			}
			return nil
		})
		_, err := r.agent.ClearContext()
		assert.ErrorContains(t, err, "has no handle")
		assert.Zero(t, r.sink.SessionIDCount(), "no empty handle is stored")
	})
}

func TestApplyState(t *testing.T) {
	t.Parallel()
	const (
		file = "/sessions/2026-09-23T18-11-57-284Z_01a0cf77-9ae4-72d8-9a42-665c431d3beb.jsonl"
		id   = "01a0cf77-9ae4-72d8-9a42-665c431d3beb"
	)
	type state struct {
		model, level, effective, sessionID, sessionFile string
	}
	for _, tc := range []struct {
		name    string
		level   string
		raw     string
		want    state
		changed bool
	}{
		{name: "no data changes nothing", level: "high", raw: ``,
			want: state{"mock/mock-model", "high", "", id, file}},
		{name: "a garbled state changes nothing", level: "high", raw: `{"model":7}`,
			want: state{"mock/mock-model", "high", "", id, file}},
		{name: "a model that does not reason runs off", level: "high", raw: `{"model":{"id":"mock-model-2","provider":"mock"}}`,
			want: state{"mock/mock-model-2", "off", "off", id, file}},
		{name: "a level with no model keeps the model", level: "high", raw: `{"thinkingLevel":"low"}`,
			want: state{"mock/mock-model", "low", "low", id, file}},
		{name: "no model and no level states no level", level: "high", raw: `{"model":{"id":"","provider":"mock"}}`,
			want: state{"mock/mock-model", "high", "", id, file}},
		{name: "the reader's Auto stays", level: agent.EffortAuto, raw: `{"model":{"id":"mock-model","provider":"mock"},"thinkingLevel":"medium"}`,
			want: state{"mock/mock-model", agent.EffortAuto, "medium", id, file}},
		{name: "a new session replaces the id and the file", level: "high", raw: `{"sessionId":"s-new","sessionFile":"/sessions/s-new.jsonl"}`,
			want: state{"mock/mock-model", "high", "", "s-new", "/sessions/s-new.jsonl"}, changed: true},
		{name: "a new session with no file yet drops the old file", level: "high", raw: `{"sessionId":"s-new"}`,
			want: state{"mock/mock-model", "high", "", "s-new", ""}, changed: true},
		{name: "a new file of the same session moves the handle", level: "high", raw: `{"sessionId":"` + id + `","sessionFile":"/elsewhere/moved.jsonl"}`,
			want: state{"mock/mock-model", "high", "", id, "/elsewhere/moved.jsonl"}, changed: true},
		{name: "the same session is no change", level: "high", raw: `{"sessionId":"` + id + `","sessionFile":"` + file + `"}`,
			want: state{"mock/mock-model", "high", "", id, file}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := newRig(t)
			r.agent.Mu.Lock()
			r.agent.thinkingLevel = tc.level
			r.agent.Mu.Unlock()

			assert.Equal(t, tc.changed, r.agent.applyState(json.RawMessage(tc.raw)))

			r.agent.Mu.Lock()
			defer r.agent.Mu.Unlock()
			assert.Equal(t, tc.want, state{r.agent.model, r.agent.thinkingLevel, r.agent.effectiveThinking, r.agent.sessionID, r.agent.sessionFile})
		})
	}
}

// The handle is the session FILE, which resumes even before omp wrote it. A
// session that states no file yet is resumed by its id.
func TestSessionHandleLocked(t *testing.T) {
	t.Parallel()
	a := &Agent{sessionID: "s1", sessionFile: "/sessions/s1.jsonl"}
	assert.Equal(t, "/sessions/s1.jsonl", a.sessionHandleLocked())
	a.sessionFile = ""
	assert.Equal(t, "s1", a.sessionHandleLocked())
	a.sessionID = ""
	assert.Empty(t, a.sessionHandleLocked())
}
