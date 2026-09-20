import type { ToolVocabularyCheck } from '~/test-support/toolVocabulary'
import { describe, expect, it } from 'vitest'
import { COPILOT_TOOL } from '~/generated/contracts/copilot-protocol'
import { documentedNamesThatReachAKind, staleGenericNames, undocumentedFallbacks } from '~/test-support/toolVocabulary'
import { copilotToolKind } from './toolKinds'

/**
 * The Copilot tools that take the uncategorized row on purpose.
 *
 * EMPTY, and it must stay that way. The table this walks is the GENERATED
 * `COPILOT_TOOL`, whose values come from `contracts/copilot-protocol.json` -- the Go
 * worker reads the same names, so a tool added there is a tool both sides see.
 */
const GENERIC: Record<string, string> = {}

const CHECK: ToolVocabularyCheck = {
  names: Object.values(COPILOT_TOOL),
  kindOf: copilotToolKind,
  generic: GENERIC,
  // An unnamed Copilot tool is a Model Context Protocol tool, an extension tool or
  // one a later release adds, and all three draw the shared card -- so `mcp` is the
  // fallback, and a CONTRACT name that reaches it is a table omission.
  fallback: 'mcp',
}

describe('copilot tool vocabulary', () => {
  it('covers every tool the contract names', () => {
    expect(
      undocumentedFallbacks(CHECK),
      'A tool added to contracts/copilot-protocol.json falls through to the Model Context '
      + 'Protocol kind. Give it a kind in COPILOT_TOOL_KINDS. Introduce a new ToolKind when '
      + 'none of the closed set fits -- there is no uncategorized rendering path.',
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

  // Four names state their own kind, and each matches what every other provider
  // answers for the same concept -- which is the point of a shared kind set.
  it('names the tools that every provider spells differently', () => {
    expect(copilotToolKind(COPILOT_TOOL.AskUser)).toBe('question')
    expect(copilotToolKind(COPILOT_TOOL.ExitPlanMode)).toBe('switch_mode')
    expect(copilotToolKind(COPILOT_TOOL.ToolSearch)).toBe('search')
    expect(copilotToolKind(COPILOT_TOOL.GenericToolSearch)).toBe('search')
  })

  // The table is the ONE place the kind is decided. A second decision in the row
  // build used to say `task` over a table that said `execute`, so the vocabulary
  // this walks and the card a reader saw stated two different things.
  it('names each background-shell tool a task rather than an execution', () => {
    expect(copilotToolKind(COPILOT_TOOL.ReadBash)).toBe('task')
    expect(copilotToolKind(COPILOT_TOOL.ListBash)).toBe('task')
    expect(copilotToolKind(COPILOT_TOOL.StopBash)).toBe('task')
    expect(copilotToolKind(COPILOT_TOOL.WriteBash)).toBe('task')
    expect(copilotToolKind(COPILOT_TOOL.Bash)).toBe('execute')
  })
})
