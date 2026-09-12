import { describe, expect, it } from 'vitest'
import { NOTIFICATION_TYPE, WORKER_AUTHORED_NOTIFICATION_TYPES } from '~/generated/contracts/worker-vocab'
import { isPlainNotificationType, isWorkerAuthoredNotification } from './notificationTypes'

describe('isWorkerAuthoredNotification', () => {
  it('accepts every type the contract states the worker is the sole writer of', () => {
    for (const type of WORKER_AUTHORED_NOTIFICATION_TYPES)
      expect(isWorkerAuthoredNotification({ type })).toBe(true)
  })

  // An agent writes this one, so a plugin gets to decide what it means.
  it('refuses an agent-emitted type', () => {
    expect(isWorkerAuthoredNotification({ type: NOTIFICATION_TYPE.Interrupted })).toBe(false)
  })

  it('refuses a row that is not an object with a string type', () => {
    expect(isWorkerAuthoredNotification(null)).toBe(false)
    expect(isWorkerAuthoredNotification('goal_updated')).toBe(false)
    expect(isWorkerAuthoredNotification({ type: 7 })).toBe(false)
    expect(isWorkerAuthoredNotification({})).toBe(false)
  })
})

describe('isPlainNotificationType', () => {
  it('accepts the types every provider renders the same way', () => {
    for (const type of [
      NOTIFICATION_TYPE.SettingsChanged,
      NOTIFICATION_TYPE.ContextCleared,
      NOTIFICATION_TYPE.Interrupted,
      NOTIFICATION_TYPE.AgentError,
      NOTIFICATION_TYPE.PlanUpdated,
      NOTIFICATION_TYPE.Compacting,
    ])
      expect(isPlainNotificationType(type)).toBe(true)
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
