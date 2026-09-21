import { describe, expect, it } from 'vitest'
import { NOTIFICATION_TYPE, WORKER_WRITTEN_NOTIFICATION_TYPES } from '~/generated/contracts/worker-vocab'
import { isPlainNotificationType } from '~/lib/notificationTypes'
import { isFinalCompactingStatus, isNotificationThreadWrapper } from './messageUtils'
import { classifyACPMessage } from './providers/acp/classification'
import { input } from './providers/testUtils'

// isFinalCompactingStatus is the single source of truth shared by the Claude,
// Codex, and ACP hidden-notification predicates (standalone + consolidated-thread
// paths). These cases pin the exact shape each provider relies on so the rule
// can't drift out from under any of them.
describe('isFinalCompactingStatus', () => {
  it('is true for a finished compaction status with status:null', () => {
    expect(isFinalCompactingStatus({ type: 'system', subtype: 'status', status: null })).toBe(true)
  })

  it('is true for a finished status carrying compact_result', () => {
    expect(isFinalCompactingStatus({ type: 'system', subtype: 'status', status: null, compact_result: 'success' })).toBe(true)
  })

  it('is true for any non-compacting status string', () => {
    expect(isFinalCompactingStatus({ type: 'system', subtype: 'status', status: 'done' })).toBe(true)
  })

  it('is true when the status field is absent (undefined !== "compacting")', () => {
    expect(isFinalCompactingStatus({ type: 'system', subtype: 'status' })).toBe(true)
  })

  it('is false for the live "compacting" status (the one visible row)', () => {
    expect(isFinalCompactingStatus({ type: 'system', subtype: 'status', status: 'compacting' })).toBe(false)
  })

  it('is false for a non-status system subtype', () => {
    expect(isFinalCompactingStatus({ type: 'system', subtype: 'compact_boundary' })).toBe(false)
  })

  it('is false for a non-system message type', () => {
    expect(isFinalCompactingStatus({ type: 'settings_changed', subtype: 'status', status: null })).toBe(false)
  })
})

// isNotificationThreadWrapper answers over BASE_NOTIFICATION_TYPES, which is the set
// that keeps a thread's members. The worker wraps every notification it persists
// (`wrapNotifContent`), and two adjacent notifications of one source join one thread
// whatever their types are, so a thread of several members is the normal shape. A
// classifier that falls past this test reads the FIRST member alone.
describe('isNotificationThreadWrapper', () => {
  // The whole set, from the whole vocabulary, in both directions. A type added to the
  // set and not listed here fails, and so does a type dropped from it.
  it('accepts exactly the notification types a thread can turn on', () => {
    const accepted = Object.values(NOTIFICATION_TYPE)
      .filter(type => isNotificationThreadWrapper({ messages: [{ type }] }))
      .sort()
    expect(accepted).toStrictEqual([
      NOTIFICATION_TYPE.SettingsChanged,
      NOTIFICATION_TYPE.ContextCleared,
      NOTIFICATION_TYPE.Interrupted,
      NOTIFICATION_TYPE.PlanUpdated,
      NOTIFICATION_TYPE.PlanExecution,
      NOTIFICATION_TYPE.Compacting,
      NOTIFICATION_TYPE.AgentError,
      ...WORKER_WRITTEN_NOTIFICATION_TYPES,
    ].sort())
  })

  // The invariant between the two sets, over the whole vocabulary. PLAIN_ROW_TYPES
  // states that a type draws as an ordinary row, and this set states that a thread
  // holding one keeps its other members. A type in the first and not the second draws
  // its own row and discards every member after it. The reverse is legal: the
  // worker-written types are in this set alone.
  it('accepts every type that draws as a plain row', () => {
    const plainOnly = Object.values(NOTIFICATION_TYPE)
      .filter(type => isPlainNotificationType(type) && !isNotificationThreadWrapper({ messages: [{ type }] }))
      .sort()
    expect(plainOnly).toStrictEqual([])
  })

  it('refuses a null wrapper and one that holds no message', () => {
    expect(isNotificationThreadWrapper(null)).toBe(false)
    expect(isNotificationThreadWrapper({ messages: [] })).toBe(false)
  })

  it('refuses a thread whose types no set accepts', () => {
    expect(isNotificationThreadWrapper({ messages: [{ type: NOTIFICATION_TYPE.RateLimit }, { type: 'tool.updated' }] })).toBe(false)
  })

  it('skips a member that is not an object and one that states no type', () => {
    expect(isNotificationThreadWrapper({ messages: [null, 'interrupted', {}, { type: '' }] })).toBe(false)
    expect(isNotificationThreadWrapper({ messages: [null, {}, { type: NOTIFICATION_TYPE.Interrupted }] })).toBe(true)
  })

  it('accepts a type the caller supplies as an extra', () => {
    const extras = new Set(['session.closed'])
    expect(isNotificationThreadWrapper({ messages: [{ type: 'session.closed' }] })).toBe(false)
    expect(isNotificationThreadWrapper({ messages: [{ type: 'session.closed' }] }, extras)).toBe(true)
  })

  it('accepts a subtype the caller recognizes', () => {
    const wrapper = { messages: [{ type: 'system', subtype: 'status' }] }
    expect(isNotificationThreadWrapper(wrapper)).toBe(false)
    expect(isNotificationThreadWrapper(wrapper, undefined, (type, subtype) => type === 'system' && subtype === 'status')).toBe(true)
  })
})

// The consequence of the entry, at the one layer where it is visible. The classifier
// of the Agent Client Protocol family is the smallest one that reaches both paths: its
// thread test calls isNotificationThreadWrapper, and its per-message path calls
// isPlainNotificationType. The same two paths run in Claude, Codex, Copilot, Pi and
// ZCode, so the shape below is not one family's.
describe('a notification thread whose only accepted type is compacting', () => {
  const compacting = { type: NOTIFICATION_TYPE.Compacting }
  // A rate limit rides the same thread and neither set accepts it: it reaches the
  // rate-limit popover rather than the transcript. `consolidateNotificationThread`
  // holds a case for the type, so the worker really does put one in a thread.
  const rateLimit = { type: NOTIFICATION_TYPE.RateLimit }

  it('keeps every member', () => {
    const classify = classifyACPMessage()
    const wrapper = { old_seqs: [], messages: [compacting, rateLimit] }
    expect(classify(input(compacting, wrapper)))
      .toStrictEqual({ kind: 'notification', entries: [{ kind: 'compaction', phase: 'start' }] })
  })

  // The case above needs two members, and this is why. The per-message set answers for
  // the FIRST member, so a one-member thread reaches the same answer with either entry
  // and proves neither.
  it('answers a one-member thread from the per-message set as well', () => {
    const classify = classifyACPMessage()
    expect(classify(input(compacting, { old_seqs: [], messages: [compacting] })))
      .toStrictEqual({ kind: 'notification', entries: [{ kind: 'compaction', phase: 'start' }] })
    expect(classify(input(compacting)))
      .toStrictEqual({ kind: 'notification', entries: [{ kind: 'compaction', phase: 'start' }] })
  })
})
