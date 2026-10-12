import { describe, expect, it } from 'vitest'
import { AgentProvider, MessageCompletion } from '~/generated/proto/leapmux/v1/agent_pb'
import { providerToolCall } from '~/test-support/toolCallFixture'
import { input } from '../../testUtils'
import { MUSE_NATIVE_MCP_ITEM_FRAME } from '../toolResults.fixtures'
import '~/components/chat/providers'

describe('museToolCall', () => {
  it('dispatches the recorded native MCP item through the registered plugin', () => {
    const frame = JSON.parse(MUSE_NATIVE_MCP_ITEM_FRAME)
    const original = structuredClone(frame)
    const call = providerToolCall(AgentProvider.MUSE_CODE, frame)
    expect(call?.kind).toBe('mcp')
    expect(call?.name).toBe('mcp__echo_probe__echo')
    expect(call?.request).toEqual({ server: 'echo_probe', tool: 'echo', args: { value: 'MUSE_MCP_EXECUTION_PROOF' } })
    expect(call?.result).toEqual({ unparsed: true, text: 'MCP_ECHO:MUSE_MCP_EXECUTION_PROOF' })
    expect(frame).toEqual(original)
  })

  it('rejects a sibling result from another native session', () => {
    const request = { method: 'item/started', params: { sessionId: 'session', item: { itemId: 'tool', kind: 'toolCall', turnId: 'turn', callId: 'call', tool: 'bash', args: '{"command":"printf native"}', status: 'inProgress' } } }
    const result = { method: 'item/completed', params: { sessionId: 'foreign-session', item: { ...request.params.item, status: 'completed', visibleOutput: 'Foreign output' } } }
    const call = providerToolCall(AgentProvider.MUSE_CODE, request, { result: input(result, undefined, AgentProvider.MUSE_CODE) })
    expect(call?.status).toBe('in_progress')
    expect(call?.result).toBeUndefined()
  })

  it('keeps a retained unfinished tool final without an invented outcome', () => {
    const call = providerToolCall(AgentProvider.MUSE_CODE, {
      method: 'item/started',
      params: { sessionId: 'session', item: { itemId: 'tool', kind: 'toolCall', turnId: 'turn', tool: 'bash', args: '{"command":"printf native"}', status: 'inProgress' } },
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
          args: JSON.stringify({ todos: [
            { text: 'Read the native source', status: 'completed' },
            { text: 'Run the native checks', status: 'in_progress' },
            { text: 'Keep the next item', status: 'pending' },
          ] }),
        },
      },
    })
    if (call?.kind !== 'todo')
      throw new Error('The native write_todos request requires a to-do call.')
    expect(call.request.items).toEqual([
      { rowKey: '0:Read the native source', content: 'Read the native source', status: 'completed', activeForm: '' },
      { rowKey: '1:Run the native checks', content: 'Run the native checks', status: 'in_progress', activeForm: '' },
      { rowKey: '2:Keep the next item', content: 'Keep the next item', status: 'pending', activeForm: '' },
    ])
    expect(call.result).toBeUndefined()
  })

  it('keeps an unknown final status without a successful outcome', () => {
    const call = providerToolCall(AgentProvider.MUSE_CODE, {
      method: 'item/completed',
      params: { sessionId: 'session', item: { itemId: 'tool', kind: 'toolCall', turnId: 'turn', tool: 'bash', args: '{"command":"echo marker"}', status: 'futureFinal', visibleOutput: 'native preview' } },
    })
    expect(call?.status).toBe('incomplete')
    expect(call?.metadata).toEqual([{ label: 'Native status', value: 'futureFinal' }, { label: 'Native output', value: 'native preview' }])
    expect(call?.result).toBeUndefined()
  })
})
