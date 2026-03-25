import { render } from '@solidjs/testing-library'
import { describe, expect, it, vi } from 'vitest'
import { MessageRole } from '~/generated/leapmux/v1/agent_pb'
import { updateSettingsLabelCache } from '~/lib/settingsLabelCache'

// Mock messageRenderers to break the circular dependency (messageRenderers
// imports from notificationRenderers at module-init time).
vi.mock('./messageRenderers', () => ({
  isObject: (v: unknown): v is Record<string, unknown> =>
    typeof v === 'object' && v !== null && !Array.isArray(v),
}))

const { renderNotificationThread, resultRenderer } = await import('./notificationRenderers')

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
})

describe('renderNotificationThread: agent_renamed', () => {
  it('standalone agent_renamed shows "Renamed to <title>"', () => {
    const messages = [{ type: 'agent_renamed', title: 'My Plan' }]
    expect(renderText(messages)).toBe('Renamed to My Plan')
  })

  it('agent_renamed with empty title renders nothing', () => {
    const messages = [{ type: 'agent_renamed', title: '' }]
    expect(renderText(messages)).toBe('')
  })

  it('agent_renamed with missing title renders nothing', () => {
    const messages = [{ type: 'agent_renamed' }]
    expect(renderText(messages)).toBe('')
  })

  it('agent_renamed combined with settings_changed in thread', () => {
    const messages = [
      { type: 'settings_changed', changes: { model: { old: 'A', new: 'B' } } },
      { type: 'agent_renamed', title: 'Debug Session' },
    ]
    const text = renderText(messages)
    expect(text).toContain('Model')
    expect(text).toContain('Renamed to Debug Session')
  })

  it('agent_renamed combined with interrupted in thread', () => {
    const messages = [
      { type: 'agent_renamed', title: 'Test Plan' },
      { type: 'interrupted' },
    ]
    const text = renderText(messages)
    expect(text).toContain('Renamed to Test Plan')
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
})
