import { describe, expect, it } from 'vitest'
import { KIMI_EVENT, KIMI_ORIGIN } from '~/generated/contracts/kimi-protocol'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { kimiFrame, kimiTranscriptFrame, kimiTranscriptMarkerFrame, kimiTranscriptTurnFrame } from '~/test-support/kimiFixtures'
import { input } from '../../testUtils'
import { kimiCompactionBoundary, kimiNotificationEntry } from './notification'

function nativeError(event: Record<string, unknown> = {}) {
  return kimiTranscriptMarkerFrame('notice', {
    level: 'error',
    message: event.message ?? '',
    event: { agentId: 'main', ...event },
  })
}

function nativeRetry(retry: Record<string, unknown>) {
  return kimiTranscriptFrame({
    op: 'step.upsert',
    turnId: 't0',
    step: {
      kind: 'step',
      stepId: 't0.1',
      turnId: 't0',
      ordinal: 1,
      state: 'running',
      retry,
    },
  })
}

function nativeTaskText(text: string) {
  return kimiTranscriptFrame({
    op: 'frame.upsert',
    turnId: 't0',
    stepId: 't0.1',
    frame: {
      kind: 'text',
      frameId: 't0.1.note',
      role: 'user',
      taskId: 'task_1',
      text,
    },
  })
}

