import { render } from '@solidjs/testing-library'
import { afterEach, describe, expect, it } from 'vitest'
import { ALL_PROVIDERS } from '~/generated/contracts/providers'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { clearSettingsLabelCache, updateSettingsLabelCache } from '~/lib/settingsLabelCache'
import { elementText, renderThreadElement, renderThreadGlyph, renderThreadHasIcon, renderThreadText } from '~/test-support/messageRenderProbes'

// Side-effect-register the Claude and Codex plugins so the provider extractor
// (plugin?.transcript.notificationEntry) actually runs in the tests that pass an agentProvider
// -- mirroring production, where renderNotificationThread is always called with one.
await import('./providers/claude/plugin')
await import('./providers/codex/plugin')

// The settings label cache is a module-level singleton; the tests that populate
// it (Workflow / Execution Mode labels) would otherwise leak their
// registrations into later cases and make results order-dependent.
afterEach(() => {
  clearSettingsLabelCache()
})

// These cases drive the pipeline the way production does: with the row's own
// provider. A compaction boundary, a rate-limit event and an API retry are all
// PROVIDER shapes, so the provider is what reads them -- the shared fallback switch
// that used to answer for them is gone, and a message rendered without a provider
// now correctly produces nothing.
//
// Claude is the default because most of the shapes below are Claude's own. The Codex
// cases pass their own provider.
function renderText(messages: unknown[], provider: AgentProvider = AgentProvider.CLAUDE_CODE): string {
  return renderThreadText(messages, provider)
}
function renderHasIcon(messages: unknown[], provider: AgentProvider = AgentProvider.CLAUDE_CODE): boolean {
  return renderThreadHasIcon(messages, provider)
}

/** Check if the rendered output contains a specific substring. */
function renderedContains(messages: unknown[], text: string, provider?: AgentProvider): boolean {
  return renderText(messages, provider).includes(text)
}

describe('the notification thread: compaction and context_cleared rendering', () => {
  // Note: The backend consolidation handles mutual exclusion between
  // compaction and context_cleared. The frontend simply renders what it receives.

  const contextClearedMsg = { type: 'context_cleared' }
  const compactBoundaryMsg = {
    type: 'system',
    subtype: 'compact_boundary',
    compact_metadata: { trigger: 'auto', pre_tokens: 100000 },
  }
  const compactingStatusMsg = {
    type: 'system',
    subtype: 'status',
    status: 'compacting',
  }

  it('context_cleared alone: shows "Context cleared"', () => {
    const messages = [contextClearedMsg]
    expect(renderedContains(messages, 'Context cleared')).toBe(true)
  })

  it('compaction alone: shows compaction', () => {
    const messages = [compactBoundaryMsg]
    expect(renderedContains(messages, 'Context compacted')).toBe(true)
    expect(renderedContains(messages, 'Context cleared')).toBe(false)
  })

  it('compacting spinner: shows spinner', () => {
    const messages = [compactingStatusMsg]
    expect(renderedContains(messages, 'Compacting context...')).toBe(true)
  })

  it('plan_execution renders together with compaction', () => {
    const planExecMsg = {
      type: 'plan_execution',
      plan_file_path: '/path/plan.md',
    }
    const messages = [planExecMsg, compactBoundaryMsg]
    const text = renderText(messages)
    expect(text).toContain('Executing plan')
    expect(text).toContain('Context compacted')
  })

  it('settings_changed with compaction renders both', () => {
    const settingsMsg = {
      type: 'settings_changed',
      changes: { model: { old: 'A', new: 'B' } },
    }
    const messages = [settingsMsg, compactBoundaryMsg]
    const text = renderText(messages)
    expect(text).toContain('Context compacted')
    expect(text).toContain('Model')
  })

  // -- Phase 4 raw-passthrough shapes ----------------------------------

  it('codex item/started+contextCompaction (raw JSON-RPC) renders the in-progress spinner', () => {
    const messages = [{
      method: 'item/started',
      params: { item: { type: 'contextCompaction', id: 'compact-1' }, threadId: 't1', turnId: 'turn1' },
    }]
    expect(renderText(messages, AgentProvider.CODEX)).toBe('Compacting context...')
  })

  it('codex completed contextCompaction item renders the completed boundary', () => {
    const messages = [{
      threadId: 't1',
      turnId: 'turn1',
      item: { type: 'contextCompaction', id: 'compact-1' },
    }]
    expect(renderText(messages, AgentProvider.CODEX)).toBe('Context compacted')
  })

  it('codex item/started for non-compaction items does NOT match the compaction spinner', () => {
    const messages = [{
      method: 'item/started',
      params: { item: { type: 'commandExecution', id: 'cmd-1' } },
    }]
    // commandExecution is not a notification — describer returns [], so the
    // thread renders empty. The point is we don't accidentally emit a
    // compaction spinner for unrelated item kinds.
    expect(renderText(messages, AgentProvider.CODEX)).not.toContain('Compacting context')
  })

  it('draws the spinner for the bare {type:"compacting"} envelope, whatever the provider', () => {
    // `{type:"compacting"}` is LeapMux's OWN envelope, so `leapmuxNotificationEntry`
    // reads it and no plugin has to. The type is in PLAIN_ROW_TYPES, so a classifier
    // that meets one already answers `notification`; a neutral extractor with no case
    // for it drew a row that held no block at all. Rows of this shape are still in the
    // database, and the five providers of the Agent Client Protocol family supply no
    // notification hook, so the neutral answer is the only one those rows can get.
    const messages = [{ type: 'compacting' }]
    expect(renderText(messages, AgentProvider.CODEX)).toContain('Compacting context')
  })

  it('legacy synthesized {type:"system",subtype:"compact_boundary",threadId} from Codex still matches Claude\'s shape', () => {
    // The Claude raw shape has identical {type:"system",subtype:"compact_boundary"} —
    // legacy Codex synthesized rows happen to render correctly via this path.
    const messages = [{ type: 'system', subtype: 'compact_boundary', threadId: 't1', turnId: 'turn1' }]
    expect(renderText(messages)).toContain('Context compacted')
  })
})

