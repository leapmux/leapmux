import type { ToolFailureFixture, ToolVocabularyCheck } from '~/test-support/toolVocabulary'
import { describe, expect, it } from 'vitest'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { providerToolCall } from '~/test-support/toolCallFixture'
import { describeToolFailureLadder, describeToolResultCorpus } from '~/test-support/toolResultCases'
import { fixturesThatChangeKind, openingFrameOf } from '~/test-support/toolVocabulary'
import { piToolKind } from './toolKinds'
import { PI_TOOL_RESULTS } from './toolResults.fixtures'
import '~/components/chat/providers'

const KINDS: ToolVocabularyCheck = {
  names: Object.keys(PI_TOOL_RESULTS.fixtures).concat(Object.keys(PI_TOOL_RESULTS.noResult)),
  kindOf: piToolKind,
  generic: {},
  fallback: 'unspecified',
}

// Each name comes from the fixture table's own keys, so the lookup misses for the type alone.
function callOf(name: string) {
  const fixture = PI_TOOL_RESULTS.fixtures[name]
  return fixture === undefined ? null : providerToolCall(AgentProvider.PI, fixture.payload, fixture.options)
}
const failureCallOf = (fixture: ToolFailureFixture) => providerToolCall(AgentProvider.PI, fixture.payload, fixture.options)

/** The `tool_execution_start` each fixture pairs with, read alone as a call still in flight. */
function requestCallOf(name: string) {
  const fixture = PI_TOOL_RESULTS.fixtures[name]
  const frame = fixture === undefined ? null : openingFrameOf(fixture)
  return frame === null ? null : providerToolCall(AgentProvider.PI, frame)
}

describe('pi tool results', () => {
  // The strict form of the kind question, which Pi can hold: `piToolKind` states the
  // kind of every tool Pi sends, so an extraction that lands elsewhere is a defect in
  // one of the two. A provider whose WIRE word outranks its table asks the narrow form
  // instead -- see `describeToolResultCorpus`.
  it('keeps every fixture on the kind its table states', () => {
    expect(
      fixturesThatChangeKind(KINDS, PI_TOOL_RESULTS, callOf),
      'The frame extracts as another kind than the table states; one of the two is wrong.',
    ).toStrictEqual([])
  })

  describeToolResultCorpus(KINDS, PI_TOOL_RESULTS, callOf)
  describeToolFailureLadder(PI_TOOL_RESULTS, { callOf, failureCallOf, requestCallOf })
})
