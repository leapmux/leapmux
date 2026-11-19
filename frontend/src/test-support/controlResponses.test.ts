import { describe, expect, it } from 'vitest'
import { CONTROL_ANSWER_MESSAGE } from '~/generated/contracts/control-answer'
import { buildAllowResponse, buildControlResponseEnvelope, buildDenyResponse, withControlChoice } from './controlResponses'

// Reach the deny reason nested in the control_response envelope:
// { response: { response: { behavior, message } } }.
function denyEnvelope(resp: Record<string, unknown>): { requestId: string, behavior: string, message: string } {
  const outer = resp.response as { request_id: string, response: { behavior: string, message: string } }
  return { requestId: outer.request_id, behavior: outer.response.behavior, message: outer.response.message }
}

describe('buildDenyResponse', () => {
  it('fills the shared rejected-by-user placeholder for a bare deny (omitted or empty reason)', () => {
    // The Claude Code SDK converts a deny into a tool_result with is_error=true, and the Anthropic
    // API rejects empty content, so the message must never be empty -- a bare deny falls back to
    // the shared constant, which the backend's NormalizeRejectionMessage then collapses to "".
    expect(denyEnvelope(buildDenyResponse('req-1')).message).toBe(CONTROL_ANSWER_MESSAGE.RejectedByUser)
    expect(denyEnvelope(buildDenyResponse('req-1', '')).message).toBe(CONTROL_ANSWER_MESSAGE.RejectedByUser)
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
    // This literal MUST match backend/internal/worker/agent/control_response.go's ControlRejectedByUserMessage
    // -- if it drifts, the backend can no longer collapse a bare deny and the "Rejected by user."
    // placeholder leaks into the transcript/rail as if it were typed feedback.
    expect(CONTROL_ANSWER_MESSAGE.RejectedByUser).toBe('Rejected by user.')
  })
})

describe('buildControlResponseEnvelope', () => {
  it('wraps one provider answer in the shared envelope', () => {
    // Copilot answers with an approval SCOPE and with a question ANSWER, which neither
    // the allow nor the deny builder can carry. Both go through this envelope.
    expect(buildControlResponseEnvelope('req-7', { behavior: 'allow', scope: 'session' })).toEqual({
      type: 'control_response',
      response: {
        subtype: 'success',
        request_id: 'req-7',
        response: { behavior: 'allow', scope: 'session' },
      },
    })
  })

  it('states the same envelope the allow and deny builders produce', () => {
    expect(buildAllowResponse('req-8', { path: '/tmp/a' }))
      .toEqual(buildControlResponseEnvelope('req-8', {
        behavior: 'allow',
        updatedInput: { path: '/tmp/a' },
      }))
    expect(buildDenyResponse('req-8', 'no'))
      .toEqual(buildControlResponseEnvelope('req-8', { behavior: 'deny', message: 'no' }))
  })
})

describe('withControlChoice', () => {
  it('adds the choice beside the behavior and keeps every other field', () => {
    const envelope = buildControlResponseEnvelope('req-1', {
      behavior: 'allow',
      updatedInput: { a: 1 },
    })
    expect(withControlChoice(envelope, 'Option A')).toStrictEqual({
      type: 'control_response',
      response: {
        subtype: 'success',
        request_id: 'req-1',
        response: {
          behavior: 'allow',
          updatedInput: { a: 1 },
          choice: 'Option A',
        },
      },
    })
  })

  it('does not change its input', () => {
    const envelope = buildDenyResponse('req-1')
    const before = JSON.stringify(envelope)
    withControlChoice(envelope, 'Revise')
    expect(JSON.stringify(envelope)).toBe(before)
  })

  it('replaces a choice the envelope already carries', () => {
    const envelope = withControlChoice(buildDenyResponse('req-1'), 'Revise')
    expect(withControlChoice(envelope, 'Reject and Exit')).toMatchObject({
      response: {
        response: { choice: 'Reject and Exit' },
      },
    })
  })

  it('builds the nested objects an envelope lacks', () => {
    expect(withControlChoice({}, 'x')).toStrictEqual({
      response: {
        response: { choice: 'x' },
      },
    })
  })

  // Only an object can carry the choice. A null, an array or a string in either
  // level is replaced rather than spread, which would put its indexes beside the
  // choice or drop the choice.
  it.each([
    ['null', null],
    [
      'an array',
      ['a', 'b'],
    ],
    ['a string', 'ok'],
  ])('replaces an outer response that holds %s', (_shape, response) => {
    expect(withControlChoice({ type: 'control_response', response }, 'x'))
      .toStrictEqual({
        type: 'control_response',
        response: {
          response: { choice: 'x' },
        },
      })
  })

  it.each([
    ['null', null],
    [
      'an array',
      ['allow'],
    ],
    ['a string', 'allow'],
  ])('replaces an inner response that holds %s and keeps the outer fields', (_shape, response) => {
    expect(withControlChoice({
      response: { subtype: 'success', request_id: 'req-1', response },
    }, 'x'))
      .toStrictEqual({
        response: {
          subtype: 'success',
          request_id: 'req-1',
          response: { choice: 'x' },
        },
      })
  })
})