describe('compaction token formatting: pre → post', () => {
  /** Wrap compaction metadata in the Claude `compact_boundary` system shape. */
  function compactMsg(compactMetadata: Record<string, unknown>) {
    return { type: 'system', subtype: 'compact_boundary', compact_metadata: compactMetadata }
  }

  /**
   * Wrap fields in the `microcompact_boundary` system shape. Claude Code emits
   * no microcompact metadata, so the renderer ignores anything here -- these
   * fixtures double as "metadata is ignored" guards.
   */
  function microcompactMsg(microcompactMetadata: Record<string, unknown>) {
    return { type: 'system', subtype: 'microcompact_boundary', microcompactMetadata }
  }

  // -- consolidated multi-message path -------------------------------------

  it('renders trigger, pre_tokens, and post_tokens as "(trigger, pre → post)"', () => {
    // Manual /compact carries post_tokens directly and no tokens_saved.
    const messages = [compactMsg({ trigger: 'manual', pre_tokens: 105424, post_tokens: 8476 })]
    expect(renderText(messages)).toBe('Context compacted (manual, 105.4k → 8.5k)')
  })

  it('derives post from pre_tokens minus tokens_saved when post_tokens is absent', () => {
    const messages = [compactMsg({ trigger: 'auto', pre_tokens: 100000, tokens_saved: 40000 })]
    expect(renderText(messages)).toBe('Context compacted (auto, 100.0k → 60.0k)')
  })

  it('prefers explicit post_tokens over deriving from tokens_saved', () => {
    const messages = [compactMsg({ pre_tokens: 100000, post_tokens: 8000, tokens_saved: 1 })]
    expect(renderText(messages)).toBe('Context compacted (100.0k → 8.0k)')
  })

  it('omits the trigger when it is absent', () => {
    const messages = [compactMsg({ pre_tokens: 105424, post_tokens: 8476 })]
    expect(renderText(messages)).toBe('Context compacted (105.4k → 8.5k)')
  })

  it('shows trigger and the pre count alone when neither post_tokens nor tokens_saved is present', () => {
    const messages = [compactMsg({ trigger: 'auto', pre_tokens: 100000 })]
    expect(renderText(messages)).toBe('Context compacted (auto, 100.0k)')
  })

  it('shows the post count alone when pre_tokens is absent', () => {
    const messages = [compactMsg({ post_tokens: 8000 })]
    expect(renderText(messages)).toBe('Context compacted (→ 8.0k)')
  })

  it('shows the trigger alone when no token counts are present', () => {
    const messages = [compactMsg({ trigger: 'manual' })]
    expect(renderText(messages)).toBe('Context compacted (manual)')
  })

  it('renders no parenthetical when neither trigger nor token counts are present', () => {
    const messages = [compactMsg({})]
    expect(renderText(messages)).toBe('Context compacted')
  })

  it('microcompaction renders a plain "Context microcompacted" with no detail', () => {
    // Claude Code emits no microcompact metadata; metadata-like fields here are
    // ignored, so no trigger or token counts appear.
    const messages = [microcompactMsg({ trigger: 'auto', preTokens: 200000, tokensSaved: 50000 })]
    expect(renderText(messages)).toBe('Context microcompacted')
  })

  it('reads camelCase keys (compactMetadata / preTokens / postTokens)', () => {
    // The consolidated CRDT path delivers camelCase keys rather than the raw
    // snake_case Claude shape; both must resolve.
    const messages = [{
      type: 'system',
      subtype: 'compact_boundary',
      compactMetadata: { trigger: 'auto', preTokens: 100000, postTokens: 8000 },
    }]
    expect(renderText(messages)).toBe('Context compacted (auto, 100.0k → 8.0k)')
  })

  it('drops a lone tokens_saved that has no pre count to anchor a transition', () => {
    // Without pre, "pre → post" cannot be formed, so the saved figure is not
    // shown as a bare number (the pre-unification "saved X tokens" behavior).
    const messages = [compactMsg({ tokens_saved: 5000 })]
    expect(renderText(messages)).toBe('Context compacted')
  })

  it('formats counts across the 1k boundary: 1000 -> "1.0k", 500 -> "500"', () => {
    // 1000 is >= 1000 so it gets the "k" suffix; 500 stays a bare integer.
    const messages = [compactMsg({ pre_tokens: 1000, post_tokens: 500 })]
    expect(renderText(messages)).toBe('Context compacted (1.0k → 500)')
  })

  // -- compact_boundary through a provider pre-pass ------------------------

  it('renders a single compact_boundary the same with the Claude or Codex provider pre-pass', () => {
    // A row an older worker synthesized carries Claude's boundary shape whatever
    // the agent was, and those rows are persisted. Both plugins therefore read it,
    // and they must read it the same way.
    const msg = compactMsg({ trigger: 'auto', pre_tokens: 100000, post_tokens: 8000 })
    const expected = 'Context compacted (auto, 100.0k → 8.0k)'
    expect(elementText(renderThreadElement([msg], AgentProvider.CLAUDE_CODE))).toBe(expected)
    expect(elementText(renderThreadElement([msg], AgentProvider.CODEX))).toBe(expected)
  })

  it('microcompaction ignores a metadata wrapper under any key (Claude emits none)', () => {
    // Neither microcompactMetadata nor the snake_case microcompact_metadata is
    // read; both render the plain label. Guards against re-adding a dead lookup.
    const messages = [{
      type: 'system',
      subtype: 'microcompact_boundary',
      microcompact_metadata: { trigger: 'auto', preTokens: 200000, tokensSaved: 50000 },
    }]
    expect(renderText(messages)).toBe('Context microcompacted')
  })

  it('clamps a derived post to 0 when tokens_saved exceeds pre_tokens', () => {
    // A provider reporting saved > pre must not render a negative size.
    const messages = [compactMsg({ trigger: 'auto', pre_tokens: 30000, tokens_saved: 50000 })]
    expect(renderText(messages)).toBe('Context compacted (auto, 30.0k → 0)')
  })

  it('renders a zero post when tokens_saved equals pre_tokens', () => {
    const messages = [compactMsg({ pre_tokens: 100000, tokens_saved: 100000 })]
    expect(renderText(messages)).toBe('Context compacted (100.0k → 0)')
  })

  it('renders a no-op transition when tokens_saved is zero', () => {
    // saved: 0 is a real number (not missing), so post derives to pre.
    const messages = [compactMsg({ pre_tokens: 100000, tokens_saved: 0 })]
    expect(renderText(messages)).toBe('Context compacted (100.0k → 100.0k)')
  })

  it('clamps an explicit negative post_tokens to 0 (not just the derived path)', () => {
    // The derived `pre - saved` post is clamped, but a provider could also report
    // a negative post_tokens directly; that must render 0, not "-5".
    const messages = [compactMsg({ pre_tokens: 100000, post_tokens: -5 })]
    expect(renderText(messages)).toBe('Context compacted (100.0k → 0)')
  })

  it('clamps an explicit negative pre_tokens to 0', () => {
    const messages = [compactMsg({ pre_tokens: -100, post_tokens: 8000 })]
    expect(renderText(messages)).toBe('Context compacted (0 → 8.0k)')
  })

  it('drops a non-finite (NaN) count instead of rendering "NaN"', () => {
    // JSON.parse cannot produce NaN, but a synthesized payload could; the count
    // degrades to omitted so the other side of the transition still shows.
    const messages = [compactMsg({ pre_tokens: Number.NaN, post_tokens: 8000 })]
    expect(renderText(messages)).toBe('Context compacted (→ 8.0k)')
  })

  it('drops a non-finite (Infinity) count instead of rendering "InfinityM"', () => {
    const messages = [compactMsg({ pre_tokens: 100000, post_tokens: Number.POSITIVE_INFINITY })]
    expect(renderText(messages)).toBe('Context compacted (100.0k)')
  })

  // -- divider markup (icon + layout, not just text) -----------------------

  it('renders a single compact boundary as a divider with the icon', () => {
    const msg = compactMsg({ trigger: 'auto', pre_tokens: 100000, post_tokens: 8000 })
    expect(renderHasIcon([msg])).toBe(true)
    expect(renderText([msg])).toBe('Context compacted (auto, 100.0k → 8.0k)')
  })

  it('renders a single microcompact boundary as a divider with the icon', () => {
    expect(renderHasIcon([microcompactMsg({})])).toBe(true)
    expect(renderText([microcompactMsg({})])).toBe('Context microcompacted')
  })

  it('renders a single compacting status as a spinner divider with the icon', () => {
    const msg = { type: 'system', subtype: 'status', status: 'compacting' }
    expect(renderHasIcon([msg])).toBe(true)
    expect(renderText([msg])).toBe('Compacting context...')
  })
})

