import type { ToolVocabularyCheck } from '~/test-support/toolVocabulary'
import { describe, expect, it } from 'vitest'
import { OH_MY_PI_TOOL } from '~/generated/contracts/ohmypi-protocol'
import { documentedNamesThatReachAKind, staleGenericNames, undocumentedFallbacks } from '~/test-support/toolVocabulary'
import { ohMyPiToolKind } from './toolKinds'

/**
 * The omp tools that take the uncategorized row on purpose.
 *
 * EMPTY, and it must stay that way. The table this walks is the GENERATED
 * `OH_MY_PI_TOOL`, whose values come from `contracts/ohmypi-protocol.json` -- the Go
 * worker reads the same names.
 */
const GENERIC: Record<string, string> = {}

const CHECK: ToolVocabularyCheck = {
  names: Object.values(OH_MY_PI_TOOL),
  kindOf: ohMyPiToolKind,
  generic: GENERIC,
  // The table answers `unspecified` for a name it does not hold: "the provider states
  // no kind", rather than "uncategorized".
  fallback: 'unspecified',
}

describe('ohmypi tool vocabulary', () => {
  it('covers every tool the contract lists', () => {
    expect(
      undocumentedFallbacks(CHECK),
      'A tool added to contracts/ohmypi-protocol.json takes the uncategorized row. Give it a kind in OH_MY_PI_TOOL_KINDS.',
    ).toEqual([])
  })

  it('keeps every documented fallback pinned to a tool that exists', () => {
    expect(staleGenericNames(CHECK), 'Delete the entry, or repoint it at the tool that replaced it.').toEqual([])
  })

  it('documents no fallback for a tool that reaches a kind', () => {
    expect(documentedNamesThatReachAKind(CHECK), 'The tool now has a kind, so the note here is stale.').toEqual([])
  })

  it('reads the question tool the way every provider does', () => {
    expect(ohMyPiToolKind(OH_MY_PI_TOOL.Ask)).toBe('question')
  })

  it('reads both names of the edit tool as an edit', () => {
    expect(ohMyPiToolKind(OH_MY_PI_TOOL.Edit)).toBe('edit')
    expect(ohMyPiToolKind(OH_MY_PI_TOOL.ApplyPatch)).toBe('edit')
  })

  it('answers unspecified for a name it does not hold, the inherited ones included', () => {
    expect(ohMyPiToolKind('mcp__github_search')).toBe('unspecified')
    expect(ohMyPiToolKind('constructor')).toBe('unspecified')
    expect(ohMyPiToolKind('toString')).toBe('unspecified')
  })
})
