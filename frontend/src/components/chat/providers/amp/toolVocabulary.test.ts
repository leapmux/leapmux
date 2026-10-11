import type { ToolVocabularyCheck } from '~/test-support/toolVocabulary'
import { describe, expect, it } from 'vitest'
import { AMP_SHELL_TOOL, AMP_SUBAGENT_TOOL } from '~/generated/contracts/amp-protocol'
import { documentedNamesThatReachAKind, staleGenericNames, undocumentedFallbacks } from '~/test-support/toolVocabulary'
import { ampToolKind } from './toolKinds'
import { AMP_TOOL_NAME } from './toolNames'

/**
 * The Amp tools that take the uncategorized row on purpose.
 *
 * EMPTY, and it must stay that way. The tables this walks are `AMP_TOOL_NAME` and the
 * generated `AMP_SUBAGENT_TOOL` and `AMP_SHELL_TOOL`, which the worker reads too.
 */
const GENERIC: Record<string, string> = {}

const CHECK: ToolVocabularyCheck = {
  names: [...Object.values(AMP_TOOL_NAME), ...Object.values(AMP_SUBAGENT_TOOL), ...Object.values(AMP_SHELL_TOOL)],
  kindOf: ampToolKind,
  generic: GENERIC,
  // The table answers `unspecified` for a name it does not hold: "the provider states
  // no kind", rather than "uncategorized".
  fallback: 'unspecified',
}

describe('amp tool vocabulary', () => {
  it('covers every tool the vocabulary lists', () => {
    expect(
      undocumentedFallbacks(CHECK),
      'A tool added to AMP_TOOL_NAME takes the uncategorized row. Give it a kind in AMP_TOOL_KINDS.',
    ).toEqual([])
  })

  it('keeps every documented fallback pinned to a tool that exists', () => {
    expect(staleGenericNames(CHECK), 'Delete the entry, or repoint it at the tool that replaced it.').toEqual([])
  })

  it('documents no fallback for a tool that reaches a kind', () => {
    expect(documentedNamesThatReachAKind(CHECK), 'The tool now has a kind, so the note here is stale.').toEqual([])
  })

  it('reads every subagent tool as a subagent launch', () => {
    for (const name of Object.values(AMP_SUBAGENT_TOOL))
      expect(ampToolKind(name), name).toBe('agent')
  })

  it('reads every shell name as a command, and the two background tools as a task', () => {
    expect(ampToolKind(AMP_SHELL_TOOL.ShellCommand)).toBe('execute')
    expect(ampToolKind(AMP_TOOL_NAME.AsyncShellCommand)).toBe('execute')
    expect(ampToolKind(AMP_TOOL_NAME.Bash)).toBe('execute')
    expect(ampToolKind(AMP_SHELL_TOOL.ShellCommandStatus)).toBe('task')
    expect(ampToolKind(AMP_SHELL_TOOL.ShellCommandKill)).toBe('task')
  })

  it('answers unspecified for a name it does not hold, the inherited ones included', () => {
    expect(ampToolKind('mcp__github__search')).toBe('unspecified')
    expect(ampToolKind('task')).toBe('unspecified')
    expect(ampToolKind('constructor')).toBe('unspecified')
    expect(ampToolKind('toString')).toBe('unspecified')
  })
})
