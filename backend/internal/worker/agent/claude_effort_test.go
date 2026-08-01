package agent

// Effort-resolver tests and the synthetic catalog they share. Deliberately NOT
// under claude_test.go's `//go:build unix` constraint: shell_test.go builds on
// every platform and calls effortTestCatalog, and the resolver these tests
// cover decides the --model/--effort flags a Claude launch carries on Windows
// just as much as on Unix.

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/leapmux/leapmux/internal/util/ptrconv"
)

// maxOnlyModelID names a synthetic catalog entry that supports max but NOT
// xhigh. The real catalog no longer has such a model -- the CLI reports xhigh
// for every effort-capable Claude model, so the static fallback lists it too --
// but the downgrade path those cases exercise is still live code, and the CLI
// can report a narrower level set again at any time. Testing it against a
// synthetic entry keeps the coverage without pinning a claim about Sonnet that
// the CLI contradicts.
const maxOnlyModelID = "maxonly"

// maxOnlyEfforts is the menu a model that supports max but NOT xhigh gets. No
// model in the current catalog is in that position -- the CLI reports xhigh for
// every effort-capable model -- so it lives here rather than in production: it
// is the shape the live conversion builds from a level set without xhigh, and
// TestConvertClaudeModels_DedupAndEffortOrdering pins that it still does
// ("auto", "max", "high", "medium", "low") straight from the CLI's report.
var maxOnlyEfforts = []*EffortInfo{
	effortTierAuto, effortTierMax, effortTierHigh, effortTierMedium, effortTierLow,
}

// effortTestCatalog is the real catalog plus {@link maxOnlyModelID}.
func effortTestCatalog() []*ModelInfo {
	out := make([]*ModelInfo, 0, len(claudeCodeAvailableModels)+1)
	out = append(out, claudeCodeAvailableModels...)
	return append(out, &ModelInfo{
		Id: maxOnlyModelID, DisplayName: "Max Only",
		DefaultEffort: EffortHigh, SupportedEfforts: maxOnlyEfforts,
	})
}

func TestClaudeEffortUpdateFlagSettings(t *testing.T) {
	tests := []struct {
		name        string
		targetModel string
		newEffort   string
		curEffort   string
		expected    map[string]interface{}
	}{
		{
			// Model-only change off opus+ultracode onto a model without xhigh: no
			// effort delta, but the stale ultracode boolean must be cleared AND the
			// stale effortLevel pinned to a level that model supports. Clearing only
			// the boolean would leave the CLI at the ultracode base "xhigh" (which the
			// CLI does not re-resolve on a model-only change), so we also pin
			// effortLevel to xhigh resolved for that model -> "high" (its default).
			name:        "model-only switch leaving ultracode for an unsupported model clears the flag and pins a safe effort",
			targetModel: maxOnlyModelID,
			newEffort:   "",
			curEffort:   "ultracode",
			expected:    map[string]interface{}{"effortLevel": "high", "ultracode": false},
		},
		{
			// Model-only change to another ultracode-capable model keeps the
			// combo: nothing to send (the model key is added by the caller).
			name:        "model-only switch between ultracode-capable models is a no-op",
			targetModel: "opus[1m]",
			newEffort:   "",
			curEffort:   "ultracode",
			expected:    nil,
		},
		{
			// Combined change already routes through claudeEffortFlagSettings,
			// which clears ultracode when leaving it; the guard is idempotent.
			name:        "combined model+effort leaving ultracode clears the flag once",
			targetModel: "sonnet",
			newEffort:   "high",
			curEffort:   "ultracode",
			expected:    map[string]interface{}{"effortLevel": "high", "ultracode": false},
		},
		{
			// Requesting ultracode on a model that can't run it downgrades the
			// level to high and never sets the ultracode boolean true (sent here
			// because high differs from the current "medium").
			name:        "ultracode requested on unsupported model downgrades without setting the flag",
			targetModel: maxOnlyModelID,
			newEffort:   "ultracode",
			curEffort:   "medium",
			expected:    map[string]interface{}{"effortLevel": "high"},
		},
		{
			name:        "enabling ultracode on a supported model sets the combo",
			targetModel: "opus",
			newEffort:   "ultracode",
			curEffort:   "high",
			expected:    map[string]interface{}{"effortLevel": "xhigh", "ultracode": true},
		},
		{
			name:        "ordinary effort change is unaffected",
			targetModel: "sonnet",
			newEffort:   "max",
			curEffort:   "high",
			expected:    map[string]interface{}{"effortLevel": "max"},
		},
		{
			name:        "no requested effort and not leaving ultracode is a no-op",
			targetModel: "sonnet",
			newEffort:   "",
			curEffort:   "high",
			expected:    nil,
		},
		{
			// An unknown model (in NEITHER the dynamic nor the static catalog -- e.g.
			// one the live CLI filtered into unavailable_models but the session is still
			// running) at ultracode is TRUSTED, mirroring effortFromApplied/
			// trustCLIUltracodeReport: the CLI is the authority on what it actually
			// applied, so a model-only or unrelated live update must NOT strip the
			// ultracode boolean just because the catalog can't confirm xhigh support.
			// Without the `known &&` gate this would emit {ultracode:false,
			// effortLevel:"xhigh"} and silently downgrade a session the CLI is happily
			// running at ultracode, contradicting the decode side that trusts the same
			// model. The two paths must agree.
			name:        "model-only update on an unknown model running ultracode preserves the flag",
			targetModel: "ghost",
			newEffort:   "",
			curEffort:   "ultracode",
			expected:    nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, newEffortResolver(effortTestCatalog()).updateFlagSettings(tt.targetModel, tt.newEffort, tt.curEffort))
		})
	}
}