describe('the notification thread: message ordering', () => {
  it('context_cleared before settings_changed preserves order', () => {
    const messages = [
      { type: 'context_cleared' },
      { type: 'settings_changed', changes: { permissionMode: { old: 'default', new: 'plan' } } },
    ]
    const text = renderText(messages)
    const clearedIdx = text.indexOf('Context cleared')
    const modeIdx = text.indexOf('Permission Mode')
    expect(clearedIdx).toBeGreaterThanOrEqual(0)
    expect(modeIdx).toBeGreaterThan(clearedIdx)
  })

  it('settings_changed before context_cleared preserves order', () => {
    const messages = [
      { type: 'settings_changed', changes: { permissionMode: { old: 'default', new: 'plan' } } },
      { type: 'context_cleared' },
    ]
    const text = renderText(messages)
    const modeIdx = text.indexOf('Permission Mode')
    const clearedIdx = text.indexOf('Context cleared')
    expect(modeIdx).toBeGreaterThanOrEqual(0)
    expect(clearedIdx).toBeGreaterThan(modeIdx)
  })

  it('uses Workflow label for Codex collaboration mode changes', () => {
    updateSettingsLabelCache(AgentProvider.CODEX, [{
      id: 'collaboration_mode',
      label: 'Workflow',
      options: [
        { id: 'default', name: 'Default' },
        { id: 'plan', name: 'Plan Mode' },
      ],
    }] as any)
    const messages = [
      { type: 'settings_changed', changes: { collaboration_mode: { old: 'default', new: 'plan' } } },
    ]
    // The notification renders under the same provider the cache was primed for.
    const text = renderThreadText(messages, AgentProvider.CODEX)
    expect(text).toContain('Workflow')
  })

  it('uses cached option-group labels for arbitrary provider settings', () => {
    updateSettingsLabelCache(AgentProvider.OPENCODE, [{
      id: 'opencode_mode',
      label: 'Execution Mode',
      options: [
        { id: 'safe', name: 'Safe' },
        { id: 'fast', name: 'Fast' },
      ],
    }] as any)
    const messages = [
      { type: 'settings_changed', changes: { opencode_mode: { old: 'safe', new: 'fast' } } },
    ]
    const text = renderThreadText(messages, AgentProvider.OPENCODE)
    expect(text).toContain('Execution Mode')
    expect(text).toContain('Safe')
    expect(text).toContain('Fast')
  })

  it('prefers a provider\'s cached label for a well-known axis over the canonical name', () => {
    // A provider can relabel a well-known axis -- Pi labels "effort" as "Thinking Level".
    // displayLabel must consult the per-provider cache for well-known ids too, so a
    // settings_changed without an inline label renders the provider's name rather than
    // the hardcoded canonical "Effort".
    updateSettingsLabelCache(AgentProvider.PI, [{
      id: 'effort',
      label: 'Thinking Level',
      options: [
        { id: 'low', name: 'Low' },
        { id: 'high', name: 'High' },
      ],
    }] as any)
    const messages = [
      { type: 'settings_changed', changes: { effort: { old: 'low', new: 'high' } } },
    ]
    const text = renderThreadText(messages, AgentProvider.PI)
    expect(text).toContain('Thinking Level')
    expect(text).not.toContain('Effort')
  })

  it('falls back to the canonical well-known axis name when the cache is unprimed', () => {
    // With no cache entry for the provider, a well-known axis still renders its canonical
    // English name (the fallback that keeps historical notifications readable).
    const messages = [
      { type: 'settings_changed', changes: { effort: { old: 'low', new: 'high' } } },
    ]
    const text = renderThreadText(messages, AgentProvider.CLAUDE_CODE)
    expect(text).toContain('Effort')
  })

  it('interrupted appears in order among other messages', () => {
    const messages = [
      { type: 'context_cleared' },
      { type: 'interrupted' },
      { type: 'settings_changed', changes: { model: { old: 'A', new: 'B' } } },
    ]
    const text = renderText(messages)
    const clearedIdx = text.indexOf('Context cleared')
    const interruptedIdx = text.indexOf('Interrupted')
    const modelIdx = text.indexOf('Model')
    expect(clearedIdx).toBeGreaterThanOrEqual(0)
    expect(interruptedIdx).toBeGreaterThan(clearedIdx)
    expect(modelIdx).toBeGreaterThan(interruptedIdx)
  })

  it('api_retry before context_cleared preserves order in one text line', () => {
    const messages = [
      { type: 'system', subtype: 'api_retry', attempt: 1, max_retries: 3 },
      { type: 'context_cleared' },
    ]
    const text = renderText(messages)
    const retryIdx = text.indexOf('API retry')
    const clearedIdx = text.indexOf('Context cleared')
    expect(retryIdx).toBeGreaterThanOrEqual(0)
    expect(clearedIdx).toBeGreaterThan(retryIdx)
  })

  it('context_cleared before api_retry preserves order after backend dedupe', () => {
    const messages = [
      { type: 'context_cleared' },
      { type: 'system', subtype: 'api_retry', attempt: 2, max_retries: 3 },
    ]
    const text = renderText(messages)
    const clearedIdx = text.indexOf('Context cleared')
    const retryIdx = text.indexOf('API retry')
    expect(clearedIdx).toBeGreaterThanOrEqual(0)
    expect(retryIdx).toBeGreaterThan(clearedIdx)
  })
})

