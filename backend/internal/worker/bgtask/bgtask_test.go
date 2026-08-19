package bgtask

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

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

// A row key is an IDENTITY, so the rule REFUSES and never rewrites. The
// earlier version stripped what a reader cannot see and capped the length,
// which made it non-injective: two provider keys mapped onto one string, the
// second background task overwrote the first's registry row, and one of the
// two vanished from the sidebar.
func TestValidateRowKey(t *testing.T) {
	t.Parallel()

	t.Run("accepts the keys providers actually send", func(t *testing.T) {
		for _, key := range []string{
			"task-1",
			"call-abc123",
			"a/b:c-d_e.1",
			"toolu_01A2b3C4d5E6f7G8h9",
			strings.Repeat("a", RowKeyByteLimit),
		} {
			assert.NoErrorf(t, ValidateRowKey(key), "%q must be accepted", key)
		}
	})

	// The empty key means "this call carries no registry linkage".
	// EnsureChildAgent and RenameBackgroundTask each test for it, so the rule
	// must not turn it into an error.
	t.Run("accepts the empty key", func(t *testing.T) {
		assert.NoError(t, ValidateRowKey(""))
	})

	// The characters the OLD rule stripped are accepted now. Stripping them
	// was the collision: at least one provider (Cursor) ships toolCallIds with
	// an embedded newline, so refusing them would drop that provider's rows,
	// and rewriting them merged distinct keys. They are cleaned where the key
	// is READ instead -- rowTitle in BackgroundTaskList.tsx.
	t.Run("accepts what a reader cannot see, because a key is not prose", func(t *testing.T) {
		for _, key := range []string{
			"call-abc\nfc-def", // the observed Cursor shape
			"ke\ty\r\x00",      // tab, carriage return, NUL
			"a\x7fb",           // DEL
			"a\u009bb",         // a C1 control
			"a\u202eb",         // a right-to-left override
			"a\u200b\u00adb",   // a zero width space and a soft hyphen
			" a  b ",           // untrimmed and unfolded
		} {
			assert.NoErrorf(t, ValidateRowKey(key), "%q must be accepted verbatim", key)
		}
	})

	// The invariant the whole rule exists for. Two keys that differ AT ALL
	// must stay two keys, so no accepted pair may share an identity. The old
	// rule mapped each of these pairs onto one string.
	t.Run("tells apart every pair the old rewrite merged", func(t *testing.T) {
		for _, pair := range [][2]string{
			{"call-abc", "call-a\u200bbc"}, // an invisible character
			{"call-abc", "call-abc\n"},     // a trailing newline
			{"call-abc", "call\u202e-abc"}, // a bidirectional override
			{"call abc", "call  abc"},      // a run the name rule would fold
			{"call-abc", " call-abc"},      // an edge the name rule would trim
			{"a\u200bb", "ab"},             // a key and the old strip's output for it
		} {
			require.NoErrorf(t, ValidateRowKey(pair[0]), "%q must be accepted for this case to bite", pair[0])
			require.NoErrorf(t, ValidateRowKey(pair[1]), "%q must be accepted for this case to bite", pair[1])
			assert.NotEqualf(t, pair[0], pair[1],
				"%q and %q must stay two keys: one registry row for two tasks loses one of them", pair[0], pair[1])
		}
	})

	// Refused, and refused rather than cut. A cap is not injective -- no total
	// function onto a bounded set is -- so the bound is kept by saying no.
	t.Run("refuses a key past the byte limit", func(t *testing.T) {
		err := ValidateRowKey(strings.Repeat("a", RowKeyByteLimit+1))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "must be at most 256 bytes")
		assert.Contains(t, err.Error(), "got 257", "the message must say how far over the key is")

		assert.Error(t, ValidateRowKey(strings.Repeat("a", 100_000)))

		// The limit counts BYTES. 86 three-byte runes is 258 bytes and 86
		// characters, so a character count would have accepted it.
		assert.NoError(t, ValidateRowKey(strings.Repeat("中", 85)))
		assert.Error(t, ValidateRowKey(strings.Repeat("中", 86)))
	})

	// BackgroundTaskItem.id is a proto `string`. A marshal of an invalid one
	// fails the WHOLE registry broadcast, so this is the one rewrite that
	// would be worth its collision -- and refusing costs nothing, because a
	// provider that emits an invalid byte in an identifier is already broken.
	t.Run("refuses invalid UTF-8", func(t *testing.T) {
		for _, key := range []string{"a\xffb", "\xff", "a\xed\xa0\x80b", "\xc0\x80"} {
			err := ValidateRowKey(key)
			require.Errorf(t, err, "%q must be refused", key)
			assert.Contains(t, err.Error(), "must be valid UTF-8")
		}

		// A U+FFFD the provider sent deliberately is valid UTF-8, so it is an
		// ordinary key. The rule tells the two apart by the ENCODING.
		assert.NoError(t, ValidateRowKey("a�b"))
	})
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
