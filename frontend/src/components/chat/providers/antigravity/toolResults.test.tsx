import type { ToolFailureFixture, ToolVocabularyCheck } from '~/test-support/toolVocabulary'
import { render } from '@solidjs/testing-library'
import { describe, expect, it } from 'vitest'
import { ToolMessage } from '~/components/chat/results/ToolMessage'
import { ANTIGRAVITY_TOOL } from '~/generated/contracts/antigravity-protocol'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { providerToolCall, toolRow } from '~/test-support/toolCallFixture'
import { describeToolFailureLadder, describeToolResultCorpus } from '~/test-support/toolResultCases'
import { fixturesOnTheUncategorizedKind, openingFrameOf } from '~/test-support/toolVocabulary'
import { antigravityToolKind } from './toolKinds'
import { ANTIGRAVITY_GENERIC_TOOLS, ANTIGRAVITY_TOOL_RESULTS } from './toolResults.fixtures'
import '~/components/chat/providers'

const KINDS: ToolVocabularyCheck = {
  names: Object.values(ANTIGRAVITY_TOOL),
  kindOf: antigravityToolKind,
  generic: ANTIGRAVITY_GENERIC_TOOLS,
  fallback: 'other',
}

function callOf(name: string) {
  const fixture = ANTIGRAVITY_TOOL_RESULTS.fixtures[name]
  return fixture === undefined
    ? null
    : providerToolCall(AgentProvider.ANTIGRAVITY_CLI, fixture.payload, fixture.options)
}
const failureCallOf = (fixture: ToolFailureFixture) => providerToolCall(AgentProvider.ANTIGRAVITY_CLI, fixture.payload, fixture.options)

function requestCallOf(name: string) {
  const fixture = ANTIGRAVITY_TOOL_RESULTS.fixtures[name]
  const frame = fixture === undefined
    ? null
    : openingFrameOf(fixture)
  return frame === null
    ? null
    : providerToolCall(AgentProvider.ANTIGRAVITY_CLI, frame, { role: 'request' })
}

describe('antigravity tool results', () => {
  it('keeps every fixture off the uncategorized kind but the documented one', () => {
    expect(fixturesOnTheUncategorizedKind(ANTIGRAVITY_TOOL_RESULTS, callOf).filter(name => !(name in ANTIGRAVITY_GENERIC_TOOLS))).toStrictEqual([])
  })

  describeToolResultCorpus(KINDS, ANTIGRAVITY_TOOL_RESULTS, callOf)
  describeToolFailureLadder(ANTIGRAVITY_TOOL_RESULTS, { callOf, failureCallOf, requestCallOf })

  it('renders a native refusal as declined rather than error', () => {
    const fixture = ANTIGRAVITY_TOOL_RESULTS.failures.find(candidate => candidate.status === 'declined')
    if (!fixture)
      throw new Error('The Antigravity refusal fixture is missing.')
    const call = failureCallOf(fixture)
    if (!call)
      throw new Error('The Antigravity refusal did not produce a tool call.')
    const { container } = render(() => <ToolMessage row={toolRow(call, 'result', { request: true })} />)
    expect(container.textContent).toContain('Declined')
    expect(container.textContent).not.toContain('Error')
  })
})
