import type { ToolVocabularyCheck } from '~/test-support/toolVocabulary'
import { describe, expect, it } from 'vitest'
import { documentedNamesThatReachAKind, staleGenericNames, undocumentedFallbacks } from '~/test-support/toolVocabulary'
import { canonicalClaudeToolName, claudeToolKind, claudeToolRowHidden } from './toolKinds'
import { CLAUDE_TOOL_NAMES } from './toolNames'

/**
 * The Claude tools that take the uncategorized row on purpose.
 *
 * EMPTY, and it must stay that way. Claude has no wire contract of its own -- it
 * reports a tool by NAME and nothing else -- so `CLAUDE_TOOL_NAMES` in
 * `toolNames.ts` is the table this walks, and every name in it reaches a kind. A
 * tool that takes the empty kind draws a wrench above a dump of its arguments and
 * identifies nothing the agent ran.
 */
const GENERIC: Record<string, string> = {}

const CHECK: ToolVocabularyCheck = {
  names: Object.values(CLAUDE_TOOL_NAMES),
  kindOf: claudeToolKind,
  generic: GENERIC,
  // Claude's table answers the EMPTY kind for a name it does not hold, which is the
  // state "the provider states no kind" -- not the state "uncategorized".
  fallback: '',
}

describe('claude tool vocabulary', () => {
  it('covers every tool the table lists', () => {
    expect(
      undocumentedFallbacks(CHECK),
      'A new tool takes the uncategorized row. Give it a kind in CLAUDE_TOOL_KINDS. '
      + 'Introduce a new ToolKind when none of the closed set fits -- there is no '
      + 'uncategorized rendering path.',
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

  // The three MCP-resource tools read or list a DOCUMENT the server holds, which is
  // the one case where Claude's own name states the kind and the table did not.
  it('maps the MCP resource tools onto read and list', () => {
    expect(claudeToolKind(CLAUDE_TOOL_NAMES.READ_MCP_RESOURCE)).toBe('read')
    expect(claudeToolKind(CLAUDE_TOOL_NAMES.READ_MCP_RESOURCE_DIR)).toBe('read')
    expect(claudeToolKind(CLAUDE_TOOL_NAMES.LIST_MCP_RESOURCES)).toBe('list')
  })

  /**
   * `ToolSearch` searches the tool REGISTRY, and `search` is the kind for that.
   *
   * `search` states a query against a corpus the session holds, and the file tree is
   * one corpus of several -- Copilot maps its own two tool-registry probes onto the
   * same kind, and it DRAWS them. The fallback would state that Claude's table holds
   * no kind for this tool, which is false, and `ir/toolKind.ts` reserves the fallback
   * for a tool that no table lists at all.
   *
   * The rows of this tool are hidden on both sides, so the kind picks no icon, no
   * label and no renderer today. That is why the entry needs a case of its own. The
   * three fallback cases at the top of this file catch the FALLBACK alone and tolerate
   * every other kind, so a mapping onto `list` or `agents` passes all three -- and a
   * reader of the table cannot see the hiding that makes the wrong kind harmless.
   */
  it('maps ToolSearch onto search rather than onto the fallback', () => {
    expect(claudeToolKind(CLAUDE_TOOL_NAMES.TOOL_SEARCH)).toBe('search')
  })
})

/**
 * A tool name is an OPEN vocabulary: an agent and a Model Context Protocol server both
 * choose their own. A plain object answers `constructor`, `toString` and `valueOf` from
 * `Object.prototype`, so a table keyed by one had to become a Map.
 */
describe('claude tool tables over an Object.prototype name', () => {
  const inherited = ['constructor', 'toString', 'valueOf', 'hasOwnProperty', '__proto__']

  it.each(inherited)('passes a tool called %s through unchanged', (name) => {
    expect(canonicalClaudeToolName(name)).toBe(name)
  })

  it.each(inherited)('gives a tool called %s the empty kind', (name) => {
    expect(claudeToolKind(name)).toBe('')
  })

  it.each(inherited)('hides neither side of a tool called %s', (name) => {
    expect(claudeToolRowHidden(name, 'request')).toBe(false)
    expect(claudeToolRowHidden(name, 'result')).toBe(false)
  })

  it('still folds every alias the table holds', () => {
    expect(canonicalClaudeToolName('Task')).toBe(CLAUDE_TOOL_NAMES.AGENT)
    expect(canonicalClaudeToolName('BashOutput')).toBe(CLAUDE_TOOL_NAMES.TASK_OUTPUT)
    expect(canonicalClaudeToolName('KillShell')).toBe(CLAUDE_TOOL_NAMES.TASK_STOP)
    expect(canonicalClaudeToolName('ListPeers')).toBe(CLAUDE_TOOL_NAMES.LIST_AGENTS)
    expect(canonicalClaudeToolName('Brief')).toBe(CLAUDE_TOOL_NAMES.SEND_USER_MESSAGE)
  })
})