describe('single-message notification labels', () => {
  // interrupted / context_cleared / agent_error render through the shared switch
  // as one-element threads -- the sole notification path.
  it('renders interrupted', () => {
    expect(renderText([{ type: 'interrupted' }])).toBe('Interrupted')
  })

  it('renders stop_ignored with the press-again instruction', () => {
    expect(renderText([{ type: 'stop_ignored' }])).toBe('Interrupt ignored — press Interrupt again to force it')
  })

  it('renders input_requeued with the reason the message appears again', () => {
    expect(renderText([{ type: 'input_requeued' }])).toBe('Message queued again — the agent dropped it before the model read it')
  })

  it('renders context_cleared', () => {
    expect(renderText([{ type: 'context_cleared' }])).toBe('Context cleared')
  })

  // A live status the provider stated in its own words -- Goose sends one for a
  // provider switch. The worker normalizes it, so one row draws every provider's.
  it('renders an agent status in the provider\'s own words', () => {
    expect(renderText([{ type: 'agent_status', text: 'Switched provider' }])).toBe('Switched provider')
  })

  it('draws nothing for an agent status that states no words', () => {
    expect(renderText([{ type: 'agent_status', text: '   ' }])).toBe('')
  })

  it('renders agent_error with its error text', () => {
    expect(renderText([{ type: 'agent_error', error: 'boom' }])).toBe('boom')
  })

  it('renders agent_error with the "Unknown error" fallback', () => {
    expect(renderText([{ type: 'agent_error' }])).toBe('Unknown error')
  })
})

