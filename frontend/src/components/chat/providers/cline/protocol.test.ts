import { describe, expect, it } from 'vitest'
import { clineEnvelope, clinePayload } from './protocol'

describe('clineEnvelope', () => {
  it('reads the event and the payload of an envelope', () => {
    expect(clineEnvelope({ version: 'v1', event: 'tool.started', sessionId: 's1', payload: { toolName: 'editor' } }))
      .toEqual({ event: 'tool.started', payload: { toolName: 'editor' } })
  })

  // An event with no payload still states the event, so a reader of the event keeps
  // it, and each payload reader finds no field.
  it('reads an envelope whose payload is absent or not a record as an empty payload', () => {
    for (const payload of [undefined, null, 'text', ['a']])
      expect(clineEnvelope({ event: 'run.completed', payload }), JSON.stringify(payload)).toEqual({ event: 'run.completed', payload: {} })
  })

  it('reads no envelope from a row with no event name', () => {
    for (const message of [{ payload: {} }, { event: '' }, { event: 3 }, 'tool.started', null, undefined, ['tool.started']])
      expect(clineEnvelope(message), JSON.stringify(message)).toBeNull()
  })
})

describe('clinePayload', () => {
  it('reads the payload of the event it asks for alone', () => {
    const row = { event: 'tool.finished', payload: { toolCallId: 'c1' } }
    expect(clinePayload(row, 'tool.finished')).toEqual({ toolCallId: 'c1' })
    expect(clinePayload(row, 'tool.started')).toBeNull()
    expect(clinePayload({ type: 'agent_error' }, 'tool.finished')).toBeNull()
  })
})
