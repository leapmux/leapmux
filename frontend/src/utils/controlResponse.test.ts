import { describe, expect, it } from 'vitest'
import { CONTROL_ANSWER_MESSAGE } from '~/generated/contracts/control-answer'
import { decodeControlBehaviorEnvelope, normalizeRejectionMessage } from './controlResponse'

describe('normalizeRejectionMessage', () => {
  it('trims a typed reason and collapses the sentinel to ""', () => {
    expect(normalizeRejectionMessage('  looks unsafe  ')).toBe('looks unsafe')
    expect(normalizeRejectionMessage(CONTROL_ANSWER_MESSAGE.RejectedByUser)).toBe('')
    expect(normalizeRejectionMessage(`  ${CONTROL_ANSWER_MESSAGE.RejectedByUser}  `)).toBe('')
    expect(normalizeRejectionMessage('   ')).toBe('')
  })
})

describe('decodeControlBehaviorEnvelope', () => {
  it('decodes an allow envelope with the request id', () => {
    expect(decodeControlBehaviorEnvelope({
      response: {
        request_id: ' r ',
        response: { behavior: ' allow ' },
      },
    }))
      .toEqual({ requestId: 'r', behavior: 'allow', message: '' })
  })

  it('decodes a deny envelope with a typed reason', () => {
    expect(decodeControlBehaviorEnvelope({
      response: {
        request_id: 'r',
        response: { behavior: 'deny', message: '  not this way  ' },
      },
    }))
      .toEqual({ requestId: 'r', behavior: 'deny', message: 'not this way' })
  })

  it('collapses the sentinel message of a bare deny', () => {
    expect(decodeControlBehaviorEnvelope({
      response: {
        response: { behavior: 'deny', message: CONTROL_ANSWER_MESSAGE.RejectedByUser },
      },
    }))
      .toEqual({ requestId: '', behavior: 'deny', message: '' })
  })

  it('returns null for a non-envelope (e.g. a JSON-RPC decision) or an unrecognized behavior', () => {
    expect(decodeControlBehaviorEnvelope({
      result: { decision: 'accept' },
    })).toBeNull()
    expect(decodeControlBehaviorEnvelope({
      response: {
        response: { behavior: 'maybe' },
      },
    })).toBeNull()
    expect(decodeControlBehaviorEnvelope('nope')).toBeNull()
  })
})
