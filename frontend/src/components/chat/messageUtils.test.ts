import { describe, expect, it } from 'vitest'
import { NOTIFICATION_TYPE, WORKER_WRITTEN_NOTIFICATION_TYPES } from '~/generated/contracts/worker-vocab'
import { isPlainNotificationType } from '~/lib/notificationTypes'
import { isNotificationThreadWrapper } from './messageUtils'
import { classifyACPMessage } from './providers/acp/classification'
import { input } from './providers/testUtils'

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

// The `compacting` envelope type is gone: no worker writes it. A row an older
// build stored draws the no-display card, as decision 22 accepts.
describe('a stored compacting envelope of an older build', () => {
  const compacting = { type: 'compacting' }

  it('draws the unrecognized card', () => {
    const classify = classifyACPMessage()
    expect(classify(input(compacting))).toStrictEqual({ kind: 'unknown' })
    expect(classify(input(compacting, { old_seqs: [], messages: [compacting] }))).toStrictEqual({ kind: 'unknown' })
  })
})
