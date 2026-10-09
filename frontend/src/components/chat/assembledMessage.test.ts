import { describe, expect, it } from 'vitest'
import { MessageCompletion } from '~/generated/proto/leapmux/v1/agent_pb'
import { appendCompletionMarker, assembledMessageDisplayText, messageCompletionFromProto, parseAssembledMessage } from './assembledMessage'

describe('assembled message', () => {
  it.each(['text', 'reasoning', 'plan'])('keeps final %s without a known outcome', (kind) => {
    const message = parseAssembledMessage({ type: 'assembled_message', kind, text: 'Native text', completion: 'finished' })
    expect(message).toEqual({ kind, text: 'Native text', completion: 'finished' })
    expect(messageCompletionFromProto(MessageCompletion.FINISHED)).toBe('finished')
    expect(message && assembledMessageDisplayText(message)).toBe('Native text\n\nText ended without a known outcome.')
  })

  it.each(['', ' \n '])('keeps the exact text before an unknown-outcome marker: %s', (text) => {
    const message = parseAssembledMessage({ type: 'assembled_message', kind: 'text', text, completion: 'finished' })
    expect(message).not.toBeNull()
    expect(message && assembledMessageDisplayText(message)).toBe(text ? `${text}\n\nText ended without a known outcome.` : 'Text ended without a known outcome.')
  })

  it('keeps invalid completion values absent', () => {
    expect(messageCompletionFromProto(5 as MessageCompletion)).toBeNull()
    expect(messageCompletionFromProto(-1 as MessageCompletion)).toBeNull()
    expect(parseAssembledMessage({ type: 'assembled_message', kind: 'text', text: '', completion: 'future' })).toBeNull()
    expect(appendCompletionMarker('Native text', null)).toBe('Native text')
  })

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

  it('reads only typed message completion', () => {
    expect(messageCompletionFromProto(MessageCompletion.ERROR)).toBe('error')
    expect(messageCompletionFromProto(MessageCompletion.INTERRUPTED)).toBe('interrupted')
    expect(messageCompletionFromProto(MessageCompletion.COMPLETE)).toBe('complete')
    expect(messageCompletionFromProto(MessageCompletion.UNSPECIFIED)).toBeNull()
    expect(messageCompletionFromProto(undefined)).toBeNull()
  })
})
