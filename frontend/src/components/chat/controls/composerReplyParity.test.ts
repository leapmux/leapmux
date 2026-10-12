import { describe, expect, it } from 'vitest'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { providerFor } from '../providers/registry'
import '../providers'

// Every registered Agent Client Protocol plugin answers the composer with the
// agent's own option, never with the shared envelope that its agent cannot read.
describe('the composer reply of each ACP plugin', () => {
  const PERMISSION_REQUEST = {
    jsonrpc: '2.0',
    id: 3,
    method: 'session/request_permission',
    params: {
      sessionId: 's',
      toolCall: { toolCallId: 'c', kind: 'execute', title: 'Run', rawInput: { command: 'ls' } },
      options: [
        { optionId: 'allow-1', name: 'Allow', kind: 'allow_once' },
        { optionId: 'reject-1', name: 'Reject', kind: 'reject_once' },
      ],
    },
  }
  const ACP_PROVIDERS = [
    AgentProvider.CURSOR,
    AgentProvider.GOOSE,
    AgentProvider.GROK_BUILD,
    AgentProvider.KILO,
    AgentProvider.KIRO,
    AgentProvider.OPENCODE,
    AgentProvider.QWEN_CODE,
    AgentProvider.REASONIX,
  ] as const

  it.each(ACP_PROVIDERS.map(provider => [AgentProvider[provider], provider] as const))('selects the agent\'s own options for %s', (_name, provider) => {
    const controls = providerFor(provider)?.controls
    expect(controls?.buildControlResponse?.(PERMISSION_REQUEST, 'use the other file', 'jsonrpc:3'))
      .toMatchObject({ jsonrpc: '2.0', result: { outcome: { outcome: 'selected', optionId: 'reject-1' } } })
    expect(controls?.buildControlResponse?.(PERMISSION_REQUEST, '', 'jsonrpc:3'))
      .toMatchObject({ jsonrpc: '2.0', result: { outcome: { outcome: 'selected', optionId: 'allow-1' } } })
  })

  // OpenCode and Kilo draw their daemon's own `once`/`reject` pair for a request that
  // states no option list, and the composer answers with the same pair.
  it.each([AgentProvider.OPENCODE, AgentProvider.KILO].map(provider => [AgentProvider[provider], provider] as const))('answers an option-less request with the daemon pair for %s', (_name, provider) => {
    const optionLess = { ...PERMISSION_REQUEST, params: { ...PERMISSION_REQUEST.params, options: undefined } }
    const controls = providerFor(provider)?.controls
    expect(controls?.extractControl?.({ payload: optionLess })).toMatchObject({ kind: 'permission', permission: { options: [{ optionId: 'once' }, { optionId: 'reject' }] } })
    expect(controls?.buildControlResponse?.(optionLess, 'use the other file', 'jsonrpc:3'))
      .toEqual({ jsonrpc: '2.0', id: 'jsonrpc:3', result: { outcome: { outcome: 'selected', optionId: 'reject' } } })
    expect(controls?.buildControlResponse?.(optionLess, '', 'jsonrpc:3'))
      .toEqual({ jsonrpc: '2.0', id: 'jsonrpc:3', result: { outcome: { outcome: 'selected', optionId: 'once' } } })
  })
})
