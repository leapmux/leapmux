import { describe, expect, it } from 'vitest'
import { NOTIFICATION_TYPE, WORKER_WRITTEN_NOTIFICATION_TYPES } from '~/generated/contracts/worker-vocab'
import { isPlainNotificationType, isWorkerWrittenNotification } from './notificationTypes'

describe('isWorkerWrittenNotification', () => {
  it('accepts every type the contract states the worker is the sole writer of', () => {
    for (const type of WORKER_WRITTEN_NOTIFICATION_TYPES)
      expect(isWorkerWrittenNotification({ type })).toBe(true)
  })

  // An agent writes this one, so a plugin gets to decide what it means.
  it('refuses an agent-emitted type', () => {
    expect(isWorkerWrittenNotification({ type: NOTIFICATION_TYPE.Interrupted })).toBe(false)
  })

  it('refuses a row that is not an object with a string type', () => {
    expect(isWorkerWrittenNotification(null)).toBe(false)
    expect(isWorkerWrittenNotification('goal_updated')).toBe(false)
    expect(isWorkerWrittenNotification({ type: 7 })).toBe(false)
    expect(isWorkerWrittenNotification({})).toBe(false)
  })
})

describe('isPlainNotificationType', () => {
  // The whole set, from the whole vocabulary, in both directions. A type added to the
  // set and not listed here fails, and so does a type dropped from it. Each of these is
  // also a type `isNotificationThreadWrapper` accepts, which `messageUtils.test.ts`
  // pins: this set answers for ONE message, and that one keeps a thread's other
  // members.
  it('accepts exactly the types every provider renders the same way', () => {
    const accepted = Object.values(NOTIFICATION_TYPE).filter(isPlainNotificationType).sort()
    expect(accepted).toStrictEqual([
      NOTIFICATION_TYPE.SettingsChanged,
      NOTIFICATION_TYPE.ContextCleared,
      NOTIFICATION_TYPE.Interrupted,
      NOTIFICATION_TYPE.AgentError,
      NOTIFICATION_TYPE.PlanUpdated,
      NOTIFICATION_TYPE.Compacting,
      NOTIFICATION_TYPE.PlanExecution,
    ].sort())
  })

  // Claude Code applies its own hidden test to a rate-limit row, so one answer for
  // every provider would be wrong.
  it('refuses a rate-limit row', () => {
    expect(isPlainNotificationType(NOTIFICATION_TYPE.RateLimitEvent)).toBe(false)
    expect(isPlainNotificationType(NOTIFICATION_TYPE.RateLimit)).toBe(false)
  })

  it('refuses an absent type and an empty one', () => {
    expect(isPlainNotificationType(undefined)).toBe(false)
    expect(isPlainNotificationType('')).toBe(false)
    expect(isPlainNotificationType('tool.updated')).toBe(false)
  })
})