/**
 * The session-goal transitions.
 *
 * The worker writes these rows only when the goal actually CHANGES -- never for
 * the progress reports Codex sends after every completed tool call -- and it
 * writes them NEUTRAL, so this one renderer serves all five providers that
 * report a goal rather than a copy in each provider plugin.
 */
describe('the notification thread: goal transitions', () => {
  it('announces a new goal with its objective', () => {
    expect(renderText([{ type: 'goal_updated', objective: 'every test passes', goal_status: 'active' }]))
      .toBe('Goal set: every test passes')
  })

  // The verb states WHAT changed, so a status flip does not read as a fresh
  // goal being set.
  it('names the transition for each finished status', () => {
    expect(renderText([{ type: 'goal_updated', objective: 'x', goal_status: 'done' }]))
      .toBe('Goal achieved: x')
    expect(renderText([{ type: 'goal_updated', objective: 'x', goal_status: 'paused' }]))
      .toBe('Goal paused: x')
    expect(renderText([{ type: 'goal_updated', objective: 'x', goal_status: 'blocked' }]))
      .toBe('Goal blocked: x')
  })

  // The provider's own word, when it says more than the neutral status does:
  // usageLimited and notSatisfied are both `blocked`.
  it('appends the provider status detail when it differs', () => {
    expect(renderText([{
      type: 'goal_updated',
      objective: 'x',
      goal_status: 'blocked',
      status_detail: 'usageLimited',
    }])).toBe('Goal blocked: x (usageLimited)')
  })

  it('omits a detail that only repeats the neutral status', () => {
    expect(renderText([{
      type: 'goal_updated',
      objective: 'x',
      goal_status: 'active',
      status_detail: 'active',
    }])).toBe('Goal set: x')
  })

  it('renders nothing for a transition with no readable objective', () => {
    expect(renderText([{ type: 'goal_updated', goal_status: 'active' }])).toBe('')
  })

  /**
   * A RESUME ends in `active`, and so does a first set, so the status alone
   * cannot tell them apart -- and reading the status alone announced "Goal set:
   * x" two rows under "Goal paused: x", for an objective nobody replaced. The
   * worker holds the row from before the write, so it writes the answer.
   */
  it('names what the change DID, not the status it left behind', () => {
    expect(renderText([{
      type: 'goal_updated',
      objective: 'x',
      goal_status: 'active',
      goal_transition: 'resumed',
    }])).toBe('Goal resumed: x')
    expect(renderText([{
      type: 'goal_updated',
      objective: 'x',
      goal_status: 'active',
      goal_transition: 'replaced',
    }])).toBe('Goal replaced: x')
    expect(renderText([{
      type: 'goal_updated',
      objective: 'x',
      goal_status: 'active',
      goal_transition: 'set',
    }])).toBe('Goal set: x')
  })

  // A row an older worker wrote carries no transition, and a token this build
  // does not know is the same situation. Both fall back to the status rather
  // than rendering nothing.
  it('falls back to the status when the transition is absent or unknown', () => {
    expect(renderText([{ type: 'goal_updated', objective: 'x', goal_status: 'paused' }]))
      .toBe('Goal paused: x')
    expect(renderText([{
      type: 'goal_updated',
      objective: 'x',
      goal_status: 'done',
      goal_transition: 'somethingNew',
    }])).toBe('Goal achieved: x')
  })

  it('announces a cleared goal, with and without its objective', () => {
    expect(renderText([{ type: 'goal_cleared', objective: 'x' }])).toBe('Goal cleared: x')
    expect(renderText([{ type: 'goal_cleared' }])).toBe('Goal cleared')
  })
})

