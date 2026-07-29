import type { Tab } from '~/stores/tab.types'
import { describe, expect, it, vi } from 'vitest'
import { TabType } from '~/generated/leapmux/v1/workspace_pb'
import { computeSeparator, insertIntoMruAgentEditor, registerEditorRef, unregisterEditorRef } from './editorRef.store'

describe('computeSeparator', () => {
  it('returns empty string when current is empty (block)', () => {
    expect(computeSeparator('', 'block')).toBe('')
  })

  it('returns empty string when current is empty (inline)', () => {
    expect(computeSeparator('', 'inline')).toBe('')
  })

  it('returns \\n\\n for block mode with existing content', () => {
    expect(computeSeparator('hello', 'block')).toBe('\n\n')
  })

  it('returns space for inline mode with existing content', () => {
    expect(computeSeparator('hello', 'inline')).toBe(' ')
  })

  it('returns empty string for inline mode when current ends with newline', () => {
    expect(computeSeparator('hello\n', 'inline')).toBe('')
  })

  it('returns \\n\\n for block mode even when current ends with newline', () => {
    expect(computeSeparator('hello\n', 'block')).toBe('\n\n')
  })
})

/**
 * The AGENT narrowing lives here and nowhere else: `mruAgentEditorDeps` hands
 * over every tab type in MRU order. Nothing covered it — every case in
 * `mruAgentEditorDeps.test.ts` adds only agents, so deleting the filter left
 * the whole suite green while "quote to agent" would target whatever tab the
 * user last touched.
 */
describe('insertIntoMruAgentEditor', () => {
  const agent = (id: string): Tab => ({ type: TabType.AGENT, id, workspaceId: 'ws' })
  const terminal = (id: string): Tab => ({ type: TabType.TERMINAL, id, workspaceId: 'ws' })

  it('skips a non-agent tab at the MRU head and reaches the agent behind it', () => {
    const set = vi.fn()
    const activate = vi.fn()
    registerEditorRef('a1', { get: () => '', set, focus: vi.fn(), insert: vi.fn() })
    try {
      insertIntoMruAgentEditor(
        // A terminal is the most recently touched tab; the agent is behind it.
        { mruTabs: () => [terminal('t1'), agent('a1')], activate },
        'hello',
      )
      expect(set).toHaveBeenCalled()
      expect(activate, 'a terminal must never be an editor target')
        .not
        .toHaveBeenCalledWith(expect.objectContaining({ id: 't1' }))
    }
    finally {
      unregisterEditorRef('a1')
    }
  })

  it('does nothing when the workspace holds no agent at all', () => {
    const activate = vi.fn()
    insertIntoMruAgentEditor({ mruTabs: () => [terminal('t1')], activate }, 'hello')
    expect(activate).not.toHaveBeenCalled()
  })
})
