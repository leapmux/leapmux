import { describe, expect, it } from 'vitest'
import { KIMI_EVENT } from '~/generated/contracts/kimi-protocol'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { kimiConversationItemRecord, kimiFrame, kimiTranscriptMarkerFrame } from '~/test-support/kimiFixtures'
import { input } from '../../testUtils'
import { kimiCompactionBoundary, kimiNotificationEntry } from './notification'

describe('kimiNotificationEntry', () => {
  it.each([
    [
      { kind: 'goal' },
      'Continuing the goal',
    ],
    [
      { kind: 'retry' },
      'Retrying the turn',
    ],
    [
      {
        kind: 'cron',
        job_id: 'nightly',
        schedule: '0 9 * * *',
        recurring: true,
        coalesced_count: 0,
        stale: false,
      },
      'Scheduled prompt nightly started a turn',
    ],
    [
      {
        kind: 'cron',
        job_id: 'nightly',
        schedule: '0 9 * * *',
        recurring: true,
        coalesced_count: 1,
        stale: true,
      },
      'Missed scheduled prompt nightly ran late',
    ],
    [
      { kind: 'hook', hook_event: 'SessionStart' },
      'Hook SessionStart started a turn',
    ],
    [
      { kind: 'skill', name: 'Review' },
      'Skill Review started a turn',
    ],
    [
      { kind: 'plugin_command', plugin_id: 'review', command: 'check' },
      'Plugin command review/check started a turn',
    ],
  ])('explains a retained native opener from %j', (origin, expected) => {
    const record = kimiConversationItemRecord({
      type: 'user_message',
      status: 'ended',
      outcome: 'completed',
      prompt_ids: [],
      placement: 'opener',
      origin,
      text: 'Prompt bytes must stay out of the notice',
      attachments: [],
      skills: [],
    })
    expect(kimiNotificationEntry({ ...record, role: 'origin' })).toStrictEqual([
      { kind: 'text', text: expected },
    ])
    expect(kimiNotificationEntry(record)).toStrictEqual([])
    expect(kimiNotificationEntry({
      ...record,
      role: 'origin',
      item: { ...record.item, placement: 'steer' },
    })).toStrictEqual([])
  })

  it.each([
    ['info', 'Child finished: Native child result'],
    ['warning', 'Warning: Child finished: Native child result'],
    ['error', 'Error: Child finished: Native child result'],
  ])('draws a durable native %s notification item', (severity, expected) => {
    expect(kimiNotificationEntry({
      type: 'leapmux.kimi.item',
      session_id: 'native',
      agent_id: 'main',
      turn_id: 'notification-turn',
      role: 'item',
      item: {
        type: 'notification',
        item_id: 'notice-1',
        status: 'ended',
        outcome: 'completed',
        created_at: '2026-11-18T00:00:00Z',
        notification_id: 'native-notice',
        notification_type: 'task.completed',
        source: { kind: 'background_task', id: 'task-1' },
        task_id: 'task-1',
        title: 'Child finished',
        body: 'Native child result',
        severity,
      },
    })).toEqual([
      { kind: 'text', text: expected },
    ])
  })

  it('reads the current native retry attempt without losing a zero delay', () => {
    expect(kimiNotificationEntry({
      type: 'leapmux.kimi.retry',
      session_id: 'native',
      agent_id: 'main',
      turn_id: 'retry-turn',
      step_index: 1,
      retry: {
        attempt: 1,
        next_attempt: 2,
        max_attempts: 10,
        delay_ms: 0,
        error: { name: 'status', message: 'The stream failed.' },
      },
    })).toStrictEqual([
      {
        kind: 'retry',
        scope: 'api',
        attempt: 2,
        maxAttempts: 10,
        delayMs: 0,
        error: 'The stream failed.',
      },
    ])
  })

  it.each([123.5, 0])('reads current compaction boundaries with %s input tokens', (pre) => {
    const record = {
      type: 'leapmux.kimi.item',
      session_id: 'native',
      agent_id: 'main',
      turn_id: 'compaction-turn',
      role: 'item',
      item: {
        type: 'system',
        item_id: 'compaction-item',
        status: 'ended',
        outcome: 'completed',
        created_at: '2026-11-18T00:00:00Z',
        kind: 'compaction',
        trigger: 'manual',
        tokens_before: pre,
        tokens_after: 0,
        compacted_count: 0,
        summary: 'Native summary',
      },
    }
    const detail = { trigger: 'manual', pre, post: 0 }
    expect(kimiNotificationEntry(record)).toStrictEqual([
      { kind: 'compaction', phase: 'end', detail },
    ])
    expect(kimiCompactionBoundary(input(record, undefined, AgentProvider.KIMI_CODE))).toStrictEqual(detail)
  })

  it.each([
    ['info', 'Hook UserPromptSubmit blocked: Native reason 🙂'],
    ['warning', 'Warning: Hook UserPromptSubmit blocked: Native reason 🙂'],
    ['error', 'Error (HOOK_BLOCKED): Hook UserPromptSubmit blocked: Native reason 🙂'],
  ])('reads current %s notices with their native title and detail', (level, text) => {
    expect(kimiNotificationEntry({
      type: 'leapmux.kimi.notice',
      session_id: 'native',
      agent_id: 'main',
      event_created_at: '2026-11-18T00:00:00Z',
      notice: {
        level,
        title: 'Hook UserPromptSubmit blocked',
        detail: 'Native reason 🙂',
        code: 'HOOK_BLOCKED',
      },
    })).toStrictEqual([
      { kind: 'text', text },
    ])
  })

  it('reads each config warning as a warning line', () => {
    expect(kimiNotificationEntry(kimiFrame(KIMI_EVENT.ConfigWarning, {
      warnings: [
        { domain: 'loopControl', message: 'First warning.' },
        { domain: 'models', message: 'Second warning.' },
      ],
    }))).toStrictEqual([
      { kind: 'text', text: 'Warning: First warning.' },
      { kind: 'text', text: 'Warning: Second warning.' },
    ])
  })

  it('ignores malformed or blank config warnings', () => {
    for (const warnings of [
      undefined,
      null,
      {},
      [
        null,
        false,
        7,
        { message: 7 },
        { message: ' ' },
      ],
    ])
      expect(kimiNotificationEntry(kimiFrame(KIMI_EVENT.ConfigWarning, { warnings }))).toStrictEqual([])
  })

  it.each([
    [
      'error',
      'Model unavailable',
      'overloaded',
      'Error (overloaded): Model unavailable',
    ],
    [
      'warning',
      'Context almost full',
      '',
      'Warning: Context almost full',
    ],
    [
      'info',
      'Task finished',
      '',
      'Task finished',
    ],
    ['error', '', '', ''],
    [
      'error',
      '',
      'provider.rate_limited',
      'Error: provider.rate_limited',
    ],
  ])('reads a public %s notice with message %s', (level, title, code, text) => {
    const notice = {
      type: 'leapmux.kimi.notice',
      session_id: 'session_1',
      agent_id: 'main',
      event_created_at: '2026-11-18T00:00:00Z',
      notice: { level, title, code },
    }
    expect(kimiNotificationEntry(notice)).toStrictEqual(text
      ? [
          { kind: 'text', text },
        ]
      : [])
  })

  it.each([
    ['Done', '', 'Done'],
    ['', 'It ended.', 'It ended.'],
    ['Finished', 'The task passed.', 'Finished: The task passed.'],
    ['', ' ', ''],
  ])('words a public task notice from the title %s and body %s', (title, body, text) => {
    const record = kimiConversationItemRecord({
      type: 'notification',
      status: 'ended',
      outcome: 'completed',
      notification_id: 'native-task-notice',
      notification_type: 'task.completed',
      source: { kind: 'background_task', id: 'task-1' },
      task_id: 'task-1',
      title,
      body,
      severity: 'info',
    })
    expect(kimiNotificationEntry(record)).toStrictEqual(text
      ? [
          { kind: 'text', text },
        ]
      : [])
  })

  it('retains a fractional retry delay and declines invalid counters', () => {
    const retry = {
      attempt: 1,
      next_attempt: 2,
      max_attempts: 5,
      delay_ms: 0.5,
      error: { name: 'RateLimit', message: ' ' },
    }
    const record = {
      type: 'leapmux.kimi.retry',
      session_id: 'session_1',
      agent_id: 'main',
      turn_id: 'turn-1',
      step_index: 1,
      retry,
    }
    expect(kimiNotificationEntry(record)).toStrictEqual([
      {
        kind: 'retry',
        scope: 'api',
        attempt: 2,
        maxAttempts: 5,
        delayMs: 0.5,
        error: 'RateLimit',
      },
    ])
    for (const next_attempt of [
      -1,
      0,
      1.5,
      Infinity,
      NaN,
      Number.MAX_SAFE_INTEGER + 1,
    ]) {
      expect(kimiNotificationEntry({
        ...record,
        retry: { ...retry, next_attempt },
      })).toStrictEqual([])
    }
    expect(kimiNotificationEntry({
      ...record,
      retry: { ...retry, delay_ms: Infinity },
    })).toStrictEqual([])
    expect(kimiNotificationEntry({ ...record, retry: {} })).toStrictEqual([])
  })

  it('ignores obsolete markers that no longer arrive in the conversation feed', () => {
    const old = kimiTranscriptMarkerFrame('notice', { level: 'warning', message: 'An obsolete notice' })
    expect(kimiNotificationEntry(old)).toStrictEqual([])
  })
})