describe('the notification thread: plan_updated', () => {
  it('without update_agent_title shows "Plan updated: <title>"', () => {
    const messages = [{ type: 'plan_updated', plan_title: 'My Plan', plan_file_path: '/p.md' }]
    expect(renderText(messages)).toBe('Plan updated: My Plan')
  })

  it('with update_agent_title:true shows "Plan updated and renamed to <title>"', () => {
    const messages = [{
      type: 'plan_updated',
      plan_title: 'Auth Refactor',
      plan_file_path: '/p.md',
      update_agent_title: true,
    }]
    expect(renderText(messages)).toBe('Plan updated and renamed to Auth Refactor')
  })

  it('with empty plan_title renders nothing', () => {
    const messages = [{ type: 'plan_updated', plan_title: '', plan_file_path: '/p.md' }]
    expect(renderText(messages)).toBe('')
  })

  it('with missing plan_title renders nothing', () => {
    const messages = [{ type: 'plan_updated', plan_file_path: '/p.md' }]
    expect(renderText(messages)).toBe('')
  })

  it('combined with settings_changed in a thread', () => {
    const messages = [
      { type: 'settings_changed', changes: { model: { old: 'A', new: 'B' } } },
      { type: 'plan_updated', plan_title: 'Debug Session', plan_file_path: '/p.md' },
    ]
    const text = renderText(messages)
    expect(text).toContain('Model')
    expect(text).toContain('Plan updated: Debug Session')
  })

  it('combined with interrupted in a thread, with auto-rename', () => {
    const messages = [
      {
        type: 'plan_updated',
        plan_title: 'Test Plan',
        plan_file_path: '/p.md',
        update_agent_title: true,
      },
      { type: 'interrupted' },
    ]
    const text = renderText(messages)
    expect(text).toContain('Plan updated and renamed to Test Plan')
    expect(text).toContain('Interrupted')
  })
})

