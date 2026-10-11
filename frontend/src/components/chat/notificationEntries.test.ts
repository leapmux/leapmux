import type { NotificationEntry } from './model/notification'
import { describe, expect, it } from 'vitest'
import { GOAL_STATUS_TOKEN, GOAL_TRANSITION, NOTIFICATION_FIELD, NOTIFICATION_TYPE } from '~/generated/contracts/worker-vocab'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { COMPACTING_LABEL, flattenNotificationEntries, leapmuxNotificationEntry, notificationEntriesFor } from './notificationEntries'
import { providerFor } from './providers/registry'

// Register the OpenCode plugin so the test uses the production dispatch path.
// This plugin supplies no notificationEntry hook.
// Its worker notifications therefore require the shared extractor.
await import('./providers/opencode/plugin')

/** The one text block a single retry entry flattens into. */
function retryText(entry: Omit<Extract<NotificationEntry, { kind: 'retry' }>, 'kind'>): string {
  const blocks = flattenNotificationEntries([{ kind: 'retry', ...entry }])
  expect(blocks).toHaveLength(1)
  // The preceding assertion checks the list length.
  // Optional chaining also satisfies the type checker.
  return blocks[0]?.kind === 'text' ? blocks[0].text : ''
}

/**
 * Use one retry label for every provider.
 * Retain the reported operation and attempt.
 * Retain the wait and error detail.
 */
describe('flattenNotificationEntries retry', () => {
  it('states a sub-second wait in milliseconds', () => {
    // Rounding this positive delay to zero would display "0s".
    // Retain a positive wait so it differs from no wait.
    expect(retryText({ scope: 'api', attempt: 1, maxAttempts: 3, delayMs: 300 })).toBe('API retry 1/3 in 300ms')
    expect(retryText({ scope: 'api', delayMs: 499 })).toBe('API retry in 499ms')
  })

  it('states a whole-second wait in seconds', () => {
    expect(retryText({ scope: 'api', attempt: 1, maxAttempts: 3, delayMs: 2000 })).toBe('API retry 1/3 in 2s')
    expect(retryText({ scope: 'summarization', delayMs: 45_000 })).toBe('Summary retry in 45s')
  })

  it('states no wait at all for a zero or absent delay', () => {
    expect(retryText({ scope: 'api', attempt: 2 })).toBe('API retry 2')
    expect(retryText({ scope: 'api', attempt: 2, delayMs: 0 })).toBe('API retry 2')
  })

  it('names the reason beside the wait', () => {
    expect(retryText({ scope: 'api', attempt: 1, maxAttempts: 3, delayMs: 250, error: '529 overloaded' }))
      .toBe('API retry 1/3 in 250ms (529 overloaded)')
  })

  // A successful retry or a report with willRetry=false has no remaining wait.
  it('states the outcome rather than a wait once the stall ended', () => {
    expect(retryText({ scope: 'api', attempt: 2, maxAttempts: 3, delayMs: 300, succeeded: true }))
      .toBe('API retry 2/3 succeeded')
    expect(retryText({ scope: 'api', attempt: 3, maxAttempts: 3, delayMs: 300, willRetry: false, error: '529 overloaded' }))
      .toBe('API retry 3/3 gave up (529 overloaded)')
  })
})

/**
 * compacting uses the worker envelope and requires a shared notification entry.
 * PLAIN_ROW_TYPES classifies that envelope as a notification.
 * A provider without a notificationEntry hook supplies no fallback.
 * Without the shared case, that row would display no block.
 */
describe('leapmuxNotificationEntry', () => {
  it('reads the compacting envelope as the neutral start of a compaction', () => {
    expect(leapmuxNotificationEntry({ type: NOTIFICATION_TYPE.Compacting }, AgentProvider.OPENCODE))
      .toStrictEqual([{ kind: 'compaction', phase: 'start' }])
  })

  it('draws the compacting row for a provider that supplies no notificationEntry hook', () => {
    // Confirm that this provider supplies no notification hook.
    expect(providerFor(AgentProvider.OPENCODE)?.transcript.notificationEntry).toBeUndefined()
    expect(flattenNotificationEntries(notificationEntriesFor({ type: NOTIFICATION_TYPE.Compacting }, AgentProvider.OPENCODE)))
      .toStrictEqual([{ kind: 'divider', text: COMPACTING_LABEL, loading: true }])
  })
})

describe('neutral goal notification entries', () => {
  it.each(Object.values(GOAL_STATUS_TOKEN).filter(status => status !== ''))('reports the updated transition for status %s with its native detail', (status) => {
    const received = {
      type: NOTIFICATION_TYPE.GoalUpdated,
      [NOTIFICATION_FIELD.Objective]: 'Keep the objective',
      [NOTIFICATION_FIELD.GoalStatus]: status,
      [NOTIFICATION_FIELD.GoalTransition]: GOAL_TRANSITION.Updated,
      [NOTIFICATION_FIELD.StatusDetail]: 'native-detail',
    }
    expect(leapmuxNotificationEntry(received, AgentProvider.OPENCODE)).toEqual([{ kind: 'text', text: 'Goal updated: Keep the objective (native-detail)' }])
  })

  it.each([undefined, '', GOAL_STATUS_TOKEN.Unknown, 'future-status', '__proto__', 'constructor', 0, -1, 2147483647])('uses a neutral unknown fallback for status %j', (status) => {
    const received = {
      type: NOTIFICATION_TYPE.GoalUpdated,
      [NOTIFICATION_FIELD.Objective]: 'Keep the objective',
      ...(status === undefined ? {} : { [NOTIFICATION_FIELD.GoalStatus]: status }),
      [NOTIFICATION_FIELD.StatusDetail]: 'native-future-state',
    }
    expect(leapmuxNotificationEntry(received, AgentProvider.OPENCODE)).toEqual([{ kind: 'text', text: 'Goal status unknown: Keep the objective (native-future-state)' }])
  })

  it('retains native detail that matches an unrecognized wire status', () => {
    expect(leapmuxNotificationEntry({ type: NOTIFICATION_TYPE.GoalUpdated, objective: 'Keep the objective', goal_status: 'future-state', status_detail: 'future-state' }, AgentProvider.OPENCODE))
      .toEqual([{ kind: 'text', text: 'Goal status unknown: Keep the objective (future-state)' }])
  })

  it.each(['__proto__', 'constructor', 'future-transition'])('refuses a prototype or unknown transition %s without an active fallback', (transition) => {
    expect(leapmuxNotificationEntry({ type: NOTIFICATION_TYPE.GoalUpdated, objective: 'Keep the objective', goal_status: GOAL_STATUS_TOKEN.Unknown, goal_transition: transition }, AgentProvider.OPENCODE))
      .toEqual([{ kind: 'text', text: 'Goal status unknown: Keep the objective' }])
  })

  it.each([undefined, ''])('retains an absent objective as no notification entry: %j', (objective) => {
    expect(leapmuxNotificationEntry({ type: NOTIFICATION_TYPE.GoalUpdated, ...(objective === undefined ? {} : { objective }), goal_status: GOAL_STATUS_TOKEN.Unknown, goal_transition: GOAL_TRANSITION.Updated }, AgentProvider.OPENCODE)).toEqual([])
  })
})
