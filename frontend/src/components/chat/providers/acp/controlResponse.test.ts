import type { PersistedControlResponse } from '../../persistedControlResponse'
import { describe, expect, it } from 'vitest'
import { acpControlResponseDisplay, acpOptionIdKind, acpPermissionResponseText } from './controlResponse'

// Mirrors what the backend persists as the request context. The worker stores the
// request whole, so an option keeps whatever fields its agent sent. This agent sends
// no `kind`, which is why the fixture omits it.
const REQUEST = {
  method: 'session/request_permission',
  params: {
    options: [
      { optionId: 'proceed_once', name: 'Allow once' },
      { optionId: 'reject', name: 'Reject' },
    ],
  },
}

// Goose sets every option's name to its own optionId, so the name is no label at all.
// A saved decision read "allow_once" until the text came from the option `kind`.
const GOOSE_REQUEST = {
  method: 'session/request_permission',
  params: {
    options: [
      { optionId: 'allow_always', name: 'allow_always', kind: 'allow_always' },
      { optionId: 'allow_once', name: 'allow_once', kind: 'allow_once' },
      { optionId: 'reject_once', name: 'reject_once', kind: 'reject_once' },
      { optionId: 'reject_always', name: 'reject_always', kind: 'reject_always' },
    ],
  },
}

function selected(optionId: string): Record<string, unknown> {
  return { result: { outcome: { outcome: 'selected', optionId } } }
}

describe('acpPermissionResponseText', () => {
  it('resolves the selected optionId to its request option name', () => {
    expect(acpPermissionResponseText(REQUEST, selected('proceed_once'))).toBe('Allow once')
    expect(acpPermissionResponseText(REQUEST, selected('reject'))).toBe('Reject')
  })

  it('reads the option kind when the agent names an option after its own id', () => {
    expect(acpPermissionResponseText(GOOSE_REQUEST, selected('allow_once'))).toBe('Allow once')
    expect(acpPermissionResponseText(GOOSE_REQUEST, selected('allow_always'))).toBe('Allow always')
    expect(acpPermissionResponseText(GOOSE_REQUEST, selected('reject_once'))).toBe('Reject')
    expect(acpPermissionResponseText(GOOSE_REQUEST, selected('reject_always'))).toBe('Reject always')
  })

  it('falls back to the well-known-kind map when the option is not in the request', () => {
    expect(acpPermissionResponseText({ params: { options: [] } }, selected('proceed_once'))).toBe('Allow once')
    expect(acpPermissionResponseText({ params: { options: [] } }, selected('always'))).toBe('Allow always')
    expect(acpPermissionResponseText({ params: { options: [] } }, selected('cancel'))).toBe('Reject')
  })

  // An agent that spells no option vocabulary of its own reuses the protocol's four kind
  // tokens as its option ids (Goose and Reasonix both do). The id then NAMES its kind, so
  // a row whose request did not persist still reads words rather than the wire token it
  // answered with -- the same leak ACP-004 closed for the request-present path.
  it('reads the protocol kind tokens as option ids when the request is gone', () => {
    expect(acpPermissionResponseText(undefined, selected('allow_once'))).toBe('Allow once')
    expect(acpPermissionResponseText(undefined, selected('allow_always'))).toBe('Allow always')
    expect(acpPermissionResponseText(undefined, selected('reject_once'))).toBe('Reject')
    expect(acpPermissionResponseText(undefined, selected('reject_always'))).toBe('Reject always')
  })

  it('passes an unknown optionId through and returns null when none was selected', () => {
    expect(acpPermissionResponseText({}, selected('mystery_opt'))).toBe('mystery_opt')
    expect(acpPermissionResponseText(REQUEST, { result: { outcome: {} } })).toBeNull()
    expect(acpPermissionResponseText(REQUEST, {})).toBeNull()
  })

  // The label reads the same either way today, because `permissionOptionLabel` has an
  // `Object.hasOwn` of its own. `acpOptionIdKind` below is where the answer differs.
  it.each(['constructor', 'toString', 'valueOf', 'hasOwnProperty'])('passes the raw optionId %s through', (optionId) => {
    expect(acpPermissionResponseText(undefined, selected(optionId))).toBe(optionId)
  })
})

describe('acpOptionIdKind', () => {
  it('reads the ids the table holds and the protocol kind tokens', () => {
    expect(acpOptionIdKind('proceed_once')).toBe('allow_once')
    expect(acpOptionIdKind('cancel')).toBe('reject_once')
    expect(acpOptionIdKind('allow_always')).toBe('allow_always')
  })

  /*
   * The optionId is wire data, and a bare index on a wire key reaches
   * `Object.prototype`. A member that resolves to a function is truthy, so `??` never
   * fired and the function itself became the option's KIND. The one caller today has
   * an `Object.hasOwn` of its own, so nothing on the screen moved -- the next caller
   * that reads the kind has no such cover.
   */
  it.each(['constructor', 'toString', 'valueOf', 'hasOwnProperty', 'isPrototypeOf', '__proto__'])('states no kind for the optionId %s', (optionId) => {
    expect(acpOptionIdKind(optionId)).toBe('')
  })

  it('states no kind for an id no agent LeapMux saw sends', () => {
    expect(acpOptionIdKind('mystery_opt')).toBe('')
  })
})

describe('acpControlResponseDisplay', () => {
  it('wraps the permission text as a label', () => {
    const cr: PersistedControlResponse = { claimToken: 'claim-1', requestId: '7', request: REQUEST, response: selected('proceed_once') }
    expect(acpControlResponseDisplay(cr)).toEqual({ kind: 'label', text: 'Allow once' })
  })

  it('returns null when no optionId was selected (caller degrades)', () => {
    const cr: PersistedControlResponse = { claimToken: 'claim-1', requestId: '7', request: REQUEST, response: {} }
    expect(acpControlResponseDisplay(cr)).toBeNull()
  })
})
