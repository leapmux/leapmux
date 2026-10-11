import { render } from '@solidjs/testing-library'
import { afterEach, describe, expect, it } from 'vitest'
import { ALL_PROVIDERS } from '~/generated/contracts/providers'
import { GOAL_STATUS_TOKEN, GOAL_TRANSITION, NOTIFICATION_TYPE } from '~/generated/contracts/worker-vocab'
import { AgentProvider, MessageCompletion } from '~/generated/proto/leapmux/v1/agent_pb'
import { clearSettingsLabelCache, updateSettingsLabelCache } from '~/lib/settingsLabelCache'
import { elementText, renderDivider, renderThreadElement, renderThreadGlyph, renderThreadHasIcon, renderThreadText } from '~/test-support/messageRenderProbes'
import { renderNotificationBlocks } from './notificationRenderers'

// Register the Claude and Codex plugins before testing their notification readers.
// Each fixture passes its actual provider to the production rendering path.
await import('./providers/claude/plugin')
await import('./providers/codex/plugin')

// The settings-label cache is shared across tests.
// Clear its registrations so a preceding case cannot change a later case's labels.
afterEach(() => {
  clearSettingsLabelCache()
})

// Pass each native notification to its provider's reader.
// Without that provider, the shared worker reader supplies no native fallback.
// Claude is the default for these fixtures. Codex cases pass Codex explicitly.
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
  // The backend keeps context_cleared and completed compaction boundaries mutually exclusive within a thread.
  // The frontend renders the received entries.

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

  it('marks a completed compaction divider but not plain notification text', () => {
    const boundary = render(() => renderThreadElement([compactBoundaryMsg], AgentProvider.CLAUDE_CODE))
    expect(boundary.container.querySelector('[data-testid="notification-divider"]')?.textContent).toContain('Context compacted')

    const plain = render(() => renderNotificationBlocks([{ kind: 'text', text: 'Context compacted' }]))
    expect(plain.container.querySelector('[data-testid="notification-divider"]')).toBeNull()
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

  // Raw notification fixtures.

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
    // commandExecution is not a compaction notification.
    // The reader returns no entry, so the thread displays no compaction spinner.
    expect(renderText(messages, AgentProvider.CODEX)).not.toContain('Compacting context')
  })

  it('draws the spinner for the bare {type:"compacting"} envelope, whatever the provider', () => {
    // The shared extractor reads the worker's compacting envelope.
    // PLAIN_ROW_TYPES classifies it as a notification before a provider reader runs.
    // A provider without a notification hook still requires this shared entry.
    const messages = [{ type: 'compacting' }]
    expect(renderText(messages, AgentProvider.CODEX)).toContain('Compacting context')
  })

  it('legacy synthesized {type:"system",subtype:"compact_boundary",threadId} from Codex still matches Claude\'s shape', () => {
    // This fixture uses the Claude system compact_boundary shape.
    // The Claude reader resolves its metadata.
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
   * Wrap the fields in the Claude microcompact_boundary envelope.
   * The reader ignores metadata for that event.
   * These fixtures verify that metadata-like fields do not change its plain label.
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
    // Ignore metadata-like microcompaction fields.
    // Display no trigger or token count for them.
    const messages = [microcompactMsg({ trigger: 'auto', preTokens: 200000, tokensSaved: 50000 })]
    expect(renderText(messages)).toBe('Context microcompacted')
  })

  it('reads camelCase keys (compactMetadata / preTokens / postTokens)', () => {
    // The consolidated fixture uses camelCase keys.
    // Resolve those keys and the native snake_case keys.
    const messages = [{
      type: 'system',
      subtype: 'compact_boundary',
      compactMetadata: { trigger: 'auto', preTokens: 100000, postTokens: 8000 },
    }]
    expect(renderText(messages)).toBe('Context compacted (auto, 100.0k → 8.0k)')
  })

  it('drops a lone tokens_saved that has no pre count to anchor a transition', () => {
    // Without a pre count, the saved count cannot supply a pre-to-post transition.
    // Do not display that saved count as a standalone token figure.
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
    // This fixture supplies the synthesized compact_boundary envelope.
    // Claude and Codex readers both support that envelope.
    // Verify that they produce the same label.
    const msg = compactMsg({ trigger: 'auto', pre_tokens: 100000, post_tokens: 8000 })
    const expected = 'Context compacted (auto, 100.0k → 8.0k)'
    expect(elementText(renderThreadElement([msg], AgentProvider.CLAUDE_CODE))).toBe(expected)
    expect(elementText(renderThreadElement([msg], AgentProvider.CODEX))).toBe(expected)
  })

  it('microcompaction ignores a metadata wrapper under any key (Claude emits none)', () => {
    // Ignore both microcompactMetadata and microcompact_metadata.
    // Each fixture must retain the plain label.
    const messages = [{
      type: 'system',
      subtype: 'microcompact_boundary',
      microcompact_metadata: { trigger: 'auto', preTokens: 200000, tokensSaved: 50000 },
    }]
    expect(renderText(messages)).toBe('Context microcompacted')
  })

  it('clamps a derived post to 0 when tokens_saved exceeds pre_tokens', () => {
    // A saved count above the pre count must not produce a negative post count.
    const messages = [compactMsg({ trigger: 'auto', pre_tokens: 30000, tokens_saved: 50000 })]
    expect(renderText(messages)).toBe('Context compacted (auto, 30.0k → 0)')
  })

  it('renders a zero post when tokens_saved equals pre_tokens', () => {
    const messages = [compactMsg({ pre_tokens: 100000, tokens_saved: 100000 })]
    expect(renderText(messages)).toBe('Context compacted (100.0k → 0)')
  })

  it('renders a no-op transition when tokens_saved is zero', () => {
    // A reported saved count of zero is present.
    // The derived post count therefore equals the pre count.
    const messages = [compactMsg({ pre_tokens: 100000, tokens_saved: 0 })]
    expect(renderText(messages)).toBe('Context compacted (100.0k → 100.0k)')
  })

  it('clamps an explicit negative post_tokens to 0 (not just the derived path)', () => {
    // Restrict both a derived and a directly reported post count to a minimum of zero.
    const messages = [compactMsg({ pre_tokens: 100000, post_tokens: -5 })]
    expect(renderText(messages)).toBe('Context compacted (100.0k → 0)')
  })

  it('clamps an explicit negative pre_tokens to 0', () => {
    const messages = [compactMsg({ pre_tokens: -100, post_tokens: 8000 })]
    expect(renderText(messages)).toBe('Context compacted (0 → 8.0k)')
  })

  it('drops a non-finite (NaN) count instead of rendering "NaN"', () => {
    // JSON cannot encode NaN, but a constructed payload can contain it.
    // Omit that count while retaining the other side of the transition.
    const messages = [compactMsg({ pre_tokens: Number.NaN, post_tokens: 8000 })]
    expect(renderText(messages)).toBe('Context compacted (→ 8.0k)')
  })

  it('drops a non-finite (Infinity) count instead of rendering "InfinityM"', () => {
    const messages = [compactMsg({ pre_tokens: 100000, post_tokens: Number.POSITIVE_INFINITY })]
    expect(renderText(messages)).toBe('Context compacted (100.0k)')
  })

  // Divider markup and layout.

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
    // Use the same provider for the notification and cached labels.
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
    // A provider can replace a well-known group's display label.
    // Consult its cache before displaying the canonical label.
    // An absent inline label must still preserve that provider-specific display.
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
    // An absent provider cache entry uses the canonical English label.
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
  // Read each worker notification through the shared extractor as a one-entry thread.
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

  // The worker normalizes the provider's reported status text.
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
 * The worker writes goal transitions in one shared envelope.
 * A durable goal change creates a row. A progress-only report creates no goal row.
 * This rendering path therefore serves every provider.
 */