describe('kimiCompactionBoundary', () => {
  const compaction = { type: 'system', kind: 'compaction', trigger: 'auto' }

  it('reads the size a completed compaction left', () => {
    const record = kimiConversationItemRecord({
      ...compaction,
      status: 'ended',
      outcome: 'completed',
      tokens_before: 9000,
      tokens_after: 1200,
    })
    expect(kimiCompactionBoundary(input(record, undefined, AgentProvider.KIMI_CODE))).toStrictEqual({ trigger: 'auto', pre: 9000, post: 1200 })
    const running = kimiConversationItemRecord(compaction)
    expect(kimiCompactionBoundary(input(running, undefined, AgentProvider.KIMI_CODE))).toBeNull()
  })

  it('reads a compaction that states no sizes as a boundary with no sizes', () => {
    const record = kimiConversationItemRecord({ ...compaction, status: 'ended', outcome: 'completed' })
    expect(kimiCompactionBoundary(input(record, undefined, AgentProvider.KIMI_CODE))).toStrictEqual({ trigger: 'auto' })
  })

  it('reads no boundary from a neutral row or a legacy compaction envelope', () => {
    expect(kimiCompactionBoundary(input({ content: 'Hello.' }, undefined, AgentProvider.KIMI_CODE))).toBeNull()
    const old = kimiTranscriptMarkerFrame('compaction', { phase: 'completed' })
    expect(kimiCompactionBoundary(input(old, undefined, AgentProvider.KIMI_CODE))).toBeNull()
  })
})
