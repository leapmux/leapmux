import type { ToolFailureFixture, ToolVocabularyCheck } from '~/test-support/toolVocabulary'
import { describe, expect, it } from 'vitest'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { providerToolCall } from '~/test-support/toolCallIr'
import { describeToolFailureLadder, describeToolResultCorpus } from '~/test-support/toolResultCases'
import { fixturesOnTheUncategorizedKind, openingFrameOf } from '~/test-support/toolVocabulary'
import { OPENCODE_TOOL_NAMES } from './toolNames'
import { OPENCODE_TOOL_RESULTS } from './toolResults.fixtures'
import '~/components/chat/providers'

const KINDS: ToolVocabularyCheck = {
  names: Object.keys(OPENCODE_TOOL_RESULTS.fixtures),
  kindOf: () => 'other',
  generic: {},
  fallback: 'other',
}
void OPENCODE_TOOL_NAMES

// Each name comes from the fixture table's own keys, so the lookup misses for the type alone.
function callOf(name: string) {
  const fixture = OPENCODE_TOOL_RESULTS.fixtures[name]
  return fixture === undefined ? null : providerToolCall(AgentProvider.OPENCODE, fixture.payload, fixture.options)
}
const failureCallOf = (fixture: ToolFailureFixture) => providerToolCall(AgentProvider.OPENCODE, fixture.payload, fixture.options)

/** The `tool_call` request that each fixture pairs with, read alone as a call still in flight. */
function requestCallOf(name: string) {
  const fixture = OPENCODE_TOOL_RESULTS.fixtures[name]
  const frame = fixture === undefined ? null : openingFrameOf(fixture)
  return frame === null ? null : providerToolCall(AgentProvider.OPENCODE, frame)
}

describe('opencode tool results', () => {
  it('keeps every fixture off the uncategorized kind', () => {
    // The narrow form of the kind question, and the only form this table can hold: it
    // answers `other` for every name. The adapter takes the kind from the title; what
    // may NOT happen is the uncategorized fallback, whose wrench identifies nothing
    // the daemon ran.
    expect(
      fixturesOnTheUncategorizedKind(OPENCODE_TOOL_RESULTS, callOf),
      'A fixture reached the uncategorized kind.',
    ).toStrictEqual([])
  })

  describeToolResultCorpus(KINDS, OPENCODE_TOOL_RESULTS, callOf)
  describeToolFailureLadder(OPENCODE_TOOL_RESULTS, { callOf, failureCallOf, requestCallOf })
})
