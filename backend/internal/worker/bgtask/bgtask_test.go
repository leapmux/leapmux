package bgtask

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

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

// NormalizeRowKey is total: it answers every refusal ValidateRowKey states, so
// a provider's unusable key costs the key's readability and never the row.
//
// The three properties that let a derived key BE an identity: it is a function
// of the provider's key alone (so a later close finds the row the upsert
// opened, in another process), it is injective in practice (so two keys never
// merge into one row), and it satisfies ValidateRowKey itself (so the next
// write does not refuse it again).
func TestNormalizeRowKey(t *testing.T) {
	t.Parallel()

	unusable := []string{
		strings.Repeat("a", RowKeyByteLimit+1),
		strings.Repeat("k", 100_000),
		"a\xffb",
		"\xc0\x80",
	}

	t.Run("returns a usable key unchanged", func(t *testing.T) {
		// Every shape the rule accepts, including the ones a reader cannot see:
		// rewriting any of them is the merge this package refuses to do.
		for _, key := range []string{
			"", "task-1", "call-abc\nfc-def", "a\u202eb", " a  b ",
			strings.Repeat("a", RowKeyByteLimit),
		} {
			assert.Equalf(t, key, NormalizeRowKey(key), "%q is usable and must pass through", key)
		}
	})

	t.Run("derives a usable key for one that is not", func(t *testing.T) {
		for _, key := range unusable {
			got := NormalizeRowKey(key)
			require.NotEqualf(t, key, got, "%q must not pass through", key)
			assert.NoErrorf(t, ValidateRowKey(got), "the derived key for %q must itself be storable", key)
			assert.NotEmpty(t, got, "an unusable key must not become the empty key, which means NO linkage")
			assert.True(t, utf8.ValidString(got), "the derived key ships as a proto string")
		}
	})

	t.Run("is a function of the key alone", func(t *testing.T) {
		// The upsert that opens the row and the close that finishes it run in
		// different calls, and after a restart in a different process.
		for _, key := range unusable {
			assert.Equal(t, NormalizeRowKey(key), NormalizeRowKey(key))
		}
	})

	t.Run("is stable under a second pass", func(t *testing.T) {
		// applyAndBroadcast normalizes a key EnsureChildAgent may already have
		// normalized. A second pass that moved the key would orphan the row.
		for _, key := range unusable {
			once := NormalizeRowKey(key)
			assert.Equal(t, once, NormalizeRowKey(once))
		}
	})

	// The property that justified refusing rather than cutting. Providers build
	// keys by prefixing a fixed namespace, so two over-long keys sharing their
	// first RowKeyByteLimit bytes is the ORDINARY case -- and a cut maps them
	// onto one row, which silently loses one background task.
	t.Run("keeps two keys apart that a cut would merge", func(t *testing.T) {
		prefix := strings.Repeat("p", RowKeyByteLimit)
		assert.NotEqual(t, NormalizeRowKey(prefix+"-one"), NormalizeRowKey(prefix+"-two"))
		// Differing in the last byte alone is still two keys.
		assert.NotEqual(t, NormalizeRowKey(prefix+"a"), NormalizeRowKey(prefix+"b"))
		// And an invalid byte does not collapse two otherwise distinct keys.
		assert.NotEqual(t, NormalizeRowKey("a\xffb"), NormalizeRowKey("a\xffc"))
	})
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

	// BackgroundTaskItem.id is a proto `string`, and a marshal of an invalid one
	// fails the WHOLE registry broadcast. The refusal is what NormalizeRowKey
	// answers, so the row survives under a derived key instead of vanishing --
	// see TestNormalizeRowKey.
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

func TestUpsertClean(t *testing.T) {
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
			got := Upsert{Title: tc.title}.Clean()
			assert.Equal(t, tc.want, got.Title)
			// The rule runs again on its own output without changing it, which
			// is what lets EnsureChildAgent clean once and the applier clean
			// again on the same string.
			assert.Equal(t, got, got.Clean(), "Clean must be idempotent")
		})
	}
}

// A shell command handed over as a title keeps every character that makes it
// runnable. This is the case the rule relaxed for: one rule for every title
// column used to turn `npm test --grep "$FOO"` into `npm test --grep FOO`, and
// the row then identified a command that nobody ran.
func TestUpsertCleanKeepsAShellCommandWhole(t *testing.T) {
	const command = `npm test --grep "$FOO" 100% c:\tmp`
	got := Upsert{Title: command, TitleIsCommand: true}.Clean()
	assert.Equal(t, command, got.Title)
	assert.True(t, got.TitleIsCommand)
}

