package cmd

import (
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/cli"
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

func TestAppliedFromSettlements_ClassifiesRequestedKeys(t *testing.T) {
	model := "sonnet"
	requested := map[string]string{
		optionids.Model:  "sonnet",
		optionids.Effort: "high",
		"service_tier":   "fast",
	}
	settlements := map[string]*leapmuxv1.AgentOptionSettlement{
		optionids.Model: {
			State: leapmuxv1.AgentOptionSettlementState_AGENT_OPTION_SETTLEMENT_STATE_CONFIRMED,
			Value: &model,
		},
		optionids.Effort: {
			State: leapmuxv1.AgentOptionSettlementState_AGENT_OPTION_SETTLEMENT_STATE_CONFIRMED,
		},
		"service_tier": {
			State: leapmuxv1.AgentOptionSettlementState_AGENT_OPTION_SETTLEMENT_STATE_UNRESOLVED,
		},
	}

	applied, notApplied, unresolved := appliedFromSettlements(requested, settlements)

	assert.Equal(t, map[string]string{
		optionids.Model: "sonnet", // settled value reported
	}, applied)
	assert.Equal(t, []string{optionids.Effort}, notApplied,
		"a confirmed removal is reported as not applied")
	assert.Equal(t, []string{"service_tier"}, unresolved)
}

func TestAppliedFromSettlements_EmptyRequestReportsNothing(t *testing.T) {
	applied, notApplied, unresolved := appliedFromSettlements(map[string]string{}, nil)
	assert.Empty(t, applied)
	assert.Empty(t, notApplied)
	assert.Empty(t, unresolved)
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

// TestBuildAgentSetOptions_ModelAloneSendsNoOtherAxis pins the request of `agent set --model B`. It
// carries the model and no other axis, so the worker decides what a model switch does to the effort
// and to every other axis that the caller did not name.
func TestBuildAgentSetOptions_ModelAloneSendsNoOtherAxis(t *testing.T) {
	opts, err := buildAgentSetOptions("sonnet", "", "", nil)
	require.NoError(t, err)
	assert.Equal(t, map[string]string{optionids.Model: "sonnet"}, opts)
}

// An empty --option, such as an unset variable in a script, is a usage error
// before the command sends anything. It never vanishes, which would apply the
// other flags alone and report success.
func TestAgentSet_RefusesAnEmptyOption(t *testing.T) {
	clearRemoteEnv(t)
	f := runControl(t, "agent", "set", fullTestID("ag-1"), "--model", "sonnet", "--option", "", "--hub", "https://stub.invalid").
		requireFailure(t, cli.CodeUsage)
	assert.Contains(t, f.Message, `expected key=value, got ""`)
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

func TestSplitKV_TrimsAndRejectsWhitespaceOnly(t *testing.T) {
	k, v, err := splitKV("  foo = bar  ")
	require.NoError(t, err)
	assert.Equal(t, "foo", k)
	assert.Equal(t, "bar", v)

	_, _, err = splitKV("   =value")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "empty option key")

	_, _, err = splitKV("key=   ")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "empty option value")
}

func TestBuildAgentSetOptions_RejectsWhitespaceDedicatedFlagsAndTrimmedDuplicates(t *testing.T) {
	_, err := buildAgentSetOptions("   ", "high", "", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--model is empty")

	_, err = buildAgentSetOptions("", "high", "", []string{" effort = low "})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "set more than once")
}

func TestAgentSet_RejectsExplicitEmptyOrWhitespaceDedicatedFlags(t *testing.T) {
	clearRemoteEnv(t)
	t.Setenv("LEAPMUX_CONTROL_CONFIG_DIR", t.TempDir())
	agID := fullTestID("ag-set-flags")

	for name, args := range map[string][]string{
		"empty model with effort":          {"agent", "set", agID, "--model", "", "--effort", "high"},
		"whitespace model with effort":     {"agent", "set", agID, "--model", "   ", "--effort", "high"},
		"whitespace effort with model":     {"agent", "set", agID, "--model", "sonnet", "--effort", " \t "},
		"empty permission-mode with model": {"agent", "set", agID, "--model", "sonnet", "--permission-mode", ""},
		"whitespace option key with model": {"agent", "set", agID, "--model", "sonnet", "--option", "  =val"},
		"whitespace option val with model": {"agent", "set", agID, "--model", "sonnet", "--option", "key=  "},
	} {
		t.Run(name, func(t *testing.T) {
			runControl(t, append(args, "--hub", "https://stub.invalid")...).requireFailure(t, cli.CodeUsage)
		})
	}
}

func TestRunAgentSet_PreflightsMissingWorkerBeforeDialing(t *testing.T) {
	agID := fullTestID("ag-set-missing-worker")
	w := &agentOpsDispatcher{}
	startSpawnIPC(t, &recordingHub{
		workers: workerRows("worker-other"),
		locateTab: &leapmuxv1.WorkspaceTab{
			Tab:         &leapmuxv1.TabIdent{TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: agID},
			TileId:      "root-1",
			WorkerId:    "worker-missing",
			WorkspaceId: "ws-1",
		},
	}, w)

	f := runControl(t, "agent", "set", agID, "--model", "sonnet").requireFailure(t, cli.CodeNotFound)
	assert.Contains(t, f.Message, "no such worker: worker-missing")
	assert.Empty(t, w.recordedCalls())
}
