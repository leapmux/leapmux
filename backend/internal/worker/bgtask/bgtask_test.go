package bgtask

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/stretchr/testify/assert"

	"github.com/leapmux/leapmux/util/validate"
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
	// DEL and the whole C1 block are control characters too. The old rule
	// tested `r < 0x20` and let both through; validate.IsUnreadable reports
	// the whole Cc category, which is U+0000-U+001F AND U+007F-U+009F.
	assert.Equal(t, "ab", SanitizeRowKey("a\x7fb"))
	assert.Equal(t, "ab", SanitizeRowKey("a\u009bb"))
	// An invisible format character is stripped for the reason a control
	// character is: the key reaches a log line, and a right-to-left override
	// reverses what the reader of that line sees.
	assert.Equal(t, "ab", SanitizeRowKey("a\u202eb"))
	assert.Equal(t, "ab", SanitizeRowKey("a\u200b\u00adb"))
	// A row key is a KEY, so an interior space is not folded and the ends are
	// not trimmed: two keys that differ only in whitespace stay two keys.
	assert.Equal(t, " a  b ", SanitizeRowKey(" a  b "))
	// An invalid byte is dropped rather than grown into a 3-byte U+FFFD. The
	// old strings.Map turned 1 byte into 3, which is how a key could get
	// LONGER than the value the provider sent.
	assert.Equal(t, "ab", SanitizeRowKey("a\xffb"))
	// A U+FFFD the provider sent deliberately survives, because it decodes
	// with size 3 and is neither a control nor an invisible character.
	assert.Equal(t, "a�b", SanitizeRowKey("a�b"))
}