// A title the rule empties clears the command flag with it: the flag describes
// a title, and there is none left to describe.
func TestUpsertCleanEmptiedTitleClearsTheCommandFlag(t *testing.T) {
	got := Upsert{Title: "  \x00\u200b\ufeff  ", TitleIsCommand: true}.Clean()
	assert.Empty(t, got.Title)
	assert.False(t, got.TitleIsCommand)
}

// An emptied title is the same blank a caller sends deliberately, so the merge
// answers both the same way: the row keeps the title it already holds.
func TestUpsertCleanEmptiedTitleKeepsTheStoredOne(t *testing.T) {
	existing := Item{Title: "Ship the parser", TitleIsCommand: false}
	merged := Upsert{Title: "  \x00\u200b\ufeff  ", TitleIsCommand: true}.Clean().ToItem().PreservingBlanksFrom(existing)
	assert.Equal(t, "Ship the parser", merged.Title)
	assert.False(t, merged.TitleIsCommand, "the stored pair is restored whole")
}

// A blank group LABEL is a blank like every other descriptive field: a partial
// upsert that omits it must not erase the heading it already set.
//
// The key alone did not cover this. A provider that sends the group key on
// every update but the workflow name only on the first one blanked the heading
// from the second update onward, and Upsert.Clean reaches the same state by
// cleaning a label of nothing but invisible characters to "".
func TestPreservingBlanksKeepsTheGroupLabelUnderTheSameKey(t *testing.T) {
	t.Parallel()

	existing := Item{GroupKey: "workflow:build", GroupLabel: "Build"}

	t.Run("a label the caller omitted", func(t *testing.T) {
		merged := Item{GroupKey: "workflow:build"}.PreservingBlanksFrom(existing)
		assert.Equal(t, "workflow:build", merged.GroupKey)
		assert.Equal(t, "Build", merged.GroupLabel)
	})

	t.Run("a label Clean emptied", func(t *testing.T) {
		merged := Upsert{GroupKey: "workflow:build", GroupLabel: "  \u200b\ufeff  "}.
			Clean().ToItem().PreservingBlanksFrom(existing)
		assert.Equal(t, "Build", merged.GroupLabel)
	})

	t.Run("a label the caller sent is not replaced", func(t *testing.T) {
		merged := Item{GroupKey: "workflow:build", GroupLabel: "Rebuild"}.PreservingBlanksFrom(existing)
		assert.Equal(t, "Rebuild", merged.GroupLabel)
	})
}

// The restore is GUARDED BY THE KEY, because the label names THAT group.
// Carrying the old heading onto a row that just moved to a new group is worse
// than a missing one: the reader cannot tell that it is wrong.
func TestPreservingBlanksDoesNotCarryAGroupLabelToANewKey(t *testing.T) {
	t.Parallel()

	existing := Item{GroupKey: "workflow:build", GroupLabel: "Build"}
	merged := Item{GroupKey: "workflow:deploy"}.PreservingBlanksFrom(existing)

	assert.Equal(t, "workflow:deploy", merged.GroupKey)
	assert.Empty(t, merged.GroupLabel, "the stored heading names the group the row LEFT")
}

// A blank KEY still keeps both: a partial upsert cannot clear a group, and the
// pair travels together so a kept key never loses the heading that names it.
func TestPreservingBlanksKeepsTheWholePairForABlankKey(t *testing.T) {
	t.Parallel()

	merged := Item{}.PreservingBlanksFrom(Item{GroupKey: "workflow:build", GroupLabel: "Build"})
	assert.Equal(t, "workflow:build", merged.GroupKey)
	assert.Equal(t, "Build", merged.GroupLabel)
}

