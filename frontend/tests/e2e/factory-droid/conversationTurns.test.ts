import type { MockModelRequestRecord } from '../helpers/mockModelScript'
import { describe, expect, it } from 'vitest'
import { droidConversationTurns } from './conversationTurns'

const diskReminder = '<system-reminder>Disk space is low on this computer. Directories checked: ["/private/test"]. The disk is 91% full.\nChoose another computer.</system-reminder>'

function request(messages: unknown[]): MockModelRequestRecord {
  return {
    protocol: 'openai-chat-completions',
    path: '/v1/chat/completions',
    body: { messages },
  }
}

describe('droidConversationTurns', () => {
  it('reads the human prompt before the native disk reminder', () => {
    const prior = { role: 'assistant', content: 'Before the current turn' }
    const prompt = { role: 'user', content: 'The exact human prompt' }
    const reminder = { role: 'user', content: diskReminder }
    expect(droidConversationTurns(request([prior, prompt, reminder]))).toEqual([
      { role: 'assistant', text: prior.content },
      { role: 'user', text: prompt.content },
    ])
  })

  it('retains human text containing the reminder', () => {
    const prompt = `Explain this warning: ${diskReminder}`
    expect(droidConversationTurns(request([
      { role: 'user', content: prompt },
    ]))).toEqual([
      { role: 'user', text: prompt },
    ])
  })

  it('retains the same text in a structured user message', () => {
    const messages = [
      {
        role: 'user',
        content: [
          { type: 'text', text: diskReminder },
        ],
      },
    ]
    expect(droidConversationTurns(request(messages))).toEqual([
      { role: 'user', text: diskReminder },
    ])
  })

  it('retains unrelated reminder-like messages and assistant output', () => {
    const prompt = '<system-reminder>Review this example.</system-reminder>'
    expect(droidConversationTurns(request([
      { role: 'user', content: prompt },
      { role: 'assistant', content: diskReminder },
    ]))).toEqual([
      { role: 'user', text: prompt },
      { role: 'assistant', text: diskReminder },
    ])
  })
})
