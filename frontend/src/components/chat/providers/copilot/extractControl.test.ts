import { describe, expect, it } from 'vitest'
import { COPILOT_EVENT } from '~/generated/contracts/copilot-protocol'
import { copilotQuestions } from './extractControl'

function questionPayload(data: Record<string, unknown>) {
  return {
    jsonrpc: '2.0',
    method: 'session.event',
    params: { sessionId: 'session-1', event: { id: 'event-1', type: COPILOT_EVENT.UserInputRequested, data: { requestId: 'native-1', question: 'Which one?', choices: ['A', 'B'], ...data } } },
  }
}

describe('copilotQuestions', () => {
  it('reads the question and its choices, and accepts an empty answer', () => {
    expect(copilotQuestions(questionPayload({ allowFreeform: true }))).toEqual([{
      question: 'Which one?',
      options: [{ label: 'A' }, { label: 'B' }],
      allowEmpty: true,
    }])
  })

  // The empty answer is the shortest answer that is not one of the choices, so a
  // request that refuses one refuses the other. The submit then waits for a choice.
  it.each([
    ['refuses one', { allowFreeform: false }],
    ['states nothing', {}],
  ])('offers no empty answer when the request %s', (_label, data) => {
    expect(copilotQuestions(questionPayload(data))[0]?.allowEmpty).toBe(false)
  })
})
