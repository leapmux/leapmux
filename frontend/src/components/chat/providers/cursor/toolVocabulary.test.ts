import type { ToolKind } from '../../model/toolKind'
import type { ToolVocabularyCheck } from '~/test-support/toolVocabulary'
import { describe, expect, it } from 'vitest'
import { CURSOR_TOOL } from '~/generated/contracts/cursor-protocol'
import { documentedNamesThatReachAKind, staleGenericNames, undocumentedFallbacks } from '~/test-support/toolVocabulary'
import { acpToolCall } from '../acp/extractors/toolCall'
import { cursorToolCallAdapter } from './extractors/toolCall'

/**
 * The names that reach the reader through no wire kind: the one the contract
 * holds (`task`), and the four that state their own name inside `rawInput` because the
 * worker knows each by its JSON-RPC method and never reads the tool name.
 */
const FRONTEND_TOOL_NAMES = ['createPlan', 'askQuestion', 'updateTodos', 'generateImage'] as const

/** The kind the adapter answers for one call that states its own name. */
function kindOf(name: string): ToolKind {
  return acpToolCall(
    { sessionUpdate: 'tool_call', toolCallId: 'vocab-1', status: 'pending', kind: 'other', rawInput: { _toolName: name } },
    cursorToolCallAdapter,
    undefined,
  ).kind
}

const CHECK: ToolVocabularyCheck = {
  names: [...Object.values(CURSOR_TOOL), ...FRONTEND_TOOL_NAMES],
  kindOf,
  generic: {},
  // The uncategorized trio folds to `mcp`: the card that identifies what ran when
  // nothing else does.
  fallback: 'mcp',
}

describe('cursor tool vocabulary', () => {
  it('covers every name a cursor call states for itself', () => {
    expect(
      undocumentedFallbacks(CHECK),
      'A self-naming tool took the uncategorized card. State its kind in the '
      + 'adapter, or drop the name the runtime no longer sends.',
    ).toEqual([])
  })

  it('keeps every documented fallback pinned to a name the adapter knows', () => {
    expect(staleGenericNames(CHECK), 'Delete the entry, or repoint it at the name that replaced it.').toEqual([])
  })

  it('documents no fallback for a name that reaches a kind', () => {
    expect(
      documentedNamesThatReachAKind(CHECK),
      'The name now has a kind, so the note here is stale and hides the next real omission.',
    ).toEqual([])
  })

  it('gives each self-naming tool the kind it takes', () => {
    expect(kindOf(CURSOR_TOOL.Task)).toBe('agent')
    expect(kindOf('createPlan')).toBe('report')
    expect(kindOf('askQuestion')).toBe('question')
    expect(kindOf('updateTodos')).toBe('todo')
    expect(kindOf('generateImage')).toBe('image')
  })

  // A name from a later release states nothing this adapter knows, and the
  // uncategorized card is the one row that identifies what ran anyway.
  it('leaves an unknown name to the uncategorized card', () => {
    expect(kindOf('a_tool_from_a_later_release')).toBe('mcp')
  })
})