describe('the notification thread: goal transitions', () => {
  it('announces a new goal with its objective', () => {
    expect(renderText([{ type: 'goal_updated', objective: 'every test passes', goal_status: 'active' }]))
      .toBe('Goal set: every test passes')
  })

  // Display the reported transition so a status change does not report a fresh Set.
  it('names the transition for each finished status', () => {
    expect(renderText([{ type: 'goal_updated', objective: 'x', goal_status: 'done' }]))
      .toBe('Goal achieved: x')
    expect(renderText([{ type: 'goal_updated', objective: 'x', goal_status: 'paused' }]))
      .toBe('Goal paused: x')
    expect(renderText([{ type: 'goal_updated', objective: 'x', goal_status: 'blocked' }]))
      .toBe('Goal blocked: x')
  })

  // Preserve the native detail when it differs from the neutral status token.
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
   * Set and Resume can both end in active.
   * The status alone cannot distinguish those transitions.
   * The worker compares the prior goal and reports the transition.
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

  // An absent or unrecognized transition uses the status fallback.
  // A recognized status retains its existing label.
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
    // The settings cache contains no foo entry.
    // The inline labels must therefore supply this fixture's display text.
    const messages = [settingsMsg({ foo: { old: 'a', new: 'b', label: 'My Setting', old_label: 'Old!', new_label: 'New!' } })]
    expect(renderText(messages)).toBe('My Setting (Old! → New!)')
  })

  it('thread path uses the "(new)" fallback when there is no old value', () => {
    const messages = [settingsMsg({ foo: { old: '', new: 'x', label: 'My Setting', new_label: 'X!' } })]
    expect(renderText(messages)).toBe('My Setting (X!)')
  })

  it('treats an omitted old key as a first-time set (the real first-set wire shape)', () => {
    // A first Set can omit old instead of sending an empty value.
    // pickString returns an empty string for that absent field.
    // The formatter then uses the new-only label.
    const messages = [settingsMsg({ foo: { new: 'x', label: 'My Setting', new_label: 'X!' } })]
    expect(renderText(messages)).toBe('My Setting (X!)')
  })

  it('keeps the arrow when the old value exists but its display resolves empty', () => {
    // An empty old_label overrides the old display text.
    // The old value still exists, so retain the transition form.
    const messages = [settingsMsg({ foo: { old: 'a', new: 'b', old_label: '', new_label: 'New!' } })]
    expect(renderText(messages)).toBe('foo ( → New!)')
  })

  it('honors an explicit empty-string label override instead of falling back to the key', () => {
    // Preserve an explicit empty inline label.
    // A truthy fallback would incorrectly display the key.
    const messages = [settingsMsg({ foo: { old: 'a', new: 'b', label: '', old_label: 'O', new_label: 'N' } })]
    expect(renderText(messages)).toBe('(O → N)')
  })

  it('thread path drops entries whose value is unchanged', () => {
    const messages = [settingsMsg({ foo: { old: 'same', new: 'same', label: 'My Setting' } })]
    expect(renderText(messages)).toBe('')
  })

  it('skips a null change entry without throwing', () => {
    // An untyped changes map can contain null.
    // Skip that entry rather than dereferencing its fields.
    const messages = [settingsMsg({ foo: null })]
    expect(renderText(messages)).toBe('')
  })

  it('skips malformed entries but still renders the well-formed ones', () => {
    const messages = [settingsMsg({ foo: null, bar: 'oops', model: { old: 'A', new: 'B' } })]
    expect(renderText(messages)).toBe('Model (A → B)')
  })
})

