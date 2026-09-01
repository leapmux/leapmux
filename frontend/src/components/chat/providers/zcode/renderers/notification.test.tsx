import { describe, expect, it } from 'vitest'
import { ZCODE_DECISION, ZCODE_EVENT, ZCODE_TOOL } from '~/generated/contracts/zcode-protocol'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { renderThreadHasIcon, renderThreadText } from '../../../messageRenderTestUtils'
import { describeZCodeNotification, zcodeNotificationThreadEntry } from './notification'

// Side-effect import to register the ZCode plugin, so renderNotificationThread can
// consult its notificationThreadEntry -- ZCode's sole notification render path.
await import('../plugin')

function event(type: string, payload: Record<string, unknown> = {}): Record<string, unknown> {
  return { type, payload, sessionId: 's-1', seq: 1 }
}

const renderZCodeText = (parsed: unknown): string => renderThreadText([parsed], AgentProvider.ZCODE)

describe('describeZCodeNotification', () => {
  describe('permission.resolved', () => {
    // Only a decision the app-server made BY ITSELF reaches a row -- one the user
    // answered is recorded as its own answer row instead.
    it('states an automatic allow', () => {
      expect(describeZCodeNotification(event(ZCODE_EVENT.PermissionResolved, {
        decision: ZCODE_DECISION.Allow,
        toolName: ZCODE_TOOL.Read,
      }))).toBe('Allowed Read automatically')
    })

    it('states a denial and appends the reason the app-server gave', () => {
      expect(describeZCodeNotification(event(ZCODE_EVENT.PermissionResolved, {
        decision: ZCODE_DECISION.Deny,
        toolName: ZCODE_TOOL.Bash,
        reason: 'plan mode forbids writes',
      }))).toBe('Denied Bash — plan mode forbids writes')
    })

    it('appends the reason for an allow too, since an auto-allow states why', () => {
      expect(describeZCodeNotification(event(ZCODE_EVENT.PermissionResolved, {
        decision: ZCODE_DECISION.Allow,
        toolName: ZCODE_TOOL.Bash,
        reason: 'yolo mode',
      }))).toBe('Allowed Bash automatically — yolo mode')
    })

    // An escalation and an input rewrite are in the app-server's enumeration. Reading
    // either as a denial would report a refusal that never happened, so each has its
    // own sentence.
    it('names an escalation and a modification without calling either a denial', () => {
      expect(describeZCodeNotification(event(ZCODE_EVENT.PermissionResolved, {
        decision: ZCODE_DECISION.Escalate,
        toolName: ZCODE_TOOL.Bash,
      }))).toBe('Escalated Bash for approval')
      expect(describeZCodeNotification(event(ZCODE_EVENT.PermissionResolved, {
        decision: ZCODE_DECISION.Modify,
        toolName: ZCODE_TOOL.Edit,
      }))).toBe('Ran Edit with modified input')
    })

    it('names the tool generically when the payload does not name one', () => {
      expect(describeZCodeNotification(event(ZCODE_EVENT.PermissionResolved, {
        decision: ZCODE_DECISION.Deny,
      }))).toBe('Denied a tool')
    })

    // Returning null is what lets the plugin HIDE the row. Reporting an unread
    // decision as a denial would state the opposite of an allow.
    it('returns null for an absent or unrecognized decision', () => {
      expect(describeZCodeNotification(event(ZCODE_EVENT.PermissionResolved))).toBeNull()
      expect(describeZCodeNotification(event(ZCODE_EVENT.PermissionResolved, {
        decision: 'deferred_to_a_future_build',
        toolName: ZCODE_TOOL.Bash,
      }))).toBeNull()
    })
  })

  describe('turn steering', () => {
    it('states a queued steer with its preview', () => {
      expect(describeZCodeNotification(event(ZCODE_EVENT.TurnSteerQueued, {
        inputPreview: 'also run the linter',
      }))).toBe('Queued for the running turn: also run the linter')
    })

    it('states a queued steer with no preview', () => {
      expect(describeZCodeNotification(event(ZCODE_EVENT.TurnSteerQueued))).toBe('Queued for the running turn')
    })

    it('states a drained steer', () => {
      expect(describeZCodeNotification(event(ZCODE_EVENT.TurnSteerDrained)))
        .toBe('Queued input delivered to the agent')
    })
  })

  describe('session.closed', () => {
    it('states the reason when the app-server gave one', () => {
      expect(describeZCodeNotification(event(ZCODE_EVENT.SessionClosed, { reason: 'idle timeout' })))
        .toBe('Session closed — idle timeout')
    })

    it('states a bare close when it gave none', () => {
      expect(describeZCodeNotification(event(ZCODE_EVENT.SessionClosed))).toBe('Session closed')
    })
  })

  // Null hands the row to the shared provider-neutral switch (settings_changed,
  // interrupted, ...), which is the point of the null contract.
  it('returns null for shapes ZCode does not own', () => {
    expect(describeZCodeNotification(event('settings_changed'))).toBeNull()
    expect(describeZCodeNotification(event(ZCODE_EVENT.SessionUpdated))).toBeNull()
    expect(describeZCodeNotification({ noType: true })).toBeNull()
    expect(describeZCodeNotification(null)).toBeNull()
    expect(describeZCodeNotification('not an object')).toBeNull()
    expect(describeZCodeNotification(undefined)).toBeNull()
  })
})

describe('zcodeNotificationThreadEntry', () => {
  it('maps a described row to a single text entry', () => {
    expect(zcodeNotificationThreadEntry(event(ZCODE_EVENT.TurnSteerDrained)))
      .toEqual([{ kind: 'text', text: 'Queued input delivered to the agent' }])
  })

  it('returns null exactly when the describer does, so the two cannot disagree', () => {
    expect(zcodeNotificationThreadEntry(event(ZCODE_EVENT.PermissionResolved))).toBeNull()
    expect(zcodeNotificationThreadEntry(event('settings_changed'))).toBeNull()
  })
})

describe('zcode notification rendering (markup)', () => {
  it('renders a standalone notification as a plain line with no divider icon', () => {
    const msg = event(ZCODE_EVENT.SessionClosed, { reason: 'idle timeout' })
    expect(renderZCodeText(msg)).toBe('Session closed — idle timeout')
    // ZCode emits no compaction boundary, so no thread entry of its own is a divider.
    expect(renderThreadHasIcon([msg], AgentProvider.ZCODE)).toBe(false)
  })

  // Regression guard: a consolidated wrapper must render EVERY entry. A provider with
  // no notificationThreadEntry hook renders only messages[0], silently dropping the rest.
  it('renders every entry of a consolidated thread', () => {
    const text = renderThreadText([
      event(ZCODE_EVENT.TurnSteerQueued, { inputPreview: 'also run the linter' }),
      event(ZCODE_EVENT.TurnSteerDrained),
      event(ZCODE_EVENT.PermissionResolved, { decision: ZCODE_DECISION.Deny, toolName: ZCODE_TOOL.Bash }),
    ], AgentProvider.ZCODE)
    expect(text).toContain('also run the linter')
    expect(text).toContain('Queued input delivered to the agent')
    expect(text).toContain('Denied Bash')
  })

  it('renders nothing for a shape neither ZCode nor the shared switch owns', () => {
    expect(renderZCodeText(event('totally_unknown_zcode_event'))).toBe('')
  })
})
