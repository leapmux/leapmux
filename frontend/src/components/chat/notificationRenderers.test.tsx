import { afterEach, describe, expect, it } from 'vitest'
import { AgentProvider } from '~/generated/leapmux/v1/agent_pb'
import { clearSettingsLabelCache, updateSettingsLabelCache } from '~/lib/settingsLabelCache'
import { elementText, renderThreadHasIcon, renderThreadText } from './messageRenderTestUtils'
import { renderNotificationThread } from './notificationRenderers'

// Side-effect-register the Claude and Codex plugins so the provider pre-pass
// (plugin.notificationThreadEntry) actually runs in the tests that pass an
// agentProvider -- mirroring production, where renderNotificationThread is
// always called with one.
await import('./providers/claude/plugin')
await import('./providers/codex/plugin')

// The settings label cache is a module-level singleton; the tests that populate
// it (Workflow / Execution Mode labels) would otherwise leak their
// registrations into later cases and make results order-dependent.
afterEach(() => {
  clearSettingsLabelCache()
})

// Provider-neutral aliases over the shared helpers (these cases drive the
// shared switch directly, with no provider pre-pass).
const renderText = (messages: unknown[]): string => renderThreadText(messages)
const renderHasIcon = (messages: unknown[]): boolean => renderThreadHasIcon(messages)

/** Check if the rendered output contains a specific substring. */
function renderedContains(messages: unknown[], text: string): boolean {
  return renderText(messages).includes(text)
}

