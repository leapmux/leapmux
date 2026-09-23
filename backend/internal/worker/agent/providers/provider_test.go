package providers

import (
	"encoding/json"
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"

	"github.com/stretchr/testify/assert"
)

// Two handlers answer for the same tab, and they must agree about which CLI a
// request means. OpenAgent spawns Claude Code for a request that omits the
// field; a listing handler that took the field literally reported no resumable
// sessions and then let OpenAgent resume one of them.
func TestProviderOrDefault(t *testing.T) {
	t.Parallel()

	registry := Registry()

	assert.Equal(t, leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
		agent.ProviderOrDefault(leapmuxv1.AgentProvider_AGENT_PROVIDER_UNSPECIFIED),
		"an omitted provider is the one OpenAgent spawns")

	// Every registered provider passes through untouched, including Claude Code
	// itself -- a default that also rewrote a stated provider would silently
	// answer for the wrong CLI.
	for _, provider := range registry.Providers() {
		assert.Equal(t, provider, agent.ProviderOrDefault(provider))
	}
}

func TestProviderFor_TurnEndToolUses(t *testing.T) {
	t.Parallel()

	registry := Registry()

	for _, tc := range []struct {
		name     string
		provider leapmuxv1.AgentProvider
		content  string
		wantOK   bool
		want     int32
	}{
		{"claude present", leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE, `{"type":"result","num_tool_uses":3}`, true, 3},
		{"claude absent", leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE, `{"type":"result"}`, false, 0},
		{"claude malformed", leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE, `{`, false, 0},
		{"codex present", leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX, `{"num_tool_uses":2}`, true, 2},
		{"codex absent", leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX, `{"type":"result"}`, false, 0},
		{"codex malformed", leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX, `{`, false, 0},
		{"pi present", leapmuxv1.AgentProvider_AGENT_PROVIDER_PI, `{"num_tool_uses":0}`, true, 0},
		{"pi absent", leapmuxv1.AgentProvider_AGENT_PROVIDER_PI, `{}`, false, 0},
		{"pi malformed", leapmuxv1.AgentProvider_AGENT_PROVIDER_PI, `{`, false, 0},
		{"acp present", leapmuxv1.AgentProvider_AGENT_PROVIDER_OPENCODE, `{"num_tool_uses":1}`, true, 1},
		{"acp absent", leapmuxv1.AgentProvider_AGENT_PROVIDER_OPENCODE, `{"type":"result"}`, false, 0},
		{"acp malformed", leapmuxv1.AgentProvider_AGENT_PROVIDER_OPENCODE, `{`, false, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			count, ok := registry.Plugin(tc.provider).TurnEndToolUses([]byte(tc.content))
			assert.Equal(t, tc.wantOK, ok)
			if ok {
				assert.Equal(t, tc.want, count)
			}
		})
	}
}

// EndsSubagentTranscript decides whether a SUBAGENT transcript already closes
// itself, so the worker knows whether to add its own subagent-end divider.
// Only Claude forwards a subagent's final envelope; everyone else must answer
// false or their child transcripts would end with no divider at all.
func TestProviderFor_EndsSubagentTranscript(t *testing.T) {
	t.Parallel()

	registry := Registry()

	for _, tc := range []struct {
		name     string
		provider leapmuxv1.AgentProvider
		content  string
		want     bool
	}{
		{"claude result", leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE, `{"type":"result","duration_ms":12}`, true},
		{"claude result with error", leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE, `{"type":"result","is_error":true}`, true},
		{"claude assistant", leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE, `{"type":"assistant"}`, false},
		{"claude no type", leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE, `{}`, false},
		{"claude malformed", leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE, `{`, false},
		{"claude empty", leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE, ``, false},
		// A result-shaped payload from another provider must NOT suppress that
		// provider's divider: only Claude actually forwards one.
		//
		// Codex is the load-bearing case. It DOES write a child turn-end
		// divider, but per TURN: a collab child ends a turn and then accepts
		// another, so a true here would suppress the closing divider for every
		// child stopped mid-life. The answer is about the SUBAGENT ending, not
		// the turn.
		{"codex result-shaped", leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX, `{"type":"result"}`, false},
		{"codex turn completed", leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX, `{"threadId":"child-1","turn":{"id":"t1","status":"completed"}}`, false},
		{"pi result-shaped", leapmuxv1.AgentProvider_AGENT_PROVIDER_PI, `{"type":"result"}`, false},
		{"opencode result-shaped", leapmuxv1.AgentProvider_AGENT_PROVIDER_OPENCODE, `{"type":"result"}`, false},
		{"goose result-shaped", leapmuxv1.AgentProvider_AGENT_PROVIDER_GOOSE, `{"type":"result"}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, registry.Plugin(tc.provider).EndsSubagentTranscript([]byte(tc.content)))
		})
	}
}

func TestProviderFor_ACPSharesNoopClassification(t *testing.T) {
	t.Parallel()

	registry := Registry()

	// ACP-based providers register Provider which embeds noop
	// classify/merge — they only provide IsInterrupt. Verify a few of
	// them route to the same behavior.
	for _, provider := range []leapmuxv1.AgentProvider{
		leapmuxv1.AgentProvider_AGENT_PROVIDER_OPENCODE,
		leapmuxv1.AgentProvider_AGENT_PROVIDER_KILO,
		leapmuxv1.AgentProvider_AGENT_PROVIDER_REASONIX,
	} {
		plugin := registry.Plugin(provider)
		assert.False(t, plugin.Classify(json.RawMessage(`{"method":"session/cancel"}`)).Consolidatable(),
			"ACP provider %v must not consolidate notifications", provider)
		assert.True(t, plugin.IsInterrupt(`{"jsonrpc":"2.0","method":"session/cancel"}`),
			"ACP provider %v must recognize session/cancel as an interrupt", provider)
		assert.True(t, plugin.IsInterrupt(`{"method":"cancel"}`),
			"ACP provider %v must accept the legacy bare cancel form", provider)
	}
}

func TestProviderFor_IsInterruptIsolatedPerProvider(t *testing.T) {
	t.Parallel()

	registry := Registry()

	// Each provider's IsInterrupt must reject formats that belong to other
	// providers — otherwise the dispatcher's provider-aware design would be
	// silently undermined by misclassification.
	cases := []struct {
		name     string
		provider leapmuxv1.AgentProvider
		ownFrame string
	}{
		{"claude", leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE, `{"type":"control_request","request":{"subtype":"interrupt"}}`},
		{"codex", leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX, `{"jsonrpc":"2.0","method":"turn/interrupt"}`},
		{"pi", leapmuxv1.AgentProvider_AGENT_PROVIDER_PI, `{"type":"abort"}`},
		{"zcode", leapmuxv1.AgentProvider_AGENT_PROVIDER_ZCODE, `{"method":"session/stop"}`},
	}
	for _, c := range cases {
		plugin := registry.Plugin(c.provider)
		assert.True(t, plugin.IsInterrupt(c.ownFrame), "%s must accept its own interrupt frame", c.name)
		// Cross-provider frames must not match.
		for _, other := range cases {
			if other.name == c.name {
				continue
			}
			assert.False(t, plugin.IsInterrupt(other.ownFrame),
				"%s plugin must reject %s's interrupt frame (%s)", c.name, other.name, other.ownFrame)
		}
		assert.False(t, plugin.IsInterrupt(`not-json`),
			"%s plugin must reject malformed input", c.name)
	}
}