func TestClaudeEffortFromApplied(t *testing.T) {
	tests := []struct {
		name      string
		applied   *string
		ultracode *bool
		curEffort string
		model     string
		want      string
	}{
		{
			name:      "opus ultracode:true maps xhigh-base back to ultracode",
			applied:   ptrconv.Ptr("xhigh"),
			ultracode: ptrconv.Ptr(true),
			curEffort: "xhigh",
			model:     "opus",
			want:      "ultracode",
		},
		{
			name:      "opus[1m] ultracode:true maps to ultracode",
			applied:   ptrconv.Ptr("xhigh"),
			ultracode: ptrconv.Ptr(true),
			curEffort: "ultracode",
			model:     "opus[1m]",
			want:      "ultracode",
		},
		{
			name:      "ultracode:true is ignored on a model that cannot run it",
			applied:   ptrconv.Ptr("xhigh"),
			ultracode: ptrconv.Ptr(true),
			curEffort: "high",
			model:     maxOnlyModelID,
			want:      "xhigh", // keep the reported level; never mislabel it as ultracode
		},
		{
			// S1: a model the catalog does NOT know (e.g. the CLI reported it in
			// unavailable_models, so convertClaudeModels filtered it) is trusted when
			// the CLI confirms ultracode:true -- we don't relabel a running ultracode
			// session to xhigh just because its model dropped out of the catalog. This
			// differs from the max-only model above, which the catalog KNOWS lacks
			// ultracode.
			name:      "unknown model ultracode:true is trusted - CLI is the authority",
			applied:   ptrconv.Ptr("xhigh"),
			ultracode: ptrconv.Ptr(true),
			curEffort: "xhigh",
			model:     "claude-future-preview",
			want:      "ultracode",
		},
		{
			// S2: the account-default sentinel is the one unknown model NOT trusted
			// for ultracode. A session stuck on the literal "default" (the CLI never
			// echoed a concrete applied.model) has no real model behind it, so a CLI
			// ultracode:true report passes the reported effort through instead of
			// minting a phantom "ultracode" against the placeholder.
			name:      "stuck sentinel ultracode:true is not promoted - no model behind it",
			applied:   ptrconv.Ptr("xhigh"),
			ultracode: ptrconv.Ptr(true),
			curEffort: "xhigh",
			model:     DefaultModelSentinel,
			want:      "xhigh",
		},
		{
			name:      "unentitled opus reports ultracode:false + xhigh - graceful downgrade",
			applied:   ptrconv.Ptr("xhigh"),
			ultracode: ptrconv.Ptr(false),
			curEffort: "ultracode",
			model:     "opus",
			want:      "xhigh",
		},
		{
			name:      "ultracode cleared with omitted effort falls back to xhigh base",
			applied:   nil,
			ultracode: ptrconv.Ptr(false),
			curEffort: "ultracode",
			model:     "opus",
			want:      "xhigh",
		},
		{
			name:      "ultracode:false with non-ultracode current is unchanged",
			applied:   nil,
			ultracode: ptrconv.Ptr(false),
			curEffort: "high",
			model:     "opus",
			want:      "high",
		},
		{
			name:      "nil ultracode passes the reported effort through",
			applied:   ptrconv.Ptr("max"),
			ultracode: nil,
			curEffort: "high",
			model:     "opus",
			want:      "max",
		},
		{
			name:      "nil applied effort retains current effort",
			applied:   nil,
			ultracode: nil,
			curEffort: "medium",
			model:     "sonnet",
			want:      "medium",
		},
		{
			// CLI omits both fields (e.g. a model switch that didn't touch
			// effort) while curEffort is still "ultracode" on a model that
			// can't run it: the guard must clear the stale value to xhigh
			// rather than mislabel that session as ultracode.
			name:      "stale ultracode on a model that lost support is cleared",
			applied:   nil,
			ultracode: nil,
			curEffort: "ultracode",
			model:     maxOnlyModelID,
			want:      "xhigh",
		},
		{
			// Same shape, but Opus genuinely supports ultracode, so the guard
			// must NOT clear it.
			name:      "stale ultracode on opus with omitted fields is retained",
			applied:   nil,
			ultracode: nil,
			curEffort: "ultracode",
			model:     "opus",
			want:      "ultracode",
		},
		{
			// A model unknown to BOTH catalogs with a stale curEffort=="ultracode"
			// and the CLI omitting the ultracode field is LEFT ALONE: the final
			// clear-stale guard is gated on `known`, so an unknown model keeps its
			// value for the same "trust the CLI / can't confirm it lacks ultracode"
			// reason that promotes an unknown model's ultracode:true report. The
			// launch flags and startup reconcile never actually drive an unknown
			// model into ultracode (both downgrade it over the static catalog), so
			// the CLI side stays safe; only the stored/broadcast label reads
			// "ultracode" until a concrete CLI report overrides it.
			name:      "unknown model with omitted fields retains stale ultracode",
			applied:   nil,
			ultracode: nil,
			curEffort: "ultracode",
			model:     "claude-future-preview",
			want:      "ultracode",
		},
		{
			// Defensive: the CLI reports applied.effort as a concrete enum or
			// null, never "". An unexpected empty string must be treated like
			// omitted (retain curEffort) rather than blanking the effort to "".
			name:      "empty applied effort is treated as omitted (retains curEffort)",
			applied:   ptrconv.Ptr(""),
			ultracode: nil,
			curEffort: "high",
			model:     "opus",
			want:      "high",
		},
		{
			// Empty applied.effort with curEffort=="ultracode" on opus: the ""
			// is ignored, so the ultracode value survives (opus supports it)
			// instead of being blanked.
			name:      "empty applied effort retains stale ultracode on opus",
			applied:   ptrconv.Ptr(""),
			ultracode: nil,
			curEffort: "ultracode",
			model:     "opus",
			want:      "ultracode",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, newEffortResolver(effortTestCatalog()).effortFromApplied(tt.applied, tt.ultracode, tt.curEffort, tt.model))
		})
	}
}

