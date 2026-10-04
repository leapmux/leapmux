import type { ACPToolCallAdapter } from './toolCall'
import { describe, expect, it, vi } from 'vitest'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { isObject, pickString } from '~/lib/jsonPick'
import { resolveMessageForRendering } from '../../registry'
import { input } from '../../testUtils'
import { acpExtractRow } from './row'

describe('acpExtractRow', () => {
  it.each(['request', 'result'] as const)('refuses an explicit no-side context before extracting a native %s', (side) => {
    const resolved = input({
      sessionUpdate: side === 'request' ? 'tool_call' : 'tool_call_update',
      toolCallId: 'native-role-call',
      kind: 'execute',
      status: side === 'request' ? 'pending' : 'completed',
      rawInput: { command: 'printf native' },
      ...(side === 'result' ? { rawOutput: 'Native bytes.' } : {}),
    }, null, AgentProvider.OPENCODE)
    const adapter = vi.fn<ACPToolCallAdapter>((_facts, base) => base())
    const span = { request: undefined, result: undefined, visibleRows: { request: false, result: false } }
    expect(acpExtractRow({ resolved, category: { kind: 'tool_use' }, span: { ...span, role: 'none' } }, adapter)).toBeNull()
    expect(adapter).not.toHaveBeenCalled()
    expect(acpExtractRow({ resolved, category: { kind: 'tool_use' }, span: { ...span, role: side } }, adapter)).toMatchObject({ kind: 'tool', role: side, call: { id: 'native-role-call' } })
    expect(adapter).toHaveBeenCalledTimes(1)
  })

  it('keeps an independent plan row before the native-tool no-side refusal', () => {
    const resolved = input({ sessionUpdate: 'plan', entries: [{ content: 'Check the native plan.', status: 'pending', priority: 'medium' }] }, null, AgentProvider.OPENCODE)
    const row = acpExtractRow({
      resolved,
      category: { kind: 'tool_use' },
      span: { request: undefined, result: undefined, role: 'none', visibleRows: { request: false, result: false } },
    })
    expect(row).toMatchObject({ kind: 'tool', role: 'result', call: { kind: 'todo', request: { items: [{ content: 'Check the native plan.' }] } } })
  })

  it.each([undefined, '', 'stored-native-session'])('passes the stored row session to the adapter without trusting native input: %j', (session) => {
    const tool = {
      sessionUpdate: 'tool_call_update',
      toolCallId: 'native-call',
      kind: 'execute',
      title: 'printf native',
      status: 'completed',
      rawInput: { command: 'printf native' },
      rawOutput: 'native',
      agentSessionId: 'untrusted-protocol-session',
    }
    const parsed = resolveMessageForRendering({
      ...input(tool, undefined, AgentProvider.OPENCODE),
      ...(session === undefined ? {} : { agentSessionId: session }),
    }, AgentProvider.OPENCODE)
    let observed: unknown
    const adapter: ACPToolCallAdapter = (facts, base) => {
      observed = facts
      return base()
    }
    const row = acpExtractRow({
      resolved: parsed,
      category: { kind: 'tool_use' },
      span: { request: undefined, result: undefined, role: 'result', visibleRows: { request: false, result: true } },
    }, adapter)
    expect(row?.kind).toBe('tool')
    expect(isObject(observed)).toBe(true)
    if (!isObject(observed))
      throw new Error('The ACP adapter must receive its tool facts.')
    expect(pickString(observed, 'agentSessionId', undefined)).toBe(session)
  })
})
