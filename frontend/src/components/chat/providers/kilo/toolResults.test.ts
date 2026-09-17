import type { ToolFailureFixture, ToolVocabularyCheck } from '~/test-support/toolVocabulary'
import { describe, expect, it } from 'vitest'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { providerToolCall } from '~/test-support/toolCallIr'
import { describeToolFailureLadder, describeToolResultCorpus } from '~/test-support/toolResultCases'
import { fixturesOnTheUncategorizedKind, openingFrameOf } from '~/test-support/toolVocabulary'
import { KILO_TOOL_KINDS } from './toolKinds'
import { KILO_TOOL_RESULTS } from './toolResults.fixtures'
import '~/components/chat/providers'

const KINDS: ToolVocabularyCheck = {
  names: Object.keys(KILO_TOOL_KINDS),
  kindOf: name => KILO_TOOL_KINDS[name] ?? 'other',
  generic: {},
  fallback: 'other',
}

// Each name comes from the fixture table's own keys, so the lookup misses for the type alone.
function callOf(name: string) {
  const fixture = KILO_TOOL_RESULTS.fixtures[name]
  return fixture === undefined ? null : providerToolCall(AgentProvider.KILO, fixture.payload, fixture.options)
}
const failureCallOf = (fixture: ToolFailureFixture) => providerToolCall(AgentProvider.KILO, fixture.payload, fixture.options)

/** The `tool_call` opener each fixture pairs with, read alone as a call still in flight. */
function openerCallOf(name: string) {
  const fixture = KILO_TOOL_RESULTS.fixtures[name]
  const frame = fixture === undefined ? null : openingFrameOf(fixture)
  return frame === null ? null : providerToolCall(AgentProvider.KILO, frame)
}

describe('kilo tool results', () => {
  it('keeps every fixture off the uncategorized kind', () => {
    // The narrow form of the kind question. The ACP word Kilo sends can outrank the
    // table's own, so the table's word is not the answer; what may NOT happen is the
    // uncategorized card, which a fixture reaches when it loses its title -- the only
    // place the registry id appears on the wire.
    expect(
      fixturesOnTheUncategorizedKind(KILO_TOOL_RESULTS, callOf),
      'A fixture reached the uncategorized kind.',
    ).toStrictEqual([])
  })

  describeToolResultCorpus(KINDS, KILO_TOOL_RESULTS, callOf)
  describeToolFailureLadder(KILO_TOOL_RESULTS, { callOf, failureCallOf, openerCallOf })
})
