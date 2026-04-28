import { render } from '@solidjs/testing-library'
import { describe, expect, it } from 'vitest'
import { MessageRole } from '~/generated/leapmux/v1/agent_pb'
import { updateSettingsLabelCache } from '~/lib/settingsLabelCache'
import { renderNotificationThread } from './notificationRenderers'
import { resultRenderer } from './providers/claude/notifications'

/** Extract all text content from the rendered container, trimmed. */
function renderText(messages: unknown[]): string {
  const result = renderNotificationThread(messages)
  if (result === null)
    return ''
  const { container } = render(() => result)
  return container.textContent?.trim() ?? ''
}

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

// ---------------------------------------------------------------------------
// resultRenderer
// ---------------------------------------------------------------------------

/** Render a result message and return trimmed text content. */
function renderResultText(parsed: Record<string, unknown>): string {
  const result = resultRenderer.render(parsed, MessageRole.SYSTEM)
  if (result === null)
    return ''
  const { container } = render(() => result)
  return container.textContent?.trim() ?? ''
}

/** Check if the result is rendered with danger color (error style). */
function isRenderedAsError(parsed: Record<string, unknown>): boolean {
  const result = resultRenderer.render(parsed, MessageRole.SYSTEM)
  if (result === null)
    return false
  const { container } = render(() => result)
  const div = container.querySelector('div')
  return div?.style.color === 'var(--danger)'
}

describe('resultRenderer', () => {
  it('returns null for non-result messages', () => {
    expect(resultRenderer.render({ type: 'other' }, MessageRole.SYSTEM)).toBeNull()
  })

  it('renders is_error=true as error', () => {
    const parsed = { type: 'result', is_error: true, result: 'Something went wrong' }
    expect(isRenderedAsError(parsed)).toBe(true)
    expect(renderResultText(parsed)).toBe('Something went wrong')
  })

  it('renders missing stop_reason with num_turns<=1 as error', () => {
    const parsed = { type: 'result', stop_reason: null, subtype: 'success', num_turns: 1, result: 'Unknown skill: foo', duration_ms: 5 }
    expect(isRenderedAsError(parsed)).toBe(true)
    expect(renderResultText(parsed)).toBe('Unknown skill: foo')
  })

  it('renders missing stop_reason with num_turns>1 as normal (not error)', () => {
    const parsed = {
      type: 'result',
      is_error: false,
      stop_reason: null,
      subtype: 'success',
      num_turns: 4,
      result: '## Context Usage\n\nSome output...',
      duration_ms: 1095,
    }
    expect(isRenderedAsError(parsed)).toBe(false)
    expect(renderResultText(parsed)).toContain('Took')
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
})
