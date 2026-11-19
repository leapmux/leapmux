import { describe, expect, it, vi } from 'vitest'
import { createControlAnswerState } from '../../controls/types'
import { openCodeQuestionHandling, openCodeQuestions } from './askUserQuestion'

describe('openCodeQuestionHandling', () => {
  it('sends native field keys as own properties', async () => {
    const payload = {
      type: 'form.created',
      data: {
        form: {
          metadata: { kind: 'question' },
          fields: [
            { key: '__proto__', type: 'string', title: 'Choice' },
          ],
        },
      },
    }
    const state = createControlAnswerState()
    state.setSelections({
      0: ['selected'],
    })
    const send = vi.fn().mockResolvedValue(undefined)
    await openCodeQuestionHandling.sendAnswer({ requestId: 'native-form', agentId: 'agent', payload }, send, openCodeQuestions(payload), state)
    const response = JSON.parse(new TextDecoder().decode(send.mock.calls[0]?.[0]))
    expect(response.result.answer).toEqual(Object.fromEntries([
      ['__proto__', 'selected'],
    ]))
  })
})
