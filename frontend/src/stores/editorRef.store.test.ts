import type { Tab } from '~/stores/tab.types'
import { describe, expect, it, vi } from 'vitest'
import { registerProvider } from '~/components/chat/providers/registry'
import { AgentProvider } from '~/generated/leapmux/v1/agent_pb'
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

  it('skips a non-steerable child agent and reaches the steerable root behind it', () => {
    const setRoot = vi.fn()
    const activate = vi.fn()
    // A non-steerable child: parentAgentId set, acceptsMessages false, non-Codex
    // provider. Its composer is disabled and it must never receive an inserted
    // mention or quote.
    const nonSteerableChild: Tab = {
      type: TabType.AGENT,
      id: 'c1',
      workspaceId: 'ws',
      parentAgentId: 'root-1',
      acceptsMessages: false,
      agentProvider: AgentProvider.CLAUDE_CODE,
    }
    // The root is always steerable (no parentAgentId).
    const root: Tab = { type: TabType.AGENT, id: 'root-1', workspaceId: 'ws' }
    registerEditorRef('root-1', { get: () => '', set: setRoot, focus: vi.fn(), insert: vi.fn() })
    try {
      insertIntoMruAgentEditor(
        // Non-steerable child is MRU; the root is behind it.
        { mruTabs: () => [nonSteerableChild, root], activate },
        'hello',
      )
      expect(setRoot, 'the steerable root must receive the text').toHaveBeenCalled()
      expect(activate, 'a non-steerable child must never be targeted')
        .not
        .toHaveBeenCalledWith(expect.objectContaining({ id: 'c1' }))
    }
    finally {
      unregisterEditorRef('root-1')
    }
  })

  it('targets a steerable child (Codex, accepts messages) directly', () => {
    const setChild = vi.fn()
    const activate = vi.fn()
    // A steerable child: Codex child that accepts messages.
    const steerableChild: Tab = {
      type: TabType.AGENT,
      id: 'c1',
      workspaceId: 'ws',
      parentAgentId: 'root-1',
      acceptsMessages: true,
      agentProvider: AgentProvider.CODEX,
    }
    registerEditorRef('c1', { get: () => '', set: setChild, focus: vi.fn(), insert: vi.fn() })
    try {
      insertIntoMruAgentEditor(
        { mruTabs: () => [steerableChild], activate },
        'hello',
      )
      expect(setChild, 'a steerable child receives the text').toHaveBeenCalled()
      expect(activate).toHaveBeenCalledWith(expect.objectContaining({ id: 'c1' }))
    }
    finally {
      unregisterEditorRef('c1')
    }
  })

  it('optimistically targets a Codex child before hydration (acceptsMessages undefined)', () => {
    const setChild = vi.fn()
    const activate = vi.fn()
    // A Codex child whose acceptsMessages is not yet known (before listAgents
    // hydration). isSteerableAgentTab routes the pre-hydration fallback through
    // the provider plugin's supportsSubagentSend, so register Codex's capability.
    registerProvider(AgentProvider.CODEX, { classify: () => ({} as never), supportsSubagentSend: true })
    const codexChildUnhydrated: Tab = {
      type: TabType.AGENT,
      id: 'c1',
      workspaceId: 'ws',
      parentAgentId: 'root-1',
      agentProvider: AgentProvider.CODEX,
    }
    registerEditorRef('c1', { get: () => '', set: setChild, focus: vi.fn(), insert: vi.fn() })
    try {
      insertIntoMruAgentEditor(
        { mruTabs: () => [codexChildUnhydrated], activate },
        'hello',
      )
      expect(setChild, 'a Codex child is optimistically steerable pre-hydration').toHaveBeenCalled()
    }
    finally {
      unregisterEditorRef('c1')
    }
  })
})
