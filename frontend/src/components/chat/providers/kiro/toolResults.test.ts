import type { ToolFailureFixture, ToolVocabularyCheck } from '~/test-support/toolVocabulary'
import { describe, expect, it } from 'vitest'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { providerToolCall } from '~/test-support/toolCallFixture'
import { describeToolFailureLadder, describeToolResultCorpus } from '~/test-support/toolResultCases'
import { fixturesOnTheUncategorizedKind, openingFrameOf } from '~/test-support/toolVocabulary'
import { KIRO_TOOL_KINDS } from './toolKinds'
import { KIRO_IDENTIFIED_TOOLS, KIRO_TOOL_RESULTS } from './toolResults.fixtures'
import '~/components/chat/providers'

const KINDS: ToolVocabularyCheck = {
  names: [...Object.keys(KIRO_TOOL_KINDS), ...Object.values(KIRO_IDENTIFIED_TOOLS)],
  kindOf: () => 'other',
  generic: {},
  fallback: 'other',
}

// Each name comes from the fixture table's own keys, so the lookup misses for the type alone.
function callOf(name: string) {
  const fixture = KIRO_TOOL_RESULTS.fixtures[name]
  return fixture === undefined ? null : providerToolCall(AgentProvider.KIRO, fixture.payload, fixture.options)
}
const failureCallOf = (fixture: ToolFailureFixture) => providerToolCall(AgentProvider.KIRO, fixture.payload, fixture.options)

/** The `tool_call` request that each fixture pairs with, read alone as a call still in flight. */
function requestCallOf(name: string) {
  const fixture = KIRO_TOOL_RESULTS.fixtures[name]
  const frame = fixture === undefined ? null : openingFrameOf(fixture)
  return frame === null ? null : providerToolCall(AgentProvider.KIRO, frame)
}

describe('kiro tool results', () => {
  it('keeps every fixture off the uncategorized kind', () => {
    expect(fixturesOnTheUncategorizedKind(KIRO_TOOL_RESULTS, callOf), 'A fixture reached the uncategorized kind.').toStrictEqual([])
  })

  it('answers each fixture with the kind its name states', () => {
    const expected: Record<string, string> = {
      ...KIRO_TOOL_KINDS,
      [KIRO_IDENTIFIED_TOOLS.Shell]: 'execute',
      [KIRO_IDENTIFIED_TOOLS.Subagent]: 'agent',
      [KIRO_IDENTIFIED_TOOLS.Question]: 'question',
      [KIRO_IDENTIFIED_TOOLS.Mcp]: 'mcp',
    }
    for (const [name, kind] of Object.entries(expected)) {
      const call = callOf(name)
      expect(call, name).not.toBeNull()
      expect(call!.kind, name).toBe(kind)
    }
  })

  describeToolResultCorpus(KINDS, KIRO_TOOL_RESULTS, callOf)
  describeToolFailureLadder(KIRO_TOOL_RESULTS, { callOf, failureCallOf, requestCallOf })
})