describe('settings change formatting: inline label overrides', () => {
  const settingsMsg = (changes: Record<string, unknown>) => ({ type: 'settings_changed', changes })

  it('thread path honors inline label / old_label / new_label overrides', () => {
    // 'foo' is absent from the settings label cache, so without the inline
    // overrides this would fall back to "foo (a → b)".
    const messages = [settingsMsg({ foo: { old: 'a', new: 'b', label: 'My Setting', old_label: 'Old!', new_label: 'New!' } })]
    expect(renderText(messages)).toBe('My Setting (Old! → New!)')
  })

  it('thread path uses the "(new)" fallback when there is no old value', () => {
    const messages = [settingsMsg({ foo: { old: '', new: 'x', label: 'My Setting', new_label: 'X!' } })]
    expect(renderText(messages)).toBe('My Setting (X!)')
  })

  it('treats an omitted old key as a first-time set (the real first-set wire shape)', () => {
    // Production omits `old` on first set rather than sending old:''. pickString
    // coerces the missing key to '', so firstSet is true and the "(new)"-only
    // form applies -- this exercises the shape the backend actually sends, which
    // the old:'' fixture above only approximates.
    const messages = [settingsMsg({ foo: { new: 'x', label: 'My Setting', new_label: 'X!' } })]
    expect(renderText(messages)).toBe('My Setting (X!)')
  })

  it('keeps the arrow when the old value exists but its display resolves empty', () => {
    // old_label:'' forces an empty old display; because the old VALUE exists this
    // is a real transition, not a first-time set, so it must NOT collapse to the
    // "(new)"-only form.
    const messages = [settingsMsg({ foo: { old: 'a', new: 'b', old_label: '', new_label: 'New!' } })]
    expect(renderText(messages)).toBe('foo ( → New!)')
  })

  it('honors an explicit empty-string label override instead of falling back to the key', () => {
    // An empty inline label is intentional and must win over displayLabel(key);
    // the old `||` treated '' as absent and showed the key instead.
    const messages = [settingsMsg({ foo: { old: 'a', new: 'b', label: '', old_label: 'O', new_label: 'N' } })]
    expect(renderText(messages)).toBe('(O → N)')
  })

  it('thread path drops entries whose value is unchanged', () => {
    const messages = [settingsMsg({ foo: { old: 'same', new: 'same', label: 'My Setting' } })]
    expect(renderText(messages)).toBe('')
  })

  it('skips a null change entry without throwing', () => {
    // The untyped JSON path could deliver a null value; dereferencing val.old
    // would otherwise throw, so a malformed entry must degrade to nothing.
    const messages = [settingsMsg({ foo: null })]
    expect(renderText(messages)).toBe('')
  })

  it('skips malformed entries but still renders the well-formed ones', () => {
    const messages = [settingsMsg({ foo: null, bar: 'oops', model: { old: 'A', new: 'B' } })]
    expect(renderText(messages)).toBe('Model (A → B)')
  })
})

// A run of consecutive text children joins into ONE paragraph, and a divider draws
// as its own block beside it. The row is sized by what it DRAWS, so a thread of ten
// short children must not lay out as ten lines.
describe('renderNotificationBlocks: the blocks a thread lays out', () => {
  const contextClearedMsg = { type: 'context_cleared' } // -> a text entry
  const compactBoundaryMsg = { // -> a divider entry
    type: 'system',
    subtype: 'compact_boundary',
    compact_metadata: { trigger: 'auto', pre_tokens: 100000 },
  }

  it('joins consecutive text children into ONE comma-joined paragraph', () => {
    const one = renderText([contextClearedMsg])
    expect(one).not.toBe('')
    expect(renderText([contextClearedMsg, contextClearedMsg, contextClearedMsg])).toBe([one, one, one].join(', '))
  })

  it('draws a divider as its own block beside the text paragraph', () => {
    const both = renderText([compactBoundaryMsg, contextClearedMsg])
    expect(both).toContain(renderText([contextClearedMsg]))
    expect(renderHasIcon([compactBoundaryMsg, contextClearedMsg])).toBe(true)
  })

  it('draws nothing for children that produce no entries', () => {
    expect(renderThreadElement([null, 'not-an-object'])).toBeNull()
    expect(renderText([null, 'not-an-object'])).toBe('')
  })
})

