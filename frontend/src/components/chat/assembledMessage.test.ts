import { describe, expect, it } from 'vitest'
import { assembledMessageDisplayText, parseAssembledMessage, parseProviderMessageCompletion } from './assembledMessage'

describe('assembled message', () => {
  it('parses completed reasoning', () => {
    expect(parseAssembledMessage({
      type: 'assembled_message',
      kind: 'reasoning',
      text: 'Reasoned answer',
      completion: 'complete',
    })).toEqual({ kind: 'reasoning', text: 'Reasoned answer', completion: 'complete' })
  })

  it('adds a durable interruption marker', () => {
    const message = parseAssembledMessage({
      type: 'assembled_message',
      kind: 'text',
      text: 'Partial answer',
      completion: 'interrupted',
    })!
    expect(assembledMessageDisplayText(message)).toBe('Partial answer\n\nText truncated by interruption.')
  })

  it('reads completion metadata from a provider message', () => {
    expect(parseProviderMessageCompletion({
      sessionUpdate: 'tool_call_update',
      _leapmux: { completion: 'error' },
    })).toBe('error')
    expect(parseProviderMessageCompletion({ _leapmux: { completion: 'unknown' } })).toBeNull()
  })
})
