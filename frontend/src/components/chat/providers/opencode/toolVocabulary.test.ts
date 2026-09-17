import type { ToolKind } from '../../ir/toolKind'
import type { ToolVocabularyCheck } from '~/test-support/toolVocabulary'
import { describe, expect, it } from 'vitest'
import { documentedNamesThatReachAKind, staleGenericNames, undocumentedFallbacks } from '~/test-support/toolVocabulary'
import { acpToolCallIR } from '../acp/extractors/toolCall'
import { openCodeToolCallAdapterFor } from './extractors/toolCall'
import { OPENCODE_TOOL_NAMES } from './toolNames'

/**
 * The wire kind each registry id arrives UNDER. The protocol states the kind for
 * every tool it carries itself; the ids the table adds are the ones a title alone
 * must identify.
 */
const WIRE_KIND: Readonly<Record<string, string>> = {
  bash: 'execute',
  glob: 'search',
  grep: 'search',
}

/** The kind the family answers for one call titled with the registry id. */
function kindOf(name: string): ToolKind {
  return acpToolCallIR(
    { sessionUpdate: 'tool_call', toolCallId: 'vocab-1', status: 'pending', kind: WIRE_KIND[name] ?? 'other', title: name, rawInput: {} },
    openCodeToolCallAdapterFor(),
    undefined,
  ).kind
}

const CHECK: ToolVocabularyCheck = {
  names: Object.values(OPENCODE_TOOL_NAMES),
  kindOf,
  generic: {},
  // The uncategorized trio folds to `mcp`: the card that identifies what ran when
  // nothing else does.
  fallback: 'mcp',
}

describe('opencode tool vocabulary', () => {
  it('covers every registry id the family branches on', () => {
    expect(
      undocumentedFallbacks(CHECK),
      'A registry id the adapter reads took the uncategorized card. State its kind in '
      + 'OPENCODE_TOOL_NAMES, or drop the id the daemon no longer sends.',
    ).toEqual([])
  })

  it('keeps every documented fallback pinned to an id the family knows', () => {
    expect(staleGenericNames(CHECK), 'Delete the entry, or repoint it at the id that replaced it.').toEqual([])
  })

  it('documents no fallback for an id that reaches a kind', () => {
    expect(
      documentedNamesThatReachAKind(CHECK),
      'The id now has a kind, so the note here is stale and hides the next real omission.',
    ).toEqual([])
  })

  // The three ids whose kind the WIRE states: the title adds the NAME, and the
  // table must not overwrite what the protocol already answers.
  it('keeps the kind the wire states for the ids it carries', () => {
    expect(kindOf('bash')).toBe('execute')
    expect(kindOf('glob')).toBe('glob')
    expect(kindOf('grep')).toBe('grep')
  })

  // An id from a later daemon release states nothing this family knows, and the
  // uncategorized card is the one row that identifies what ran anyway.
  it('leaves an unknown id to the uncategorized card', () => {
    expect(kindOf('a_tool_from_a_later_release')).toBe('mcp')
  })
})