describe('kimiNotificationEntry', () => {
  it('reads native skill activation facts without a new turn', () => {
    expect(kimiNotificationEntry(kimiTranscriptMarkerFrame('skill', {
      agentId: 'main',
      activationId: 'activation-1',
      skillName: 'review',
      trigger: 'model-tool',
      skillArgs: 'staged changes',
      skillSource: 'project',
      skillPath: '/project/.agents/skills/review/SKILL.md',
    }))).toStrictEqual([
      { kind: 'text', text: 'Activated skill review\nArguments: staged changes\nTrigger: model-tool\nSource: project\nPath: /project/.agents/skills/review/SKILL.md' },
    ])
  })

  it('reads native plugin command activation without a skill name', () => {
    expect(kimiNotificationEntry(kimiTranscriptMarkerFrame('skill', {
      agentId: 'main',
      activationId: 'activation-2',
      variant: 'plugin_command',
      pluginId: 'checks',
      commandName: 'review',
      commandArgs: 'staged changes',
      trigger: 'user-slash',
    }))).toStrictEqual([
      { kind: 'text', text: 'Activated plugin command checks/review\nArguments: staged changes\nTrigger: user-slash' },
    ])
  })

  it('reads buffered cron delivery facts and preserves zero and false', () => {
    expect(kimiNotificationEntry(kimiTranscriptMarkerFrame('cron.fired', {
      origin: {
        kind: 'cron_job',
        jobId: 'job-1',
        cron: '*/5 * * * *',
        recurring: false,
        coalescedCount: 0,
        stale: false,
      },
      prompt: 'Check the build.',
    }))).toStrictEqual([
      { kind: 'text', text: 'Scheduled prompt job-1 was delivered\nSchedule: */5 * * * *\nRecurring: no\nCoalesced firings: 0\nStale: no\n\nCheck the build.' },
    ])
  })

  it('rejects invalid skill identities and optional fields', () => {
    const valid = {
      agentId: 'main',
      activationId: 'a1',
      skillName: 'review',
      trigger: 'nested-skill',
    }
    for (const patch of [
      { agentId: 'other' },
      { agentId: undefined },
      { activationId: '' },
      { activationId: 0 },
      { skillName: ' ' },
      { trigger: undefined },
      { skillArgs: null },
      { skillPath: [] },
      { skillSource: false },
      { variant: 'unknown' },
    ]) {
      expect(kimiNotificationEntry(kimiTranscriptMarkerFrame('skill', {
        ...valid,
        ...patch,
      }))).toStrictEqual([])
    }
  })

  it('rejects incomplete or invalid cron delivery facts', () => {
    const origin = {
      kind: 'cron_job',
      jobId: 'job-1',
      cron: '* * * * *',
      recurring: true,
      coalescedCount: 1,
      stale: true,
    }
    for (const patch of [
      { kind: 'user' },
      { jobId: ' ' },
      { cron: undefined },
      { recurring: 0 },
      { stale: undefined },
      { coalescedCount: -1 },
      { coalescedCount: 0.5 },
      { coalescedCount: Number.MAX_SAFE_INTEGER + 1 },
    ]) {
      expect(kimiNotificationEntry(kimiTranscriptMarkerFrame('cron.fired', {
        origin: {
          ...origin,
          ...patch,
        },
        prompt: 'Check.',
      }))).toStrictEqual([])
    }
    for (const prompt of [undefined, '', ' ', 0, null]) {
      expect(kimiNotificationEntry(kimiTranscriptMarkerFrame('cron.fired', { origin, prompt }))).toStrictEqual([])
    }
  })

  it('reads actual skill and plugin origin identities separately from activation', () => {
    for (const [payload, text] of [
      [
        { kind: 'skill_activation', activationId: 'a1', skillName: 'review' },
        'Skill review started a turn',
      ],
      [
        {
          kind: 'plugin_command',
          activationId: 'a2',
          pluginId: 'checks',
          commandName: 'review',
        },
        'Plugin command checks/review started a turn',
      ],
      [
        { kind: 'cron_job', jobId: 'job-1' },
        'Scheduled prompt job-1 started a turn',
      ],
    ] as const) {
      expect(kimiNotificationEntry(kimiTranscriptTurnFrame({
        origin: { kind: 'other', payload },
      }))).toStrictEqual([
        { kind: 'text', text },
      ])
    }
  })

  it('reads the original native autonomous origin and task prompt', () => {
    for (const [origin, prompt, text] of [
      [
        {
          kind: 'cron',
          payload: { kind: 'cron_job', jobId: 'daily' },
        },
        '',
        'Scheduled prompt daily started a turn',
      ],
      [
        {
          kind: 'other',
          payload: { kind: 'system_trigger', name: 'goal_continuation' },
        },
        '',
        'Continuing the goal',
      ],
      [
        { kind: 'task', taskId: 'task-1' },
        'Task finished: All tests pass',
        'Task finished: All tests pass',
      ],
    ] as const) {
      expect(kimiNotificationEntry(kimiTranscriptFrame({
        op: 'turn.upsert',
        turn: {
          kind: 'turn',
          turnId: 't0',
          state: 'running',
          origin,
          prompt,
        },
      }))).toStrictEqual([
        { kind: 'text', text },
      ])
    }
  })

  it('reads native task notification text without treating it as user input', () => {
    expect(kimiNotificationEntry(kimiTranscriptFrame({
      op: 'frame.upsert',
      turnId: 't0',
      stepId: 't0.1',
      frame: {
        kind: 'text',
        frameId: 't0.1.f1',
        role: 'user',
        taskId: 'task-1',
        text: 'Task finished\nAll tests pass',
      },
    }))).toStrictEqual([
      { kind: 'text', text: 'Task finished\nAll tests pass' },
    ])
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
  ])('reads a native notice with level %s and message %s', (level, message, code, text) => {
    const frame = kimiTranscriptFrame({
      op: 'marker.upsert',
      item: {
        kind: 'marker',
        markerId: 'live-m1',
        marker: 'notice',
        payload: {
          level,
          message,
          event: { code, message },
        },
      },
    })
    expect(kimiNotificationEntry(frame)).toStrictEqual(text
      ? [
          { kind: 'text', text },
        ]
      : [])
  })

  it('reads retry facts from a complete native step operation', () => {
    const frame = kimiTranscriptFrame({
      op: 'step.upsert',
      turnId: 't0',
      step: {
        kind: 'step',
        stepId: 't0.1',
        turnId: 't0',
        ordinal: 1,
        state: 'running',
        retry: {
          failedAttempt: 1,
          nextAttempt: 2,
          maxAttempts: 10,
          delayMs: 545.13,
          errorName: 'APIStatusError',
          errorMessage: 'The stream failed.',
        },
      },
    })
    expect(kimiNotificationEntry(frame)).toStrictEqual([
      {
        kind: 'retry',
        scope: 'api',
        attempt: 2,
        maxAttempts: 10,
        delayMs: 545.13,
        error: 'The stream failed.',
      },
    ])
  })

  it('reads completed and cancelled native compaction markers', () => {
    const item = {
      kind: 'marker',
      markerId: 'live-m1',
      marker: 'compaction',
      payload: {
        phase: 'completed',
        result: { tokensBefore: 9000, tokensAfter: 1000 },
      },
    }
    const frame = kimiTranscriptFrame({ op: 'marker.upsert', item })
    expect(kimiNotificationEntry(frame)).toStrictEqual([
      {
        kind: 'compaction',
        phase: 'end',
        detail: { pre: 9000, post: 1000 },
      },
    ])
    expect(kimiCompactionBoundary(input(frame, undefined, AgentProvider.KIMI_CODE))).toStrictEqual({ pre: 9000, post: 1000 })
    expect(kimiNotificationEntry(kimiTranscriptFrame({
      op: 'marker.upsert',
      item: {
        ...item,
        payload: { phase: 'cancelled' },
      },
    }))).toStrictEqual([
      { kind: 'compaction', phase: 'end', outcome: 'aborted' },
    ])
  })

  it('reads a native blocked hook marker and ignores a passed hook', () => {
    const item = {
      kind: 'marker',
      markerId: 'live-m1',
      marker: 'hook',
      payload: { hookEvent: 'UserPromptSubmit', blocked: true, content: 'The hook blocked this prompt.' },
    }
    expect(kimiNotificationEntry(kimiTranscriptFrame({ op: 'marker.upsert', item }))).toStrictEqual([
      {
        kind: 'hook',
        outcome: 'blocked',
        event: 'UserPromptSubmit',
        detail: 'The hook blocked this prompt.',
      },
    ])
    expect(kimiNotificationEntry(kimiTranscriptFrame({
      op: 'marker.upsert',
      item: {
        ...item,
        payload: { ...item.payload, blocked: false },
      },
    }))).toStrictEqual([])
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

  it('reads a blocked hook result as a blocked hook entry', () => {
    expect(kimiNotificationEntry(kimiTranscriptMarkerFrame('hook', {
      blocked: true,
      hookEvent: 'UserPromptSubmit',
      content: 'The hook blocked this prompt.',
    }))).toStrictEqual([
      {
        kind: 'hook',
        outcome: 'blocked',
        event: 'UserPromptSubmit',
        detail: 'The hook blocked this prompt.',
      },
    ])
  })

  it('words every original native origin with its name and without one', () => {
    const started = (nativeKind: string, kind: string, name = '') => kimiNotificationEntry(kimiTranscriptTurnFrame({
      origin: {
        kind: nativeKind,
        payload: {
          kind,
          ...(kind === KIMI_ORIGIN.SkillActivation
            ? { skillName: name }
            : kind === KIMI_ORIGIN.PluginCommand
              ? { pluginId: name, commandName: name }
              : kind === KIMI_ORIGIN.CronJob || kind === KIMI_ORIGIN.CronMissed
                ? { jobId: name }
                : { name }),
        },
      },
    }))
    const cases: [string, string, string, string][] = [
      [
        KIMI_ORIGIN.SystemTrigger,
        'other',
        'Started by nightly',
        'Started by the agent',
      ],
      [
        KIMI_ORIGIN.CronJob,
        'cron',
        'Scheduled prompt nightly started a turn',
        'A scheduled prompt started a turn',
      ],
      [
        KIMI_ORIGIN.CronMissed,
        'cron',
        'Missed scheduled prompt nightly ran late',
        'A missed scheduled prompt ran late',
      ],
      [
        KIMI_ORIGIN.Retry,
        'other',
        'Retrying the turn',
        'Retrying the turn',
      ],
      [
        KIMI_ORIGIN.HookResult,
        'hook',
        'Hook nightly started a turn',
        'A hook started a turn',
      ],
      [
        KIMI_ORIGIN.SkillActivation,
        'other',
        'Skill nightly started a turn',
        'A skill started a turn',
      ],
      [
        KIMI_ORIGIN.PluginCommand,
        'other',
        'Plugin command nightly/nightly started a turn',
        'A plugin command started a turn',
      ],
      [
        KIMI_ORIGIN.Injection,
        'other',
        'The agent received injected context',
        'The agent received injected context',
      ],
      [
        KIMI_ORIGIN.ShellCommand,
        'user',
        'Shell command nightly started a turn',
        'A shell command started a turn',
      ],
      [
        KIMI_ORIGIN.CompactionSummary,
        'compaction',
        'Continuing from the compacted context',
        'Continuing from the compacted context',
      ],
    ]
    for (const [kind, nativeKind, named, unnamed] of cases) {
      expect(started(nativeKind, kind, 'nightly'), kind).toStrictEqual([
        { kind: 'text', text: named },
      ])
      expect(started(nativeKind, kind), kind).toStrictEqual([
        { kind: 'text', text: unnamed },
      ])
    }
    expect(started('other', 'system_trigger', 'goal_continuation')).toStrictEqual([
      { kind: 'text', text: 'Continuing the goal' },
    ])
    for (const [kind, nativeKind] of ([
      [KIMI_ORIGIN.User, 'user'],
      [KIMI_ORIGIN.Task, 'task'],
      [KIMI_ORIGIN.BackgroundTask, 'task'],
    ] as const))
      expect(started(nativeKind, kind, 'x'), kind).toStrictEqual([])
    expect(cases.length + 3).toBe(Object.keys(KIMI_ORIGIN).length)
  })

  it('words nothing for a native turn whose origin is absent or malformed', () => {
    expect(kimiNotificationEntry(kimiTranscriptTurnFrame({ origin: undefined }))).toStrictEqual([])
    expect(kimiNotificationEntry(kimiTranscriptTurnFrame({ origin: 'retry' }))).toStrictEqual([])
  })

  it('reads complete retry facts and a zero delay from a native step', () => {
    expect(kimiNotificationEntry(nativeRetry({
      failedAttempt: 1,
      nextAttempt: 2,
      maxAttempts: 5,
      delayMs: 1000,
      errorName: 'RateLimit',
      errorMessage: 'Slow down',
    }))).toStrictEqual([
      {
        kind: 'retry',
        scope: 'api',
        attempt: 2,
        maxAttempts: 5,
        delayMs: 1000,
        error: 'Slow down',
      },
    ])
    expect(kimiNotificationEntry(nativeRetry({
      failedAttempt: 1,
      nextAttempt: 2,
      maxAttempts: 5,
      delayMs: 0,
      errorName: 'RateLimit',
    }))).toStrictEqual([
      {
        kind: 'retry',
        scope: 'api',
        attempt: 2,
        maxAttempts: 5,
        delayMs: 0,
        error: 'RateLimit',
      },
    ])
  })

  it('rejects incomplete retry facts and invalid counters without losing fractional delay', () => {
    expect(kimiNotificationEntry(nativeRetry({}))).toStrictEqual([])
    for (const count of [
      -1,
      0,
      1.5,
      Infinity,
      NaN,
      Number.MAX_SAFE_INTEGER + 1,
    ]) {
      expect(kimiNotificationEntry(nativeRetry({
        failedAttempt: 1,
        nextAttempt: count,
        maxAttempts: count,
        delayMs: 0,
      }))).toStrictEqual([])
    }
    expect(kimiNotificationEntry(nativeRetry({
      failedAttempt: 1,
      nextAttempt: 2,
      maxAttempts: 5,
      delayMs: Infinity,
    }))).toStrictEqual([])
    expect(kimiNotificationEntry(nativeRetry({
      failedAttempt: 1,
      nextAttempt: 2,
      maxAttempts: 5,
      delayMs: 0.5,
      errorMessage: ' ',
    }))).toStrictEqual([
      {
        kind: 'retry',
        scope: 'api',
        attempt: 2,
        maxAttempts: 5,
        delayMs: 0.5,
      },
    ])
  })

  it('reads the compaction lifecycle', () => {
    expect(kimiNotificationEntry(kimiTranscriptMarkerFrame('compaction', {
      phase: 'started',
      ...{ trigger: 'manual' },
    }))).toStrictEqual([])
    expect(kimiNotificationEntry(kimiTranscriptMarkerFrame('compaction', {
      phase: 'completed',
      ...{
        result: { tokensBefore: 9000, tokensAfter: 1200 },
      },
    })))
      .toStrictEqual([
        {
          kind: 'compaction',
          phase: 'end',
          detail: { pre: 9000, post: 1200 },
        },
      ])
    expect(kimiNotificationEntry(kimiTranscriptMarkerFrame('compaction', {
      phase: 'blocked',
      ...{},
    }))).toStrictEqual([])
    expect(kimiNotificationEntry(kimiTranscriptMarkerFrame('compaction', {
      phase: 'cancelled',
      ...{},
    }))).toStrictEqual([
      { kind: 'compaction', phase: 'end', outcome: 'aborted' },
    ])
  })

  it('reads a compaction that states no trigger and no sizes', () => {
    expect(kimiNotificationEntry(kimiTranscriptMarkerFrame('compaction', {
      phase: 'started',
      ...{},
    }))).toStrictEqual([])
    expect(kimiNotificationEntry(kimiTranscriptMarkerFrame('compaction', {
      phase: 'completed',
      ...{},
    }))).toStrictEqual([
      { kind: 'compaction', phase: 'end', detail: {} },
    ])
    // A context compacted to nothing states a size of zero, not an absent size.
    expect(kimiNotificationEntry(kimiTranscriptMarkerFrame('compaction', {
      phase: 'completed',
      ...{
        result: { tokensBefore: 10, tokensAfter: 0 },
      },
    })))
      .toStrictEqual([
        {
          kind: 'compaction',
          phase: 'end',
          detail: { pre: 10, post: 0 },
        },
      ])
  })

  it('reads a warning, an error and a task notice', () => {
    expect(kimiNotificationEntry(kimiFrame(KIMI_EVENT.Warning, { message: 'Low disk' }))).toStrictEqual([
      { kind: 'text', text: 'Warning: Low disk' },
    ])
    expect(kimiNotificationEntry(nativeError({ code: 'x.y', message: 'Broke' }))).toStrictEqual([
      { kind: 'text', text: 'Error (x.y): Broke' },
    ])
    expect(kimiNotificationEntry(nativeError({ message: 'Broke' }))).toStrictEqual([
      { kind: 'text', text: 'Error: Broke' },
    ])
    expect(kimiNotificationEntry(nativeError({}))).toStrictEqual([])
    expect(kimiNotificationEntry(nativeTaskText('Background agent completed\nDone.')))
      .toStrictEqual([
        { kind: 'text', text: 'Background agent completed\nDone.' },
      ])
  })

  it('reads an error that states only its code', () => {
    expect(kimiNotificationEntry(nativeError({ code: 'provider.rate_limited' }))).toStrictEqual([
      { kind: 'text', text: 'Error: provider.rate_limited' },
    ])
  })

  it('reads a task notice by whichever of its title and body it states', () => {
    expect(kimiNotificationEntry(nativeTaskText('Done'))).toStrictEqual([
      { kind: 'text', text: 'Done' },
    ])
    expect(kimiNotificationEntry(nativeTaskText('It ended.'))).toStrictEqual([
      { kind: 'text', text: 'It ended.' },
    ])
    expect(kimiNotificationEntry(nativeTaskText(''))).toStrictEqual([])
  })

  it('reads nothing from a row it does not own', () => {
    expect(kimiNotificationEntry({ type: 'interrupted' })).toStrictEqual([])
    expect(kimiNotificationEntry(kimiTranscriptTurnFrame({ state: 'completed' }))).toStrictEqual([])
  })
})

describe('kimiCompactionBoundary', () => {
  it('reads the size a compaction left', () => {
    const parsed = input(kimiTranscriptMarkerFrame('compaction', {
      phase: 'completed',
      ...{
        result: { tokensBefore: 9000, tokensAfter: 1200 },
      },
    }), undefined, AgentProvider.KIMI_CODE)
    expect(kimiCompactionBoundary(parsed)).toStrictEqual({ pre: 9000, post: 1200 })
    expect(kimiCompactionBoundary(input(kimiTranscriptMarkerFrame('compaction', {
      phase: 'started',
      ...{},
    }), undefined, AgentProvider.KIMI_CODE))).toBeNull()
  })

  // A compaction that states no sizes is still a boundary: the gauge resets at it.
  it('reads a compaction that states no sizes as a boundary with no sizes', () => {
    expect(kimiCompactionBoundary(input(kimiTranscriptMarkerFrame('compaction', {
      phase: 'completed',
      ...{},
    }), undefined, AgentProvider.KIMI_CODE))).toStrictEqual({})
  })

  it('reads no boundary from a row that is not a Kimi event', () => {
    expect(kimiCompactionBoundary(input({ content: 'Hello.' }, undefined, AgentProvider.KIMI_CODE))).toBeNull()
  })
})
