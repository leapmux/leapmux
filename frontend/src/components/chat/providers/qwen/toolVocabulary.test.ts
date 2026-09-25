import type { ToolVocabularyCheck } from '~/test-support/toolVocabulary'
import { describe, expect, it } from 'vitest'
import { QWEN_TOOL } from '~/generated/contracts/qwen-protocol'
import { documentedNamesThatReachAKind, staleGenericNames, undocumentedFallbacks } from '~/test-support/toolVocabulary'
import { acpToolCall } from '../acp/extractors/toolCall'
import { qwenToolCallAdapter } from './extractors/toolCall'
import { QWEN_TOOL_KINDS, QWEN_TOOL_NAME } from './toolKinds'

/**
 * The smallest arguments a tool must state for its own kind to build. The file
 * changes state the FILE they change, which the model requires of an edit or a write.
 */
const MINIMAL_INPUT: Readonly<Record<string, Record<string, unknown>>> = {
  [QWEN_TOOL_NAME.Edit]: { file_path: '/p/a.ts', old_string: 'before', new_string: 'after' },
  [QWEN_TOOL_NAME.NotebookEdit]: { notebook_path: '/p/a.ipynb', new_source: 'x = 1' },
  [QWEN_TOOL_NAME.WriteFile]: { file_path: '/p/a.ts', content: 'export const a = 1\n' },
  [QWEN_TOOL.TodoWrite]: { todos: [] },
}

/**
 * One pending call, as Qwen states it: a display title, the ACP kind `other`, and the
 * real tool name in `_meta.toolName`. The kind here is deliberately the uninformative
 * one, so the name alone must decide the row.
 */
function kindOf(name: string) {
  return acpToolCall(
    { sessionUpdate: 'tool_call', toolCallId: 'qwen-vocab', status: 'pending', title: 'Display title', kind: 'other', rawInput: MINIMAL_INPUT[name] ?? {}, _meta: { toolName: name } },
    qwenToolCallAdapter,
    undefined,
  ).kind
}

const CHECK: ToolVocabularyCheck = {
  names: [...Object.keys(QWEN_TOOL_KINDS), QWEN_TOOL.Agent, QWEN_TOOL.Workflow, QWEN_TOOL.TodoWrite],
  kindOf,
  generic: {},
  fallback: 'mcp',
}

describe('qwen tool vocabulary', () => {
  it('covers every tool the kind table lists', () => {
    expect(
      undocumentedFallbacks(CHECK),
      'A tool Qwen identifies in _meta.toolName took the uncategorized card. State its kind '
      + 'in QWEN_TOOL_KINDS, or drop the name Qwen no longer sends.',
    ).toEqual([])
  })

  it('keeps every documented fallback pinned to a tool that exists', () => {
    expect(staleGenericNames(CHECK)).toEqual([])
  })

  it('documents no fallback for a tool that reaches a kind', () => {
    expect(documentedNamesThatReachAKind(CHECK)).toEqual([])
  })

  it('gives each branch tool its own kind', () => {
    expect(kindOf(QWEN_TOOL.Agent)).toBe('agent')
    expect(kindOf(QWEN_TOOL.Workflow)).toBe('agent')
    expect(kindOf(QWEN_TOOL.TodoWrite)).toBe('todo')
    expect(kindOf(QWEN_TOOL.AskUserQuestion)).toBe('question')
    expect(kindOf(QWEN_TOOL.RunShellCommand)).toBe('execute')
    expect(kindOf(QWEN_TOOL.ExitPlanMode)).toBe('switch_mode')
  })

  it('draws a Model Context Protocol tool by its server and tool', () => {
    const call = acpToolCall({ sessionUpdate: 'tool_call', toolCallId: 'c', status: 'pending', kind: 'other', rawInput: { q: 'x' }, _meta: { toolName: 'mcp__docs__search' } }, qwenToolCallAdapter, undefined)
    expect(call.kind === 'mcp' && call.request).toMatchObject({ server: 'docs', tool: 'search', args: { q: 'x' } })
  })

  it('leaves an unknown tool to the kind the wire states', () => {
    expect(kindOf('a_tool_from_a_later_release')).toBe('mcp')
    expect(acpToolCall({ sessionUpdate: 'tool_call', toolCallId: 'c', status: 'pending', kind: 'read', rawInput: { file_path: '/a' }, _meta: { toolName: 'new_reader' } }, qwenToolCallAdapter, undefined).kind).toBe('read')
  })

  it('reads a frame with no tool name through the shared build', () => {
    expect(acpToolCall({ sessionUpdate: 'tool_call', toolCallId: 'c', status: 'pending', kind: 'execute', rawInput: { command: 'ls' } }, qwenToolCallAdapter, undefined).kind).toBe('execute')
  })
})
