import type { ToolFailureFixture, ToolVocabularyCheck } from '~/test-support/toolVocabulary'
import { describe, expect, it } from 'vitest'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { providerToolCall } from '~/test-support/toolCallFixture'
import { describeToolFailureLadder, describeToolResultCorpus } from '~/test-support/toolResultCases'
import { fixturesOnTheUncategorizedKind, openingFrameOf } from '~/test-support/toolVocabulary'
import { CURSOR_TOOL_RESULTS } from './toolResults.fixtures'
import '~/components/chat/providers'

const KINDS: ToolVocabularyCheck = {
  names: Object.keys(CURSOR_TOOL_RESULTS.fixtures),
  kindOf: () => 'other',
  generic: {},
  fallback: 'other',
}

// Each name comes from the fixture table's own keys, so the lookups miss for the type alone.
function callOf(name: string) {
  const fixture = CURSOR_TOOL_RESULTS.fixtures[name]
  return fixture === undefined ? null : providerToolCall(AgentProvider.CURSOR, fixture.payload, fixture.options)
}
const failureCallOf = (fixture: ToolFailureFixture) => providerToolCall(AgentProvider.CURSOR, fixture.payload, fixture.options)

/** The `tool_call` request that each fixture pairs with, read alone as a call still in flight. */
function requestCallOf(name: string) {
  const fixture = CURSOR_TOOL_RESULTS.fixtures[name]
  const frame = fixture === undefined ? null : openingFrameOf(fixture)
  return frame === null ? null : providerToolCall(AgentProvider.CURSOR, frame)
}

describe('cursor tool results', () => {
  it('keeps every fixture off the uncategorized kind', () => {
    // The narrow form of the kind question, and the only form this table can hold: it
    // answers `other` for every name. A cursor row takes its tool name from the title,
    // the saved record, or the `_toolName` the call carried; a fixture on the
    // uncategorized card lost all three.
    expect(
      fixturesOnTheUncategorizedKind(CURSOR_TOOL_RESULTS, callOf),
      'A fixture reached the uncategorized kind.',
    ).toStrictEqual([])
  })

  it('answers each fixture with the kind its own naming states', () => {
    const expected: Record<string, string> = {
      task: 'agent',
      createPlan: 'report',
      updateTodos: 'todo',
      generateImage: 'image',
      askQuestion: 'question',
      grep: 'grep',
      bash: 'execute',
      read: 'read',
      str_replace: 'edit',
      mcp_probe_echo: 'mcp',
    }
    for (const [name, kind] of Object.entries(expected)) {
      const call = callOf(name)
      expect(call, name).not.toBeNull()
      expect(call!.kind, name).toBe(kind)
    }
  })

  describeToolResultCorpus(KINDS, CURSOR_TOOL_RESULTS, callOf)
  describeToolFailureLadder(CURSOR_TOOL_RESULTS, { callOf, failureCallOf, requestCallOf })
})
