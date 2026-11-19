import type { PersistedControlResponse } from '../persistedControlResponse'
import { describe, expect, it, vi } from 'vitest'
import { WORKSPACE_TRUST_CONTROL_TYPE, WORKSPACE_TRUST_DECISION } from '~/generated/contracts/workspace-trust'
import { isWorkspaceTrustQuestion, withWorkspaceTrustResponse } from './workspaceTrust'

const QUESTION = {
  type: WORKSPACE_TRUST_CONTROL_TYPE.Question,
  path: '/work/project',
  configuration: ['MCP servers'],
}

/** A saved answer to `request`. */
function savedAnswer(request: Record<string, unknown> | undefined, decision: string): PersistedControlResponse {
  return {
    requestId: 'workspace-trust:session-1',
    claimToken: '',
    request,
    response: {
      type: 'control_response',
      response: {
        request_id: 'workspace-trust:session-1',
        response: { decision },
      },
    },
  }
}

describe('isWorkspaceTrustQuestion', () => {
  it('recognizes the question by its type', () => {
    expect(isWorkspaceTrustQuestion(QUESTION)).toBe(true)
    expect(isWorkspaceTrustQuestion({ type: WORKSPACE_TRUST_CONTROL_TYPE.Question })).toBe(true)
  })

  it('reads no question from another payload', () => {
    expect(isWorkspaceTrustQuestion({ type: 'permission' })).toBe(false)
    expect(isWorkspaceTrustQuestion({ method: 'session.event' })).toBe(false)
    expect(isWorkspaceTrustQuestion({})).toBe(false)
    expect(isWorkspaceTrustQuestion(undefined)).toBe(false)
  })
})

describe('withWorkspaceTrustResponse', () => {
  it('reads the two saved answers', () => {
    const wrapped = withWorkspaceTrustResponse(vi.fn(() => null))
    expect(wrapped(savedAnswer(QUESTION, WORKSPACE_TRUST_DECISION.Trust))).toEqual({ kind: 'label', text: 'Trusted this folder' })
    expect(wrapped(savedAnswer(QUESTION, WORKSPACE_TRUST_DECISION.Reject))).toEqual({ kind: 'label', text: 'Did not trust this folder' })
  })

  it('reads no label from an answer without a known decision', () => {
    const display = vi.fn(() => ({ kind: 'label' as const, text: 'the plugin label' }))
    const wrapped = withWorkspaceTrustResponse(display)
    expect(wrapped(savedAnswer(QUESTION, 'maybe'))).toBeNull()
    expect(wrapped({
      ...savedAnswer(QUESTION, ''),
      response: undefined,
    })).toBeNull()
    expect(display).not.toHaveBeenCalled()
  })

  it('hands the answer to another request to the plugin', () => {
    const display = vi.fn(() => ({ kind: 'label' as const, text: 'the plugin label' }))
    const wrapped = withWorkspaceTrustResponse(display)
    expect(wrapped(savedAnswer({ method: 'session.event' }, WORKSPACE_TRUST_DECISION.Trust))).toEqual({ kind: 'label', text: 'the plugin label' })
    expect(wrapped(savedAnswer(undefined, WORKSPACE_TRUST_DECISION.Trust))).toEqual({ kind: 'label', text: 'the plugin label' })
    expect(display).toHaveBeenCalledTimes(2)
  })
})