func TestModelSupportsUltracode(t *testing.T) {
	// Against the static catalog: every xhigh-capable model lists ultracode; a
	// model with no xhigh (the synthetic max-only entry) and one with no effort
	// axis at all (Haiku) do not. Unlike supports, unknown models are NOT trusted.
	static := newEffortResolver(effortTestCatalog())
	assert.True(t, static.supportsUltracode("fable[1m]"))
	assert.True(t, static.supportsUltracode("opus"))
	assert.True(t, static.supportsUltracode("opus[1m]"))
	assert.True(t, static.supportsUltracode("sonnet"))
	assert.True(t, static.supportsUltracode("sonnet[1m]"))
	assert.False(t, static.supportsUltracode(maxOnlyModelID))
	assert.False(t, static.supportsUltracode("haiku"))
	assert.False(t, static.supportsUltracode("claude-future-preview"), "unknown models are not trusted for ultracode")
	assert.False(t, static.supportsUltracode(""))

	// Against a dynamic catalog: a model the static catalog never heard of is
	// ultracode-capable when the live CLI advertises xhigh for it (this is what
	// makes "auto from xhigh" work for future models without a code change), and
	// not otherwise.
	dynamic := newEffortResolver(convertClaudeModels([]claudeCodeModelInfo{
		{Value: "mythos", DisplayName: "Mythos 6", SupportsEffort: true, SupportedEffortLevels: []string{"low", "medium", "high", "xhigh", "max"}},
		{Value: "sprite", DisplayName: "Sprite 1", SupportsEffort: true, SupportedEffortLevels: []string{"low", "medium", "high"}},
	}, nil))
	assert.True(t, dynamic.supportsUltracode("mythos"), "xhigh-capable dynamic model is ultracode-capable")
	assert.False(t, dynamic.supportsUltracode("sprite"), "non-xhigh dynamic model is not ultracode-capable")
	assert.False(t, dynamic.supportsUltracode("opus"), "model absent from the dynamic catalog is not trusted")
}

