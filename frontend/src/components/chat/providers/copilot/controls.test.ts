import { describe, expect, it } from 'vitest'
import { COPILOT_EVENT } from '~/generated/contracts/copilot-protocol'
import { copilotQuestions } from './controls'

describe('copilotQuestions', () => {
  it('reads the question and its choices, and accepts an empty answer', () => {
    const payload = {
      jsonrpc: '2.0',
      method: 'session.event',
      params: { sessionId: 'session-1', event: { id: 'event-1', type: COPILOT_EVENT.UserInputRequested, data: { requestId: 'native-1', question: 'Which one?', choices: ['A', 'B'], allowFreeform: true } } },
    }
    expect(copilotQuestions(payload)).toEqual([{
      question: 'Which one?',
      options: [{ label: 'A' }, { label: 'B' }],
      allowEmpty: true,
    }])
  })
})
