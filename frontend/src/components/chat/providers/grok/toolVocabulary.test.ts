import type { ToolVocabularyCheck } from '~/test-support/toolVocabulary'
import { describe, expect, it } from 'vitest'
import { GROK_TOOL } from '~/generated/contracts/grok-protocol'
import { documentedNamesThatReachAKind, staleGenericNames, undocumentedFallbacks } from '~/test-support/toolVocabulary'
import { acpToolCall } from '../acp/extractors/toolCall'
import { grokToolCallAdapter } from './extractors/toolCall'
import { GROK_TOOL_KINDS, GROK_TOOL_NAME } from './toolKinds'

/**
 * The smallest arguments a tool must state for its own kind to build.
 *
 * The two file changes state the FILE they change: the model refuses an `edit` or a
 * `write` whose request states none, and degrades it to the uncategorized row.
 */
const MINIMAL_INPUT: Readonly<Record<string, Record<string, unknown>>> = {
  [GROK_TOOL_NAME.SearchReplace]: { file_path: '/p/a.ts', old_string: 'before', new_string: 'after' },
  [GROK_TOOL_NAME.Write]: { file_path: '/p/a.ts', content: 'export const a = 1\n' },
  [GROK_TOOL_NAME.TodoWrite]: { todos: [] },
  [GROK_TOOL_NAME.UseTool]: { tool_name: 'linear__list_issues', tool_input: {} },
}

/**
 * One pending call, as Grok's first `tool_call` states it: the name as the title and
 * in `_meta["x.ai/tool"]`, the model's own arguments, and no kind.
 */
function kindOf(name: string) {
  return acpToolCall(
    { sessionUpdate: 'tool_call', toolCallId: 'grok-vocab', status: 'pending', title: name, rawInput: MINIMAL_INPUT[name] ?? {}, _meta: { 'x.ai/tool': { version: 1, name } } },
    grokToolCallAdapter,
    undefined,
  ).kind
}

const CHECK: ToolVocabularyCheck = {
  names: [...Object.keys(GROK_TOOL_KINDS), GROK_TOOL.SpawnSubagent, GROK_TOOL_NAME.TodoWrite, GROK_TOOL_NAME.UseTool, GROK_TOOL_NAME.Workflow],
  kindOf,
  generic: {
    [GROK_TOOL_NAME.UseTool]: 'It calls a Model Context Protocol tool by name, and the row draws the call it wraps on the MCP card.',
  },
  // A tool Grok adds later states no kind on its first frame, and the uncategorized
  // trio folds to `mcp`: the card that identifies what ran when nothing else does.
  fallback: 'mcp',
}

describe('grok tool vocabulary', () => {
  it('covers every tool the kind table lists', () => {
    expect(
      undocumentedFallbacks(CHECK),
      'A tool Grok identifies in its _meta took the uncategorized card. State its kind in '
      + 'GROK_TOOL_KINDS, or drop the name Grok no longer sends.',
    ).toEqual([])
  })

  it('keeps every documented fallback pinned to a tool that exists', () => {
    expect(staleGenericNames(CHECK), 'Delete the entry, or repoint it at the tool that replaced it.').toEqual([])
  })

  it('documents no fallback for a tool that reaches a kind', () => {
    expect(documentedNamesThatReachAKind(CHECK), 'The tool now has a kind, so the note here is stale.').toEqual([])
  })

  it('gives each branch tool its own kind', () => {
    expect(kindOf(GROK_TOOL.SpawnSubagent)).toBe('agent')
    expect(kindOf(GROK_TOOL_NAME.Workflow)).toBe('agent')
    expect(kindOf(GROK_TOOL_NAME.TodoWrite)).toBe('todo')
    expect(kindOf(GROK_TOOL_NAME.RunTerminalCommand)).toBe('execute')
    expect(kindOf(GROK_TOOL_NAME.ListDir)).toBe('list')
  })

  it('leaves an unknown tool to the uncategorized card', () => {
    expect(kindOf('a_tool_from_a_later_release')).toBe('mcp')
  })

  // The identity outranks the title: the presentation update replaces the title with
  // prose, and the kind must not change with it.
  it('reads the identity when the title is prose', () => {
    const call = acpToolCall(
      { sessionUpdate: 'tool_call_update', toolCallId: 'c', status: 'pending', kind: 'other', title: 'List `/w`', rawInput: { variant: 'ListDir', target_directory: '/w' }, _meta: { 'x.ai/tool': { name: 'list_dir' } } },
      grokToolCallAdapter,
      undefined,
    )
    expect(call.kind).toBe('list')
    expect(call.name).toBe('list_dir')
  })

  it('takes the title as the name when the frame states no identity', () => {
    expect(acpToolCall({ sessionUpdate: 'tool_call', toolCallId: 'c', status: 'pending', title: 'grep', rawInput: { pattern: 'x' } }, grokToolCallAdapter, undefined).kind).toBe('grep')
  })
})
