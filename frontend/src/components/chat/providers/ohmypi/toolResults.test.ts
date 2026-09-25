import type { ToolFailureFixture, ToolVocabularyCheck } from '~/test-support/toolVocabulary'
import { describe, expect, it } from 'vitest'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { providerToolCall } from '~/test-support/toolCallFixture'
import { describeToolFailureLadder, describeToolResultCorpus } from '~/test-support/toolResultCases'
import { fixturesThatChangeKind, openingFrameOf } from '~/test-support/toolVocabulary'
import { ohMyPiToolKind } from './toolKinds'
import { OH_MY_PI_TOOL_RESULTS } from './toolResults.fixtures'
import '~/components/chat/providers'

const KINDS: ToolVocabularyCheck = {
  names: Object.keys(OH_MY_PI_TOOL_RESULTS.fixtures).concat(Object.keys(OH_MY_PI_TOOL_RESULTS.noResult)),
  kindOf: ohMyPiToolKind,
  generic: {},
  fallback: 'unspecified',
}

// Each name comes from the fixture table's own keys, so the lookup misses for the type alone.
function callOf(name: string) {
  const fixture = OH_MY_PI_TOOL_RESULTS.fixtures[name]
  return fixture === undefined ? null : providerToolCall(AgentProvider.OH_MY_PI, fixture.payload, fixture.options)
}
const failureCallOf = (fixture: ToolFailureFixture) => providerToolCall(AgentProvider.OH_MY_PI, fixture.payload, fixture.options)

/** The `tool_execution_start` each fixture pairs with, read alone as a call still running. */
function requestCallOf(name: string) {
  const fixture = OH_MY_PI_TOOL_RESULTS.fixtures[name]
  const frame = fixture === undefined ? null : openingFrameOf(fixture)
  return frame === null ? null : providerToolCall(AgentProvider.OH_MY_PI, frame)
}

describe('ohmypi tool results', () => {
  it('keeps every fixture on the kind its table states', () => {
    expect(
      fixturesThatChangeKind(KINDS, OH_MY_PI_TOOL_RESULTS, callOf),
      'The frame extracts as another kind than the table states; one of the two is wrong.',
    ).toStrictEqual([])
  })

  describeToolResultCorpus(KINDS, OH_MY_PI_TOOL_RESULTS, callOf)
  describeToolFailureLadder(OH_MY_PI_TOOL_RESULTS, { callOf, failureCallOf, requestCallOf })
})
