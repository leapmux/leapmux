import { describe, expect, it } from 'vitest'
import { AgentProvider } from '~/generated/leapmux/v1/agent_pb'
import { getProviderPlugin } from './registry'

// Side-effect import to register the Codex plugin.
import './codex'

describe('codex classify', () => {
  const plugin = getProviderPlugin(AgentProvider.CODEX)!

  it('hides thread/status/changed notifications', () => {
    const parent = {
      method: 'thread/status/changed',
      params: {
        threadId: '019d0b79-3982-7bf2-b85c-890371421ade',
        status: {
          type: 'active',
          activeFlags: ['waitingOnApproval'],
        },
      },
    }
    const result = plugin.classify(parent, null)
    expect(result).toEqual({ kind: 'hidden' })
  })
})

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

  function makeCodexDecisionPayload(requestId: string, codexDecision: unknown) {
    return {
      type: 'control_response',
      response: {
        subtype: 'success',
        request_id: requestId,
        response: { codexDecision },
      },
    }
  }

  function decode(bytes: Uint8Array): Record<string, unknown> {
    return JSON.parse(new TextDecoder().decode(bytes))
  }

  it('translates allow to accept', () => {
    const result = plugin.buildControlResponse!(makeAllowPayload('42'))
    expect(result).not.toBeNull()
    const parsed = decode(result!)
    expect(parsed).toMatchObject({
      jsonrpc: '2.0',
      id: 42,
      result: { decision: 'accept' },
    })
  })

  it('translates deny to decline', () => {
    const result = plugin.buildControlResponse!(makeDenyPayload('42', 'No thanks'))
    expect(result).not.toBeNull()
    const parsed = decode(result!)
    expect(parsed).toMatchObject({
      jsonrpc: '2.0',
      id: 42,
      result: { decision: 'decline' },
    })
  })

  it('handles non-numeric request IDs', () => {
    const result = plugin.buildControlResponse!(makeAllowPayload('abc'))
    expect(result).not.toBeNull()
    const parsed = decode(result!)
    expect(parsed).toMatchObject({
      jsonrpc: '2.0',
      id: 'abc',
      result: { decision: 'accept' },
    })
  })

  it('passes through string codexDecision as-is', () => {
    const result = plugin.buildControlResponse!(makeCodexDecisionPayload('7', 'cancel'))
    expect(result).not.toBeNull()
    const parsed = decode(result!)
    expect(parsed).toMatchObject({
      jsonrpc: '2.0',
      id: 7,
      result: { decision: 'cancel' },
    })
  })

  it('passes through object codexDecision as-is', () => {
    const decision = { acceptWithExecpolicyAmendment: { execpolicy_amendment: ['touch'] } }
    const result = plugin.buildControlResponse!(makeCodexDecisionPayload('9', decision))
    expect(result).not.toBeNull()
    const parsed = decode(result!)
    expect(parsed).toMatchObject({
      jsonrpc: '2.0',
      id: 9,
      result: { decision },
    })
  })
})
