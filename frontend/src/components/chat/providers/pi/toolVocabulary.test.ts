import type { ToolVocabularyCheck } from '~/test-support/toolVocabulary'
import { describe, expect, it } from 'vitest'
import { PI_TOOL } from '~/generated/contracts/pi-protocol'
import { documentedNamesThatReachAKind, staleGenericNames, undocumentedFallbacks } from '~/test-support/toolVocabulary'
import { piToolKind } from './toolKinds'

/**
 * The Pi tools that take the uncategorized row on purpose.
 *
 * EMPTY, and it must stay that way. The table this walks is the GENERATED `PI_TOOL`,
 * whose values come from `contracts/pi-protocol.json` -- the Go worker reads the same
 * names.
 */
const GENERIC: Record<string, string> = {}

const CHECK: ToolVocabularyCheck = {
  names: Object.values(PI_TOOL),
  kindOf: piToolKind,
  generic: GENERIC,
  // Pi's table answers the EMPTY kind for a name it does not hold, which is the state
  // "the provider states no kind" rather than "uncategorized".
  fallback: '',
}

describe('pi tool vocabulary', () => {
  it('covers every tool the contract names', () => {
    expect(
      undocumentedFallbacks(CHECK),
      'A tool added to contracts/pi-protocol.json takes the uncategorized row. Give it a '
      + 'kind in PI_TOOL_KINDS. Introduce a new ToolKind when none of the closed set fits '
      + '-- there is no uncategorized rendering path.',
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

  // The question tool rpiv-ask-user-question registers. A question is the agent
  // stopping to reason with the reader, which is what every provider answers for it.
  it('names the question tool the way every provider does', () => {
    expect(piToolKind(PI_TOOL.AskUserQuestion)).toBe('question')
  })
})
