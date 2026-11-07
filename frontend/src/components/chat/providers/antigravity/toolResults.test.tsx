import type { ToolFailureFixture, ToolVocabularyCheck } from '~/test-support/toolVocabulary'
import { render } from '@solidjs/testing-library'
import { describe, expect, it } from 'vitest'
import { ToolMessage } from '~/components/chat/results/ToolMessage'
import { ANTIGRAVITY_TOOL } from '~/generated/contracts/antigravity-protocol'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { providerRow, providerToolCall, toolRow } from '~/test-support/toolCallFixture'
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
  it.each([
    '{broken',
    'null',
    '[]',
    '0',
    '"scalar"',
  ])('retains malformed arguments through unsupported extraction: %s', (argumentsJson) => {
    expect(providerRow(AgentProvider.ANTIGRAVITY_CLI, {
      type: 'CORTEX_STEP_TYPE_GENERIC',
      status: 'CORTEX_STEP_STATUS_DONE',
      metadata: {
        toolCall: { id: 'bad', name: 'future', argumentsJson },
      },
    })).toBeNull()
  })

  it('retains unknown native payloads through unsupported extraction', () => {
    expect(providerRow(AgentProvider.ANTIGRAVITY_CLI, {
      type: 'CORTEX_STEP_TYPE_FUTURE',
      status: 'CORTEX_STEP_STATUS_DONE',
      futurePayload: { text: 'retain me' },
    })).toBeNull()
  })

  it.each([
    [
      'read_file',
      'CORTEX_STEP_TYPE_VIEW_FILE',
      { Path: '/p/a.ts' },
    ],
    [
      'run_command',
      'CORTEX_STEP_TYPE_RUN_COMMAND',
      { CommandLine: 'echo x' },
    ],
    [
      'edit_file',
      'CORTEX_STEP_TYPE_CODE_ACTION',
      { Path: '/p/a.ts', Target: 'x', Replacement: 'y' },
    ],
    [
      'write_file',
      'CORTEX_STEP_TYPE_CODE_ACTION',
      { Path: '/p/a.ts', Content: 'y' },
    ],
    [
      'invoke_subagent',
      'CORTEX_STEP_TYPE_INVOKE_SUBAGENT',
      { Prompt: 'task' },
    ],
    ['call_mcp_tool', 'CORTEX_STEP_TYPE_MCP_TOOL', {}],
  ])('retains generic output when %s has no structured result', (name, type, args) => {
    const call = providerToolCall(AgentProvider.ANTIGRAVITY_CLI, {
      type,
      status: 'CORTEX_STEP_STATUS_DONE',
      metadata: {
        toolCall: {
          id: 'fallback',
          name,
          argumentsJson: JSON.stringify(args),
        },
      },
      generic: {
        result: { result: 'NATIVE_GENERIC_OUTPUT' },
      },
    })
    expect(JSON.stringify(call?.result)).toContain('NATIVE_GENERIC_OUTPUT')
  })
  it('reads the native recipient and message arguments', () => {
    const call = providerToolCall(AgentProvider.ANTIGRAVITY_CLI, {
      type: 'CORTEX_STEP_TYPE_GENERIC',
      status: 'CORTEX_STEP_STATUS_DONE',
      metadata: {
        toolCall: { id: 'native-message', name: 'send_message', argumentsJson: '{"Recipient":"child-cascade","Message":"Continue the native task"}' },
      },
      generic: {
        result: { result: 'Message sent' },
      },
    })
    expect(call?.request).toEqual({ to: 'child-cascade', text: 'Continue the native task' })
    if (!call)
      throw new Error('The native message did not produce a tool call.')
    const { container } = render(() => <ToolMessage row={toolRow(call, 'request')} />)
    expect(container.textContent).toContain('child-cascade')
    expect(container.textContent).toContain('Continue the native task')
  })
})
