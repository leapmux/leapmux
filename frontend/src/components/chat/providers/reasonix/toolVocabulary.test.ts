import type { ToolVocabularyCheck } from '~/test-support/toolVocabulary'
import { describe, expect, it } from 'vitest'
import { REASONIX_TOOL } from '~/generated/contracts/reasonix-protocol'
import { documentedNamesThatReachAKind, staleGenericNames, undocumentedFallbacks } from '~/test-support/toolVocabulary'
import { acpToolCall } from '../acp/extractors/toolCall'
import { reasonixToolCallAdapter } from './extractors/toolCall'
import { REASONIX_TOOL_KINDS, REASONIX_TOOL_NAME } from './toolKinds'

/**
 * The smallest arguments a tool must state for its own kind to build.
 *
 * The four file changes state the FILE they change. The model refuses a `delete`, an
 * `edit`, a `move` or a `write` whose request names none -- the row composes its
 * header from that list at every state of the call -- and degrades such a call to the
 * uncategorized row, so a case that states no file tests that row and not the tool.
 */
const MINIMAL_INPUT: Readonly<Record<string, Record<string, unknown>>> = {
  [REASONIX_TOOL_NAME.DeleteRange]: { path: '/p/a.ts' },
  [REASONIX_TOOL_NAME.DeleteSymbol]: { path: '/p/a.ts' },
  [REASONIX_TOOL_NAME.EditFile]: { path: '/p/a.ts', old_string: 'before', new_string: 'after' },
  [REASONIX_TOOL_NAME.MoveFile]: { source_path: '/p/a.ts', destination_path: '/p/b.ts' },
  [REASONIX_TOOL_NAME.MultiEdit]: { path: '/p/a.ts', edits: [{ old_string: 'before', new_string: 'after' }] },
  [REASONIX_TOOL_NAME.TodoWrite]: { todos: [] },
  [REASONIX_TOOL_NAME.WriteFile]: { path: '/p/a.ts', content: 'export const a = 1\n' },
}

/** One pending call titled with the name, the way every Reasonix call states its own. */
function kindOf(name: string) {
  return acpToolCall(
    { sessionUpdate: 'tool_call', toolCallId: 'rx-vocab', status: 'pending', kind: 'other', title: name, rawInput: MINIMAL_INPUT[name] ?? {} },
    reasonixToolCallAdapter,
    undefined,
  ).kind
}

const CHECK: ToolVocabularyCheck = {
  names: [...Object.keys(REASONIX_TOOL_KINDS), REASONIX_TOOL.Task, REASONIX_TOOL.ReadOnlyTask, 'todo_write'],
  kindOf,
  generic: {},
  // The uncategorized trio folds to `mcp`: the card that identifies what ran when
  // nothing else does.
  fallback: 'mcp',
}

describe('reasonix tool vocabulary', () => {
  it('covers every tool the title table lists', () => {
    expect(
      undocumentedFallbacks(CHECK),
      'A tool Reasonix identifies by its title took the uncategorized card. State its '
      + 'kind in REASONIX_TOOL_KINDS, or drop the name the daemon no longer sends.',
    ).toEqual([])
  })

  it('keeps every documented fallback pinned to a tool that exists', () => {
    expect(staleGenericNames(CHECK), 'Delete the entry, or repoint it at the tool that replaced it.').toEqual([])
  })

  it('documents no fallback for a tool that reaches a kind', () => {
    expect(
      documentedNamesThatReachAKind(CHECK),
      'The tool now has a kind, so the note here is stale and hides the next real omission.',
    ).toEqual([])
  })

  it('gives each tool the kind the title table states', () => {
    expect(kindOf('glob')).toBe('glob')
    expect(kindOf('grep')).toBe('grep')
    expect(kindOf('ls')).toBe('list')
    expect(kindOf('move_file')).toBe('move')
    expect(kindOf(REASONIX_TOOL.Task)).toBe('agent')
    expect(kindOf('todo_write')).toBe('todo')
  })

  // A tool a later release adds draws the MCP card, which identifies what ran; the
  // capability wrapper unwraps to the name it carries.
  it('leaves an unknown tool to the uncategorized card', () => {
    expect(kindOf('a_tool_from_a_later_release')).toBe('mcp')
  })
})
