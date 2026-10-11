import type { ToolFailureFixture, ToolVocabularyCheck } from '~/test-support/toolVocabulary'
import { describe, expect, it } from 'vitest'
import { MIMO_TOOL } from '~/generated/contracts/mimo-protocol'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { providerToolCall } from '~/test-support/toolCallFixture'
import { describeToolFailureLadder, describeToolResultCorpus } from '~/test-support/toolResultCases'
import { fixturesOnTheUncategorizedKind, openingFrameOf } from '~/test-support/toolVocabulary'
import { mimoToolKind } from './toolKinds'
import { MIMO_GENERIC_TOOLS, MIMO_TOOL_RESULTS } from './toolResults.fixtures'
import '~/components/chat/providers'

const KINDS: ToolVocabularyCheck = {
  names: Object.values(MIMO_TOOL),
  kindOf: mimoToolKind,
  generic: MIMO_GENERIC_TOOLS,
  fallback: 'other',
}

function callOf(name: string) {
  const fixture = MIMO_TOOL_RESULTS.fixtures[name]
  return fixture === undefined ? null : providerToolCall(AgentProvider.MIMO_CODE, fixture.payload, fixture.options)
}
const failureCallOf = (fixture: ToolFailureFixture) => providerToolCall(AgentProvider.MIMO_CODE, fixture.payload, fixture.options)

/** The opening frame each fixture pairs with, read alone as a call still in flight. */
function requestCallOf(name: string) {
  const fixture = MIMO_TOOL_RESULTS.fixtures[name]
  const frame = fixture === undefined ? null : openingFrameOf(fixture)
  return frame === null ? null : providerToolCall(AgentProvider.MIMO_CODE, frame, { role: 'request' })
}

describe('mimo tool results', () => {
  it('keeps every fixture off the uncategorized kind but the documented one', () => {
    // `invalid` takes the generic card on purpose: it names no real tool.
    expect(
      fixturesOnTheUncategorizedKind(MIMO_TOOL_RESULTS, callOf).filter(name => !(name in MIMO_GENERIC_TOOLS)),
      'A fixture the table lists reached the uncategorized kind.',
    ).toStrictEqual([])
  })

  describeToolResultCorpus(KINDS, MIMO_TOOL_RESULTS, callOf)
  describeToolFailureLadder(MIMO_TOOL_RESULTS, { callOf, failureCallOf, requestCallOf })
})