// The worker closes a subagent transcript with one subagent_ended notification.
// It renders as a labelled rule in the turn-end divider style, with the same
// status glyph the Background tasks list uses for that final status.
describe('the notification thread: subagent_ended', () => {
  it('labels a completed subagent', () => {
    expect(renderText([{ type: 'subagent_ended', status: 'completed' }]))
      .toContain('Subagent completed')
  })

  it('labels a failed subagent', () => {
    expect(renderText([{ type: 'subagent_ended', status: 'failed' }]))
      .toContain('Subagent failed')
  })

  it('labels a stopped subagent', () => {
    expect(renderText([{ type: 'subagent_ended', status: 'stopped' }]))
      .toContain('Subagent stopped')
  })

  it('labels an interrupted subagent', () => {
    expect(renderText([{ type: 'subagent_ended', status: 'interrupted' }]))
      .toContain('Subagent interrupted')
  })

  // An unrecognized status still ends the transcript; the label must not invent
  // an outcome it cannot know.
  it('falls back to a neutral label for an unknown status', () => {
    const text = renderText([{ type: 'subagent_ended', status: 'who-knows' }])
    expect(text).toContain('Subagent ended')
    expect(text).not.toContain('completed')
  })

  it('renders as a divider row with a glyph, not plain text', () => {
    expect(renderHasIcon([{ type: 'subagent_ended', status: 'completed' }])).toBe(true)
  })

  // The model states the OUTCOME and `notificationRenderers` picks the glyph, so a
  // map that answered two outcomes with one glyph would still pass every test
  // above. Four outcomes a reader must tell apart draw four distinct glyphs.
  it('draws a distinct glyph for each outcome it can name', () => {
    const glyph = (status: string) => renderThreadGlyph([{ type: 'subagent_ended', status }])
    const drawn = ['completed', 'failed', 'stopped', 'interrupted'].map(glyph)
    expect(drawn.every(svg => svg !== null && svg !== '')).toBe(true)
    expect(new Set(drawn).size).toBe(4)
  })

  // An unknown status states no outcome, so it shares the `stopped` glyph rather
  // than claiming one of the other three.
  it('draws the stopped glyph for a status it cannot name', () => {
    expect(renderThreadGlyph([{ type: 'subagent_ended', status: 'who-knows' }]))
      .toBe(renderThreadGlyph([{ type: 'subagent_ended', status: 'stopped' }]))
  })

  // The default is the compaction arrow, which every subagent outcome overrides.
  it('overrides the default divider glyph', () => {
    // A compact boundary arrives in Claude Code's own system shape, so this one
    // fixture needs the provider the rest of the group can leave to the default.
    const boundary = { type: 'system', subtype: 'compact_boundary', compact_metadata: { trigger: 'auto' } }
    const compaction = renderThreadGlyph([boundary], AgentProvider.CLAUDE_CODE)
    expect(compaction).not.toBeNull()
    expect(renderThreadGlyph([{ type: 'subagent_ended', status: 'completed' }])).not.toBe(compaction)
  })
})

describe('the notification thread: subagent_report', () => {
  it.each(ALL_PROVIDERS)('renders the agent label and Markdown report for provider %s', (provider) => {
    const { container } = render(() => renderThreadElement([
      { type: 'subagent_report', label: 'Parser reviewer', text: '**Finding**\n\n- Fixed' },
    ], provider))

    expect(container.textContent).toContain('Parser reviewer reported')
    expect(container.querySelector('strong')?.textContent).toBe('Finding')
    expect(container.querySelector('li')?.textContent).toBe('Fixed')
  })

  it('shows Claude Code\'s flagged delivery status', () => {
    expect(renderText([{ type: 'subagent_report', label: 'Reviewer', text: 'Check this', status: 'flagged' }]))
      .toContain('Reviewer reported — security warning')
  })

  it('states that Claude Code withheld a report from the parent', () => {
    expect(renderText([{ type: 'subagent_report', label: 'Reviewer', text: 'Child-only report', status: 'withheld' }]))
      .toContain('Reviewer report withheld')
  })
})
