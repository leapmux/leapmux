import { describe, expect, it } from 'vitest'
import { openCodeCompactionStart, openCodeNotificationEntry } from './notification'

describe('openCodeNotificationEntry', () => {
  it('reads native retry time and error without losing a zero delay', () => {
    expect(openCodeNotificationEntry({
      type: 'session.retry.scheduled',
      created: 0,
      data: {
        attempt: 1,
        at: 0,
        error: { type: 'api', message: 'busy' },
      },
    })).toEqual([
      {
        kind: 'retry',
        scope: 'api',
        attempt: 1,
        delayMs: 0,
        error: 'busy',
      },
    ])
  })

  it('distinguishes starts from completed and failed compactions', () => {
    expect(openCodeCompactionStart({ type: 'session.compaction.started' })).toBe(true)
    expect(openCodeCompactionStart({ type: 'compaction', status: 'completed' })).toBe(false)
    expect(openCodeNotificationEntry({
      type: 'compaction',
      status: 'completed',
      reason: 'manual',
      summary: 'saved',
    })).toEqual([
      {
        kind: 'compaction',
        phase: 'end',
        outcome: 'completed',
        detail: { trigger: 'manual' },
      },
    ])
    expect(openCodeNotificationEntry({
      type: 'session.compaction.failed',
      data: {
        reason: 'auto',
        error: { type: 'api', message: 'unavailable' },
      },
    })).toEqual([
      {
        kind: 'compaction',
        phase: 'end',
        outcome: 'failed',
        detail: { trigger: 'auto' },
        error: 'unavailable',
      },
    ])
  })

  it('leaves unrelated or incomplete native records unclassified', () => {
    expect(openCodeNotificationEntry({ type: 'session.retry.scheduled' })).toEqual([])
    expect(openCodeNotificationEntry({ type: 'compaction', status: 'unknown' })).toEqual([])
  })
})
