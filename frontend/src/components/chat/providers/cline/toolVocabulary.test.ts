import type { ToolVocabularyCheck } from '~/test-support/toolVocabulary'
import { describe, expect, it } from 'vitest'
import { CLINE_TOOL, CLINE_TOOL_PREFIX } from '~/generated/contracts/cline-protocol'
import { documentedNamesThatReachAKind, staleGenericNames, undocumentedFallbacks } from '~/test-support/toolVocabulary'
import { clineToolKind } from './toolKinds'
import { CLINE_TOOL_NAME } from './toolNames'

/**
 * The Cline tools that take the uncategorized row on purpose.
 *
 * EMPTY, and it must stay that way. The tables this walks are `CLINE_TOOL_NAME` and the
 * generated `CLINE_TOOL`, which the worker reads too.
 */
const GENERIC: Record<string, string> = {}

const CHECK: ToolVocabularyCheck = {
  names: [...Object.values(CLINE_TOOL_NAME), ...Object.values(CLINE_TOOL)],
  kindOf: clineToolKind,
  generic: GENERIC,
  // The table answers `unspecified` for a name it does not hold: "the provider states
  // no kind", rather than "uncategorized".
  fallback: 'unspecified',
}

describe('cline tool vocabulary', () => {
  it('covers every tool the vocabulary lists', () => {
    expect(
      undocumentedFallbacks(CHECK),
      'A tool added to CLINE_TOOL_NAME takes the uncategorized row. Give it a kind in CLINE_TOOL_KINDS.',
    ).toEqual([])
  })

  it('keeps every documented fallback pinned to a tool that exists', () => {
    expect(staleGenericNames(CHECK), 'Delete the entry, or repoint it at the tool that replaced it.').toEqual([])
  })

  it('documents no fallback for a tool that reaches a kind', () => {
    expect(documentedNamesThatReachAKind(CHECK), 'The tool now has a kind, so the note here is stale.').toEqual([])
  })

  it('reads the tools the worker dispatches on', () => {
    expect(clineToolKind(CLINE_TOOL.RunCommands)).toBe('execute')
    expect(clineToolKind(CLINE_TOOL.SpawnAgent)).toBe('agent')
    expect(clineToolKind(CLINE_TOOL.AskQuestion)).toBe('question')
    expect(clineToolKind(CLINE_TOOL.SwitchToActMode)).toBe('switch_mode')
  })

  // A configured agent of `.cline/agents/` is a tool of its own for each agent,
  // `subagent_<name>_<hash>`, which runs a child as spawn_agent does.
  it('reads a configured agent\'s tool as a subagent', () => {
    expect(clineToolKind(`${CLINE_TOOL_PREFIX.ConfiguredAgent}reviewer_1a2b`)).toBe('agent')
    expect(clineToolKind('github__subagent_search')).toBe('unspecified')
  })

  it('answers unspecified for a name it does not hold, the inherited ones included', () => {
    expect(clineToolKind('mcp__github__search')).toBe('unspecified')
    expect(clineToolKind('constructor')).toBe('unspecified')
    expect(clineToolKind('toString')).toBe('unspecified')
  })
})