// CleanTitle touches the title pair and nothing else.
func TestUpsertCleanLeavesEveryOtherFieldAlone(t *testing.T) {
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
	assert.Equal(t, u, u.Clean())
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

// One provider field with an invalid byte must cost that field's readability
// and nothing else.
//
// `proto.Marshal` fails the WHOLE message for one bad string, and
// `broadcastBackgroundTasks` puts the entire registry in one message -- so the
// unrepaired version dropped every row from the sidebar, and kept dropping
// them, because sqlite stores the bad bytes verbatim and the cold-start seed
// reads them back on every worker boot.
func TestItemsToProtoSurvivesAnInvalidUTF8ProviderField(t *testing.T) {
	t.Parallel()

	rows := []Item{
		{RowKey: "ok-1", Title: "Ship the parser"},
		{
			RowKey: "bad-1", GroupKey: "workflow:build", GroupLabel: "g\xffl",
			Title: "t\xffl", Description: "d\xffd", ActiveForm: "a\xffb",
		},
		{RowKey: "ok-2", Title: "Also fine"},
	}

	out := ItemsToProto(rows)
	_, err := proto.Marshal(&leapmuxv1.AgentBackgroundTasksChanged{AgentId: "root-1", Tasks: out})
	require.NoError(t, err, "one bad field must not drop the whole registry broadcast")

	require.Len(t, out, 3)
	assert.Equal(t, "workflow:build", out[1].GetGroupKey(),
		"the group key is a join identity, so it ships exactly as the provider wrote it")
	assert.Equal(t, "gl", out[1].GetGroupLabel())
	assert.Equal(t, "tl", out[1].GetTitle())
	assert.Equal(t, "dd", out[1].GetDescription())
	assert.Equal(t, "ab", out[1].GetActiveForm())
	assert.Equal(t, "Ship the parser", out[0].GetTitle(), "a clean row is untouched")

	// A U+FFFD the provider sent deliberately is valid UTF-8 and survives.
	kept := ItemsToProto([]Item{{RowKey: "x", ActiveForm: "a�b"}})
	assert.Equal(t, "a�b", kept[0].GetActiveForm())
}

// An unusable GROUP KEY costs the grouping, never the broadcast and never the
// key's meaning.
//
// The key joins rows into one heading on the client, so repairing it would put
// two providers' groups under one heading while the worker's own cache kept
// them apart. Shipping it unrepaired is worse still: one invalid byte fails the
// marshal of the whole registry message and empties the sidebar. Dropping the
// grouping is the only answer that costs neither.
//
// `Upsert.Clean` already clears one at the write boundary, so no Item built by
// an ordinary path carries an unusable key. This states the same rule at the
// projection, where the cost of being wrong is every row rather than one.
func TestItemToProtoDropsAnUnusableGroupKeyRatherThanRepairingIt(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct{ name, key string }{
		{"invalid UTF-8", "g\xffk"},
		{"past the byte limit", strings.Repeat("g", RowKeyByteLimit+1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := Item{RowKey: "r", GroupKey: tc.key, GroupLabel: "CI", Title: "t"}.ToProto()

			assert.Empty(t, out.GetGroupKey(), "an unusable key must not reach the wire in any form")
			assert.Empty(t, out.GetGroupLabel(), "a heading that names no group has nothing to name")
			assert.Equal(t, "t", out.GetTitle(), "the row itself survives, ungrouped")

			_, err := proto.Marshal(&leapmuxv1.AgentBackgroundTasksChanged{AgentId: "root-1", Tasks: []*leapmuxv1.BackgroundTaskItem{out}})
			require.NoError(t, err, "the whole registry broadcast must still marshal")
		})
	}
}

// A USABLE group key is an identity and reaches the wire byte for byte, even
// when it holds characters a label would lose. Two providers' keys that differ
// only in an invisible character must stay two headings.
func TestItemToProtoShipsAUsableGroupKeyVerbatim(t *testing.T) {
	t.Parallel()

	for _, key := range []string{"workflow:build", "call-a\u200bbc", "call-\u202eghi", "call-abc\nfc-def"} {
		assert.Equal(t, key, Item{RowKey: "r", GroupKey: key}.ToProto().GetGroupKey(), key)
	}
}

// Every provider-chosen LABEL field is bounded, for the reason the row key is:
// the provider picks the length, the row lives for the agent's life, and every
// registry broadcast carries it again.
func TestUpsertCleanCapsTheLabelFields(t *testing.T) {
	t.Parallel()

	long := strings.Repeat("x", LabelByteLimit*2)
	// A USABLE group key, so this case measures the label cap and nothing else.
	// An unusable one drops its label outright, which is a different rule --
	// TestUpsertCleanDropsAnUnusableGroupKey covers that.
	got := Upsert{
		RowKey:      "k-" + long,
		GroupKey:    "workflow:build",
		ActiveForm:  long,
		Description: long,
		GroupLabel:  long,
	}.Clean()

	assert.Len(t, got.ActiveForm, LabelByteLimit)
	assert.Len(t, got.Description, LabelByteLimit)
	assert.Len(t, got.GroupLabel, LabelByteLimit)

	// The ROW key passes through byte-identical. A cut is non-injective, which
	// is why ValidateRowKey refuses an unusable key rather than shortening it --
	// shortening two distinct keys onto one merges two rows. The sink refuses
	// the write; `Clean` is not where that happens.
	assert.Equal(t, "k-"+long, got.RowKey)
	assert.Equal(t, "workflow:build", got.GroupKey, "a usable group key is untouched")
}

// A group key the registry cannot use costs the GROUPING and nothing else.
func TestUpsertCleanDropsAnUnusableGroupKey(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name     string
		groupKey string
	}{
		{"past the byte limit", strings.Repeat("g", RowKeyByteLimit+1)},
		{"invalid UTF-8, which fails the whole registry marshal", "wf:\xff"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := Upsert{
				RowKey:     "row-1",
				Title:      "Run the tests",
				GroupKey:   tc.groupKey,
				GroupLabel: "Test workflow",
			}.Clean()

			assert.Empty(t, got.GroupKey)
			assert.Empty(t, got.GroupLabel)
			// The ROW survives whole. That is the difference from a row key.
			assert.Equal(t, "row-1", got.RowKey)
			assert.Equal(t, "Run the tests", got.Title)
		})
	}

	// A usable group key is untouched, key and label alike.
	kept := Upsert{RowKey: "row-1", GroupKey: "workflow:build", GroupLabel: "Build"}.Clean()
	assert.Equal(t, "workflow:build", kept.GroupKey)
	assert.Equal(t, "Build", kept.GroupLabel)
}

