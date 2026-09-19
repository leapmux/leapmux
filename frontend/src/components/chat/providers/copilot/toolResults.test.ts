import type { ToolFailureFixture, ToolVocabularyCheck } from '~/test-support/toolVocabulary'
import { describe, expect, it } from 'vitest'
import { COPILOT_TOOL } from '~/generated/contracts/copilot-protocol'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { providerToolCall } from '~/test-support/toolCallIr'
import { describeToolFailureLadder, describeToolResultCorpus } from '~/test-support/toolResultCases'
import { fixturesOnTheUncategorizedKind, openingFrameOf } from '~/test-support/toolVocabulary'
import { copilotToolKind } from './toolKinds'
import { COPILOT_TOOL_RESULTS } from './toolResults.fixtures'
import '~/components/chat/providers'

const KINDS: ToolVocabularyCheck = {
  names: [...Object.values(COPILOT_TOOL)],
  kindOf: copilotToolKind,
  generic: {},
  fallback: 'mcp',
}

/** The call one fixture's completion reads, or thrown when the name holds no fixture. */
function callOf(name: string) {
  const fixture = COPILOT_TOOL_RESULTS.fixtures[name]
  if (fixture === undefined)
    throw new Error(`No copilot result fixture for ${name}`)
  return providerToolCall(AgentProvider.GITHUB_COPILOT, fixture.payload, fixture.options)
}
const failureCallOf = (fixture: ToolFailureFixture) => providerToolCall(AgentProvider.GITHUB_COPILOT, fixture.payload, fixture.options)

/** The `tool.execution_start` each fixture pairs with, read alone as a call still in flight. */
function requestCallOf(name: string) {
  const fixture = COPILOT_TOOL_RESULTS.fixtures[name]
  if (fixture === undefined)
    throw new Error(`No copilot result fixture for ${name}`)
  const frame = openingFrameOf(fixture)
  return frame === null ? null : providerToolCall(AgentProvider.GITHUB_COPILOT, frame, { spanType: name })
}

describe('copilot tool results', () => {
  it('keeps every fixture off the uncategorized kind', () => {
    // The narrow form of the kind question. The phrasing rule may turn a grep into the
    // ACP search kind, so the table's own word is not the answer; what may NOT happen
    // is a name the table holds taking the uncategorized fallback.
    expect(
      fixturesOnTheUncategorizedKind(COPILOT_TOOL_RESULTS, callOf),
      'A fixture the table lists reached the uncategorized kind.',
    ).toStrictEqual([])
  })

  describeToolResultCorpus(KINDS, COPILOT_TOOL_RESULTS, callOf)
  describeToolFailureLadder(COPILOT_TOOL_RESULTS, { callOf, failureCallOf, requestCallOf })
})
