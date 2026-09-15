import { describe, expect, it } from 'vitest'
import { MessageCompletion } from '~/generated/proto/leapmux/v1/agent_pb'
import { assembledMessageDisplayText, messageCompletionFromProto, parseAssembledMessage } from './assembledMessage'

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

  it('reads only typed message completion', () => {
    expect(messageCompletionFromProto(MessageCompletion.ERROR)).toBe('error')
    expect(messageCompletionFromProto(MessageCompletion.INTERRUPTED)).toBe('interrupted')
    expect(messageCompletionFromProto(MessageCompletion.COMPLETE)).toBe('complete')
    expect(messageCompletionFromProto(MessageCompletion.UNSPECIFIED)).toBeNull()
    expect(messageCompletionFromProto(undefined)).toBeNull()
  })
})
