package cmd

import (
	"testing"

	"github.com/leapmux/leapmux/internal/util/optionids"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSplitKV_HappyPath covers the simplest case: one '=' produces
// (key, value, nil).
func TestSplitKV_HappyPath(t *testing.T) {
	k, v, err := splitKV("foo=bar")
	require.NoError(t, err)
	assert.Equal(t, "foo", k)
	assert.Equal(t, "bar", v)
}

// TestSplitKV_FirstEqualsWins pins the "value contains '='" case
// (e.g. base64-encoded values, query strings). Splitting on every
// '=' would corrupt values like "csrf=abc=def==".
func TestSplitKV_FirstEqualsWins(t *testing.T) {
	k, v, err := splitKV("csrf=abc=def==")
	require.NoError(t, err)
	assert.Equal(t, "csrf", k)
	assert.Equal(t, "abc=def==", v)
}

// TestSplitKV_EmptyValueRejected pins that `--option key=` fails fast at parse time rather
// than parsing to an empty value. The worker treats an empty option value on the edit path as a
// NO-OP, not a clear (see sanitizeIncomingOptions's "not a clear" guard), so a permissive parse
// would report "applied" with the assignment silently vanished; clearing is not supported here.
func TestSplitKV_EmptyValueRejected(t *testing.T) {
	_, _, err := splitKV("key=")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "empty option value")
}

// TestSplitKV_EmptyKeyRejected pins the symmetric edge: a leading '=' (`--option =value`) is an
// empty key, which would persist no axis (the worker drops an unknown id). Reject it at parse
// time so the malformed flag is an error instead of a silent drop.
func TestSplitKV_EmptyKeyRejected(t *testing.T) {
	_, _, err := splitKV("=value")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "empty option key")
}

// TestSplitKV_NoEqualsRejected covers the user error case:
// `--option key` without a value should produce a clear hint
// rather than silently mapping to "key" → "" or vice versa.
func TestSplitKV_NoEqualsRejected(t *testing.T) {
	_, _, err := splitKV("flag-only")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "key=value")
}

// TestSplitKV_EmptyStringRejected pins the trivial bad case so we
// don't accept an entirely empty `--option` value silently.
func TestSplitKV_EmptyStringRejected(t *testing.T) {
	_, _, err := splitKV("")
	require.Error(t, err)
}

// TestAppliedFromConfirmed_FiltersToRequestedKeys is the regression guard for the `applied`
// report: the worker's confirmed map is the FULL option set, so the CLI must project it onto
// ONLY the axes the user requested. A requested axis the provider did not apply (here effort,
// baked into the new model id and therefore absent from confirmed) is reported under notApplied
// -- not as an `applied` entry with a cryptic "" -- so the user plainly sees it did not take;
// inherited axes the user never set (permission mode, a provider-default sandbox) must NOT leak
// into either list.
func TestAppliedFromConfirmed_FiltersToRequestedKeys(t *testing.T) {
	requested := map[string]string{optionids.Model: "sonnet", optionids.Effort: "high"}
	confirmed := map[string]string{
		optionids.Model: "sonnet",
		// effort is absent: the settled model bakes its effort into the id, so the worker
		// stripped the requested tier.
		optionids.PermissionMode: "plan",            // inherited, never requested
		"sandbox":                "workspace-write", // a provider default, never requested
	}

	applied, notApplied := appliedFromConfirmed(requested, confirmed)

	assert.Equal(t, map[string]string{
		optionids.Model: "sonnet", // settled value reported
	}, applied, "only the requested axes that settled are reported; inherited ones don't leak")
	assert.Equal(t, []string{optionids.Effort}, notApplied,
		"a requested axis absent from confirmed is reported as not-applied, not as an empty applied value")
}

// TestAppliedFromConfirmed_EmptyRequestReportsNothing pins the trivial edge: with no requested
// axes both lists are empty regardless of how full the worker's confirmed map is.
func TestAppliedFromConfirmed_EmptyRequestReportsNothing(t *testing.T) {
	applied, notApplied := appliedFromConfirmed(map[string]string{}, map[string]string{optionids.Model: "sonnet"})
	assert.Empty(t, applied)
	assert.Empty(t, notApplied)
}

// TestStringSliceFlag_AccumulatesValues is the multi-pass `flag.Value`
// contract: each `Set` appends so `--option a=1 --option b=2`
// produces ["a=1", "b=2"].
func TestStringSliceFlag_AccumulatesValues(t *testing.T) {
	s := stringSliceFlag{}
	require.NoError(t, s.Set("a=1"))
	require.NoError(t, s.Set("b=2"))
	require.NoError(t, s.Set("c=3"))
	assert.Equal(t, []string{"a=1", "b=2", "c=3"}, s.values)
}

// TestStringSliceFlag_StringIsHumanReadable covers the small
// `String()` implementation. flag.PrintDefaults uses this for help
// text; an unreadable representation would confuse users running
// `leapmux remote agent set --help`.
func TestStringSliceFlag_StringIsHumanReadable(t *testing.T) {
	s := stringSliceFlag{values: []string{"a=1", "b=2"}}
	got := s.String()
	assert.Contains(t, got, "a=1")
	assert.Contains(t, got, "b=2")
}

// TestStringSliceFlag_EmptyStartsAsNilNotEmptySlice pins the
// initial state so callers can branch on `len(extras.values) == 0`
// without worrying about a non-nil-but-empty distinction.
func TestStringSliceFlag_EmptyStartsAsNilNotEmptySlice(t *testing.T) {
	s := stringSliceFlag{}
	assert.Empty(t, s.values)
	assert.Nil(t, s.values)
}

// TestBuildAgentSetOptions_MergesFlagsAndOptions verifies the dedicated --model/--effort/
// --permission-mode flags and the repeatable --option key=value merge into one map keyed by
// option-group id.
func TestBuildAgentSetOptions_MergesFlagsAndOptions(t *testing.T) {
	opts, err := buildAgentSetOptions("opus", "high", "plan", []string{"sandbox=workspace-write"})
	require.NoError(t, err)
	assert.Equal(t, map[string]string{
		optionids.Model:          "opus",
		optionids.Effort:         "high",
		optionids.PermissionMode: "plan",
		"sandbox":                "workspace-write",
	}, opts)
}

// TestBuildAgentSetOptions_OptionFormOfWellKnownAxisAlone allows setting a well-known axis via
// --option WITHOUT its dedicated flag -- only a DUPLICATE assignment is rejected, not the generic
// form on its own.
func TestBuildAgentSetOptions_OptionFormOfWellKnownAxisAlone(t *testing.T) {
	opts, err := buildAgentSetOptions("", "", "", []string{"model=sonnet"})
	require.NoError(t, err)
	assert.Equal(t, "sonnet", opts[optionids.Model])
}

// TestBuildAgentSetOptions_RejectsDuplicateKey is the [G5] guard: a key set both by a dedicated
// flag and an --option, or by two --options, is rejected so a contradictory pair can't silently
// resolve to whichever assignment is applied last.
func TestBuildAgentSetOptions_RejectsDuplicateKey(t *testing.T) {
	// Dedicated --effort and --option effort= for the same axis.
	_, err := buildAgentSetOptions("", "high", "", []string{"effort=low"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "set more than once")

	// Two --options for the same key.
	_, err = buildAgentSetOptions("", "", "", []string{"sandbox=read-only", "sandbox=workspace-write"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "set more than once")

	// A malformed --option still surfaces the splitKV parse error.
	_, err = buildAgentSetOptions("", "", "", []string{"no-equals"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "key=value")
}
