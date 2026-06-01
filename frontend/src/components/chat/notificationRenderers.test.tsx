import type { JSXElement } from 'solid-js'
import { render } from '@solidjs/testing-library'
import { afterEach, describe, expect, it } from 'vitest'
import { AgentProvider } from '~/generated/leapmux/v1/agent_pb'
import { clearSettingsLabelCache, updateSettingsLabelCache } from '~/lib/settingsLabelCache'
import { agentErrorRenderer, compactBoundaryRenderer, compactingRenderer, contextClearedRenderer, interruptedRenderer, microcompactBoundaryRenderer, planUpdatedRenderer, renderNotificationThread, settingsChangedRenderer } from './notificationRenderers'
import { resultRenderer } from './providers/claude/notifications'

// Side-effect-register the Claude and Codex plugins so the provider pre-pass
// (plugin.notificationThreadEntry) actually runs in the parity tests that pass
// an agentProvider -- mirroring production, where renderNotificationThread is
// always called with one.
await import('./providers/claude/plugin')
await import('./providers/codex/plugin')

// The settings label cache is a module-level singleton; the tests that populate
// it (Workflow / Execution Mode labels) would otherwise leak their
// registrations into later cases and make results order-dependent.
afterEach(() => {
  clearSettingsLabelCache()
})

/** Render a JSX element and return its trimmed text content. */
function elementText(el: JSXElement | null): string {
  if (el === null)
    return ''
  const { container } = render(() => el)
  return container.textContent?.trim() ?? ''
}

/** Extract all text content from a rendered notification thread, trimmed. */
function renderText(messages: unknown[]): string {
  return elementText(renderNotificationThread(messages))
}

/** Check if the rendered output contains a specific substring. */
function renderedContains(messages: unknown[], text: string): boolean {
  return renderText(messages).includes(text)
}

/**
 * Assert the standalone renderer and the aggregate notification thread produce
 * the same trimmed text, equal to `expected`. `renderStandalone` is a factory so
 * each render mounts a fresh element.
 */
function expectTextParity(renderStandalone: () => JSXElement | null, messages: unknown[], expected: string): void {
  const standalone = elementText(renderStandalone())
  expect(standalone).toBe(expected)
  expect(renderText(messages)).toBe(standalone)
}

/**
 * Assert the standalone renderer and the aggregate notification thread produce
 * byte-identical markup (icon included) for the same single message.
 */
