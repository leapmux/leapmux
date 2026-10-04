import type { RowExtractionInput } from '../../../rowExtractionTypes'
import { describe, expect, it } from 'vitest'
import { AgentProvider, MessageCompletion } from '~/generated/proto/leapmux/v1/agent_pb'
import { input } from '../../testUtils'
import { classifyDeepseekHarnessMessage } from '../classification'
import { deepseekHarnessSpanRole } from '../spanRole'
import { deepseekHarnessToolCall } from './toolCall'
import '../plugin'

const provider = AgentProvider.DEEPSEEK_HARNESS
const call = (id: string, name: string, args: unknown = {}) => ({ type: 'tool/call', seq: 1, time: 1000, data: { callId: id, name, arguments: JSON.stringify(args) } })
const result = (id: string, content: unknown[] = [], fields: Record<string, unknown> = {}) => ({ type: 'tool/result', seq: 2, time: 2000, data: { message: { toolCallId: id, role: 'tool', content, isError: false }, ...fields } })

function row(payload: Record<string, unknown>, request?: Record<string, unknown>, reply?: Record<string, unknown>, completion?: MessageCompletion): RowExtractionInput {
  const resolved = { ...input(payload, null, provider), ...(completion !== undefined ? { completion } : {}) }
  return {
    resolved,
    category: classifyDeepseekHarnessMessage(resolved),
    span: {
      request: request ? input(request, null, provider) : undefined,
      result: reply ? input(reply, null, provider) : undefined,
      role: deepseekHarnessSpanRole(resolved),
      visibleRows: { request: request !== undefined, result: reply !== undefined },
    },
    ...(completion !== undefined ? { completion } : {}),
  }
}

describe('deepseekHarnessToolCall', () => {
  it('pairs a result with the arguments of the same native call', () => {
    const request = call('native-call', 'bash', { command: 'exit 7', description: 'Report a failed command.' })
    const reply = result('native-call', [{ type: 'text', text: 'Native stderr\n[exit code: 7]' }])
    const extracted = deepseekHarnessToolCall(row(reply, request))
    expect(extracted).toMatchObject({ id: 'native-call', kind: 'execute', status: 'completed', request: { command: 'exit 7' }, result: { commands: [{ output: 'Native stderr', exitCode: 7 }] } })
  })

  it('does not borrow arguments or output from another native call', () => {
    const request = call('mine', 'bash', { command: 'printf mine', description: 'Print this call.' })
    const foreign = result('other', [{ type: 'text', text: 'Foreign output' }])
    expect(deepseekHarnessToolCall(row(request, undefined, foreign))).toMatchObject({ kind: 'execute', status: 'in_progress', request: { command: 'printf mine' } })
    expect(deepseekHarnessToolCall(row(request, undefined, foreign))).not.toHaveProperty('result')
    const own = result('mine', [{ type: 'text', text: 'Mine' }])
    const other = call('other', 'bash', { command: 'dangerous command' })
    expect(deepseekHarnessToolCall(row(own, other))).not.toMatchObject({ request: { command: 'dangerous command' } })
  })

  it('preserves an empty successful output and a zero exit code', () => {
    const request = call('native-call', 'bash', { command: 'true', description: 'Return without output.' })
    const reply = result('native-call', [{ type: 'text', text: '' }])
    expect(deepseekHarnessToolCall(row(reply, request))).toMatchObject({ status: 'completed', result: { commands: [{ output: '', exitCode: 0 }] } })
  })

  it('reads dedicated native JavaScript source as code execution', () => {
    const request = call('code-call', 'workflow', { script: 'return 21 * 2', meta: { name: 'native-compute' } })
    const reply = result('code-call', [{ type: 'text', text: '{"value":42}' }])
    expect(deepseekHarnessToolCall(row(reply, request))).toMatchObject({ kind: 'execute', request: { command: 'return 21 * 2', language: 'javascript' }, result: { commands: [{ output: '{"value":42}' }] } })
  })

  it('keeps dedicated JavaScript source when its native execution fails', () => {
    const source = 'throw new Error(String(4 + 3))'
    const request = call('failed-code', 'workflow', { script: source, meta: { name: 'native-code' } })
    const reply = result('failed-code', [{ type: 'text', text: 'Workflow execution failed: 7' }])
    reply.data.message.isError = true
    expect(deepseekHarnessToolCall(row(reply, request))).toMatchObject({ kind: 'execute', status: 'failed', request: { command: source, language: 'javascript' }, result: { failure: true, text: 'Workflow execution failed: 7' } })
  })

  it('uses the native committed hunks rather than the requested replacement', () => {
    const request = call('edit-call', 'edit', { file_path: '/work/a', old_string: 'old', new_string: 'requested' })
    const reply = result('edit-call', [{ type: 'text', text: 'Updated file' }], { meta: { diffs: [{ path: '/work/a', oldText: 'old with context', newText: 'applied with context' }] } })
    expect(deepseekHarnessToolCall(row(reply, request))).toMatchObject({ kind: 'edit', request: { changes: [{ newStr: 'requested' }] }, result: { changes: [{ oldStr: 'old with context', newStr: 'applied with context' }] } })
    expect(deepseekHarnessToolCall(row(result('edit-call', [{ type: 'text', text: 'No hunks' }]), request))).toMatchObject({ kind: 'edit', result: { unparsed: true, text: 'No hunks' } })
  })

  it('keeps a failed edit as a failure without claiming an applied change', () => {
    const request = call('edit-call', 'edit', { file_path: '/work/a', old_string: 'old', new_string: 'new' })
    const reply = result('edit-call')
    reply.data.message.isError = true
    reply.data.message.content = [{ type: 'text', text: 'Observation is stale.' }]
    expect(deepseekHarnessToolCall(row(reply, request))).toMatchObject({ kind: 'edit', status: 'failed', result: { failure: true, text: 'Observation is stale.' } })
  })

  it('preserves native generic content and MCP tool identity', () => {
    const request = call('mcp-call', 'mcp__results__inspect', { count: 0, enabled: false })
    const reply = result('mcp-call', [{ type: 'text', text: 'Native MCP content' }, { type: 'resource', resource: { uri: 'file:///native', mimeType: 'text/plain', text: 'Native resource' } }])
    const extracted = deepseekHarnessToolCall(row(reply, request))
    expect(extracted).toMatchObject({ kind: 'mcp', request: { server: 'results', tool: 'inspect', args: { count: 0, enabled: false } }, result: { content: [{ type: 'text', text: 'Native MCP content' }, { type: 'resource', text: 'Native resource' }] } })
  })

  it('keeps a retained call cancelled without a fabricated native result', () => {
    const request = call('interrupted-call', 'bash', { command: 'wait', description: 'Wait for cancellation.' })
    const extracted = deepseekHarnessToolCall(row(request, undefined, undefined, MessageCompletion.INTERRUPTED))
    expect(extracted).toMatchObject({ kind: 'execute', status: 'cancelled' })
    expect(extracted).not.toHaveProperty('result')
  })

  it('rejects absent call identities and malformed arguments without a cast', () => {
    expect(deepseekHarnessToolCall(row({ type: 'tool/result', data: { message: { content: [] } } }))).toBeNull()
    const request = call('call', 'bash')
    request.data.arguments = 'not json'
    expect(deepseekHarnessToolCall(row(request))).toMatchObject({ kind: 'execute', request: { command: '' } })
  })
})