// TestSanitizeRowKeyCapsLength pins the cap that nothing else on the path
// supplies. A row key is a primary key column, it stays in memory for the
// life of the agent, and every registry snapshot broadcast carries it again.
// MaxTasks caps how MANY rows exist and says nothing about how large one is.
func TestSanitizeRowKeyCapsLength(t *testing.T) {
	assert.Len(t, SanitizeRowKey(strings.Repeat("a", RowKeyByteLimit)), RowKeyByteLimit,
		"a key exactly at the limit is kept whole")
	assert.Len(t, SanitizeRowKey(strings.Repeat("a", RowKeyByteLimit+1)), RowKeyByteLimit,
		"one byte over the limit loses one byte")
	assert.Len(t, SanitizeRowKey(strings.Repeat("a", 100_000)), RowKeyByteLimit,
		"a provider cannot store a key of any size it likes")

	// The cut lands at a rune boundary, never inside one: a partial rune is
	// invalid UTF-8, which fails the proto broadcast marshal.
	//
	// RowKeyByteLimit is 256, which is not a multiple of 3, so a run of 3-byte
	// runes puts the limit inside the 86th rune. The cut moves back to 255
	// bytes.
	cut := SanitizeRowKey(strings.Repeat("中", 100))
	assert.True(t, utf8.ValidString(cut), "the cut result must be valid UTF-8")
	assert.Equal(t, strings.Repeat("中", RowKeyByteLimit/3), cut)

	// The cap counts the KEPT bytes, not the input bytes: a key padded with
	// invisible characters does not lose the text behind them.
	padded := strings.Repeat("\u200b", 1000) + "call-abc"
	assert.Equal(t, "call-abc", SanitizeRowKey(padded))
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
		{"strips an invisible format character", "Deploy\u200b the\u00ad hub\ufeff", "Deploy the hub"},
		{"keeps a quote, a backslash, a dollar and a percent", `100% of $HOME "quoted" c:\path`, `100% of $HOME "quoted" c:\path`},
		{"folds a run of whitespace to one space", "  Fix   parser \t\n Add\u00a0tests  ", "Fix parser Add tests"},
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

// A shell command handed over as a title keeps every character that makes it
// runnable. This is the case the rule relaxed for: one rule for every title
// column used to turn `npm test --grep "$FOO"` into `npm test --grep FOO`, and
// the row then identified a command that nobody ran.
func TestUpsertCleanTitleKeepsAShellCommandWhole(t *testing.T) {
	const command = `npm test --grep "$FOO" 100% c:\tmp`
	got := Upsert{Title: command, TitleIsCommand: true}.CleanTitle()
	assert.Equal(t, command, got.Title)
	assert.True(t, got.TitleIsCommand)
}

// A title the rule empties clears the command flag with it: the flag describes
// a title, and there is none left to describe.
func TestUpsertCleanTitleEmptiedTitleClearsTheCommandFlag(t *testing.T) {
	got := Upsert{Title: "  \x00\u200b\ufeff  ", TitleIsCommand: true}.CleanTitle()
	assert.Empty(t, got.Title)
	assert.False(t, got.TitleIsCommand)
}

// An emptied title is the same blank a caller sends deliberately, so the merge
// answers both the same way: the row keeps the title it already holds.
func TestUpsertCleanTitleEmptiedTitleKeepsTheStoredOne(t *testing.T) {
	existing := Item{Title: "Ship the parser", TitleIsCommand: false}
	merged := Upsert{Title: "  \x00\u200b\ufeff  ", TitleIsCommand: true}.CleanTitle().ToItem().PreservingBlanksFrom(existing)
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

// TestCleanTitleRunes covers the helper directly. Two provider call sites read
// it, and both used to cut BEFORE the clean.
func TestCleanTitleRunes(t *testing.T) {
	t.Run("cleans before it cuts", func(t *testing.T) {
		// The whole point. A cut-first rule kept 80 invisible runes, the clean
		// then emptied them, and the row kept the title it already held.
		assert.Equal(t, "Fix the auth bug",
			CleanTitleRunes(strings.Repeat("\u200b", 85)+"Fix the auth bug", 80))
	})

	t.Run("cuts to the rune count", func(t *testing.T) {
		assert.Equal(t, strings.Repeat("a", 80), CleanTitleRunes(strings.Repeat("a", 500), 80))
		assert.Equal(t, "short", CleanTitleRunes("short", 80))
	})

	// The byte cap inside CleanName binds first for a wide script: 128 bytes
	// is 42 CJK runes, so the 80-rune display cap never applies to them.
	t.Run("the byte cap binds before the rune cap for a wide script", func(t *testing.T) {
		got := CleanTitleRunes(strings.Repeat("\u4e2d", 200), 80)
		assert.Equal(t, strings.Repeat("\u4e2d", 42), got)
		assert.True(t, utf8.ValidString(got))
	})

	t.Run("trims the space the rune cut exposes", func(t *testing.T) {
		// The cut lands right after "aaa ", and the trailing space must go --
		// the same second trim CleanName applies after its own cut.
		assert.Equal(t, "aaa", CleanTitleRunes("aaa bbb", 4))
	})

	t.Run("folds and strips like every other title writer", func(t *testing.T) {
		assert.Equal(t, "Fix parser Add tests", CleanTitleRunes("  Fix   parser \t\n Add\u00a0tests  ", 80))
		assert.Equal(t, "txt.exe", CleanTitleRunes("\u202etxt.exe", 80))
		assert.Equal(t, `npm test --grep "$FOO"`, CleanTitleRunes(`npm test --grep "$FOO"`, 80))
	})

	t.Run("returns empty when nothing survives", func(t *testing.T) {
		assert.Empty(t, CleanTitleRunes("", 80))
		assert.Empty(t, CleanTitleRunes("   ", 80))
		assert.Empty(t, CleanTitleRunes("\u200b\ufeff\x00", 80))
	})

	// The sink runs CleanName again on this result, so the two must agree.
	t.Run("is already what CleanName returns for it", func(t *testing.T) {
		for _, in := range []string{
			"  Fix   parser  ", strings.Repeat("a", 500), strings.Repeat("\u4e2d", 200),
			"aaa bbb", "\u202etxt.exe",
		} {
			got := CleanTitleRunes(in, 80)
			assert.Equalf(t, got, validate.CleanName(got), "CleanName must be a no-op on %q", got)
		}
	})
}
