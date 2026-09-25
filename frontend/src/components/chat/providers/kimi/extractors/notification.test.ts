import { describe, expect, it } from 'vitest'
import { KIMI_EVENT, KIMI_ORIGIN } from '~/generated/contracts/kimi-protocol'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { kimiFrame } from '~/test-support/kimiFixtures'
import { input } from '../../testUtils'
import { kimiCompactionBoundary, kimiNotificationEntry } from './notification'

describe('kimiNotificationEntry', () => {
  it('states why the agent started a turn by itself', () => {
    const started = (kind: string, name = '') => kimiNotificationEntry(kimiFrame(KIMI_EVENT.TurnStarted, { origin: { kind, name } }))
    expect(started('system_trigger', 'goal_continuation')).toStrictEqual([{ kind: 'text', text: 'Continuing the goal' }])
    expect(started('cron_job', 'daily')).toStrictEqual([{ kind: 'text', text: 'Scheduled prompt daily fired' }])
    expect(started('retry')).toStrictEqual([{ kind: 'text', text: 'Retrying the turn' }])
    expect(started('user')).toStrictEqual([])
    expect(started('background_task')).toStrictEqual([])
  })

  it('words every origin a turn can start from, with its name and without one', () => {
    const started = (kind: string, name = '') => kimiNotificationEntry(kimiFrame(KIMI_EVENT.TurnStarted, { origin: { kind, name } }))
    const cases: [string, string, string][] = [
      [KIMI_ORIGIN.SystemTrigger, 'Started by nightly', 'Started by the agent'],
      [KIMI_ORIGIN.CronJob, 'Scheduled prompt nightly fired', 'A scheduled prompt fired'],
      [KIMI_ORIGIN.CronMissed, 'Missed scheduled prompt nightly ran late', 'A missed scheduled prompt ran late'],
      [KIMI_ORIGIN.Retry, 'Retrying the turn', 'Retrying the turn'],
      [KIMI_ORIGIN.HookResult, 'Hook nightly started a turn', 'A hook started a turn'],
      [KIMI_ORIGIN.SkillActivation, 'Skill nightly started a turn', 'A skill started a turn'],
      [KIMI_ORIGIN.PluginCommand, 'Plugin command nightly started a turn', 'A plugin command started a turn'],
      [KIMI_ORIGIN.Injection, 'The agent received injected context', 'The agent received injected context'],
      [KIMI_ORIGIN.ShellCommand, 'Shell command nightly started a turn', 'A shell command started a turn'],
      [KIMI_ORIGIN.CompactionSummary, 'Continuing from the compacted context', 'Continuing from the compacted context'],
    ]
    for (const [kind, named, unnamed] of cases) {
      expect(started(kind, 'nightly'), kind).toStrictEqual([{ kind: 'text', text: named }])
      expect(started(kind), kind).toStrictEqual([{ kind: 'text', text: unnamed }])
    }
    // The user's message, and the task notification, already state why these turns ran.
    for (const kind of [KIMI_ORIGIN.User, KIMI_ORIGIN.Task, KIMI_ORIGIN.BackgroundTask])
      expect(started(kind, 'x'), kind).toStrictEqual([])
    // Every origin the contract lists is either worded above or silent on purpose.
    expect(cases.length + 3).toBe(Object.keys(KIMI_ORIGIN).length)
  })

  it('words nothing for a turn start that states no origin', () => {
    expect(kimiNotificationEntry(kimiFrame(KIMI_EVENT.TurnStarted))).toStrictEqual([])
    expect(kimiNotificationEntry(kimiFrame(KIMI_EVENT.TurnStarted, { origin: 'retry' }))).toStrictEqual([])
  })

  it('reads a retry', () => {
    expect(kimiNotificationEntry(kimiFrame(KIMI_EVENT.TurnStepRetrying, { nextAttempt: 2, maxAttempts: 5, delayMs: 1000, errorName: 'RateLimit', errorMessage: 'Slow down' })))
      .toStrictEqual([{ kind: 'retry', scope: 'api', attempt: 2, maxAttempts: 5, delayMs: 1000, error: 'Slow down' }])
  })

  it('reads a retry that states only some of its fields', () => {
    expect(kimiNotificationEntry(kimiFrame(KIMI_EVENT.TurnStepRetrying))).toStrictEqual([{ kind: 'retry', scope: 'api' }])
    // The error name stands in for a missing message, and a zero delay is a delay.
    expect(kimiNotificationEntry(kimiFrame(KIMI_EVENT.TurnStepRetrying, { delayMs: 0, errorName: 'RateLimit' })))
      .toStrictEqual([{ kind: 'retry', scope: 'api', delayMs: 0, error: 'RateLimit' }])
  })

  it('reads the compaction lifecycle', () => {
    expect(kimiNotificationEntry(kimiFrame(KIMI_EVENT.CompactionStarted, { trigger: 'manual' }))).toStrictEqual([{ kind: 'compaction', phase: 'start', detail: { trigger: 'manual' } }])
    expect(kimiNotificationEntry(kimiFrame(KIMI_EVENT.CompactionCompleted, { result: { tokensBefore: 9000, tokensAfter: 1200 } })))
      .toStrictEqual([{ kind: 'compaction', phase: 'end', detail: { pre: 9000, post: 1200 } }])
    expect(kimiNotificationEntry(kimiFrame(KIMI_EVENT.CompactionBlocked))).toStrictEqual([{ kind: 'status', text: 'Compaction is waiting for the running turn to end' }])
    expect(kimiNotificationEntry(kimiFrame(KIMI_EVENT.CompactionCancelled))).toStrictEqual([{ kind: 'status', text: 'Compaction cancelled' }])
  })

  it('reads a compaction that states no trigger and no sizes', () => {
    expect(kimiNotificationEntry(kimiFrame(KIMI_EVENT.CompactionStarted))).toStrictEqual([{ kind: 'compaction', phase: 'start' }])
    expect(kimiNotificationEntry(kimiFrame(KIMI_EVENT.CompactionCompleted))).toStrictEqual([{ kind: 'compaction', phase: 'end', detail: {} }])
    // A context compacted to nothing states a size of zero, not an absent size.
    expect(kimiNotificationEntry(kimiFrame(KIMI_EVENT.CompactionCompleted, { result: { tokensBefore: 10, tokensAfter: 0 } })))
      .toStrictEqual([{ kind: 'compaction', phase: 'end', detail: { pre: 10, post: 0 } }])
  })

  it('reads a warning, an error and a task notice', () => {
    expect(kimiNotificationEntry(kimiFrame(KIMI_EVENT.Warning, { message: 'Low disk' }))).toStrictEqual([{ kind: 'text', text: 'Warning: Low disk' }])
    expect(kimiNotificationEntry(kimiFrame(KIMI_EVENT.Error, { code: 'x.y', message: 'Broke' }))).toStrictEqual([{ kind: 'text', text: 'Error (x.y): Broke' }])
    expect(kimiNotificationEntry(kimiFrame(KIMI_EVENT.Error, { message: 'Broke' }))).toStrictEqual([{ kind: 'text', text: 'Error: Broke' }])
    expect(kimiNotificationEntry(kimiFrame(KIMI_EVENT.Error, {}))).toStrictEqual([])
    expect(kimiNotificationEntry(kimiFrame(KIMI_EVENT.TaskNotified, { title: 'Background agent completed', body: 'Done.' })))
      .toStrictEqual([{ kind: 'text', text: 'Background agent completed: Done.' }])
  })

  it('reads an error that states only its code', () => {
    expect(kimiNotificationEntry(kimiFrame(KIMI_EVENT.Error, { code: 'provider.rate_limited' }))).toStrictEqual([{ kind: 'text', text: 'Error: provider.rate_limited' }])
  })

  it('reads a task notice by whichever of its title and body it states', () => {
    expect(kimiNotificationEntry(kimiFrame(KIMI_EVENT.TaskNotified, { title: 'Done' }))).toStrictEqual([{ kind: 'text', text: 'Done' }])
    expect(kimiNotificationEntry(kimiFrame(KIMI_EVENT.TaskNotified, { body: 'It ended.' }))).toStrictEqual([{ kind: 'text', text: 'It ended.' }])
    expect(kimiNotificationEntry(kimiFrame(KIMI_EVENT.TaskNotified))).toStrictEqual([])
  })

  it('reads nothing from a row it does not own', () => {
    expect(kimiNotificationEntry({ type: 'interrupted' })).toStrictEqual([])
    expect(kimiNotificationEntry(kimiFrame(KIMI_EVENT.TurnEnded))).toStrictEqual([])
  })
})

describe('kimiCompactionBoundary', () => {
  it('reads the size a compaction left', () => {
    const parsed = input(kimiFrame(KIMI_EVENT.CompactionCompleted, { result: { tokensBefore: 9000, tokensAfter: 1200 } }), undefined, AgentProvider.KIMI_CODE)
    expect(kimiCompactionBoundary(parsed)).toStrictEqual({ pre: 9000, post: 1200 })
    expect(kimiCompactionBoundary(input(kimiFrame(KIMI_EVENT.CompactionStarted), undefined, AgentProvider.KIMI_CODE))).toBeNull()
  })

  // A compaction that states no sizes is still a boundary: the gauge resets at it.
  it('reads a compaction that states no sizes as a boundary with no sizes', () => {
    expect(kimiCompactionBoundary(input(kimiFrame(KIMI_EVENT.CompactionCompleted), undefined, AgentProvider.KIMI_CODE))).toStrictEqual({})
  })

  it('reads no boundary from a row that is not a Kimi event', () => {
    expect(kimiCompactionBoundary(input({ content: 'Hello.' }, undefined, AgentProvider.KIMI_CODE))).toBeNull()
  })
})
