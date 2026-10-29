import { describe, expect, it } from 'vitest'
import { AgentProvider, MessageCompletion } from '~/generated/proto/leapmux/v1/agent_pb'
import { providerToolCall } from '~/test-support/toolCallFixture'
import { typedResult } from '../../../model/toolCall'
import { input } from '../../testUtils'
import { MUSE_NATIVE_MCP_ITEM_FRAME, museToolResultSources } from '../toolResults.fixtures'
import '~/components/chat/providers'

function shellSources(output: unknown, code = 0, overrides: Record<string, unknown> = {}) {
  const text = JSON.stringify({
    chunk_id: 'exec-1-1',
    command: 'printf native',
    exit_code: code,
    terminal_status: code === 0
      ? 'completed'
      : 'failed',
    output,
    ...overrides,
  })
  const source = museToolResultSources(text, { command: 'printf native' })
  source.payload.params.item.status = code === 0
    ? 'completed'
    : 'failed'
  return { ...source, text }
}

describe('museToolCall', () => {
  it.each([
    { label: 'stdout', output: 'Native output\n', code: 0 },
    { label: 'empty output', output: '', code: 0 },
    { label: 'failed stderr', output: 'Native error\n', code: 7 },
    {
      label: 'long output',
      output: 'Native output\n'.repeat(1000),
      code: 0,
    },
  ])('reads exact $label from a verified native execution envelope', ({ output, code }) => {
    const source = shellSources(output, code)
    const original = structuredClone(source)
    const call = providerToolCall(AgentProvider.MUSE_CODE, source.payload, { supplementalContent: source.supplement })
    if (call?.kind !== 'execute')
      throw new Error('The native shell requires an execute call.')
    expect(typedResult(call)?.commands).toEqual([
      { output, exitCode: code },
    ])
    expect(source).toEqual(original)
  })

  it.each([null, 0, false, {}].map(output => ({ output })))('keeps the native preview for malformed output: $output', ({ output }) => {
    const source = shellSources(output)
    const call = providerToolCall(AgentProvider.MUSE_CODE, source.payload, { supplementalContent: source.supplement })
    if (call?.kind !== 'execute')
      throw new Error('The native shell requires an execute call.')
    expect(typedResult(call)?.commands[0]?.output).toBe(source.text)
  })

  it.each([
    { chunk_id: '' },
    { command: 'another command' },
  ])('keeps the preview for a mismatched execution envelope: %j', (overrides) => {
    const source = shellSources('Do not select this output.', 0, overrides)
    const call = providerToolCall(AgentProvider.MUSE_CODE, source.payload, { supplementalContent: source.supplement })
    if (call?.kind !== 'execute')
      throw new Error('The native shell requires an execute call.')
    expect(typedResult(call)?.commands[0]?.output).toBe(source.text)
  })

  it('does not unwrap ordinary JSON without the native execution envelope', () => {
    const text = '{"output":"ordinary JSON","exit_code":0}'
    const source = museToolResultSources(text, { command: 'printf native' })
    const call = providerToolCall(AgentProvider.MUSE_CODE, source.payload, { supplementalContent: source.supplement })
    if (call?.kind !== 'execute')
      throw new Error('The native shell requires an execute call.')
    expect(typedResult(call)?.commands[0]?.output).toBe(text)
  })

  it.each(['absent', 'foreign'])('keeps the preview when exact native source data is %s', (kind) => {
    const source = shellSources('Unverified output.')
    if (kind === 'foreign')
      source.result.stream.id = 'foreign'
    const call = providerToolCall(AgentProvider.MUSE_CODE, source.payload, {
      supplementalContent: kind === 'foreign'
        ? source.supplement
        : undefined,
    })
    if (call?.kind !== 'execute')
      throw new Error('The native shell requires an execute call.')
    expect(typedResult(call)?.commands[0]).toEqual({ output: source.text, outputUnavailable: true })
  })
  it('dispatches the recorded native MCP item through the registered plugin', () => {
    const frame = JSON.parse(MUSE_NATIVE_MCP_ITEM_FRAME)
    const original = structuredClone(frame)
    const call = providerToolCall(AgentProvider.MUSE_CODE, frame)
    expect(call?.kind).toBe('mcp')
    expect(call?.name).toBe('mcp__echo_probe__echo')
    expect(call?.request).toEqual({
      server: 'echo_probe',
      tool: 'echo',
      args: { value: 'MUSE_MCP_EXECUTION_PROOF' },
    })
    expect(call?.result).toEqual({ unparsed: true, text: 'MCP_ECHO:MUSE_MCP_EXECUTION_PROOF' })
    expect(frame).toEqual(original)
  })

  it('rejects a sibling result from another native session', () => {
    const request = {
      method: 'item/started',
      params: {
        sessionId: 'session',
        item: {
          itemId: 'tool',
          kind: 'toolCall',
          turnId: 'turn',
          callId: 'call',
          tool: 'bash',
          args: '{"command":"printf native"}',
          status: 'inProgress',
        },
      },
    }
    const result = {
      method: 'item/completed',
      params: {
        sessionId: 'foreign-session',
        item: { ...request.params.item, status: 'completed', visibleOutput: 'Foreign output' },
      },
    }
    const call = providerToolCall(AgentProvider.MUSE_CODE, request, {
      result: input(result, undefined, AgentProvider.MUSE_CODE),
    })
    expect(call?.status).toBe('in_progress')
    expect(call?.result).toBeUndefined()
  })

  it('keeps a retained unfinished tool final without an invented outcome', () => {
    const call = providerToolCall(AgentProvider.MUSE_CODE, {
      method: 'item/started',
      params: {
        sessionId: 'session',
        item: {
          itemId: 'tool',
          kind: 'toolCall',
          turnId: 'turn',
          tool: 'bash',
          args: '{"command":"printf native"}',
          status: 'inProgress',
        },
      },
    }, { completion: MessageCompletion.FINISHED })
    expect(call?.status).toBe('incomplete')
    expect(call?.result).toBeUndefined()
  })

  it('reads the actual native to-do text and statuses from the request', () => {
    const call = providerToolCall(AgentProvider.MUSE_CODE, {
      method: 'item/started',
      params: {
        sessionId: 'session',
        item: {
          itemId: 'todos',
          kind: 'toolCall',
          turnId: 'turn',
          tool: 'write_todos',
          status: 'inProgress',
          args: JSON.stringify({
            todos: [
              { text: 'Read the native source', status: 'completed' },
              { text: 'Run the native checks', status: 'in_progress' },
              { text: 'Keep the next item', status: 'pending' },
            ],
          }),
        },
      },
    })
    if (call?.kind !== 'todo')
      throw new Error('The native write_todos request requires a to-do call.')
    expect(call.request.items).toEqual([
      {
        rowKey: '0:Read the native source',
        content: 'Read the native source',
        status: 'completed',
        activeForm: '',
      },
      {
        rowKey: '1:Run the native checks',
        content: 'Run the native checks',
        status: 'in_progress',
        activeForm: '',
      },
      {
        rowKey: '2:Keep the next item',
        content: 'Keep the next item',
        status: 'pending',
        activeForm: '',
      },
    ])
    expect(call.result).toBeUndefined()
  })

  it('keeps an unknown final status without a successful outcome', () => {
    const call = providerToolCall(AgentProvider.MUSE_CODE, {
      method: 'item/completed',
      params: {
        sessionId: 'session',
        item: {
          itemId: 'tool',
          kind: 'toolCall',
          turnId: 'turn',
          tool: 'bash',
          args: '{"command":"echo marker"}',
          status: 'futureFinal',
          visibleOutput: 'native preview',
        },
      },
    })
    expect(call?.status).toBe('incomplete')
    expect(call?.metadata).toEqual([
      { label: 'Native status', value: 'futureFinal' },
      { label: 'Native output', value: 'native preview' },
    ])
    expect(call?.result).toBeUndefined()
  })
})