function expectMarkupParity(renderStandalone: () => JSXElement | null, messages: unknown[]): void {
  const standalone = render(() => renderStandalone()).container
  const aggregate = render(() => renderNotificationThread(messages)).container
  // Assert the icon on BOTH sides so the parity check can't pass by both paths
  // dropping it; then the full innerHTML equality covers the rest of the markup.
  expect(standalone.querySelector('svg')).not.toBeNull()
  expect(aggregate.querySelector('svg')).not.toBeNull()
  expect(standalone.innerHTML).toBe(aggregate.innerHTML)
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
    // Phase 4.1 stops emitting this shape; old DB rows fall through to the
    // raw-JSON fallback bubble. This test pins the migration boundary.
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

  // -- aggregate (notification_thread) path --------------------------------

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

  // -- standalone (per-message) renderer path ------------------------------

  it('standalone compactBoundaryRenderer renders "(trigger, pre → post)"', () => {
    const el = compactBoundaryRenderer.render(
      compactMsg({ trigger: 'manual', pre_tokens: 105424, post_tokens: 8476 }),
      undefined,
    )
    expect(elementText(el)).toBe('Context compacted (manual, 105.4k → 8.5k)')
  })

  it('standalone microcompactBoundaryRenderer renders a plain "Context microcompacted"', () => {
    const el = microcompactBoundaryRenderer.render(
      microcompactMsg({ trigger: 'auto', preTokens: 200000, tokensSaved: 50000 }),
      undefined,
    )
    expect(elementText(el)).toBe('Context microcompacted')
  })

  it('standalone compactBoundaryRenderer handles the Codex boundary with no metadata', () => {
    const el = compactBoundaryRenderer.render({ method: 'thread/compacted' }, undefined)
    expect(elementText(el)).toBe('Context compacted')
  })

  it('renders identical text from the standalone and aggregate paths for the same metadata', () => {
    // Both paths share formatCompactionDetail, so the per-message renderer and
    // the consolidated thread must agree byte-for-byte.
    const msg = compactMsg({ trigger: 'auto', pre_tokens: 100000, post_tokens: 8000 })
    expectTextParity(() => compactBoundaryRenderer.render(msg, undefined), [msg], 'Context compacted (auto, 100.0k → 8.0k)')
  })

  it('microcompaction renders identical text from the standalone and aggregate paths', () => {
    // The other half of the parity guarantee: microcompact must agree too.
    const msg = microcompactMsg({ trigger: 'auto', preTokens: 200000, tokensSaved: 50000 })
    expectTextParity(() => microcompactBoundaryRenderer.render(msg, undefined), [msg], 'Context microcompacted')
  })

  it('compact-boundary parity holds through a provider pre-pass (Claude and Codex)', () => {
    // Production always calls renderNotificationThread with an agentProvider, so
    // the plugin notificationThreadEntry pre-pass runs before the shared switch.
    // Both Claude and Codex return null for compact_boundary, so the aggregate
    // must still match the standalone renderer with a provider in play.
    const msg = compactMsg({ trigger: 'auto', pre_tokens: 100000, post_tokens: 8000 })
    const standalone = elementText(compactBoundaryRenderer.render(msg, undefined))
    expect(standalone).toBe('Context compacted (auto, 100.0k → 8.0k)')
    expect(elementText(renderNotificationThread([msg], AgentProvider.CLAUDE_CODE))).toBe(standalone)
    expect(elementText(renderNotificationThread([msg], AgentProvider.CODEX))).toBe(standalone)
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

  // -- divider markup parity (icon + layout, not just text) ----------------

  it('standalone and aggregate compact boundaries render identical markup, icon included', () => {
    // The standalone renderer used to emit a bare <div> with no icon while the
    // thread divider had the down-arrow icon; both now route through
    // CompactionDivider, so the full markup (not just the trimmed text) matches.
    const msg = compactMsg({ trigger: 'auto', pre_tokens: 100000, post_tokens: 8000 })
    expectMarkupParity(() => compactBoundaryRenderer.render(msg, undefined), [msg])
  })

  it('standalone and aggregate microcompact boundaries render identical markup, icon included', () => {
    const msg = microcompactMsg({})
    expectMarkupParity(() => microcompactBoundaryRenderer.render(msg, undefined), [msg])
  })

  it('standalone and aggregate compacting spinners render identical markup, icon included', () => {
    // compactingRenderer now routes through CompactionDivider (loading) instead
    // of a hand-rolled <Spinner/>, so the standalone per-message spinner and the
    // consolidated thread spinner emit identical markup -- the same drift
    // guarantee the boundary rows already had.
    const msg = { type: 'system', subtype: 'status', status: 'compacting' }
    expectMarkupParity(() => compactingRenderer.render(msg, undefined), [msg])
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

describe('shared notification labels: standalone vs thread parity', () => {
  // interrupted / context_cleared / agent_error now read from the same shared
  // constants as the thread switch, so the standalone renderer and the aggregate
  // thread must agree. These lock the anti-drift contract: re-inlining either
  // side's literal would fail here.
  it('interrupted renders identically standalone and aggregated', () => {
    const msg = { type: 'interrupted' }
    expectTextParity(() => interruptedRenderer.render(msg, undefined), [msg], 'Interrupted')
  })

  it('context_cleared renders identically standalone and aggregated', () => {
    const msg = { type: 'context_cleared' }
    expectTextParity(() => contextClearedRenderer.render(msg, undefined), [msg], 'Context cleared')
  })

  it('agent_error renders its error identically standalone and aggregated', () => {
    const msg = { type: 'agent_error', error: 'boom' }
    expectTextParity(() => agentErrorRenderer.render(msg, undefined), [msg], 'boom')
  })

  it('agent_error falls back to the shared "Unknown error" label on both paths', () => {
    const msg = { type: 'agent_error' }
    expectTextParity(() => agentErrorRenderer.render(msg, undefined), [msg], 'Unknown error')
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

  it('standalone and aggregate paths render plan_updated identically', () => {
    // Both paths share planUpdatedLabel, so the per-message renderer and the
    // consolidated thread must agree on the wording.
    const msg = { type: 'plan_updated', plan_title: 'My Plan', plan_file_path: '/p.md' }
    expectTextParity(() => planUpdatedRenderer.render(msg, undefined), [msg], 'Plan updated: My Plan')
  })

  it('standalone and aggregate paths render the auto-rename variant identically', () => {
    const msg = { type: 'plan_updated', plan_title: 'Auth Refactor', plan_file_path: '/p.md', update_agent_title: true }
    expectTextParity(() => planUpdatedRenderer.render(msg, undefined), [msg], 'Plan updated and renamed to Auth Refactor')
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

  it('thread and standalone paths render settings changes identically', () => {
    const changes = { foo: { old: 'a', new: 'b', label: 'My Setting', oldLabel: 'Old!', newLabel: 'New!' } }
    expectTextParity(() => settingsChangedRenderer.render(settingsMsg(changes), undefined), [settingsMsg(changes)], 'My Setting (Old! → New!)')
  })
})

// ---------------------------------------------------------------------------
// resultRenderer
// ---------------------------------------------------------------------------

/** Render a result message and return trimmed text content. */
function renderResultText(parsed: Record<string, unknown>): string {
  return elementText(resultRenderer.render(parsed))
}

/** Check if the result is rendered with danger color (error style). */
function isRenderedAsError(parsed: Record<string, unknown>): boolean {
  const result = resultRenderer.render(parsed)
  if (result === null)
    return false
  const { container } = render(() => result)
  const div = container.querySelector('div')
  return div?.style.color === 'var(--danger)'
}

describe('resultRenderer', () => {
  it('returns null for non-result messages', () => {
    expect(resultRenderer.render({ type: 'other' })).toBeNull()
  })

  it('renders is_error=true as error', () => {
    const parsed = { type: 'result', is_error: true, result: 'Something went wrong' }
    expect(isRenderedAsError(parsed)).toBe(true)
    expect(renderResultText(parsed)).toBe('Something went wrong')
  })

  it('renders a zero-turn unknown-command result (is_error:false) as a plain divider, not a danger dump', () => {
    // Claude Code reports unknown slash commands as is_error:false results that
    // echo their already-shown message. Trust is_error: show "Took Xs" rather
    // than a red dump of the result text. (The renderer ignores stop_reason /
    // num_turns now, so the fixture omits them.)
    const parsed = {
      type: 'result',
      is_error: false,
      subtype: 'success',
      result: 'Unknown command: /non-existent-skill',
      duration_ms: 24,
    }
    expect(isRenderedAsError(parsed)).toBe(false)
    const text = renderResultText(parsed)
    expect(text).toBe('Took 24ms')
    expect(text).not.toContain('Unknown command')
  })

  it('renders the /usage subscription result as a plain divider, not a danger dump', () => {
    const parsed = {
      type: 'result',
      is_error: false,
      subtype: 'success',
      result: 'You are currently using your subscription to power your Claude Code usage',
      duration_ms: 3,
    }
    expect(isRenderedAsError(parsed)).toBe(false)
    const text = renderResultText(parsed)
    expect(text).toBe('Took 3ms')
    expect(text).not.toContain('subscription')
  })

  it('renders a success result as a plain "Took Xs" divider, discarding its raw result text', () => {
    const parsed = {
      type: 'result',
      is_error: false,
      subtype: 'success',
      result: '## Context Usage\n\nSome output...',
      duration_ms: 1095,
    }
    expect(isRenderedAsError(parsed)).toBe(false)
    const text = renderResultText(parsed)
    expect(text).toBe('Took 1.1s')
    expect(text).not.toContain('Context Usage')
  })

  it('renders a non-error result with an absent subtype as a plain divider, not its raw text', () => {
    // A non-error result that omits `subtype` must be treated as success-like
    // (mirroring the error branch's `subtype && ...` guard), so it collapses to
    // "Took Xs" rather than leaking the raw echo text into the label.
    const parsed = {
      type: 'result',
      is_error: false,
      result: 'You are currently using your subscription to power your Claude Code usage',
      duration_ms: 7,
    }
    expect(isRenderedAsError(parsed)).toBe(false)
    const text = renderResultText(parsed)
    expect(text).toBe('Took 7ms')
    expect(text).not.toContain('subscription')
  })

  it('renders a non-error success result with a missing duration_ms as "Turn ended"', () => {
    // A missing duration_ms has no meaningful "Took" value, so the duration-only
    // divider falls back to "Turn ended" instead of a fake "Took 0ms".
    const parsed = { type: 'result', is_error: false, subtype: 'success', result: 'done' }
    expect(isRenderedAsError(parsed)).toBe(false)
    expect(renderResultText(parsed)).toBe('Turn ended')
  })

  it('renders a non-error success result with a real zero duration_ms as "Took 0ms"', () => {
    // A genuine zero is distinct from missing — an instant turn is "Took 0ms".
    const parsed = { type: 'result', is_error: false, subtype: 'success', result: 'done', duration_ms: 0 }
    expect(isRenderedAsError(parsed)).toBe(false)
    expect(renderResultText(parsed)).toBe('Took 0ms')
  })

  it('renders a non-success non-error result with a missing duration_ms as just its text', () => {
    // displayText present + no duration -> the suffix is dropped, not "(0ms)".
    const parsed = { type: 'result', is_error: false, subtype: 'cancelled', result: 'Cancelled' }
    expect(isRenderedAsError(parsed)).toBe(false)
    expect(renderResultText(parsed)).toBe('Cancelled')
  })

  it('renders a non-success non-error result with a real zero duration_ms as "<text> (0ms)"', () => {
    // displayText present + real zero -> the suffix is kept, mirroring "Took 0ms".
    const parsed = { type: 'result', is_error: false, subtype: 'cancelled', result: 'Cancelled', duration_ms: 0 }
    expect(isRenderedAsError(parsed)).toBe(false)
    expect(renderResultText(parsed)).toBe('Cancelled (0ms)')
  })

  it('renders success subtype with duration', () => {
    const parsed = { type: 'result', subtype: 'success', stop_reason: 'end_turn', result: 'done', duration_ms: 5000 }
    expect(isRenderedAsError(parsed)).toBe(false)
    expect(renderResultText(parsed)).toBe('Took 5.0s')
  })

  it('renders non-success subtype with result text and duration', () => {
    const parsed = { type: 'result', subtype: 'cancelled', stop_reason: 'end_turn', result: 'Cancelled', duration_ms: 2000 }
    expect(isRenderedAsError(parsed)).toBe(false)
    expect(renderResultText(parsed)).toBe('Cancelled (2.0s)')
  })

  it('renders error with subtype as humanized divider + detail', () => {
    const parsed = {
      type: 'result',
      is_error: true,
      subtype: 'error_during_execution',
      errors: ['[ede_diagnostic] result_type=user', 'Error: Request was aborted.'],
      duration_ms: 28563,
    }
    expect(isRenderedAsError(parsed)).toBe(true)
    const text = renderResultText(parsed)
    expect(text).toContain('Error during execution (29s)')
    expect(text).toContain('[ede_diagnostic] result_type=user')
    expect(text).toContain('Error: Request was aborted.')
  })

  it('renders error with subtype but no errors array shows subtype only', () => {
    const parsed = {
      type: 'result',
      is_error: true,
      subtype: 'error_during_execution',
      duration_ms: 5000,
    }
    const text = renderResultText(parsed)
    expect(text).toBe('Error during execution (5.0s)')
  })

  it('renders error without subtype as inline error (legacy behavior)', () => {
    const parsed = { type: 'result', is_error: true, result: 'Something went wrong', duration_ms: 100 }
    const text = renderResultText(parsed)
    expect(text).toBe('Something went wrong (100ms)')
    expect(text).not.toContain('\n')
  })

  it('renders the zero-turn /context result as a plain divider, not a danger dump', () => {
    // The `/context` table is already shown as an assistant bubble above, so the
    // redundant result envelope renders as a normal "Took Xs" turn-end divider
    // rather than dumping its table in danger red.
    const parsed = {
      type: 'result',
      subtype: 'success',
      is_error: false,
      result: '## Context Usage\n\n**Model:** claude-opus-4-8[1m]\n',
      duration_ms: 2062,
    }
    expect(isRenderedAsError(parsed)).toBe(false)
    const text = renderResultText(parsed)
    expect(text).toBe('Took 2.1s')
    expect(text).not.toContain('Context Usage')
  })

  it('still renders a genuinely failed result red even when its text starts with the context-usage header', () => {
    const parsed = { type: 'result', is_error: true, result: '## Context Usage\nboom', duration_ms: 5 }
    expect(isRenderedAsError(parsed)).toBe(true)
  })
})
