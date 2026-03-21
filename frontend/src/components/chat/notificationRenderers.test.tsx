import { render } from '@solidjs/testing-library'
import { describe, expect, it, vi } from 'vitest'

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
    const modeIdx = text.indexOf('Mode')
    expect(clearedIdx).toBeGreaterThanOrEqual(0)
    expect(modeIdx).toBeGreaterThan(clearedIdx)
  })

  it('settings_changed before context_cleared preserves order', () => {
    const messages = [
      { type: 'settings_changed', changes: { permissionMode: { old: 'default', new: 'plan' } } },
      { type: 'context_cleared' },
    ]
    const text = renderText(messages)
    const modeIdx = text.indexOf('Mode')
    const clearedIdx = text.indexOf('Context cleared')
    expect(modeIdx).toBeGreaterThanOrEqual(0)
    expect(clearedIdx).toBeGreaterThan(modeIdx)
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

/** Render a result message via resultRenderer and return its text and style. */
function renderResult(parsed: Record<string, unknown>): { text: string, color: string } {
  const el = resultRenderer.render(parsed, 'assistant')
  if (el === null)
    return { text: '', color: '' }
  const { container } = render(() => el)
  const div = container.querySelector('div')
  return {
    text: div?.textContent?.trim() ?? '',
    color: div?.style.color ?? '',
  }
}

describe('resultRenderer: stop_reason null handling', () => {
  it('success subtype with context stats does not show as error', () => {
    const { text, color } = renderResult({
      type: 'result',
      subtype: 'success',
      stop_reason: null,
      result: 'Context usage: 50k tokens',
      duration_ms: 1000,
    })
    expect(color).toBe('')
    expect(text).toContain('Took')
  })

  it('success subtype with known error prefix shows as error', () => {
    const { text, color } = renderResult({
      type: 'result',
      subtype: 'success',
      stop_reason: null,
      result: 'Unknown skill: foo',
      duration_ms: 500,
    })
    expect(color).toBe('var(--danger)')
    expect(text).toBe('Unknown skill: foo')
  })

  it('missing subtype with result text shows as error', () => {
    const { text, color } = renderResult({
      type: 'result',
      stop_reason: null,
      result: 'Something went wrong',
      duration_ms: 200,
    })
    expect(color).toBe('var(--danger)')
    expect(text).toBe('Something went wrong')
  })
})
