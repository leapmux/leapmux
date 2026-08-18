package bgtask

import (
	"strings"
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

func TestUpsertCleanTitle(t *testing.T) {
	cases := []struct {
		name  string
		title string
		want  string
	}{
		{"cuts an over-long ASCII title to the byte limit", strings.Repeat("a", 200), strings.Repeat("a", 128)},
		{"cuts an over-long CJK title at a rune boundary", strings.Repeat("一", 50), strings.Repeat("一", 42)},
		{"strips a control character", "Hello\x00World", "HelloWorld"},
		{"strips the templating characters", `100% of $HOME "quoted" c:\path`, "100 of HOME quoted c:path"},
		{"trims the surrounding whitespace", "   Deploy the hub   ", "Deploy the hub"},
		{"keeps a clean title unchanged", "Ship the parser", "Ship the parser"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Upsert{Title: tc.title}.CleanTitle()
			assert.Equal(t, tc.want, got.Title)
			// The rule runs again on its own output without changing it, which
			// is what lets EnsureChildAgent clean once and the applier clean
			// again on the same string.
			assert.Equal(t, got, got.CleanTitle(), "CleanTitle must be idempotent")
		})
	}
}

// The accepted cost of one rule for every title column: a shell command handed
// over as a title loses the characters that make it runnable. The flag stays,
// because what is left is still a command, only stripped.
func TestUpsertCleanTitleStripsAShellCommand(t *testing.T) {
	got := Upsert{Title: `npm test --grep "$FOO"`, TitleIsCommand: true}.CleanTitle()
	assert.Equal(t, "npm test --grep FOO", got.Title)
	assert.True(t, got.TitleIsCommand, "a stripped command is still a command")
}

// A title the rule empties clears the command flag with it: the flag describes
// a title, and there is none left to describe.
func TestUpsertCleanTitleEmptiedTitleClearsTheCommandFlag(t *testing.T) {
	got := Upsert{Title: `  $$%%  `, TitleIsCommand: true}.CleanTitle()
	assert.Empty(t, got.Title)
	assert.False(t, got.TitleIsCommand)
}

// An emptied title is the same blank a caller sends deliberately, so the merge
// answers both the same way: the row keeps the title it already holds.
func TestUpsertCleanTitleEmptiedTitleKeepsTheStoredOne(t *testing.T) {
	existing := Item{Title: "Ship the parser", TitleIsCommand: false}
	merged := Upsert{Title: `  $$%%  `, TitleIsCommand: true}.CleanTitle().ToItem().PreservingBlanksFrom(existing)
	assert.Equal(t, "Ship the parser", merged.Title)
	assert.False(t, merged.TitleIsCommand, "the stored pair is restored whole")
}

// CleanTitle touches the title pair and nothing else.
func TestUpsertCleanTitleLeavesEveryOtherFieldAlone(t *testing.T) {
	u := Upsert{
		RowKey:        "task-1",
		Kind:          KindShell,
		ChildAgentID:  "child-1",
		ParentAgentID: "root-1",
		GroupKey:      "wf-1",
		GroupLabel:    "Workflow $1",
		Title:         "Ship the parser",
		Description:   "/tmp/out$.txt",
		ActiveForm:    "running $HOME",
		Status:        StatusRunning,
	}
	assert.Equal(t, u, u.CleanTitle())
}
