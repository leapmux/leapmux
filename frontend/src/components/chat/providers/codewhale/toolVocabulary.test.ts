import type { ToolVocabularyCheck } from '~/test-support/toolVocabulary'
import { describe, expect, it } from 'vitest'
import { CODEWHALE_TOOL } from '~/generated/contracts/codewhale-protocol'
import { documentedNamesThatReachAKind, staleGenericNames, undocumentedFallbacks } from '~/test-support/toolVocabulary'
import { codewhaleMcpToolName, codewhaleToolKind } from './toolKinds'

/**
 * The Codewhale tools that take the uncategorized row on purpose.
 *
 * EMPTY, and it must stay that way. The table this walks is the GENERATED
 * `CODEWHALE_TOOL`, whose values come from `contracts/codewhale-protocol.json` -- the
 * Go worker reads the same names.
 */
const GENERIC: Record<string, string> = {}

const CHECK: ToolVocabularyCheck = {
  names: Object.values(CODEWHALE_TOOL),
  kindOf: codewhaleToolKind,
  generic: GENERIC,
  fallback: 'other',
}

describe('codewhale tool vocabulary', () => {
  it('covers every tool the contract lists', () => {
    expect(
      undocumentedFallbacks(CHECK),
      'A tool added to contracts/codewhale-protocol.json takes the uncategorized row. Give it a '
      + 'kind in CODEWHALE_TOOL_KINDS.',
    ).toEqual([])
  })

  it('keeps every documented fallback pinned to a tool that exists', () => {
    expect(staleGenericNames(CHECK), 'Delete the entry, or repoint it at the tool that replaced it.').toEqual([])
  })

  it('documents no fallback for a tool that reaches a kind', () => {
    expect(documentedNamesThatReachAKind(CHECK), 'The note is stale and hides the next real omission.').toEqual([])
  })

  // The item kind is a name heuristic of the runtime, which puts `todo_write` under
  // `file_change` and `bash` under `tool_call`. The table is what decides.
  it('decides the kind from the tool name, never the item kind', () => {
    expect(codewhaleToolKind(CODEWHALE_TOOL.Bash)).toBe('execute')
    expect(codewhaleToolKind(CODEWHALE_TOOL.TodoWrite)).toBe('todo')
    expect(codewhaleToolKind(CODEWHALE_TOOL.ApplyPatch)).toBe('edit')
    expect(codewhaleToolKind(CODEWHALE_TOOL.RequestUserInput)).toBe('question')
  })

  it('tells an absent name from an unknown one', () => {
    expect(codewhaleToolKind('')).toBe('unspecified')
    expect(codewhaleToolKind('a_tool_from_a_later_release')).toBe('other')
  })
})

describe('codewhaleMcpToolName', () => {
  it('splits a model-facing name at the first separator after the prefix', () => {
    expect(codewhaleMcpToolName('mcp_docs_search')).toStrictEqual({ server: 'docs', tool: 'search' })
    expect(codewhaleMcpToolName('mcp_docs_search_all')).toStrictEqual({ server: 'docs', tool: 'search_all' })
    expect(codewhaleToolKind('mcp_docs_search')).toBe('mcp')
  })

  it('refuses a name that states no server or no tool', () => {
    expect(codewhaleMcpToolName('mcp_')).toBeNull()
    expect(codewhaleMcpToolName('mcp_docs')).toBeNull()
    expect(codewhaleMcpToolName('mcp__search')).toBeNull()
    expect(codewhaleMcpToolName('mcp_docs_')).toBeNull()
    expect(codewhaleToolKind('mcp_docs')).toBe('other')
  })

  it('reads no other name as a server tool', () => {
    expect(codewhaleMcpToolName(CODEWHALE_TOOL.Bash)).toBeNull()
    expect(codewhaleMcpToolName('constructor')).toBeNull()
    expect(codewhaleToolKind('constructor')).toBe('other')
    expect(codewhaleToolKind('toString')).toBe('other')
  })
})