// ValidateGroupKey and ValidateRowKey answer the same class, because the two
// values are the same KIND -- a provider-chosen identity the registry joins on.
// Only what the CALLER does with the refusal differs.
func TestGroupKeyAndRowKeyShareOneClass(t *testing.T) {
	t.Parallel()

	for _, s := range []string{
		"",
		"ordinary-key",
		"a\nb",
		strings.Repeat("k", RowKeyByteLimit),
		strings.Repeat("k", RowKeyByteLimit+1),
		"bad\xffutf8",
	} {
		assert.Equalf(t, ValidateRowKey(s) == nil, ValidateGroupKey(s) == nil,
			"the two identities must accept and refuse the same values: %q", s)
	}
}

// The label cap keeps whitespace: `Description` carries a file path and
// `ActiveForm` carries prose, so folding a run would change what they mean.
func TestUpsertCleanKeepsLabelWhitespace(t *testing.T) {
	t.Parallel()

	got := Upsert{Description: "/tmp/a  b/c.md", ActiveForm: "step  one"}.Clean()
	assert.Equal(t, "/tmp/a  b/c.md", got.Description)
	assert.Equal(t, "step  one", got.ActiveForm)
}

// A GROUP HEADING takes the title rule, not its siblings'. It is one line of
// model-written prose with no structure to keep, so it trims and folds -- and
// that is what lets a heading of nothing but invisible characters reach the
// empty string, where PreservingBlanksFrom restores the stored one. Under the
// sibling rule it stripped to a run of SPACES, which is not "", so the group
// drew an empty heading over a stored name that was still good.
func TestUpsertCleanFoldsTheGroupHeading(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "Build the parser",
		Upsert{GroupLabel: "  Build  the   parser  "}.Clean().GroupLabel)
	assert.Empty(t, Upsert{GroupLabel: "  \u200b\ufeff  "}.Clean().GroupLabel,
		"a heading with nothing a reader can see is a blank, not a run of spaces")
	// Still capped at the LABEL limit, not the tab-title limit: the scan stop
	// travels with the byte limit, and a fixed one cut every heading to 128.
	assert.Len(t, Upsert{GroupLabel: strings.Repeat("x", LabelByteLimit*2)}.Clean().GroupLabel, LabelByteLimit)
}

// A LINE BREAK is whitespace too, and the label rule keeps it for the reason it
// keeps a space run: a reader SEES the gap it makes. Deleting it left nothing in
// its place and glued the words on either side into one token, which is what a
// sidebar row then showed.
//
// The GROUP LABEL is the exception, and it FOLDS the break to one space. It is
// a heading rather than prose: it draws on one line, so a break in it has no
// gap to make, and the reader folds it anyway on the way to the screen. Folding
// at the write point is what keeps the stored value and the drawn one the same
// string -- and it is what lets a heading of nothing but invisible characters
// reach "" and take the blank-means-keep rule.
func TestUpsertCleanKeepsALineBreakInALabel(t *testing.T) {
	t.Parallel()

	got := Upsert{
		ActiveForm:  "Running tests\nfor the parser",
		Description: "line one\nline two",
		GroupLabel:  "group\nlabel",
	}.Clean()

	assert.Equal(t, "Running tests\nfor the parser", got.ActiveForm,
		"deleting the newline read as `Running testsfor the parser`")
	assert.Equal(t, "line one\nline two", got.Description)
	assert.Equal(t, "group label", got.GroupLabel,
		"a heading draws on one line, so the break folds rather than glues")

	// The NON-whitespace controls still go, so a bidirectional override cannot
	// reorder what the sidebar shows.
	reordered := Upsert{ActiveForm: "safe\u202ereversed\x00"}.Clean()
	assert.Equal(t, "safereversed", reordered.ActiveForm)
}
