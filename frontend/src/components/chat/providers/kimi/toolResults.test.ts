import type { ToolFailureFixture, ToolVocabularyCheck } from '~/test-support/toolVocabulary'
import { describe, expect, it } from 'vitest'
import { KIMI_TOOL } from '~/generated/contracts/kimi-protocol'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { providerToolCall } from '~/test-support/toolCallFixture'
import { describeToolFailureLadder, describeToolResultCorpus } from '~/test-support/toolResultCases'
import { fixturesOnTheUncategorizedKind, openingFrameOf } from '~/test-support/toolVocabulary'
import { kimiToolKind } from './toolKinds'
import { KIMI_TOOL_RESULTS } from './toolResults.fixtures'
import '~/components/chat/providers'

const KINDS: ToolVocabularyCheck = {
  names: [...Object.values(KIMI_TOOL)],
  kindOf: kimiToolKind,
  generic: {},
  fallback: 'other',
}

function callOf(name: string) {
  const fixture = KIMI_TOOL_RESULTS.fixtures[name]
  if (fixture === undefined)
    throw new Error(`No kimi result fixture for ${name}`)
  return providerToolCall(AgentProvider.KIMI_CODE, fixture.payload, fixture.options)
}
const failureCallOf = (fixture: ToolFailureFixture) => providerToolCall(AgentProvider.KIMI_CODE, fixture.payload, fixture.options)

/** The `tool.call.started` each fixture pairs with, read alone as a call still in flight. */
function requestCallOf(name: string) {
  const fixture = KIMI_TOOL_RESULTS.fixtures[name]
  if (fixture === undefined)
    throw new Error(`No kimi result fixture for ${name}`)
  const frame = openingFrameOf(fixture)
  return frame === null ? null : providerToolCall(AgentProvider.KIMI_CODE, frame, { spanType: name })
}

describe('kimi tool results', () => {
  it('keeps every fixture off the uncategorized kind', () => {
    expect(fixturesOnTheUncategorizedKind(KIMI_TOOL_RESULTS, callOf)).toStrictEqual([])
  })

  it('extracts the kind the table states for every fixture', () => {
    for (const name of Object.keys(KIMI_TOOL_RESULTS.fixtures))
      expect(callOf(name)?.kind, name).toBe(kimiToolKind(name))
  })

  describeToolResultCorpus(KINDS, KIMI_TOOL_RESULTS, callOf)
  describeToolFailureLadder(KIMI_TOOL_RESULTS, { callOf, failureCallOf, requestCallOf })
})
