import { describe, expect, it } from 'vitest'
import { MessageCompletion } from '~/generated/proto/leapmux/v1/agent_pb'
import { acpToolCall } from '../../acp/extractors/toolCall'
import { geminiToolCallAdapter } from './toolCall'

function call(record: Record<string, unknown>, completion?: MessageCompletion, status = 'completed') {
  const frame = { sessionUpdate: 'tool_call_update', toolCallId: record.id, status, kind: 'other', content: [] }
  const supplement = { ...frame, rawOutput: { geminiToolRecord: record } }
  return acpToolCall(frame, geminiToolCallAdapter, supplement, completion)
}

describe('geminiToolCallAdapter', () => {
  it('classifies a failed shell command from a successful native tool invocation', () => {
    const result = call({ id: 'run_shell_command__c1', name: 'run_shell_command', args: { command: 'printf native; exit 7' }, status: 'success', resultDisplay: 'native', result: [{ functionResponse: { response: { output: '<untrusted_context>\nOutput: native\nExit Code: 7\nProcess Group PGID: 123\n</untrusted_context>' } } }] })
    expect(result.kind).toBe('execute')
    expect(result.status).toBe('failed')
    expect(result.request).toMatchObject({ command: 'printf native; exit 7' })
    expect(result).toMatchObject({ result: { commands: [{ output: 'native', exitCode: 7 }] } })
  })

  it('preserves an interrupted incomplete call beside its recovered native shell result', () => {
    const result = call({ id: 'run_shell_command__c1', name: 'run_shell_command', args: { command: 'exit 7' }, status: 'success', resultDisplay: 'native', result: [{ functionResponse: { response: { output: 'Output: native\nExit Code: 7\nProcess Group PGID: 123' } } }] }, MessageCompletion.INTERRUPTED, 'in_progress')
    expect(result.status).toBe('cancelled')
    expect(result).toMatchObject({ result: { commands: [{ output: 'native', exitCode: 7 }] } })
  })

  it('reads native typed todos and permits an explicit empty list', () => {
    const result = call({ id: 'write_todos__c2', name: 'write_todos', status: 'success', args: { todos: [{ description: 'native task', status: 'in_progress' }, { description: 'removed task', status: 'cancelled' }] }, result: [] })
    expect(result.kind).toBe('todo')
    expect(result.request).toMatchObject({ items: [{ content: 'native task', status: 'in_progress' }, { content: 'removed task', status: 'deleted' }] })
    expect(call({ id: 'write_todos__c3', name: 'write_todos', status: 'success', args: { todos: [] }, result: [] })).toMatchObject({ kind: 'todo', request: { items: [] }, result: { items: [] } })
  })

  it('reads recovered image bytes and the native file path', () => {
    const result = call({ id: 'read_file__c4', name: 'read_file', status: 'success', args: { file_path: '/work/native.png' }, result: [{ functionResponse: { response: { output: 'Binary content provided (1 item(s)).' } } }, { inlineData: { mimeType: 'image/png', data: 'native-image-bytes' } }] })
    expect(result).toMatchObject({ kind: 'read', request: { path: '/work/native.png' }, images: [{ mimeType: 'image/png', data: 'native-image-bytes', filePath: '/work/native.png' }] })
  })

  it('uses the native display to identify an MCP server with underscores', () => {
    const result = call({ id: 'mcp_result_probe_inspect__c5', name: 'mcp_result_probe_inspect', status: 'success', displayName: 'inspect (result_probe MCP Server)', args: { count: 0, enabled: false, text: '' }, result: [{ functionResponse: { response: { output: '<untrusted_context>\nNATIVE_MCP_INSPECT\n</untrusted_context>' } } }] })
    expect(result).toMatchObject({ kind: 'mcp', request: { server: 'result_probe', tool: 'inspect', args: { count: 0, enabled: false, text: '' } }, result: { content: [{ type: 'text', text: 'NATIVE_MCP_INSPECT' }] } })
  })

  it('refuses another call\'s supplement and preserves an unknown native call', () => {
    const frame = { sessionUpdate: 'tool_call_update', toolCallId: 'call-a', status: 'completed', kind: 'other', content: [] }
    const result = acpToolCall(frame, geminiToolCallAdapter, { ...frame, rawOutput: { geminiToolRecord: { id: 'call-b', name: 'read_file', args: { file_path: '/foreign' } } } })
    expect(result.kind).not.toBe('read')
    expect(call({ id: 'new_tool__c6', name: 'new_tool', status: 'success', args: { value: 0 }, result: [{ functionResponse: { response: { output: 'complete native result' } } }] })).toMatchObject({ kind: 'mcp', request: { args: { value: 0 } }, result: { content: [{ type: 'text', text: 'complete native result' }] } })
  })
})
