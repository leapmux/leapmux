import type { PersistedControlResponse } from '../../persistedControlResponse'
import { describe, expect, it } from 'vitest'
import { zcodeControlResponseDisplay } from './controlResponse'

function response(result: Record<string, unknown>, params: Record<string, unknown> = {}, method = 'interaction/requestUserInput'): PersistedControlResponse {
  return { requestId: 'request-1', claimToken: 'claim-1', request: { id: 7, method, params }, response: { id: 7, result } }
}

function questionRequest(...questions: string[]): Record<string, unknown> {
  return { input: { questions: questions.map(question => ({ question })) } }
}

const plan = { schema: { interaction: 'plan_approval' } }

describe('zcodeControlResponseDisplay', () => {
  it.each([
    ['allow', 'Allow'],
    ['deny', 'Deny'],
    ['escalate', 'Escalated'],
    ['modify', 'Modified'],
  ])('renders the native permission decision %s', (decision, text) => {
    expect(zcodeControlResponseDisplay(response({ decision }, {}, 'interaction/requestPermission'))).toEqual({ kind: 'label', text })
  })

  it('preserves a native permission rejection reason without attributing it to the user', () => {
    expect(zcodeControlResponseDisplay(response({ decision: 'deny', reason: 'No offered option allows this operation.' }, {}, 'interaction/requestPermission')))
      .toEqual({ kind: 'label', text: 'Deny\nNo offered option allows this operation.' })
  })

  it('renders native answers in the complete request order', () => {
    expect(zcodeControlResponseDisplay(response({ action: 'accept', content: { answers: { Second: 'b', First: 'a' }, answer_0: 'wrong', answer_1: 'wrong' } }, questionRequest('First', 'Second'))))
      .toEqual({ kind: 'label', text: 'First: a\nSecond: b' })
  })

  it('recovers question labels from native request display fields', () => {
    expect(zcodeControlResponseDisplay(response({ action: 'accept', content: { answer: 'Postgres' } }, { questions: [{ question: 'Database?' }] })))
      .toEqual({ kind: 'label', text: 'Database?: Postgres' })
  })

  it('uses positional answers when keyed answers are absent', () => {
    expect(zcodeControlResponseDisplay(response({ action: 'accept', content: { answer_0: 'a', answer_1: 'b', answer: 'ignored' } }, questionRequest('First', 'Second'))))
      .toEqual({ kind: 'label', text: 'First: a\nSecond: b' })
  })

  it('keeps the final native value when question text repeats', () => {
    expect(zcodeControlResponseDisplay(response({ action: 'accept', content: { answer_0: 'first', answer_1: 'last' } }, questionRequest('Repeated', 'Repeated'))))
      .toEqual({ kind: 'label', text: 'Repeated: last' })
  })

  it('uses a single answer only for one question', () => {
    expect(zcodeControlResponseDisplay(response({ action: 'accept', content: { answer: 'a' } }, questionRequest('One'))))
      .toEqual({ kind: 'label', text: 'One: a' })
    expect(zcodeControlResponseDisplay(response({ action: 'accept', content: { answer: 'a' } }, questionRequest('One', 'Two')))).toBeNull()
  })

  it('normalizes strings and string arrays as the native consumer does', () => {
    expect(zcodeControlResponseDisplay(response({ action: 'accept', content: { answer_0: '  value  ', answer_1: [' a ', false, '', ' b '] } }, questionRequest('One', 'Two'))))
      .toEqual({ kind: 'label', text: 'One: value\nTwo: a, b' })
  })

  it('does not show answers that the native consumer discards', () => {
    expect(zcodeControlResponseDisplay(response({ action: 'accept', content: { answers: {}, answer_0: 'ignored' } }, questionRequest('One')))).toBeNull()
    expect(zcodeControlResponseDisplay(response({ action: 'accept', content: { answers: { Unrelated: 'ignored' } } }, questionRequest('One')))).toBeNull()
    expect(zcodeControlResponseDisplay(response({ action: 'accept', content: { answer_0: 0, answer_1: false, answer_2: ' ' } }, questionRequest('One', 'Two', 'Three')))).toBeNull()
  })

  it.each([
    ['decline', 'Reject'],
    ['cancel', 'Cancel'],
  ])('renders %s without claiming that its ignored reason reached the model', (action, text) => {
    expect(zcodeControlResponseDisplay(response({ action, reason: 'The native mapper drops this field.' }, questionRequest('One'))))
      .toEqual({ kind: 'label', text })
  })

  it.each([
    { answer: 'approve' },
    { answer_0: 'approve' },
    { answers: { 'Review this implementation plan.': 'approve' } },
  ])('recognizes the native plan approval answer %j', (content) => {
    expect(zcodeControlResponseDisplay(response({ action: 'accept', content }, plan))).toEqual({ kind: 'label', text: 'Approve' })
  })

  it('preserves native plan feedback and refuses to infer approval from an empty accept', () => {
    expect(zcodeControlResponseDisplay(response({ action: 'accept', content: { answer: '  Add tests.  ' } }, plan)))
      .toEqual({ kind: 'feedback', message: 'Add tests.' })
    expect(zcodeControlResponseDisplay(response({ action: 'accept', content: {} }, plan))).toEqual({ kind: 'label', text: 'Reject' })
    expect(zcodeControlResponseDisplay(response({ action: 'accept', content: { answers: { 'Review this implementation plan.': '' }, answer_0: 'approve' } }, plan)))
      .toEqual({ kind: 'label', text: 'Reject' })
  })

  it('returns no derived label for an unknown or corrupt native response', () => {
    expect(zcodeControlResponseDisplay(response({ action: 'unknown' }))).toBeNull()
    expect(zcodeControlResponseDisplay(response({ decision: 'unknown' }, {}, 'interaction/requestPermission'))).toBeNull()
    expect(zcodeControlResponseDisplay({ ...response({ action: 'accept' }), response: undefined })).toBeNull()
    expect(zcodeControlResponseDisplay({ ...response({ action: 'accept' }), request: undefined })).toBeNull()
    expect(zcodeControlResponseDisplay(response({ action: 'accept', content: { answer: 'ignored' } }, { questions: [null, 0, {}] }))).toBeNull()
  })
})
