import type { MessageCategory } from '../messageClassification'
import { describe, expect, it } from 'vitest'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { claudeExtractRow } from './claude/extractors/row'
import { CLAUDE_TOOL_NAMES } from './claude/toolNames'
import { providerFor } from './registry'
import { input } from './testUtils'
import './claude/plugin'
import './testMocks'

/** The three span sides an isolated extraction resolves to nothing. */
const NO_SIDES = { current: undefined, request: undefined, result: undefined, role: 'other' as const }

/** Construct a ToolSearch tool_use assistant message. */
function toolSearchRequest(args: Record<string, unknown> = {}) {
  return {
    type: 'assistant',
    message: {
      content: [{
        type: 'tool_use',
        id: 'test-toolsearch',
        name: CLAUDE_TOOL_NAMES.TOOL_SEARCH,
        input: { query: 'select:Read,Glob,Grep', ...args },
      }],
    },
  }
}

/** Construct a ToolSearch tool_result user message. */
function toolSearchResult(matches: string[]) {
  return {
    type: 'user',
    message: {
      role: 'user',
      content: [{
        tool_use_id: 'test-toolsearch',
        type: 'tool_result',
        content: matches.map(name => ({ type: 'tool_reference', tool_name: name })),
      }],
    },
    tool_use_result: { tool_name: CLAUDE_TOOL_NAMES.TOOL_SEARCH, matches, query: 'select:Read', total_deferred_tools: 19 },
  }
}

/**
 * `ToolSearch` is a deferred-tool DISCOVERY probe: the model asks which tools
 * exist before it calls one. Neither side says anything a reader acts on, so
 * both extract to `hidden` -- and they do it from the tool name rather than from
 * the worker's span column, which an isolated render does not carry.
 */
describe('toolSearch rows', () => {
  it('hides the request row', () => {
    const payload = toolSearchRequest()
    const category: MessageCategory = { kind: 'tool_use' }
    expect(claudeExtractRow({ parsed: input(payload), category, sides: NO_SIDES })).toEqual({ kind: 'hidden' })
  })

  it('hides the result row', () => {
    const payload = toolSearchResult(['Read', 'Glob', 'Grep'])
    expect(claudeExtractRow({ parsed: input(payload), category: { kind: 'tool_result' }, sides: NO_SIDES })).toEqual({ kind: 'hidden' })
  })

  it('classifies both sides as hidden once the span column names the tool', () => {
    const plugin = providerFor(AgentProvider.CLAUDE_CODE)!
    expect(plugin?.transcript.classify({ ...input(toolSearchRequest()), spanType: CLAUDE_TOOL_NAMES.TOOL_SEARCH }).kind).toBe('hidden')
    expect(plugin?.transcript.classify({ ...input(toolSearchResult(['Read'])), spanType: CLAUDE_TOOL_NAMES.TOOL_SEARCH }).kind).toBe('hidden')
  })
})
