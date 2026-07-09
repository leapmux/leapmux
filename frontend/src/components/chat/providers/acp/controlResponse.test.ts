import type { PersistedControlResponse } from '../../persistedControlResponse'
import { describe, expect, it } from 'vitest'
import { acpControlResponseDisplay, acpPermissionResponseText } from './controlResponse'

// Mirrors what the backend actually persists as the pruned request context: optionId + name only
// (the ACP option `kind` is pruned away, so the fixture omits it too).
const REQUEST = {
  method: 'session/request_permission',
  params: {
    options: [
      { optionId: 'proceed_once', name: 'Allow once' },
      { optionId: 'reject', name: 'Reject' },
    ],
  },
}

function selected(optionId: string): Record<string, unknown> {
  return { result: { outcome: { outcome: 'selected', optionId } } }
}

describe('acppermissionresponsetext', () => {
  it('resolves the selected optionId to its request option name', () => {
    expect(acpPermissionResponseText(REQUEST, selected('proceed_once'))).toBe('Allow once')
    expect(acpPermissionResponseText(REQUEST, selected('reject'))).toBe('Reject')
  })

  it('falls back to the well-known-kind map when the option is not in the request', () => {
    expect(acpPermissionResponseText({ params: { options: [] } }, selected('proceed_once'))).toBe('Allow once')
    expect(acpPermissionResponseText({ params: { options: [] } }, selected('always'))).toBe('Always allow')
    expect(acpPermissionResponseText({ params: { options: [] } }, selected('cancel'))).toBe('Reject')
  })

  it('passes an unknown optionId through and returns null when none was selected', () => {
    expect(acpPermissionResponseText({}, selected('mystery_opt'))).toBe('mystery_opt')
    expect(acpPermissionResponseText(REQUEST, { result: { outcome: {} } })).toBeNull()
    expect(acpPermissionResponseText(REQUEST, {})).toBeNull()
  })
})

describe('acpcontrolresponsedisplay', () => {
  it('wraps the permission text as a label', () => {
    const cr: PersistedControlResponse = { provider: 'GOOSE', requestId: '7', request: REQUEST, response: selected('proceed_once') }
    expect(acpControlResponseDisplay(cr)).toEqual({ kind: 'label', text: 'Allow once' })
  })

  it('returns null when no optionId was selected (caller degrades)', () => {
    const cr: PersistedControlResponse = { provider: 'GOOSE', requestId: '7', request: REQUEST, response: {} }
    expect(acpControlResponseDisplay(cr)).toBeNull()
  })
})
