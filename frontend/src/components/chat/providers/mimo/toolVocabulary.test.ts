import type { ToolVocabularyCheck } from '~/test-support/toolVocabulary'
import { describe, expect, it } from 'vitest'
import { MIMO_TOOL } from '~/generated/contracts/mimo-protocol'
import { documentedNamesThatReachAKind, staleGenericNames, undocumentedFallbacks } from '~/test-support/toolVocabulary'
import { mimoToolKind } from './toolKinds'
import { MIMO_GENERIC_TOOLS } from './toolResults.fixtures'

const CHECK: ToolVocabularyCheck = {
  names: Object.values(MIMO_TOOL),
  kindOf: mimoToolKind,
  generic: MIMO_GENERIC_TOOLS,
  fallback: 'other',
}

describe('mimo tool vocabulary', () => {
  it('covers every tool the contract lists', () => {
    expect(
      undocumentedFallbacks(CHECK),
      'A tool added to contracts/mimo-protocol.json takes the uncategorized row. Give it a kind in MIMO_TOOL_KINDS.',
    ).toEqual([])
  })

  it('keeps every documented fallback pinned to a tool that exists', () => {
    expect(staleGenericNames(CHECK), 'Delete the entry, or repoint it at the tool that replaced it.').toEqual([])
  })

  it('documents no fallback for a tool that reaches a kind', () => {
    expect(documentedNamesThatReachAKind(CHECK), 'The tool now has a kind, so the note here is stale.').toEqual([])
  })

  // The two names that differ from the OpenCode names they resemble: `task` is the
  // to-do tool and `actor` starts a subagent.
  it('reads task as the to-do tool and actor as the subagent tool', () => {
    expect(mimoToolKind(MIMO_TOOL.Task)).toBe('todo')
    expect(mimoToolKind(MIMO_TOOL.Actor)).toBe('agent')
  })

  it('tells an absent name from an unknown one', () => {
    expect(mimoToolKind('')).toBe('unspecified')
    expect(mimoToolKind('a_tool_from_a_later_release')).toBe('other')
    expect(mimoToolKind('constructor')).toBe('other')
  })
})
