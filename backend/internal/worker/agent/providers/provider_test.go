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
		{"codewhale present", leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEWHALE, `{"event":"turn.completed","num_tool_uses":4}`, true, 4},
		{"codewhale absent", leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEWHALE, `{"event":"turn.completed"}`, false, 0},
		{"codewhale malformed", leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEWHALE, `{`, false, 0},
		{"oh my pi present", leapmuxv1.AgentProvider_AGENT_PROVIDER_OH_MY_PI, `{"type":"agent_end","num_tool_uses":4}`, true, 4},
		{"oh my pi absent", leapmuxv1.AgentProvider_AGENT_PROVIDER_OH_MY_PI, `{"type":"agent_end"}`, false, 0},
		{"oh my pi malformed", leapmuxv1.AgentProvider_AGENT_PROVIDER_OH_MY_PI, `{`, false, 0},
		{"acp present", leapmuxv1.AgentProvider_AGENT_PROVIDER_OPENCODE, `{"num_tool_uses":1}`, true, 1},
		{"acp absent", leapmuxv1.AgentProvider_AGENT_PROVIDER_OPENCODE, `{"type":"result"}`, false, 0},
		{"acp malformed", leapmuxv1.AgentProvider_AGENT_PROVIDER_OPENCODE, `{`, false, 0},
		// MiMo's turn end is its idle status, and the worker's own count rides in the
		// row's metadata, which the merged content states at the top level.
		{"mimo present", leapmuxv1.AgentProvider_AGENT_PROVIDER_MIMO_CODE, `{"type":"session.status","num_tool_uses":4}`, true, 4},
		{"mimo absent", leapmuxv1.AgentProvider_AGENT_PROVIDER_MIMO_CODE, `{"type":"session.status"}`, false, 0},
		{"mimo malformed", leapmuxv1.AgentProvider_AGENT_PROVIDER_MIMO_CODE, `{`, false, 0},
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

// Child output without native tool counts must not invent completion metadata.
func TestProviderFor_ChildMessagesDoNotInventToolCounts(t *testing.T) {
	t.Parallel()

	registry := Registry()

	for _, tc := range []struct {
		name     string
		provider leapmuxv1.AgentProvider
		content  string
		want     bool
	}{
		{"claude result", leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE, `{"type":"result","duration_ms":12}`, false},
		{"claude result with error", leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE, `{"type":"result","is_error":true}`, false},
		{"claude assistant", leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE, `{"type":"assistant"}`, false},
		{"claude no type", leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE, `{}`, false},
		{"claude malformed", leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE, `{`, false},
		{"claude empty", leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE, ``, false},
		{"codex result-shaped", leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX, `{"type":"result"}`, false},
		{"codex turn completed", leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX, `{"threadId":"child-1","turn":{"id":"t1","status":"completed"}}`, false},
		{"pi result-shaped", leapmuxv1.AgentProvider_AGENT_PROVIDER_PI, `{"type":"result"}`, false},
		{"oh my pi agent_end", leapmuxv1.AgentProvider_AGENT_PROVIDER_OH_MY_PI, `{"type":"agent_end","messages":[]}`, false},
		{"opencode result-shaped", leapmuxv1.AgentProvider_AGENT_PROVIDER_OPENCODE, `{"type":"result"}`, false},
		{"goose result-shaped", leapmuxv1.AgentProvider_AGENT_PROVIDER_GOOSE, `{"type":"result"}`, false},
		{"codewhale child message", leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEWHALE, `{"kind":"message","index":3,"block":0,"message":{"role":"assistant","content":[{"type":"text","text":"done"}]}}`, false},
		{"grok turn completed", leapmuxv1.AgentProvider_AGENT_PROVIDER_GROK_BUILD, `{"jsonrpc":"2.0","method":"_x.ai/session_notification","params":{"update":{"sessionUpdate":"turn_completed"}}}`, false},
		{"qwen end turn", leapmuxv1.AgentProvider_AGENT_PROVIDER_QWEN_CODE, `{"jsonrpc":"2.0","method":"_qwencode/end_turn","params":{"reason":"end_turn"}}`, false},
		{"kiro turn end", leapmuxv1.AgentProvider_AGENT_PROVIDER_KIRO, `{"sessionUpdate":"session_info_update","_meta":{"kiro":{"kind":"turn_end","stopReason":"end_turn"}}}`, false},
		{"mimo result-shaped", leapmuxv1.AgentProvider_AGENT_PROVIDER_MIMO_CODE, `{"type":"result"}`, false},
		{"mimo idle", leapmuxv1.AgentProvider_AGENT_PROVIDER_MIMO_CODE, `{"type":"session.status","properties":{"status":{"type":"idle"}}}`, false},
		{"amp result", leapmuxv1.AgentProvider_AGENT_PROVIDER_AMP, `{"type":"result","subtype":"success","is_error":false}`, false},
		{"cline assistant finished", leapmuxv1.AgentProvider_AGENT_PROVIDER_CLINE, `{"version":"v1","event":"assistant.finished","payload":{"text":"done"}}`, false},
		{"cline run completed", leapmuxv1.AgentProvider_AGENT_PROVIDER_CLINE, `{"version":"v1","event":"run.completed","payload":{"reason":"completed"}}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			count, ok := registry.Plugin(tc.provider).TurnEndToolUses([]byte(tc.content))
			assert.Equal(t, tc.want, ok)
			assert.Zero(t, count)
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
		leapmuxv1.AgentProvider_AGENT_PROVIDER_QWEN_CODE,
		leapmuxv1.AgentProvider_AGENT_PROVIDER_GROK_BUILD,
		leapmuxv1.AgentProvider_AGENT_PROVIDER_KIRO,
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
		{"codewhale", leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEWHALE, `{"frame":"interrupt"}`},
		{"kimi", leapmuxv1.AgentProvider_AGENT_PROVIDER_KIMI_CODE, `{"action":"abort"}`},
		// omp kept Pi's abort command when it forked, so the two own the SAME frame.
		{"oh my pi", leapmuxv1.AgentProvider_AGENT_PROVIDER_OH_MY_PI, `{"type":"abort"}`},
	}
	for _, c := range cases {
		plugin := registry.Plugin(c.provider)
		assert.True(t, plugin.IsInterrupt(c.ownFrame), "%s must accept its own interrupt frame", c.name)
		// Cross-provider frames must not match.
		for _, other := range cases {
			// Two protocols that own byte-identical frames (Pi and Oh My Pi) cannot
			// be told apart by any classifier, so only DISTINCT frames are compared.
			if other.name == c.name || other.ownFrame == c.ownFrame {
				continue
			}
			assert.False(t, plugin.IsInterrupt(other.ownFrame),
				"%s plugin must reject %s's interrupt frame (%s)", c.name, other.name, other.ownFrame)
		}
		assert.False(t, plugin.IsInterrupt(`not-json`),
			"%s plugin must reject malformed input", c.name)
	}

	// MiMo interrupts over HTTP, Amp with a signal and Cline with a hub command.
	// None writes a frame, so none recognizes one.
	for _, provider := range []leapmuxv1.AgentProvider{
		leapmuxv1.AgentProvider_AGENT_PROVIDER_MIMO_CODE,
		leapmuxv1.AgentProvider_AGENT_PROVIDER_AMP,
		leapmuxv1.AgentProvider_AGENT_PROVIDER_CLINE,
	} {
		plugin := registry.Plugin(provider)
		for _, c := range cases {
			assert.False(t, plugin.IsInterrupt(c.ownFrame), "%v plugin must reject %s's interrupt frame", provider, c.name)
		}
	}
}
