import { render } from '@solidjs/testing-library'
import { describe, expect, it, vi } from 'vitest'

// Mock messageRenderers to break the circular dependency (messageRenderers
// imports from notificationRenderers at module-init time).
vi.mock('./messageRenderers', () => ({
  isObject: (v: unknown): v is Record<string, unknown> =>
    typeof v === 'object' && v !== null && !Array.isArray(v),
}))

const { renderNotificationThread } = await import('./notificationRenderers')

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

describe('renderNotificationThread: compaction vs context_cleared ordering', () => {
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

  it('compaction AFTER context_cleared: shows compaction, hides "Context cleared"', () => {
    const messages = [contextClearedMsg, compactBoundaryMsg]
    expect(renderedContains(messages, 'Context compacted')).toBe(true)
    expect(renderedContains(messages, 'Context cleared')).toBe(false)
  })

  it('compaction BEFORE context_cleared: hides compaction, shows "Context cleared"', () => {
    const messages = [compactBoundaryMsg, contextClearedMsg]
    expect(renderedContains(messages, 'Context compacted')).toBe(false)
    expect(renderedContains(messages, 'Context cleared')).toBe(true)
  })

  it('plan_execution renders together with compaction', () => {
    const planExecMsg = {
      type: 'plan_execution',
      context_cleared: true,
      plan_file_path: '/path/plan.md',
    }
    const messages = [planExecMsg, compactBoundaryMsg]
    const text = renderText(messages)
    expect(text).toContain('Executing plan with clean context')
    expect(text).toContain('Context compacted')
  })

  it('compacting spinner AFTER context_cleared: shows spinner, hides "Context cleared"', () => {
    const messages = [contextClearedMsg, compactingStatusMsg]
    expect(renderedContains(messages, 'Compacting context...')).toBe(true)
    expect(renderedContains(messages, 'Context cleared')).toBe(false)
  })

  it('context_cleared alone: shows "Context cleared"', () => {
    const messages = [contextClearedMsg]
    expect(renderedContains(messages, 'Context cleared')).toBe(true)
  })

  it('compaction alone: shows compaction', () => {
    const messages = [compactBoundaryMsg]
    expect(renderedContains(messages, 'Context compacted')).toBe(true)
    expect(renderedContains(messages, 'Context cleared')).toBe(false)
  })

  it('settings_changed with contextCleared BEFORE compaction: shows compaction, hides "Context cleared"', () => {
    const settingsMsg = {
      type: 'settings_changed',
      changes: { model: { old: 'A', new: 'B' } },
      contextCleared: true,
    }
    const messages = [settingsMsg, compactBoundaryMsg]
    const text = renderText(messages)
    expect(text).toContain('Context compacted')
    expect(text).not.toContain('Context cleared')
    expect(text).toContain('Model')
  })

  it('compaction BEFORE settings_changed with contextCleared: hides compaction, shows "Context cleared"', () => {
    const settingsMsg = {
      type: 'settings_changed',
      changes: { model: { old: 'A', new: 'B' } },
      contextCleared: true,
    }
    const messages = [compactBoundaryMsg, settingsMsg]
    const text = renderText(messages)
    expect(text).not.toContain('Context compacted')
    expect(text).toContain('Context cleared')
    expect(text).toContain('Model')
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
