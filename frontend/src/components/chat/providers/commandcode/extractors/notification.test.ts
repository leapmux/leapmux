import { describe, expect, it } from 'vitest'
import { commandCodeCompactionBoundary, commandCodeNotificationEntry } from './notification'

function event(type: string, fields: Record<string, unknown>) {
  return {
    type: 'event',
    event: { type, ...fields },
  }
}

describe('commandCodeNotificationEntry', () => {
  it('does not call a finished manual attempt a successful compaction', () => {
    expect(commandCodeNotificationEntry(event('compaction_done', { trigger: 'manual', tokensSaved: 0 }))).toEqual([])
    expect(commandCodeNotificationEntry(event('compaction_outcome', { trigger: 'manual', outcome: 'failed' }))).toEqual([
      { kind: 'compaction', phase: 'end', outcome: 'failed' },
    ])
    expect(commandCodeNotificationEntry(event('compaction_outcome', { trigger: 'manual', outcome: 'too-small' }))).toEqual([
      { kind: 'compaction', phase: 'end', outcome: 'skipped' },
    ])
  })

  it('reads the successful native manual outcome even when the token savings equal zero', () => {
    expect(commandCodeNotificationEntry(event('compaction_outcome', {
      trigger: 'manual',
      outcome: 'summarized',
      tokensBefore: 1000,
      tokensAfter: 1000,
    }))).toEqual([
      {
        kind: 'compaction',
        phase: 'end',
        outcome: 'completed',
        detail: { trigger: 'manual', pre: 1000, post: 1000 },
      },
    ])
  })

  it('ignores consumed done frames for both automatic and manual compaction', () => {
    expect(commandCodeNotificationEntry(event('compaction_done', { tokensSaved: 101 }))).toEqual([])
    expect(commandCodeNotificationEntry(event('compaction_done', { trigger: 'manual', tokensSaved: 101 }))).toEqual([])
  })
})

describe('native notification facts', () => {
  it('reads an automatic summarized outcome with sizes', () => {
    const frame = event('compaction_outcome', {
      trigger: 'overflow',
      outcome: 'summarized',
      tokensBefore: 60000,
      tokensAfter: 30000,
    })
    expect(commandCodeNotificationEntry(frame)).toEqual([
      {
        kind: 'compaction',
        phase: 'end',
        outcome: 'completed',
        detail: { trigger: 'overflow', pre: 60000, post: 30000 },
      },
    ])
    expect(commandCodeCompactionBoundary({
      parentObject: frame,
      rawText: '',
      topLevel: frame,
      wrapper: null,
    })).toEqual({ trigger: 'overflow', pre: 60000, post: 30000 })
  })

  it('validates compaction counts and keeps zero', () => {
    expect(commandCodeNotificationEntry(event('compaction_outcome', { outcome: 'summarized', tokensBefore: -1, tokensAfter: 0 }))).toEqual([
      {
        kind: 'compaction',
        phase: 'end',
        outcome: 'completed',
        detail: { trigger: 'auto', post: 0 },
      },
    ])
    expect(commandCodeNotificationEntry(event('compaction_outcome', { outcome: 'summarized', tokensBefore: Infinity, tokensAfter: NaN }))).toEqual([
      {
        kind: 'compaction',
        phase: 'end',
        outcome: 'completed',
        detail: { trigger: 'auto' },
      },
    ])
  })

  it('builds stream_restart through retryEntry', () => {
    expect(commandCodeNotificationEntry(event('stream_restart', { attempt: 2, maxAttempts: 3 }))).toEqual([
      {
        kind: 'retry',
        scope: 'api',
        attempt: 2,
        maxAttempts: 3,
        error: 'The connection dropped mid-response',
      },
    ])
    expect(commandCodeNotificationEntry(event('api_retry', { attempt: -1, delayMs: Infinity, error: '   ' }))).toEqual([
      { kind: 'retry', scope: 'api' },
    ])
  })

  it.each([
    [
      'notice',
      { level: 'warning', message: 'A native warning' },
      { kind: 'text', text: 'Warning: A native warning' },
    ],
    [
      'notice',
      { level: 'info', message: 'A native notice' },
      { kind: 'text', text: 'A native notice' },
    ],
    [
      'skill_loaded',
      { name: 'docs' },
      { kind: 'text', text: 'Loaded skill: docs' },
    ],
    [
      'mod_error',
      { modId: 'hooks', hook: 'beforeToolCall', error: 'failed command' },
      { kind: 'text', text: 'Mod hooks failed in beforeToolCall: failed command' },
    ],
    [
      'continuation_recovery',
      { kind: 'length', attempt: 1, maxAttempts: 3 },
      { kind: 'status', text: 'The model stopped early (token limit). Command Code asks it to continue (1/3)' },
    ],
    [
      'tool_hooks',
      {
        lines: ['first hook', 'second hook'],
      },
      { kind: 'text', text: 'first hook\nsecond hook' },
    ],
  ])('reads the native %s notification', (type, fields, entry) => {
    expect(commandCodeNotificationEntry(event(type, fields))).toEqual([entry])
  })

  it('handles absent and malformed optional fields', () => {
    for (const type of ['notice', 'skill_loaded', 'tool_hooks'])
      expect(commandCodeNotificationEntry(event(type, {}))).toEqual([])
    expect(commandCodeNotificationEntry(event('tool_hooks', {
      lines: [null, {}, '', 'valid'],
    }))).toEqual([
      { kind: 'text', text: 'valid' },
    ])
    expect(commandCodeNotificationEntry(event('mod_error', {}))).toEqual([
      { kind: 'text', text: 'A mod failed' },
    ])
    expect(commandCodeNotificationEntry(event('continuation_recovery', { attempt: -1, maxAttempts: 1.5 }))).toEqual([
      { kind: 'status', text: 'The model stopped early. Command Code asks it to continue' },
    ])
    expect(commandCodeNotificationEntry(event('compaction_outcome', { outcome: 'future' }))).toEqual([])
  })
})
