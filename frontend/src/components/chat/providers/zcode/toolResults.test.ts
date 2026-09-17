import type { ToolFailureFixture, ToolVocabularyCheck } from '~/test-support/toolVocabulary'
import { describe, expect, it } from 'vitest'
import { ZCODE_TOOL } from '~/generated/contracts/zcode-protocol'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { providerToolCall } from '~/test-support/toolCallIr'
import { describeToolFailureLadder, describeToolResultCorpus } from '~/test-support/toolResultCases'
import { fixturesOnTheUncategorizedKind, openingFrameOf } from '~/test-support/toolVocabulary'
import { zcodeToolKind } from './toolKinds'
import { ZCODE_TOOL_RESULTS } from './toolResults.fixtures'
import '~/components/chat/providers'

const KINDS: ToolVocabularyCheck = {
  names: Object.values(ZCODE_TOOL),
  kindOf: zcodeToolKind,
  generic: {},
  fallback: 'other',
}

// Each name comes from the fixture table's own keys, so the lookup misses for the type alone.
function callOf(name: string) {
  const fixture = ZCODE_TOOL_RESULTS.fixtures[name]
  return fixture === undefined ? null : providerToolCall(AgentProvider.ZCODE, fixture.payload, fixture.options)
}
const failureCallOf = (fixture: ToolFailureFixture) => providerToolCall(AgentProvider.ZCODE, fixture.payload, fixture.options)

/** The `scheduled` frame each fixture pairs with, read alone as a call still in flight. */
function openerCallOf(name: string) {
  const fixture = ZCODE_TOOL_RESULTS.fixtures[name]
  const frame = fixture === undefined ? null : openingFrameOf(fixture)
  return frame === null ? null : providerToolCall(AgentProvider.ZCODE, frame)
}

describe('zcode tool results', () => {
  it('keeps every fixture off the uncategorized kind', () => {
    // The narrow form of the kind question. A ZCode row can draw a kind the table does
    // not state, so the table's word is not the answer; what may NOT happen is a name
    // the table lists reaching the uncategorized card.
    expect(
      fixturesOnTheUncategorizedKind(ZCODE_TOOL_RESULTS, callOf),
      'A fixture the table lists reached the uncategorized kind.',
    ).toStrictEqual([])
  })

  describeToolResultCorpus(KINDS, ZCODE_TOOL_RESULTS, callOf)
  describeToolFailureLadder(ZCODE_TOOL_RESULTS, { callOf, failureCallOf, openerCallOf })
})
