import type { MockModelRequestRecord } from '../helpers/mockModelScript'
import { describe, expect, it } from 'vitest'
import { kimiModelTurns } from './modelTurns'

function chatRequest(messages: Array<{ role: string, content: string }>): MockModelRequestRecord {
  return { protocol: 'openai-chat-completions', body: { messages } } as unknown as MockModelRequestRecord
}

describe('kimiModelTurns', () => {
  it('classifies the date-change reminder row as context', () => {
    const turns = kimiModelTurns(chatRequest([
      { role: 'user', content: 'Reply once for the probe.\n\n<br />' },
      { role: 'user', content: '<system-reminder>\nToday\'s date is 2026-10-10. The current date is restated in a reminder whenever it changes.\n</system-reminder>' },
    ]))
    expect(turns).toEqual([{ role: 'user', text: 'Reply once for the probe.\n\n<br />' }])
  })

  it('keeps a user row that holds a reminder beside its own text', () => {
    const turns = kimiModelTurns(chatRequest([
      { role: 'user', content: '<system-reminder>\nToday\'s date is 2026-10-10.\n</system-reminder>\nAnswer with the date.' },
    ]))
    expect(turns).toEqual([{ role: 'user', text: expect.stringContaining('Answer with the date.') }])
  })

  it('keeps the assistant turns of the generic reader', () => {
    const turns = kimiModelTurns(chatRequest([
      { role: 'user', content: 'First prompt.' },
      { role: 'assistant', content: 'First answer.' },
      { role: 'user', content: '<system-reminder>\nToday\'s date is 2026-10-10.\n</system-reminder>' },
      { role: 'user', content: 'Second prompt.' },
    ]))
    expect(turns).toEqual([
      { role: 'user', text: 'First prompt.' },
      { role: 'assistant', text: 'First answer.' },
      { role: 'user', text: 'Second prompt.' },
    ])
  })
})