describe('renderNotificationThread: compaction and context_cleared rendering', () => {
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

  it('codex thread/compacted (raw JSON-RPC) renders as "Context compacted"', () => {
    const messages = [{ method: 'thread/compacted', params: { threadId: 't1', turnId: 'turn1' } }]
    expect(renderText(messages)).toBe('Context compacted')
  })

  it('codex thread/compacted picks up compaction detail when metadata is present (not hardcoded)', () => {
    // The aggregate Codex branch routes through the shared compactBoundaryLabel,
    // so a thread/compacted carrying compact_metadata renders detail too -- and
    // stays in lockstep with the standalone renderer.
    const messages = [{ method: 'thread/compacted', compact_metadata: { trigger: 'auto', pre_tokens: 100000, post_tokens: 8000 } }]
    expect(renderText(messages)).toBe('Context compacted (auto, 100.0k → 8.0k)')
  })

  it('codex item/started+contextCompaction (raw JSON-RPC) renders the in-progress spinner', () => {
    const messages = [{
      method: 'item/started',
      params: { item: { type: 'contextCompaction', id: 'compact-1' }, threadId: 't1', turnId: 'turn1' },
    }]
    expect(renderText(messages)).toBe('Compacting context...')
  })

  it('codex item/started for non-compaction items does NOT match the compaction spinner', () => {
    const messages = [{
      method: 'item/started',
      params: { item: { type: 'commandExecution', id: 'cmd-1' } },
    }]
    // commandExecution is not a notification — describer returns [], so the
    // thread renders empty. The point is we don't accidentally emit a
    // compaction spinner for unrelated item kinds.
    expect(renderText(messages)).not.toContain('Compacting context')
  })

  it('thread/compacted alongside settings_changed renders both in order', () => {
    const messages = [
      { method: 'thread/compacted', params: { threadId: 't1', turnId: 'turn1' } },
      { type: 'settings_changed', changes: { model: { old: 'A', new: 'B' } } },
    ]
    const text = renderText(messages)
    const compactedIdx = text.indexOf('Context compacted')
    const modelIdx = text.indexOf('Model')
    expect(compactedIdx).toBeGreaterThanOrEqual(0)
    expect(modelIdx).toBeGreaterThan(compactedIdx)
  })

  it('legacy synthesized {type:"compacting"} envelope no longer matches the spinner (accepted regression)', () => {
    // Phase 4.1 stops emitting this shape; the shared switch deliberately has no
    // arm for the bare `{type:"compacting"}` type, so old DB rows produce no
    // entry. (Before notifications routed through this one renderer, such a row
    // fell through to the raw-JSON bubble; now it simply renders nothing.) This
    // test pins the migration boundary.
    const messages = [{ type: 'compacting' }]
    expect(renderText(messages)).not.toContain('Compacting context')
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
    // Production always calls renderNotificationThread with an agentProvider, so
    // the plugin notificationThreadEntry pre-pass runs before the shared switch.
    // Both Claude and Codex return null for compact_boundary, so the shared
    // switch produces the label regardless of provider.
    const msg = compactMsg({ trigger: 'auto', pre_tokens: 100000, post_tokens: 8000 })
    const expected = 'Context compacted (auto, 100.0k → 8.0k)'
    expect(elementText(renderNotificationThread([msg], AgentProvider.CLAUDE_CODE))).toBe(expected)
    expect(elementText(renderNotificationThread([msg], AgentProvider.CODEX))).toBe(expected)
  })

  it('renders a single Codex thread/compacted boundary with no metadata', () => {
    expect(renderText([{ method: 'thread/compacted' }])).toBe('Context compacted')
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

describe('renderNotificationThread: message ordering', () => {
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
    updateSettingsLabelCache([], [{
      key: 'collaboration_mode',
      label: 'Workflow',
      options: [
        { id: 'default', name: 'Default' },
        { id: 'plan', name: 'Plan Mode' },
      ],
    }] as any)
    const messages = [
      { type: 'settings_changed', changes: { collaboration_mode: { old: 'default', new: 'plan' } } },
    ]
    const text = renderText(messages)
    expect(text).toContain('Workflow')
  })

  it('uses cached option-group labels for arbitrary provider settings', () => {
    updateSettingsLabelCache([], [{
      key: 'opencode_mode',
      label: 'Execution Mode',
      options: [
        { id: 'safe', name: 'Safe' },
        { id: 'fast', name: 'Fast' },
      ],
    }] as any)
    const messages = [
      { type: 'settings_changed', changes: { opencode_mode: { old: 'safe', new: 'fast' } } },
    ]
    const text = renderText(messages)
    expect(text).toContain('Execution Mode')
    expect(text).toContain('Safe')
    expect(text).toContain('Fast')
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
    const retryIdx = text.indexOf('API Retry')
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
    const retryIdx = text.indexOf('API Retry')
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

  it('renders context_cleared', () => {
    expect(renderText([{ type: 'context_cleared' }])).toBe('Context cleared')
  })

  it('renders agent_error with its error text', () => {
    expect(renderText([{ type: 'agent_error', error: 'boom' }])).toBe('boom')
  })

  it('renders agent_error with the "Unknown error" fallback', () => {
    expect(renderText([{ type: 'agent_error' }])).toBe('Unknown error')
  })
})

describe('renderNotificationThread: plan_updated', () => {
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

  it('thread path honors inline label / oldLabel / newLabel overrides', () => {
    // 'foo' is absent from the settings label cache, so without the inline
    // overrides this would fall back to "foo (a → b)".
    const messages = [settingsMsg({ foo: { old: 'a', new: 'b', label: 'My Setting', oldLabel: 'Old!', newLabel: 'New!' } })]
    expect(renderText(messages)).toBe('My Setting (Old! → New!)')
  })

  it('thread path uses the "(new)" fallback when there is no old value', () => {
    const messages = [settingsMsg({ foo: { old: '', new: 'x', label: 'My Setting', newLabel: 'X!' } })]
    expect(renderText(messages)).toBe('My Setting (X!)')
  })

  it('treats an omitted old key as a first-time set (the real first-set wire shape)', () => {
    // Production omits `old` on first set rather than sending old:''. pickString
    // coerces the missing key to '', so firstSet is true and the "(new)"-only
    // form applies -- this exercises the shape the backend actually sends, which
    // the old:'' fixture above only approximates.
    const messages = [settingsMsg({ foo: { new: 'x', label: 'My Setting', newLabel: 'X!' } })]
    expect(renderText(messages)).toBe('My Setting (X!)')
  })

  it('keeps the arrow when the old value exists but its display resolves empty', () => {
    // oldLabel:'' forces an empty old display; because the old VALUE exists this
    // is a real transition, not a first-time set, so it must NOT collapse to the
    // "(new)"-only form.
    const messages = [settingsMsg({ foo: { old: 'a', new: 'b', oldLabel: '', newLabel: 'New!' } })]
    expect(renderText(messages)).toBe('foo ( → New!)')
  })

  it('honors an explicit empty-string label override instead of falling back to the key', () => {
    // An empty inline label is intentional and must win over displayLabel(key);
    // the old `||` treated '' as absent and showed the key instead.
    const messages = [settingsMsg({ foo: { old: 'a', new: 'b', label: '', oldLabel: 'O', newLabel: 'N' } })]
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
