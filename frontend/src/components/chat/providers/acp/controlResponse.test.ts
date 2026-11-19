import type { ControlPrompt, PermissionOption } from '../../model/controlPrompt'
import type { PersistedControlResponse } from '../../persistedControlResponse'
import { describe, expect, it } from 'vitest'
import { acpControlResponseSummary, acpOptionIdKind, acpPermissionOptions, acpPermissionResponseText, acpPermissionToolCall } from './controlResponse'

// The options of the permission prompt that the Worker records with the answer. This
// agent states no `kind`, so the Worker reads each option as `other`.
const OPTIONS: PermissionOption[] = [
  { optionId: 'proceed_once', kind: 'other', name: 'Allow once' },
  { optionId: 'reject', kind: 'other', name: 'Reject' },
]

// Goose sets every option's name to its own optionId, so the name is no label at all.
// A saved decision read "allow_once" until the text came from the option `kind`.
const GOOSE_OPTIONS: PermissionOption[] = [
  { optionId: 'allow_always', name: 'allow_always', kind: 'allow_always' },
  { optionId: 'allow_once', name: 'allow_once', kind: 'allow_once' },
  { optionId: 'reject_once', name: 'reject_once', kind: 'reject_once' },
  { optionId: 'reject_always', name: 'reject_always', kind: 'reject_always' },
]

/** The permission prompt of `options`, as the Worker records it. */
function permissionPrompt(options: PermissionOption[]): ControlPrompt {
  return {
    kind: 'permission',
    permission: { options },
  }
}

function selected(optionId: string): Record<string, unknown> {
  return {
    result: {
      outcome: { outcome: 'selected', optionId },
    },
  }
}

