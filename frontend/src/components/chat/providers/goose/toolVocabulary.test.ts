import type { ToolKind } from '../../ir/toolKind'
import type { ToolVocabularyCheck } from '~/test-support/toolVocabulary'
import { describe, expect, it } from 'vitest'
import { documentedNamesThatReachAKind, staleGenericNames, undocumentedFallbacks } from '~/test-support/toolVocabulary'
import { acpToolCallIR } from '../acp/extractors/toolCall'
import { gooseToolCallAdapter } from './extractors/toolCall'
import { GOOSE_DEVELOPER_TOOL, GOOSE_TOOL_KINDS } from './toolKinds'

/** The kind the adapter answers for one `_meta` tool name. */
function kindOf(toolName: string, extensionName?: string, rawInput: Record<string, unknown> = {}): ToolKind {
  const separator = toolName.indexOf('__')
  const extension = extensionName ?? (separator >= 0 ? toolName.slice(0, separator) : '')
  const name = separator >= 0 ? toolName.slice(separator + 2) : toolName
  return acpToolCallIR(
    {
      sessionUpdate: 'tool_call',
      toolCallId: 'goose-vocab',
      status: 'pending',
      kind: 'other',
      title: toolName,
      _meta: { goose: { toolCall: { toolName: name, ...(extension ? { extensionName: extension } : {}) } } },
      rawInput,
    },
    gooseToolCallAdapter,
    undefined,
  ).kind
}

/** The extension each table name runs inside. */
const EXTENSION_OF: Readonly<Record<string, string>> = {
  delegate: 'summon',
  todo_write: 'todo',
}

/**
 * The smallest arguments a tool must state for its own kind to build.
 *
 * The two file changes state the FILE they change. The IR refuses an `edit` or a
 * `write` whose request names none -- the row composes its header from that list at
 * every state of the call -- and degrades such a call to the uncategorized row, so a
 * case that states no file tests that row and not the tool. Goose spells the two
 * sides of an edit `before` and `after`, which the developer branch renames.
 */
const INPUT_OF: Readonly<Record<string, Record<string, unknown>>> = {
  [GOOSE_DEVELOPER_TOOL.Edit]: { path: '/p/a.ts', before: 'before', after: 'after' },
  [GOOSE_DEVELOPER_TOOL.Write]: { path: '/p/a.ts', content: 'export const a = 1\n' },
  todo_write: { content: '- [ ] One' },
}

const CHECK: ToolVocabularyCheck = {
  names: [...Object.keys(GOOSE_TOOL_KINDS), 'delegate', 'todo_write'],
  kindOf: name => kindOf(name, EXTENSION_OF[name] ?? 'developer', INPUT_OF[name] ?? {}),
  generic: {},
  // The uncategorized trio folds to `mcp`: the card that identifies what ran when
  // nothing else does.
  fallback: 'mcp',
}

describe('goose tool vocabulary', () => {
  it('covers every tool the metadata table lists', () => {
    expect(
      undocumentedFallbacks(CHECK),
      'A tool the `_meta` record identifies took the uncategorized card. State its kind '
      + 'in GOOSE_TOOL_KINDS or the extension branches, or drop the name the '
      + 'daemon no longer sends.',
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

  it('gives each tool the kind its platform extension states', () => {
    expect(kindOf('delegate', 'summon', { instructions: 'Inspect' })).toBe('agent')
    expect(kindOf('todo_write', 'todo', { content: '- [ ] One' })).toBe('todo')
    expect(kindOf('edit', 'developer', INPUT_OF.edit)).toBe('edit')
    expect(kindOf('shell', 'developer')).toBe('execute')
    expect(kindOf('tree', 'developer')).toBe('list')
  })

  // An extension Goose added later draws the MCP card that states the server and the
  // tool, which is exactly what ran.
  it('leaves an extension it does not decorate to the MCP card', () => {
    expect(kindOf('recall', 'memory')).toBe('mcp')
  })
})
