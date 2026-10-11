import { describe, expect, it } from 'vitest'
import { ohMyPiContentText } from './messageContent'

describe('ohMyPiContentText', () => {
  it('joins the text blocks and leaves out the thinking and the tool calls', () => {
    const frame = { type: 'message_end', message: { role: 'assistant', content: [{ type: 'thinking', thinking: 'Plan.' }, { type: 'text', text: 'One.' }, { type: 'toolCall', id: 'c' }, { type: 'text', text: 'Two.' }] } }
    expect(ohMyPiContentText(frame)).toBe('One.\n\nTwo.')
  })

  it('reads the plain-string content of a custom message', () => {
    expect(ohMyPiContentText({ message: { role: 'custom', content: 'Notice.' } })).toBe('Notice.')
  })

  it('reads the text blocks of a custom message that carries blocks', () => {
    expect(ohMyPiContentText({ message: { role: 'custom', content: [{ type: 'text', text: 'One.' }, { type: 'text', text: 'Two.' }] } })).toBe('One.\n\nTwo.')
  })

  it('reads the plain-string content of a message that is not custom as no text', () => {
    // Only omp's custom message carries a plain string. An assistant message that did
    // would be a shape omp never sends, so it draws nothing rather than a guess.
    expect(ohMyPiContentText({ message: { role: 'assistant', content: 'Hello.' } })).toBe('')
  })

  it('reads no content as the empty string', () => {
    expect(ohMyPiContentText({})).toBe('')
    expect(ohMyPiContentText({ message: { role: 'assistant' } })).toBe('')
    expect(ohMyPiContentText({ message: 'not a record' })).toBe('')
  })
})
