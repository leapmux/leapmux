import { describe, expect, it } from 'vitest'
import { MIMO_EVENT } from '~/generated/contracts/mimo-protocol'
import { compactionFrame, errorFrame, mimoFrame, statusFrame, TEST_SESSION, toolFrame } from '~/test-support/mimoFixtures'
import { mimoCompactionEnded, mimoErrorText, mimoNotificationEntry } from './notification'

describe('mimoNotificationEntry', () => {
  it('reads a retry of the same turn', () => {
    expect(mimoNotificationEntry(statusFrame('retry', { attempt: 2, message: 'overloaded', next: 1 }))).toEqual([
      { kind: 'retry', scope: 'api', attempt: 2, error: 'overloaded' },
    ])
  })

  it('reads the two ends of a compaction', () => {
    expect(mimoNotificationEntry(compactionFrame(false))).toEqual([{ kind: 'compaction', phase: 'start', detail: { trigger: 'manual' } }])
    expect(mimoNotificationEntry(compactionFrame(true, true))).toEqual([{ kind: 'compaction', phase: 'end', detail: { trigger: 'auto' } }])
  })

  it('reads an error outside a turn', () => {
    expect(mimoNotificationEntry(errorFrame('ProviderModelNotFoundError', 'no such model'))).toEqual([
      { kind: 'text', text: 'MiMo reported an error: ProviderModelNotFoundError: no such model' },
    ])
  })

  it('states nothing for any other row', () => {
    expect(mimoNotificationEntry(statusFrame('idle'))).toEqual([])
    expect(mimoNotificationEntry(toolFrame('bash', {}))).toEqual([])
    expect(mimoNotificationEntry({ type: 'session.error', properties: { error: {} } })).toEqual([])
    expect(mimoNotificationEntry({ content: 'hi' })).toEqual([])
  })

  // The first retry states its attempt, and a later release may drop the message. The
  // entry states only what the frame gives.
  it('reads a retry that states no attempt and no message', () => {
    expect(mimoNotificationEntry(statusFrame('retry'))).toEqual([{ kind: 'retry', scope: 'api' }])
    expect(mimoNotificationEntry(statusFrame('retry', { attempt: '2', message: '' }))).toEqual([{ kind: 'retry', scope: 'api' }])
  })

  it('reads the attempt zero, and not as an absent attempt', () => {
    expect(mimoNotificationEntry(statusFrame('retry', { attempt: 0 }))).toEqual([{ kind: 'retry', scope: 'api', attempt: 0 }])
  })

  it('states nothing for a status event with no status', () => {
    expect(mimoNotificationEntry(mimoFrame(MIMO_EVENT.SessionStatus, { sessionID: TEST_SESSION }))).toEqual([])
  })

  // MiMo writes the projection once the compaction ends. A null projection is the
  // start of one, as an absent projection is.
  it('reads a compaction with a null projection as its start', () => {
    const frame = compactionFrame(false)
    const part = (frame.properties as { part: Record<string, unknown> }).part
    expect(mimoNotificationEntry({ ...frame, properties: { ...frame.properties as object, part: { ...part, projection: null } } }))
      .toEqual([{ kind: 'compaction', phase: 'start', detail: { trigger: 'manual' } }])
  })

  it('reads only a true auto flag as an automatic compaction', () => {
    const frame = compactionFrame(true)
    const part = (frame.properties as { part: Record<string, unknown> }).part
    expect(mimoNotificationEntry({ ...frame, properties: { ...frame.properties as object, part: { ...part, auto: 'true' } } }))
      .toEqual([{ kind: 'compaction', phase: 'end', detail: { trigger: 'manual' } }])
  })

  it('states nothing for a part update with no part', () => {
    expect(mimoNotificationEntry(mimoFrame(MIMO_EVENT.MessagePartUpdated, { sessionID: TEST_SESSION }))).toEqual([])
  })

  it('reads an error that states only its name, or only its message', () => {
    expect(mimoNotificationEntry(errorFrame('ProviderAuthError', ''))).toEqual([{ kind: 'text', text: 'MiMo reported an error: ProviderAuthError' }])
    expect(mimoNotificationEntry(errorFrame('', 'no credentials'))).toEqual([{ kind: 'text', text: 'MiMo reported an error: no credentials' }])
    expect(mimoNotificationEntry(mimoFrame(MIMO_EVENT.SessionError, { sessionID: TEST_SESSION, error: { name: 'UnknownError' } })))
      .toEqual([{ kind: 'text', text: 'MiMo reported an error: UnknownError' }])
  })
})

describe('mimoErrorText', () => {
  it('reads the name and the message of the error', () => {
    expect(mimoErrorText({ error: { name: 'APIError', data: { message: 'rate limited' } } })).toEqual({ name: 'APIError', message: 'rate limited' })
  })

  it.each([
    ['no error', {}],
    ['an error that is text', { error: 'boom' }],
    ['an error with no data', { error: {} }],
    ['a message that is not text', { error: { data: { message: 42 } } }],
  ])('reads empty words from %s', (_name, properties) => {
    expect(mimoErrorText(properties)).toEqual({ name: '', message: '' })
  })
})

describe('mimoCompactionEnded', () => {
  it('reads a projection as the end and an absent or null one as not', () => {
    expect(mimoCompactionEnded({ projection: { summary: 'x' } })).toBe(true)
    // An empty projection still states that the compaction ended.
    expect(mimoCompactionEnded({ projection: '' })).toBe(true)
    expect(mimoCompactionEnded({ projection: null })).toBe(false)
    expect(mimoCompactionEnded({})).toBe(false)
  })
})
