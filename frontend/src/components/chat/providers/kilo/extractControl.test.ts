import { describe, expect, it } from 'vitest'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { providerFor } from '../registry'
import './plugin'

const extract = providerFor(AgentProvider.KILO)!.controls!.extractControl!

function permission(options?: unknown) {
  return {
    payload: {
      jsonrpc: '2.0',
      id: 'native-permission',
      method: 'session/request_permission',
      params: {
        sessionId: 'native-session',
        toolCall: {
          toolCallId: 'call-1',
          title: 'bash',
          rawInput: { command: 'pwd' },
        },
        ...(options === undefined
          ? {}
          : { options }),
      },
    },
  }
}

describe('kiloExtractControl', () => {
  it.each([undefined, [], null, false, 'invalid'])('supplies native once and reject IDs when no usable options arrive: %j', (options) => {
    const result = extract(permission(options))
    expect(result?.kind).toBe('permission')
    expect(result?.kind === 'permission' && result.permission.options).toEqual([
      { optionId: 'once', kind: 'allow_once', name: 'Allow' },
      { optionId: 'reject', kind: 'reject_once', name: 'Deny' },
    ])
  })

  it('preserves the actual options supplied by the provider', () => {
    const options = [
      { optionId: 'native-once', kind: 'allow_once', name: 'Approve this call' },
    ]
    const result = extract(permission(options))
    expect(result?.kind === 'permission' && result.permission.options).toEqual(options)
    expect(options).toEqual([
      { optionId: 'native-once', kind: 'allow_once', name: 'Approve this call' },
    ])
  })

  it('returns no permission control for another request', () => {
    expect(extract({
      payload: { method: 'unrelated', params: {} },
    })).toBeNull()
  })
})
