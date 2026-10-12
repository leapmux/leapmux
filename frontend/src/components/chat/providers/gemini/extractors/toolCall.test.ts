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

  it('keeps an approved shell command completed', () => {
    const result = call({ id: 'run_shell_command__c7', name: 'run_shell_command', args: { command: 'printf native' }, status: 'success', resultDisplay: 'native', result: [{ functionResponse: { response: { output: 'Output: native\nExit Code: 0\nProcess Group PGID: 123' } } }] })
    expect(result.status).toBe('completed')
  })

  it('refuses another call\'s supplement and preserves an unknown native call', () => {
    const frame = { sessionUpdate: 'tool_call_update', toolCallId: 'call-a', status: 'completed', kind: 'other', content: [] }
    const result = acpToolCall(frame, geminiToolCallAdapter, { ...frame, rawOutput: { geminiToolRecord: { id: 'call-b', name: 'read_file', args: { file_path: '/foreign' } } } })
    expect(result.kind).not.toBe('read')
    expect(call({ id: 'new_tool__c6', name: 'new_tool', status: 'success', args: { value: 0 }, result: [{ functionResponse: { response: { output: 'complete native result' } } }] })).toMatchObject({ kind: 'mcp', request: { args: { value: 0 } }, result: { content: [{ type: 'text', text: 'complete native result' }] } })
  })
})

/**
 * The live calls whose tool is NOT in `GEMINI_TOOL_KINDS`.
 *
 * `toolKinds.test.ts` pins the names that the table maps. These cases pin the names that
 * the adapter builds from branches of its own, and the names that the shared build reads
 * at the kind that the wire states.
 */
describe('geminiToolCallAdapter live calls outside the kind table', () => {
  function live(name: string, frame: Record<string, unknown>) {
    return acpToolCall({ sessionUpdate: 'tool_call', toolCallId: `${name}__live`, status: 'pending', title: name, content: [], ...frame }, geminiToolCallAdapter, undefined)
  }

  it('reads a to-do update that carries a list as a checklist', () => {
    const result = live('write_todos', { kind: 'other', rawInput: { todos: [{ description: 'native task', status: 'pending' }] } })
    expect(result).toMatchObject({ kind: 'todo', name: 'write_todos', request: { items: [{ content: 'native task', status: 'pending' }] } })
  })

  // A to-do update with no list states no checklist, so it takes the shared build at the
  // kind that the wire states.
  it('reads a to-do update that carries no list at the wire kind', () => {
    expect(live('write_todos', { kind: 'other', rawInput: { todos: 'not a list' } })).toMatchObject({ kind: 'mcp', name: 'write_todos' })
  })

  it('reads a subagent launch as an agent call', () => {
    expect(live('invoke_agent', { kind: 'other', rawInput: { agent_name: 'generalist', prompt: 'Inspect the parser.' } })).toMatchObject({ kind: 'agent', name: 'invoke_agent' })
  })

  it.each([
    ['exit_plan_mode', 'other', 'mcp'],
    ['complete_task', 'other', 'mcp'],
    ['new_tool', 'search', 'search'],
  ])('reads %s at the wire kind %s', (name, wireKind, kind) => {
    expect(live(name, { kind: wireKind, rawInput: {} })).toMatchObject({ kind, name })
  })
})

/**
 * A call the reader refused never ran.
 *
 * Gemini CLI answers a Deny with the permission outcome `cancel`. It then fails the call
 * with one sentence, `Tool "<name>" was canceled by the user.`, in a content block, and
 * records no tool call in its session file (`runTool`,
 * `packages/cli/src/acp/acpSession.ts`). The failed update is therefore the whole
 * statement, and that exact sentence is the one native signal.
 */
describe('geminiToolCallAdapter refused calls', () => {
  const refusal = (toolName: string) => `Tool "${toolName}" was canceled by the user.`

  /** The failed update Gemini CLI sends after the Deny answer. */
  function ended(toolCallId: string, kind: string, text: string, frame: Record<string, unknown> = {}) {
    return acpToolCall({ sessionUpdate: 'tool_call_update', toolCallId, status: 'failed', kind, content: [{ type: 'content', content: { type: 'text', text } }], ...frame }, geminiToolCallAdapter, undefined)
  }

  it('reads a refused shell command as declined, with the refusal as the result', () => {
    const result = ended('run_shell_command__denied', 'execute', refusal('run_shell_command'))
    expect(result.kind).toBe('execute')
    expect(result.status).toBe('declined')
    expect(result.result).toStrictEqual({ failure: true, text: refusal('run_shell_command') })
    expect(result.images).toStrictEqual([])
    expect(result.degradation).toBeUndefined()
  })

  // A plan the reader sent back reaches Gemini CLI as the same `cancel` outcome, on the
  // plan tool, which the protocol states as the `other` kind.
  it('reads a plan the reader sent back as declined', () => {
    const result = ended('exit_plan_mode__denied', 'other', refusal('exit_plan_mode'), { title: 'Exit plan mode' })
    expect(result.status).toBe('declined')
    expect(result.result).toStrictEqual({ failure: true, text: refusal('exit_plan_mode') })
    expect(result.degradation).toBeUndefined()
  })

  // The sentence states the tool itself, so a call whose identifier states none is
  // still a refusal.
  it('reads a refusal whose call identifier states no tool', () => {
    expect(ended('denied-call', 'execute', refusal('run_shell_command')).status).toBe('declined')
  })

  it('keeps another failure of the same tool a failure', () => {
    expect(ended('run_shell_command__missing', 'execute', 'Tool "run_shell_command" not found in registry.').status).toBe('failed')
  })

  // A refusal is the WHOLE text. The same words inside a longer one are the tool's own.
  it('keeps the refusal words inside a longer text a failure', () => {
    expect(ended('run_shell_command__longer', 'execute', `${refusal('run_shell_command')} Try again.`).status).toBe('failed')
    expect(ended('run_shell_command__quoted', 'execute', `Error: ${refusal('run_shell_command')}`).status).toBe('failed')
  })

  // A call that ran and PRINTED the words is a call that completed.
  it('keeps a completed call that printed the refusal words completed', () => {
    const frame = { sessionUpdate: 'tool_call_update', toolCallId: 'run_shell_command__printed', status: 'completed', kind: 'execute', content: [{ type: 'content', content: { type: 'text', text: refusal('run_shell_command') } }] }
    expect(acpToolCall(frame, geminiToolCallAdapter, undefined).status).toBe('completed')
  })
})