describe('acpPermissionResponseText', () => {
  it('resolves the selected optionId to the name of its option', () => {
    expect(acpPermissionResponseText(OPTIONS, selected('proceed_once'))).toBe('Allow once')
    expect(acpPermissionResponseText(OPTIONS, selected('reject'))).toBe('Reject')
  })

  it('reads the option kind when the agent names an option after its own id', () => {
    expect(acpPermissionResponseText(GOOSE_OPTIONS, selected('allow_once'))).toBe('Allow once')
    expect(acpPermissionResponseText(GOOSE_OPTIONS, selected('allow_always'))).toBe('Allow always')
    expect(acpPermissionResponseText(GOOSE_OPTIONS, selected('reject_once'))).toBe('Reject')
    expect(acpPermissionResponseText(GOOSE_OPTIONS, selected('reject_always'))).toBe('Reject always')
  })

  it('falls back to the well-known-kind map when the list holds no such option', () => {
    expect(acpPermissionResponseText([], selected('proceed_once'))).toBe('Allow once')
    expect(acpPermissionResponseText([], selected('always'))).toBe('Allow always')
    expect(acpPermissionResponseText([], selected('cancel'))).toBe('Reject')
  })

  // An agent that spells no option vocabulary of its own reuses the protocol's four kind
  // tokens as its option ids (Goose and Reasonix both do). The id then NAMES its kind, so
  // a row that recorded no prompt still reads words rather than the wire token it
  // answered with -- the same leak ACP-004 closed for the prompt-present path.
  it('reads the protocol kind tokens as option ids when the list is empty', () => {
    expect(acpPermissionResponseText([], selected('allow_once'))).toBe('Allow once')
    expect(acpPermissionResponseText([], selected('allow_always'))).toBe('Allow always')
    expect(acpPermissionResponseText([], selected('reject_once'))).toBe('Reject')
    expect(acpPermissionResponseText([], selected('reject_always'))).toBe('Reject always')
  })

  // The protocol's cancel selects no option, so only the outcome word states it. A
  // selected option still wins, and a blank id is no option.
  it('reads a cancelled outcome as Cancelled, and a selected option over it', () => {
    expect(acpPermissionResponseText(OPTIONS, {
      result: {
        outcome: { outcome: 'cancelled' },
      },
    })).toBe('Cancelled')
    expect(acpPermissionResponseText([], {
      result: {
        outcome: { outcome: 'cancelled', optionId: '  ' },
      },
    })).toBe('Cancelled')
    expect(acpPermissionResponseText(OPTIONS, {
      result: {
        outcome: { outcome: 'cancelled', optionId: 'reject' },
      },
    })).toBe('Reject')
    expect(acpPermissionResponseText(OPTIONS, {
      result: {
        outcome: { outcome: 'selected' },
      },
    }), 'a selection with no id states nothing').toBeNull()
  })

  it('passes an unknown optionId through and returns null when none was selected', () => {
    expect(acpPermissionResponseText([], selected('mystery_opt'))).toBe('mystery_opt')
    expect(acpPermissionResponseText(OPTIONS, {
      result: { outcome: {} },
    })).toBeNull()
    expect(acpPermissionResponseText(OPTIONS, {})).toBeNull()
  })

  // The label reads the same either way today, because `permissionOptionLabel` has an
  // `Object.hasOwn` of its own. `acpOptionIdKind` below is where the answer differs.
  it.each([
    'constructor',
    'toString',
    'valueOf',
    'hasOwnProperty',
  ])('passes the raw optionId %s through', (optionId) => {
    expect(acpPermissionResponseText([], selected(optionId))).toBe(optionId)
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
  it.each([
    'constructor',
    'toString',
    'valueOf',
    'hasOwnProperty',
    'isPrototypeOf',
    '__proto__',
  ])('states no kind for the optionId %s', (optionId) => {
    expect(acpOptionIdKind(optionId)).toBe('')
  })

  it('states no kind for an id no agent LeapMux saw sends', () => {
    expect(acpOptionIdKind('mystery_opt')).toBe('')
  })
})

describe('acpControlResponseSummary', () => {
  it('wraps the permission text as a label', () => {
    const cr: PersistedControlResponse = {
      claimToken: 'claim-1',
      requestId: '7',
      request: undefined,
      response: selected('proceed_once'),
      prompt: permissionPrompt(OPTIONS),
    }
    expect(acpControlResponseSummary(cr)).toEqual({ kind: 'label', text: 'Allow once' })
  })

  // The prompt states the words, not the native request beside it.
  it('reads the option name from the recorded prompt, not from the native request', () => {
    const cr: PersistedControlResponse = {
      claimToken: 'claim-1',
      requestId: '7',
      request: {
        params: {
          options: [
            { optionId: 'proceed_once', name: 'Native words' },
          ],
        },
      },
      response: selected('proceed_once'),
      prompt: permissionPrompt(OPTIONS),
    }
    expect(acpControlResponseSummary(cr)).toEqual({ kind: 'label', text: 'Allow once' })
  })

  it('reads the option kind of its id for a row that recorded no permission prompt', () => {
    const base = {
      claimToken: 'claim-1',
      requestId: '7',
      request: undefined,
      response: selected('proceed_always'),
    }
    expect(acpControlResponseSummary(base)).toEqual({ kind: 'label', text: 'Allow always' })
    expect(acpControlResponseSummary({
      ...base,
      prompt: { kind: 'plan' },
    })).toEqual({ kind: 'label', text: 'Allow always' })
  })

  it('returns null when no optionId was selected (caller degrades)', () => {
    const cr: PersistedControlResponse = {
      claimToken: 'claim-1',
      requestId: '7',
      request: undefined,
      response: {},
      prompt: permissionPrompt(OPTIONS),
    }
    expect(acpControlResponseSummary(cr)).toBeNull()
  })
})

// A permission that offers no reject option takes the protocol's `cancelled` answer
// for a typed reason, and a stop that withdraws a request stores the same answer. The
// saved row states it rather than the vague "Responded".
describe('acpControlResponseSummary of a cancelled permission', () => {
  it('reads a cancelled outcome as Cancelled', () => {
    const cr: PersistedControlResponse = {
      claimToken: 'claim-1',
      requestId: '7',
      request: undefined,
      response: {
        result: {
          outcome: { outcome: 'cancelled' },
        },
      },
      prompt: permissionPrompt(OPTIONS),
    }
    expect(acpControlResponseSummary(cr)).toEqual({ kind: 'label', text: 'Cancelled' })
  })
})

describe('acpPermissionToolCall', () => {
  it('reads the tool call that the request states', () => {
    expect(acpPermissionToolCall({
      params: {
        toolCall: { toolCallId: 'call-1', title: 'Run' },
      },
    })).toEqual({ toolCallId: 'call-1', title: 'Run' })
  })

  it.each([
    ['an absent request', undefined],
    ['a request with no params', {}],
    [
      'a request with no tool call',
      {
        params: { options: [] },
      },
    ],
    [
      'a tool call that is no object',
      {
        params: { toolCall: 'Run' },
      },
    ],
  ])('reads no tool call from %s', (_shape, request) => {
    expect(acpPermissionToolCall(request)).toBeUndefined()
  })
})

describe('acpPermissionOptions', () => {
  const OPTIONS = [
    { optionId: 'once', kind: 'allow_once', name: 'Allow once' },
    { optionId: 'reject', kind: 'reject_once', name: 'Reject' },
  ]

  it('reads the options that the request offered, in their order', () => {
    expect(acpPermissionOptions({
      params: { options: OPTIONS },
    })).toStrictEqual(OPTIONS)
  })

  it('reads no option from an absent request or one with no option list', () => {
    expect(acpPermissionOptions(undefined)).toStrictEqual([])
    expect(acpPermissionOptions({})).toStrictEqual([])
    expect(acpPermissionOptions({
      params: {
        toolCall: { toolCallId: 'call-1' },
      },
    })).toStrictEqual([])
  })

  /*
   * `options` is wire data. A STRING was the quiet case: its `length` is the length of
   * the string, so a reader that asserted an array walked its CHARACTERS.
   */
  it.each([
    ['a string', 'allow_once'],
    ['a number', 7],
    [
      'an object',
      { optionId: 'once' },
    ],
    ['a boolean', true],
  ])('reads no option from an options field that holds %s', (_shape, options) => {
    expect(acpPermissionOptions({
      params: { options },
    })).toStrictEqual([])
  })

  // An element that is no object states no kind, and a display reads the kind.
  it('drops an element that cannot state a kind and keeps the ones that can', () => {
    expect(acpPermissionOptions({
      params: {
        options: [null, 'allow_once', 42, OPTIONS[0], []],
      },
    })).toStrictEqual([
      { optionId: 'once', kind: 'allow_once', name: 'Allow once' },
    ])
  })

  // Each field is READ, so a non-string states nothing rather than handing a number
  // to `permissionOptionLabel`. An empty name is no name: its kind fallback states
  // the truthful words instead.
  it('reads each option field and drops a name that states nothing', () => {
    expect(acpPermissionOptions({
      params: {
        options: [
          { optionId: 'once', kind: 'allow_once', name: '' },
          {
            optionId: 7,
            kind: null,
            name: { text: 'x' },
          },
        ],
      },
    })).toStrictEqual([
      { optionId: 'once', kind: 'allow_once' },
      { optionId: '', kind: '' },
    ])
  })
})