// Combine consecutive text entries into one paragraph.
// Display each divider as its own block.
// Several short text entries must not create several layout rows.
describe('renderNotificationBlocks: the blocks a thread lays out', () => {
  const contextClearedMsg = { type: 'context_cleared' } // Text entry.
  const compactBoundaryMsg = { // Divider entry.
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

// Native child results retain the same rendering as root results.
describe('native child completion', () => {
  const nativeResult = (overrides: Record<string, unknown> = {}) => ({
    type: 'result',
    subtype: 'success',
    is_error: false,
    duration_ms: 12,
    ...overrides,
  })

  it('shows the native completion duration', () => {
    expect(renderDivider(nativeResult(), AgentProvider.CLAUDE_CODE))
      .toEqual({ text: 'Turn ended (12ms)', isError: false })
  })

  it('shows native failure details and duration', () => {
    const rendered = renderDivider(nativeResult({
      subtype: 'error_during_execution',
      is_error: true,
      errors: ['The child could not read its assigned file.'],
    }), AgentProvider.CLAUDE_CODE)
    expect(rendered.isError).toBe(true)
    expect(rendered.text).toContain('Turn failed (12ms)')
    expect(rendered.text).toContain('The child could not read its assigned file.')
  })

  it('states an explicit interruption without inventing a native failure', () => {
    const rendered = renderDivider(nativeResult({ subtype: 'error_during_execution', is_error: true }), AgentProvider.CLAUDE_CODE, MessageCompletion.INTERRUPTED)
    expect(rendered).toEqual({ text: 'Turn interrupted (12ms)', isError: false })
  })

  it('preserves a native cancellation result', () => {
    expect(renderDivider(nativeResult({ subtype: 'cancelled', is_error: true }), AgentProvider.CLAUDE_CODE))
      .toEqual({ text: 'Turn interrupted (12ms)', isError: false })
  })

  it('does not invent a missing native duration', () => {
    expect(renderDivider({ type: 'result', subtype: 'success' }, AgentProvider.CLAUDE_CODE))
      .toEqual({ text: 'Turn ended', isError: false })
  })

  it('retains a zero native duration', () => {
    expect(renderDivider(nativeResult({ duration_ms: 0 }), AgentProvider.CLAUDE_CODE))
      .toEqual({ text: 'Turn ended (0ms)', isError: false })
  })

  it('retains each native error detail', () => {
    const rendered = renderDivider(nativeResult({
      subtype: 'error_during_execution',
      is_error: true,
      errors: ['The assigned file does not exist.', 'The native child stopped.'],
    }), AgentProvider.CLAUDE_CODE)
    expect(rendered.text).toContain('The assigned file does not exist.')
    expect(rendered.text).toContain('The native child stopped.')
  })

  it('retains failure text when the subtype is absent', () => {
    const rendered = renderDivider({ type: 'result', is_error: true, result: 'The native child could not finish.' }, AgentProvider.CLAUDE_CODE)
    expect(rendered.isError).toBe(true)
    expect(rendered.text).toContain('The native child could not finish.')
  })

  it('keeps the compaction divider independent from native completion', () => {
    const boundary = { type: 'system', subtype: 'compact_boundary', compact_metadata: { trigger: 'auto' } }
    expect(renderThreadGlyph([boundary], AgentProvider.CLAUDE_CODE)).not.toBeNull()
    expect(renderDivider(nativeResult(), AgentProvider.CLAUDE_CODE).text).toBe('Turn ended (12ms)')
    expect(renderText([boundary], AgentProvider.CLAUDE_CODE)).toContain('Context compacted')
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

describe('shared goal notification rendering', () => {
  it('renders an updated unknown goal without a false set or blocked label', () => {
    const { container } = render(() => renderThreadElement([{ type: NOTIFICATION_TYPE.GoalUpdated, objective: 'Keep the objective', goal_status: GOAL_STATUS_TOKEN.Unknown, goal_transition: GOAL_TRANSITION.Updated, status_detail: 'native-future-state' }], AgentProvider.CLAUDE_CODE))
    expect(container.textContent).toBe('Goal updated: Keep the objective (native-future-state)')
    expect(container.textContent).not.toContain('Goal set')
    expect(container.textContent).not.toContain('Goal blocked')
    expect(container.querySelector('[data-testid="notification-divider"]')).toBeNull()
  })

  it('renders a neutral unknown goal when no recognized transition exists', () => {
    const { container } = render(() => renderThreadElement([{ type: NOTIFICATION_TYPE.GoalUpdated, objective: 'Keep the objective', goal_status: GOAL_STATUS_TOKEN.Unknown, status_detail: 'native-future-state' }], AgentProvider.CLAUDE_CODE))
    expect(container.textContent).toBe('Goal status unknown: Keep the objective (native-future-state)')
    expect(container.textContent).not.toContain('Goal set')
    expect(container.textContent).not.toContain('Goal blocked')
  })
})
