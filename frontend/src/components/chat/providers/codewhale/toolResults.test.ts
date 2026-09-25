import type { ToolFailureFixture, ToolVocabularyCheck } from '~/test-support/toolVocabulary'
import { describe, expect, it } from 'vitest'
import { CODEWHALE_TOOL } from '~/generated/contracts/codewhale-protocol'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { providerToolCall } from '~/test-support/toolCallFixture'
import { describeToolFailureLadder, describeToolResultCorpus } from '~/test-support/toolResultCases'
import { fixturesThatChangeKind, openingFrameOf } from '~/test-support/toolVocabulary'
import { codewhaleToolKind } from './toolKinds'
import { CODEWHALE_TOOL_RESULTS } from './toolResults.fixtures'
import '~/components/chat/providers'

const KINDS: ToolVocabularyCheck = {
  names: Object.values(CODEWHALE_TOOL),
  kindOf: codewhaleToolKind,
  generic: {},
  fallback: 'other',
}

// Each name comes from the fixture table's own keys, so the lookup misses for the type alone.
function callOf(name: string) {
  const fixture = CODEWHALE_TOOL_RESULTS.fixtures[name]
  return fixture === undefined ? null : providerToolCall(AgentProvider.CODEWHALE, fixture.payload, fixture.options)
}
const failureCallOf = (fixture: ToolFailureFixture) => providerToolCall(AgentProvider.CODEWHALE, fixture.payload, fixture.options)

/** The opening frame each fixture pairs with, read alone as a call still in flight. */
function requestCallOf(name: string) {
  const fixture = CODEWHALE_TOOL_RESULTS.fixtures[name]
  const frame = fixture === undefined ? null : openingFrameOf(fixture)
  return frame === null ? null : providerToolCall(AgentProvider.CODEWHALE, frame, { role: 'request' })
}

describe('codewhale tool results', () => {
  // The STRICT form of the kind question: every fixture draws the kind the table states.
  // Codewhale reports a tool by name alone, so no wire kind outranks the table. The two
  // facades refine their kind from an `action` argument, and their fixtures state the
  // action that keeps the table's own word; `extractors/toolCall.test.ts` covers the rest.
  it('draws the kind the table states for every fixture', () => {
    expect(
      fixturesThatChangeKind(KINDS, CODEWHALE_TOOL_RESULTS, callOf),
      'A fixture drew a kind the table does not state.',
    ).toStrictEqual([])
  })

  describeToolResultCorpus(KINDS, CODEWHALE_TOOL_RESULTS, callOf)
  describeToolFailureLadder(CODEWHALE_TOOL_RESULTS, { callOf, failureCallOf, requestCallOf })
})
