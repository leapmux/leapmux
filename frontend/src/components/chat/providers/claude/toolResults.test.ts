import type { ToolFailureFixture, ToolResultFixture, ToolVocabularyCheck } from '~/test-support/toolVocabulary'
import { describe, expect, it } from 'vitest'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { providerToolCall } from '~/test-support/toolCallIr'
import { describeToolFailureLadder, describeToolResultCorpus } from '~/test-support/toolResultCases'
import { fixturesThatChangeKind, openingFrameOf } from '~/test-support/toolVocabulary'
import { claudeToolKind } from './toolKinds'
import { CLAUDE_TOOL_NAMES } from './toolNames'
import { CLAUDE_TOOL_RESULTS } from './toolResults.fixtures'
import '~/components/chat/providers'

const KINDS: ToolVocabularyCheck = {
  names: Object.values(CLAUDE_TOOL_NAMES),
  kindOf: claudeToolKind,
  generic: {},
  fallback: '',
}

/** One fixture's own frame, read through the span column a result row sits in. */
function frameCall(fixture: ToolResultFixture, name: string) {
  return providerToolCall(AgentProvider.CLAUDE_CODE, fixture.payload, { ...fixture.options, spanType: name })
}

/** The call one fixture extracts, named by its span column the way a result row is. */
function callOf(name: string) {
  const fixture = CLAUDE_TOOL_RESULTS.fixtures[name]
  // Every name the corpus walks comes from the table beside it; one that does not
  // states a fixture the corpus forgot.
  if (!fixture)
    throw new Error(`No fixture states ${name}`)
  return frameCall(fixture, name)
}

/** The call one FAILED frame extracts, through the same extraction. */
function failureCallOf(fixture: ToolFailureFixture) {
  return frameCall(fixture, fixture.name)
}

/** The `tool_use` block each fixture pairs with, read alone as a call still in flight. */
function requestCallOf(name: string) {
  const fixture = CLAUDE_TOOL_RESULTS.fixtures[name]
  if (!fixture)
    throw new Error(`No fixture states ${name}`)
  const frame = openingFrameOf(fixture)
  return frame === null ? null : providerToolCall(AgentProvider.CLAUDE_CODE, frame, { spanType: name })
}

describe('claude tool results', () => {
  // The strict form of the kind question, which Claude can hold: `claudeToolKind`
  // states the kind of every tool the CLI sends, so an extraction that lands elsewhere
  // is a defect in one of the two. A provider whose WIRE word outranks its table asks
  // the narrow form instead -- see `describeToolResultCorpus`.
  it('keeps every fixture on the kind its table states', () => {
    expect(
      fixturesThatChangeKind(KINDS, CLAUDE_TOOL_RESULTS, callOf),
      'The frame extracts as another kind than the table states; one of the two is wrong.',
    ).toStrictEqual([])
  })

  describeToolResultCorpus(KINDS, CLAUDE_TOOL_RESULTS, callOf)
  describeToolFailureLadder(CLAUDE_TOOL_RESULTS, { callOf, failureCallOf, requestCallOf })
})
