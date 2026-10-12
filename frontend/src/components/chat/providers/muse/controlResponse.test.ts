import type { PersistedControlResponse } from '../../persistedControlResponse'
import { museControlResponseSummary } from './controlResponse'

function questionRequest(questions: unknown): Record<string, unknown> {
  return { method: 'userInput/requested', params: { sessionId: 'session', userInputId: 'input', questions } }
}

function answered(answers: unknown): PersistedControlResponse {
  return { requestId: 'req-1', claimToken: 'claim', request: questionRequest([{ id: 'q1', question: 'Choose the native style', header: 'Style', options: [{ label: 'Alpha' }, { label: 'Beta' }], selection: { mode: 'single' } }]), response: { jsonrpc: '2.0', id: 1, method: 'userInput/answer', params: { sessionId: 'session', userInputId: 'input', answers } } }
}

describe('museControlResponseSummary', () => {
  it('shows the question and its selected answer', () => {
    expect(museControlResponseSummary(answered([{ questionId: 'q1', selectedLabel: 'Beta' }])))
      .toEqual({ kind: 'label', text: 'Choose the native style: Beta' })
  })

  it('joins a multiple selection and rides its note on the same line', () => {
    expect(museControlResponseSummary(answered([{ questionId: 'q1', selectedLabels: ['Alpha', 'Beta'], note: 'Both fit.' }])))
      .toEqual({ kind: 'label', text: 'Choose the native style: Alpha, Beta, Both fit.' })
  })

  it('shows a typed answer in place of the options', () => {
    expect(museControlResponseSummary(answered([{ questionId: 'q1', freeText: 'A native typed answer' }])))
      .toEqual({ kind: 'label', text: 'Choose the native style: A native typed answer' })
  })

  it('shows one line per answered question in the asked order', () => {
    const cr = answered([{ questionId: 'q2', selectedLabel: 'Second' }, { questionId: 'q1', selectedLabel: 'First' }])
    cr.request = questionRequest([
      { id: 'q1', question: 'First choice', header: 'One', options: [{ label: 'First' }], selection: { mode: 'single' } },
      { id: 'q2', question: 'Second choice', header: 'Two', options: [{ label: 'Second' }], selection: { mode: 'single' } },
    ])
    expect(museControlResponseSummary(cr)).toEqual({ kind: 'label', text: 'First choice: First\nSecond choice: Second' })
  })

  it.each([
    ['an absent result', answered(undefined as unknown as unknown[])],
    ['a non-array result', answered({})],
    ['an unreadable entry', answered([null])],
    ['an answer to a question the request did not ask', answered([{ questionId: 'q9', selectedLabel: 'Beta' }])],
    ['an empty answer', answered([{ questionId: 'q1' }])],
    ['an approval envelope', { requestId: 'r', claimToken: 'c', request: questionRequest([]), response: { jsonrpc: '2.0', id: 2, method: 'approval/decide', params: { sessionId: 'session', approvalId: 'approval', choiceId: 'deny' } } }],
  ])('degrades the neutral label for %s', (_label, cr) => {
    expect(museControlResponseSummary(cr as PersistedControlResponse)).toBeNull()
  })
})
