import { describe, expect, it } from 'vitest'
import { buildDenyResponse, CONTROL_REJECTED_BY_USER_MESSAGE } from './controlResponse'

// Reach the deny reason nested in the control_response envelope:
// { response: { response: { behavior, message } } }.
function denyEnvelope(resp: Record<string, unknown>): { requestId: string, behavior: string, message: string } {
  const outer = resp.response as { request_id: string, response: { behavior: string, message: string } }
  return { requestId: outer.request_id, behavior: outer.response.behavior, message: outer.response.message }
}

describe('builddenyresponse', () => {
  it('fills the shared rejected-by-user placeholder for a bare deny (omitted or empty reason)', () => {
    // The Claude Code SDK converts a deny into a tool_result with is_error=true, and the Anthropic
    // API rejects empty content, so the message must never be empty -- a bare deny falls back to
    // the shared constant, which the backend's NormalizeRejectionMessage then collapses to "".
    expect(denyEnvelope(buildDenyResponse('req-1')).message).toBe(CONTROL_REJECTED_BY_USER_MESSAGE)
    expect(denyEnvelope(buildDenyResponse('req-1', '')).message).toBe(CONTROL_REJECTED_BY_USER_MESSAGE)
  })

  it('passes a typed rejection reason through unchanged', () => {
    expect(denyEnvelope(buildDenyResponse('req-1', 'looks unsafe')).message).toBe('looks unsafe')
  })

  it('carries the request id and deny behavior in the control_response envelope', () => {
    const r = buildDenyResponse('req-42')
    expect(r.type).toBe('control_response')
    const env = denyEnvelope(r)
    expect(env.requestId).toBe('req-42')
    expect(env.behavior).toBe('deny')
  })

  it('pins the placeholder byte-identical to the backend ControlRejectedByUserMessage', () => {
    // This literal MUST match backend/internal/worker/agent/factory.go's ControlRejectedByUserMessage
    // -- if it drifts, the backend can no longer collapse a bare deny and the "Rejected by user."
    // placeholder leaks into the transcript/rail as if it were typed feedback.
    expect(CONTROL_REJECTED_BY_USER_MESSAGE).toBe('Rejected by user.')
  })
})
