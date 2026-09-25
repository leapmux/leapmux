import type { ToolVocabularyCheck } from '~/test-support/toolVocabulary'
import { describe, expect, it } from 'vitest'
import { KIRO_KIND, KIRO_TOOL_TITLE } from '~/generated/contracts/kiro-protocol'
import { documentedNamesThatReachAKind, staleGenericNames, undocumentedFallbacks } from '~/test-support/toolVocabulary'
import { acpToolCall } from '../acp/extractors/toolCall'
import { kiroToolCallAdapter } from './extractors/toolCall'
import { KIRO_TOOL, KIRO_TOOL_KINDS } from './toolKinds'

/**
 * The smallest arguments a tool must state for its own kind to build.
 *
 * The file changes state the FILE they change: the model refuses a file change whose
 * request states none, and degrades it to the uncategorized row.
 */
const MINIMAL_INPUT: Readonly<Record<string, Record<string, unknown>>> = {
  [KIRO_TOOL.WriteFile]: { path: '/p/a.ts', text: 'export const a = 1\n' },
  [KIRO_TOOL.ReplaceInFile]: { path: '/p/a.ts', oldStr: 'before', newStr: 'after' },
  [KIRO_TOOL.AppendToFile]: { path: '/p/a.ts', text: 'more\n' },
  [KIRO_TOOL.DeleteFile]: { targetFile: '/p/a.ts', explanation: 'unused' },
  [KIRO_TOOL.ControlProcess]: { action: 'stop', terminalId: 'term-1' },
}

/** One pending call, as Kiro's first `tool_call` states it: the title, the arguments and `_meta.kiro`. */
function kindOf(title: string) {
  return acpToolCall(
    { sessionUpdate: 'tool_call', toolCallId: 'kiro-vocab', status: 'pending', title, kind: 'other', rawInput: MINIMAL_INPUT[title] ?? {}, _meta: { kiro: { toolOrigin: 'default' } } },
    kiroToolCallAdapter,
    undefined,
  ).kind
}

const CHECK: ToolVocabularyCheck = {
  names: Object.keys(KIRO_TOOL_KINDS),
  kindOf,
  generic: {},
  // A tool Kiro adds later states `other` on its first frame, and the uncategorized
  // trio folds to `mcp`: the card that identifies what ran when nothing else does.
  fallback: 'mcp',
}

describe('kiro tool vocabulary', () => {
  it('covers every tool the kind table lists', () => {
    expect(
      undocumentedFallbacks(CHECK),
      'A tool Kiro identifies by its title took the uncategorized card. State its kind in '
      + 'KIRO_TOOL_KINDS, or drop the title Kiro no longer sends.',
    ).toEqual([])
  })

  it('keeps every documented fallback pinned to a tool that exists', () => {
    expect(staleGenericNames(CHECK), 'Delete the entry, or repoint it at the tool that replaced it.').toEqual([])
  })

  it('documents no fallback for a tool that reaches a kind', () => {
    expect(documentedNamesThatReachAKind(CHECK), 'The tool now has a kind, so the note here is stale.').toEqual([])
  })

  it('reaches the kind the table states for each title', () => {
    for (const [title, kind] of Object.entries(KIRO_TOOL_KINDS))
      expect(kindOf(title), title).toBe(kind)
  })

  it('reads a shell command by its wire kind, whatever its title', () => {
    expect(acpToolCall({ sessionUpdate: 'tool_call', toolCallId: 'run_command_1', status: 'pending', title: 'Print a marker', kind: 'execute', rawInput: { command: 'echo hi' } }, kiroToolCallAdapter, undefined).kind).toBe('execute')
  })

  it('reads a subagent, a question and an MCP tool by their identity', () => {
    const spawn = acpToolCall({ sessionUpdate: 'tool_call', toolCallId: 'invoke_subagent_1', status: 'pending', title: 'Sub-agent: helper', kind: 'other', rawInput: { name: 'helper', prompt: 'go' }, _meta: { kiro: { kind: KIRO_KIND.AgentSubtask, agentSubtaskId: 's' } } }, kiroToolCallAdapter, undefined)
    expect(spawn.kind).toBe('agent')
    const question = acpToolCall({ sessionUpdate: 'tool_call', toolCallId: 't_q', status: 'pending', title: 'Which DB?', kind: 'other', _meta: { kiro: { toolId: 'user_input', userInputOptions: [] } } }, kiroToolCallAdapter, undefined)
    expect(question.kind).toBe('question')
    const mcp = acpToolCall({ sessionUpdate: 'tool_call', toolCallId: 'm', status: 'pending', title: '@probe/echo', kind: 'other', rawInput: { text: 'hi' } }, kiroToolCallAdapter, undefined)
    expect(mcp.kind).toBe('mcp')
  })

  it('leaves an unknown tool to the uncategorized card', () => {
    expect(kindOf('A Tool From A Later Release')).toBe('mcp')
  })

  it('keeps the two titles that the worker reads in the contract', () => {
    expect(KIRO_TOOL_KINDS[KIRO_TOOL_TITLE.TaskList]).toBe('todo')
    expect(KIRO_TOOL_KINDS[KIRO_TOOL_TITLE.SwitchToExecution]).toBe('switch_mode')
  })
})
