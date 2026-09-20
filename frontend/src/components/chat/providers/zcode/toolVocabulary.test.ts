import type { ToolVocabularyCheck } from '~/test-support/toolVocabulary'
import { describe, expect, it } from 'vitest'
import { ZCODE_TOOL } from '~/generated/contracts/zcode-protocol'
import { documentedNamesThatReachAKind, staleGenericNames, undocumentedFallbacks } from '~/test-support/toolVocabulary'
import { zcodeToolKind } from './toolKinds'

/**
 * The ZCode tools that take the uncategorized row on purpose.
 *
 * EMPTY, and it must stay that way. The table this walks is the GENERATED
 * `ZCODE_TOOL`, whose values come from `contracts/zcode-protocol.json` -- the Go
 * worker reads the same names.
 */
const GENERIC: Record<string, string> = {}

const CHECK: ToolVocabularyCheck = {
  names: Object.values(ZCODE_TOOL),
  kindOf: zcodeToolKind,
  generic: GENERIC,
  fallback: 'other',
}

describe('zcode tool vocabulary', () => {
  it('covers every tool the contract names', () => {
    expect(
      undocumentedFallbacks(CHECK),
      'A tool added to contracts/zcode-protocol.json takes the uncategorized row. Give it a '
      + 'kind in ZCODE_TOOL_KINDS. Introduce a new ToolKind when none of the closed set fits '
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

  // The three JavaScript tools run code in ZCode's own sandbox, which is the work a
  // shell does. Both web-search spellings take `web_search`: the CORPUS is what
  // separates the two kinds, and `search` queries one the session holds.
  it('gives a kind to the tools its own protocol never states', () => {
    expect(zcodeToolKind(ZCODE_TOOL.Js)).toBe('execute')
    expect(zcodeToolKind(ZCODE_TOOL.JsReset)).toBe('execute')
    expect(zcodeToolKind(ZCODE_TOOL.JsAddNodeModuleDir)).toBe('execute')
    expect(zcodeToolKind(ZCODE_TOOL.ServerWebSearch)).toBe('web_search')
    expect(zcodeToolKind(ZCODE_TOOL.WebSearch)).toBe('web_search')
  })

  // An EMPTY name states no kind at all, which is a different answer from a name the
  // table does not hold: the first row says nothing, the second says "uncategorized".
  it('tells an absent name from an unknown one', () => {
    expect(zcodeToolKind('')).toBe('unspecified')
    expect(zcodeToolKind('a_tool_from_a_later_release')).toBe('other')
  })
})
