package bgtask

import (
	"testing"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/stretchr/testify/assert"
)

func TestStatusWireRoundTrip(t *testing.T) {
	cases := []Status{
		StatusPending,
		StatusRunning,
		StatusCompleted,
		StatusFailed,
		StatusStopped,
		StatusInterrupted,
	}
	for _, s := range cases {
		assert.Equal(t, s, StatusFromWire(StatusWire(s)), "round trip for %d", s)
	}
}

func TestStatusFromWireUnknown(t *testing.T) {
	assert.Equal(t, StatusPending, StatusFromWire("nonsense"))
	assert.Equal(t, StatusPending, StatusFromWire(""))
}

func TestKindWireRoundTrip(t *testing.T) {
	assert.Equal(t, "subagent", KindWire(KindSubagent))
	assert.Equal(t, "shell", KindWire(KindShell))
	assert.Equal(t, KindSubagent, KindFromWire("subagent"))
	assert.Equal(t, KindShell, KindFromWire("shell"))
	// Unknown falls through to Subagent.
	assert.Equal(t, KindSubagent, KindFromWire("nope"))
}

func TestStatusIsFinished(t *testing.T) {
	finished := []Status{StatusCompleted, StatusFailed, StatusStopped, StatusInterrupted}
	active := []Status{StatusPending, StatusRunning}
	for _, s := range finished {
		assert.True(t, s.IsFinished(), "%d should be final", s)
	}
	for _, s := range active {
		assert.False(t, s.IsFinished(), "%d should NOT be final", s)
	}
}

func TestItemToProto(t *testing.T) {
	now := time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)
	item := Item{
		RowKey:        "task-1",
		ChildAgentID:  "child-1",
		ParentAgentID: "root-1",
		Kind:          KindSubagent,
		GroupKey:      "workflow:x",
		GroupLabel:    "x",
		Title:         "Do thing",
		Description:   "desc",
		ActiveForm:    "running Bash",
		Status:        StatusRunning,
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	p := item.ToProto()
	assert.Equal(t, "task-1", p.GetId())
	assert.Equal(t, leapmuxv1.BackgroundTaskKind_BACKGROUND_TASK_KIND_SUBAGENT, p.GetKind())
	assert.Equal(t, "child-1", p.GetChildAgentId())
	assert.Equal(t, "root-1", p.GetParentAgentId())
	assert.Equal(t, "workflow:x", p.GetGroupKey())
	assert.Equal(t, "Do thing", p.GetTitle())
	assert.Equal(t, "running Bash", p.GetActiveForm())
	assert.Equal(t, leapmuxv1.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_RUNNING, p.GetStatus())
	assert.Empty(t, p.GetEndedAt(), "zero EndedAt renders as empty")

	// Shell kind + final status.
	shell := Item{RowKey: "sh-1", Kind: KindShell, Status: StatusCompleted, EndedAt: now}
	sp := shell.ToProto()
	assert.Equal(t, leapmuxv1.BackgroundTaskKind_BACKGROUND_TASK_KIND_SHELL, sp.GetKind())
	assert.Equal(t, leapmuxv1.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_COMPLETED, sp.GetStatus())
	assert.NotEmpty(t, sp.GetEndedAt())
}

func TestItemsToProto(t *testing.T) {
	items := []Item{{RowKey: "a"}, {RowKey: "b"}}
	out := ItemsToProto(items)
	assert.Len(t, out, 2)
	assert.Equal(t, "a", out[0].GetId())
	assert.Equal(t, "b", out[1].GetId())
}

func TestSanitizeRowKey(t *testing.T) {
	// Embedded newline (observed in Cursor toolCallIds) is stripped, not replaced.
	assert.Equal(t, "call-abcfc-def", SanitizeRowKey("call-abc\nfc-def"))
	// Tab + carriage return + NUL stripped.
	assert.Equal(t, "key", SanitizeRowKey("ke\ty\r\x00"))
	// Clean key unchanged.
	assert.Equal(t, "task-1", SanitizeRowKey("task-1"))
	// Empty stays empty.
	assert.Empty(t, SanitizeRowKey(""))
	// Printable punctuation (>= 0x20) preserved.
	assert.Equal(t, "a/b:c-d_e.1", SanitizeRowKey("a/b:c-d_e.1"))
	// DEL (0x7f) is NOT a control char per the < 0x20 rule and is preserved.
	assert.Equal(t, "a\x7fb", SanitizeRowKey("a\x7fb"))
}
