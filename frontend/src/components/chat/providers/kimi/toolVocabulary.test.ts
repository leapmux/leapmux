import type { ToolVocabularyCheck } from '~/test-support/toolVocabulary'
import { describe, expect, it } from 'vitest'
import { KIMI_TOOL } from '~/generated/contracts/kimi-protocol'
import { documentedNamesThatReachAKind, staleGenericNames, undocumentedFallbacks } from '~/test-support/toolVocabulary'
import { kimiToolKind } from './toolKinds'

/**
 * The Kimi Code tools that take the uncategorized row on purpose.
 *
 * EMPTY, and it must stay that way. The table this walks is the GENERATED `KIMI_TOOL`,
 * whose values come from `contracts/kimi-protocol.json` -- the Go worker reads the same
 * names, so a tool added there is a tool both sides see.
 */
const GENERIC: Record<string, string> = {}

const CHECK: ToolVocabularyCheck = {
  names: Object.values(KIMI_TOOL),
  kindOf: kimiToolKind,
  generic: GENERIC,
  // A name the table does not hold is a tool a later release adds, which takes the
  // uncategorized card; a Model Context Protocol tool states its prefix and takes `mcp`.
  fallback: 'other',
}

describe('kimi tool vocabulary', () => {
  it('covers every tool the contract lists', () => {
    expect(
      undocumentedFallbacks(CHECK),
      'A tool added to contracts/kimi-protocol.json falls through to the uncategorized kind. Give it a kind in KIMI_TOOL_KINDS.',
    ).toEqual([])
  })

  it('keeps every documented fallback pinned to a tool that exists', () => {
    expect(staleGenericNames(CHECK)).toEqual([])
  })

  it('documents no fallback for a tool that reaches a kind', () => {
    expect(documentedNamesThatReachAKind(CHECK)).toEqual([])
  })

  it('reads a Model Context Protocol name as that kind', () => {
    expect(kimiToolKind('mcp__github__search_repos')).toBe('mcp')
  })

  it('reads a name no table holds as uncategorized, and no name as unspecified', () => {
    expect(kimiToolKind('FutureTool')).toBe('other')
    expect(kimiToolKind('')).toBe('unspecified')
    // A wire name that shadows an `Object.prototype` member is no entry of the table.
    expect(kimiToolKind('constructor')).toBe('other')
    expect(kimiToolKind('toString')).toBe('other')
  })

  it('names the tools every provider spells differently', () => {
    expect(kimiToolKind(KIMI_TOOL.AskUserQuestion)).toBe('question')
    expect(kimiToolKind(KIMI_TOOL.ExitPlanMode)).toBe('switch_mode')
    expect(kimiToolKind(KIMI_TOOL.AgentSwarm)).toBe('agent')
    expect(kimiToolKind(KIMI_TOOL.TaskOutput)).toBe('task')
    expect(kimiToolKind(KIMI_TOOL.CronCreate)).toBe('trigger')
  })
})