func TestResolveClaudeEffortForModel(t *testing.T) {
	tests := []struct {
		name   string
		model  string
		effort string
		want   string
	}{
		{"fable[1m] keeps ultracode", "fable[1m]", "ultracode", "ultracode"},
		{"opus keeps ultracode", "opus", "ultracode", "ultracode"},
		{"opus[1m] keeps ultracode", "opus[1m]", "ultracode", "ultracode"},
		{"max-only model downgrades ultracode to high", maxOnlyModelID, "ultracode", "high"},
		{"haiku downgrades ultracode to high", "haiku", "ultracode", "high"},
		{"unknown downgrades ultracode to high", "claude-future-preview", "ultracode", "high"},
		{"max-only model downgrades xhigh to high", maxOnlyModelID, "xhigh", "high"},
		{"opus keeps xhigh", "opus", "xhigh", "xhigh"},
		{"unknown trusts xhigh", "claude-future-preview", "xhigh", "xhigh"},
		{"supported effort passes through", "sonnet", "high", "high"},
		{"auto passes through", "sonnet", "auto", "auto"},
		{"empty passes through", "opus", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, newEffortResolver(effortTestCatalog()).resolveEffort(tt.model, tt.effort))
		})
	}
}

func TestClaudeEffortFlagSettings(t *testing.T) {
	tests := []struct {
		name      string
		newEffort string
		curEffort string
		expected  map[string]interface{}
	}{
		{
			name:      "enable ultracode sets xhigh base + ultracode true",
			newEffort: "ultracode",
			curEffort: "high",
			expected:  map[string]interface{}{"effortLevel": "xhigh", "ultracode": true},
		},
		{
			name:      "leaving ultracode clears the ultracode flag",
			newEffort: "max",
			curEffort: "ultracode",
			expected:  map[string]interface{}{"effortLevel": "max", "ultracode": false},
		},
		{
			name:      "ordinary effort change carries no ultracode key",
			newEffort: "high",
			curEffort: "medium",
			expected:  map[string]interface{}{"effortLevel": "high"},
		},
		{
			name:      "ultracode unchanged is a no-op",
			newEffort: "ultracode",
			curEffort: "ultracode",
			expected:  nil,
		},
		{
			name:      "auto transition is handled elsewhere (no flag settings)",
			newEffort: "auto",
			curEffort: "ultracode",
			expected:  nil,
		},
		{
			name:      "empty effort is a no-op",
			newEffort: "",
			curEffort: "high",
			expected:  nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, claudeEffortFlagSettings(tt.newEffort, tt.curEffort))
		})
	}
}

func TestBuildStartupFlagSettings_Ultracode(t *testing.T) {
	tests := []struct {
		name          string
		model         string
		effort        string
		wantUltracode bool
	}{
		{"fable[1m] ultracode enables the combo", "fable[1m]", EffortUltracode, true},
		{"opus ultracode enables the combo", "opus", EffortUltracode, true},
		{"opus[1m] ultracode enables the combo", "opus[1m]", EffortUltracode, true},
		{"max-only model ultracode is not enabled (unsupported)", maxOnlyModelID, EffortUltracode, false},
		{"haiku ultracode is not enabled (unsupported)", "haiku", EffortUltracode, false},
		{"unknown model ultracode is not enabled", "claude-future-preview", EffortUltracode, false},
		{"opus non-ultracode adds no effort keys", "opus", "xhigh", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := &ClaudeCodeAgent{model: tt.model, effort: tt.effort}
			fs := a.buildStartupFlagSettings(nil)
			if tt.wantUltracode {
				assert.Equal(t, "xhigh", fs["effortLevel"])
				assert.Equal(t, true, fs["ultracode"])
			} else {
				_, hasUltra := fs["ultracode"]
				assert.False(t, hasUltra, "ultracode key should be absent")
				_, hasLevel := fs["effortLevel"]
				assert.False(t, hasLevel, "effortLevel key should be absent for non-ultracode startup")
			}
		})
	}
}
