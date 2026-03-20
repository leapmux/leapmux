import { describe, expect, it } from 'vitest'
import { AgentProvider } from '~/generated/leapmux/v1/agent_pb'
import { getProviderPlugin } from './registry'

// Side-effect import to register the Codex plugin.
import './codex'

describe('codex buildControlResponse', () => {
  const plugin = getProviderPlugin(AgentProvider.CODEX)!

  function makeAllowPayload(requestId: string) {
    return {
      type: 'control_response',
      response: {
        subtype: 'success',
        request_id: requestId,
        response: { behavior: 'allow', updatedInput: {} },
      },
    }
  }

  function makeDenyPayload(requestId: string, message: string) {
    return {
      type: 'control_response',
      response: {
        subtype: 'success',
        request_id: requestId,
        response: { behavior: 'deny', message },
      },
    }
  }

  function decode(bytes: Uint8Array): Record<string, unknown> {
    return JSON.parse(new TextDecoder().decode(bytes))
  }

  it('translates allow to approved', () => {
    const result = plugin.buildControlResponse!(makeAllowPayload('42'))
    expect(result).not.toBeNull()
    const parsed = decode(result!)
    expect(parsed).toMatchObject({
      jsonrpc: '2.0',
      id: 42,
      result: { decision: 'approved' },
    })
  })

  it('translates deny to denied', () => {
    const result = plugin.buildControlResponse!(makeDenyPayload('42', 'No thanks'))
    expect(result).not.toBeNull()
    const parsed = decode(result!)
    expect(parsed).toMatchObject({
      jsonrpc: '2.0',
      id: 42,
      result: { decision: 'denied' },
    })
  })

  it('handles non-numeric request IDs', () => {
    const result = plugin.buildControlResponse!(makeAllowPayload('abc'))
    expect(result).not.toBeNull()
    const parsed = decode(result!)
    expect(parsed).toMatchObject({
      jsonrpc: '2.0',
      id: 'abc',
      result: { decision: 'approved' },
    })
  })
})
