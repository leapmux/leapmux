import type { PersistedControlResponse } from '../../persistedControlResponse'
import type { ACPReplyPolicy } from './controlResponse'
import { describe, expect, it } from 'vitest'
import { acpBuildControlResponse, acpControlFeedbackAsFollowUpMessage, acpControlFeedbackRule, acpControlResponseBuilder, acpControlResponseSummary, acpOptionIdKind, acpPermissionResponseText } from './controlResponse'
import { acpPermissionOptions } from './extractControl'

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

  // The protocol's cancel selects no option, so only the outcome word states it. A
  // selected option still wins, and a blank id is no option.
  it('reads a cancelled outcome as Cancelled, and a selected option over it', () => {
    expect(acpPermissionResponseText(REQUEST, { result: { outcome: { outcome: 'cancelled' } } })).toBe('Cancelled')
    expect(acpPermissionResponseText(undefined, { result: { outcome: { outcome: 'cancelled', optionId: '  ' } } })).toBe('Cancelled')
    expect(acpPermissionResponseText(REQUEST, { result: { outcome: { outcome: 'cancelled', optionId: 'reject' } } })).toBe('Reject')
    expect(acpPermissionResponseText(REQUEST, { result: { outcome: { outcome: 'selected' } } }), 'a selection with no id states nothing').toBeNull()
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

describe('acpControlResponseSummary', () => {
  it('wraps the permission text as a label', () => {
    const cr: PersistedControlResponse = { claimToken: 'claim-1', requestId: '7', request: REQUEST, response: selected('proceed_once') }
    expect(acpControlResponseSummary(cr)).toEqual({ kind: 'label', text: 'Allow once' })
  })

  it('returns null when no optionId was selected (caller degrades)', () => {
    const cr: PersistedControlResponse = { claimToken: 'claim-1', requestId: '7', request: REQUEST, response: {} }
    expect(acpControlResponseSummary(cr)).toBeNull()
  })
})

const PERMISSION = {
  jsonrpc: '2.0',
  id: 7,
  method: 'session/request_permission',
  params: {
    toolCall: { toolCallId: 'call-1', title: 'Run' },
    options: [
      { optionId: 'always', kind: 'allow_always', name: 'Always' },
      { optionId: 'once', kind: 'allow_once', name: 'Once' },
      { optionId: 'no', kind: 'reject_once', name: 'No' },
      { optionId: 'never', kind: 'reject_always', name: 'Never' },
    ],
  },
}

const NO_PLAN: ACPReplyPolicy = { isPlanApproval: () => false }

describe('acpBuildControlResponse', () => {
  // A typed reason rejects the call. The agent reads only its own option ids, so the
  // reply selects its reject option -- the shared deny envelope reached the agent as a
  // frame it could not read, and the turn waited on the request forever.
  it('rejects a permission with the agent\'s own reject option', () => {
    expect(acpBuildControlResponse(PERMISSION, 'use the other file', 'jsonrpc:7', NO_PLAN)).toEqual({
      jsonrpc: '2.0',
      id: 'jsonrpc:7',
      result: { outcome: { outcome: 'selected', optionId: 'no' } },
    })
  })

  it('prefers a one-time reject, then an always reject, then the protocol cancel', () => {
    const onlyAlways = { params: { options: [{ optionId: 'once', kind: 'allow_once' }, { optionId: 'never', kind: 'reject_always' }] } }
    expect(acpBuildControlResponse(onlyAlways, 'no', 'r', NO_PLAN)).toMatchObject({ result: { outcome: { outcome: 'selected', optionId: 'never' } } })
    const noReject = { params: { options: [{ optionId: 'once', kind: 'allow_once' }] } }
    expect(acpBuildControlResponse(noReject, 'no', 'r', NO_PLAN)).toMatchObject({ result: { outcome: { outcome: 'cancelled' } } })
  })

  it('approves a permission once from an empty composer', () => {
    expect(acpBuildControlResponse(PERMISSION, '', 'r', NO_PLAN)).toEqual({
      jsonrpc: '2.0',
      id: 'r',
      result: { outcome: { outcome: 'selected', optionId: 'once' } },
    })
  })

  it('puts the reason in the provider\'s own field when it has one', () => {
    const policy: ACPReplyPolicy = { isPlanApproval: () => false, permissionRejectReason: (result, reason) => ({ ...result, _meta: { note: reason } }) }
    expect(acpBuildControlResponse(PERMISSION, 'try again', 'r', policy)).toMatchObject({
      result: { outcome: { outcome: 'selected', optionId: 'no' }, _meta: { note: 'try again' } },
    })
  })

  // An editor reply to a plan always rejects it, whether or not the reader typed a
  // reason: the approval button owns the allow path. An empty Reject that approved
  // would implement a plan the reader turned down.
  it('always rejects a plan approval, with or without a reason', () => {
    const plan: ACPReplyPolicy = { isPlanApproval: () => true }
    expect(acpBuildControlResponse(PERMISSION, '', 'r', plan)).toMatchObject({ response: { request_id: 'r', response: { behavior: 'deny' } } })
    expect(acpBuildControlResponse(PERMISSION, 'split it', 'r', plan)).toMatchObject({ response: { response: { behavior: 'deny', message: 'split it' } } })
  })

  it('keeps the shared envelope for a request that states no options', () => {
    expect(acpBuildControlResponse({ method: 'vendor/ask' }, 'no', 'r', NO_PLAN)).toMatchObject({ response: { response: { behavior: 'deny', message: 'no' } } })
    expect(acpBuildControlResponse({ method: 'vendor/ask' }, '', 'r', NO_PLAN)).toMatchObject({ response: { response: { behavior: 'allow' } } })
  })

  it('approves with an always option when the request offers no one-time allow', () => {
    const onlyAlways = { params: { options: [{ optionId: 'no', kind: 'reject_once' }, { optionId: 'always', kind: 'allow_always' }] } }
    expect(acpBuildControlResponse(onlyAlways, '', 'r', NO_PLAN)).toEqual({ jsonrpc: '2.0', id: 'r', result: { outcome: { outcome: 'selected', optionId: 'always' } } })
  })

  // An option with an empty id is no answer the agent can read. The reply takes the
  // next option of the kinds in order, and the protocol cancel when none is left.
  it('skips an option whose id is empty', () => {
    const blankFirst = { params: { options: [
      { optionId: '', kind: 'allow_once' },
      { optionId: 'always', kind: 'allow_always' },
      { optionId: '', kind: 'reject_once' },
      { optionId: 'never', kind: 'reject_always' },
    ] } }
    expect(acpBuildControlResponse(blankFirst, '', 'r', NO_PLAN)).toMatchObject({ result: { outcome: { outcome: 'selected', optionId: 'always' } } })
    expect(acpBuildControlResponse(blankFirst, 'no', 'r', NO_PLAN)).toMatchObject({ result: { outcome: { outcome: 'selected', optionId: 'never' } } })
    const allBlank = { params: { options: [{ optionId: '', kind: 'allow_once' }, { optionId: '', kind: 'reject_once' }] } }
    expect(acpBuildControlResponse(allBlank, 'no', 'r', NO_PLAN)).toEqual({ jsonrpc: '2.0', id: 'r', result: { outcome: { outcome: 'cancelled' } } })
    expect(acpBuildControlResponse(allBlank, '', 'r', NO_PLAN), 'no allow option is left to approve with').toBeUndefined()
  })

  it('puts the reason in the provider\'s own field on the protocol cancel too', () => {
    const policy: ACPReplyPolicy = { isPlanApproval: () => false, permissionRejectReason: (result, reason) => ({ ...result, _meta: { note: reason } }) }
    const noReject = { params: { options: [{ optionId: 'once', kind: 'allow_once' }] } }
    expect(acpBuildControlResponse(noReject, 'why not', 'r', policy)).toEqual({
      jsonrpc: '2.0',
      id: 'r',
      result: { outcome: { outcome: 'cancelled' }, _meta: { note: 'why not' } },
    })
  })

  // The plan row answers a plan approval, so neither the request's options nor the
  // provider's reason field shape the reply: the worker writes the provider's own.
  it('answers a plan approval through the plan envelope whatever options and reason field it has', () => {
    const plan: ACPReplyPolicy = { isPlanApproval: () => true, permissionRejectReason: (result, reason) => ({ ...result, _meta: { note: reason } }) }
    const reply = acpBuildControlResponse(PERMISSION, 'split it', 'r', plan)
    expect(reply).toMatchObject({ response: { request_id: 'r', response: { behavior: 'deny', message: 'split it' } } })
    expect(reply).not.toHaveProperty('result')
    expect(acpControlFeedbackAsFollowUpMessage(PERMISSION, plan)).toBe(false)
  })
})

describe('acpControlFeedbackAsFollowUpMessage', () => {
  it('sends the reason of a rejected permission as a message of its own', () => {
    expect(acpControlFeedbackAsFollowUpMessage(PERMISSION, NO_PLAN)).toBe(true)
  })

  it('keeps the reason in the reply when the provider has a field for it', () => {
    expect(acpControlFeedbackAsFollowUpMessage(PERMISSION, { isPlanApproval: () => false, permissionRejectReason: result => result })).toBe(false)
  })

  it('leaves a plan approval and a request with no options to the worker', () => {
    expect(acpControlFeedbackAsFollowUpMessage(PERMISSION, { isPlanApproval: () => true })).toBe(false)
    expect(acpControlFeedbackAsFollowUpMessage({ method: 'vendor/ask' }, NO_PLAN)).toBe(false)
  })
})

// The two hooks a registration builds. Each asks the provider's OWN control reader
// which requests are plan approvals, so the composer and the banner cannot disagree.
describe('acpControlResponseBuilder', () => {
  // The reader draws `vendor/plan` as a plan, and every other request as a permission
  // with the request's own options, as a provider's reader does.
  const planReader = (input: { payload: Record<string, unknown> }) => input.payload.method === 'vendor/plan'
    ? { kind: 'plan' as const }
    : { kind: 'permission' as const, permission: { options: acpPermissionOptions(input.payload) } }

  it('rejects what the reader draws as a plan through the shared plan envelope', () => {
    const build = acpControlResponseBuilder(planReader)
    expect(build({ method: 'vendor/plan', params: { options: PERMISSION.params.options } }, '', 'r')).toMatchObject({ response: { response: { behavior: 'deny' } } })
  })

  it('answers every other permission with its own options, and the reason in the provider field', () => {
    const build = acpControlResponseBuilder(planReader, (result, reason) => ({ ...result, _meta: { why: reason } }))
    expect(build(PERMISSION, 'no', 'r')).toEqual({ jsonrpc: '2.0', id: 'r', result: { outcome: { outcome: 'selected', optionId: 'no' }, _meta: { why: 'no' } } })
    expect(build(PERMISSION, '', 'r')).toEqual({ jsonrpc: '2.0', id: 'r', result: { outcome: { outcome: 'selected', optionId: 'once' } } })
  })

  // The reader decides, not the raw option list: a request that the banner draws as
  // no permission answers with no option of the request, and its reason stays in
  // the reply. A builder that read the raw list sent an option to a surface that
  // expects the shared envelope.
  it('answers a request the reader draws as no permission through the shared envelope', () => {
    const noPermission = () => null
    const build = acpControlResponseBuilder(noPermission)
    expect(build(PERMISSION, 'no', 'r')).toMatchObject({ response: { request_id: 'r', response: { behavior: 'deny', message: 'no' } } })
    expect(build(PERMISSION, '', 'r')).toMatchObject({ response: { request_id: 'r', response: { behavior: 'allow' } } })
    expect(acpControlFeedbackRule(noPermission)(PERMISSION)).toBe(false)
  })
})

describe('acpControlFeedbackRule', () => {
  // The reader draws `vendor/plan` as a plan, and every other request as a permission
  // with the request's own options, as a provider's reader does.
  const planReader = (input: { payload: Record<string, unknown> }) => input.payload.method === 'vendor/plan'
    ? { kind: 'plan' as const }
    : { kind: 'permission' as const, permission: { options: acpPermissionOptions(input.payload) } }

  it('sends a reason as a message only for a permission whose provider has no field for it', () => {
    expect(acpControlFeedbackRule(planReader)(PERMISSION)).toBe(true)
    expect(acpControlFeedbackRule(planReader, result => result)(PERMISSION)).toBe(false)
    expect(acpControlFeedbackRule(planReader)({ method: 'vendor/plan', params: { options: PERMISSION.params.options } })).toBe(false)
  })
})

// The banner reads a request's options through the provider's own reader, and the
// composer must answer with the same options. OpenCode and Kilo supply their daemon's
// own pair for a request that states none, so a composer that read the raw list sent
// the shared envelope, which the daemon cannot read, and the turn waited forever.
describe('acpBuildControlResponse with the provider\'s option reader', () => {
  const OPTION_LESS = { method: 'session/request_permission', params: { sessionId: 's', toolCall: { toolCallId: 'c' } } }
  const DAEMON_PAIR = [{ optionId: 'once', kind: 'allow_once' }, { optionId: 'reject', kind: 'reject_once' }]

  it('answers with the options that the reader supplies', () => {
    const policy: ACPReplyPolicy = { isPlanApproval: () => false, permissionOptions: () => DAEMON_PAIR }
    expect(acpBuildControlResponse(OPTION_LESS, 'use the other file', 'r', policy)).toEqual({ jsonrpc: '2.0', id: 'r', result: { outcome: { outcome: 'selected', optionId: 'reject' } } })
    expect(acpBuildControlResponse(OPTION_LESS, '', 'r', policy)).toEqual({ jsonrpc: '2.0', id: 'r', result: { outcome: { outcome: 'selected', optionId: 'once' } } })
    expect(acpControlFeedbackAsFollowUpMessage(OPTION_LESS, policy)).toBe(true)
  })

  // An empty composer approves a permission. A permission that offers no allow option
  // has nothing to approve with, and the agent reads no other reply, so the composer
  // keeps the draft and sends nothing.
  it('sends nothing from an empty composer when no option allows', () => {
    const policy: ACPReplyPolicy = { isPlanApproval: () => false, permissionOptions: () => [{ optionId: 'no', kind: 'reject_once' }] }
    expect(acpBuildControlResponse(OPTION_LESS, '', 'r', policy)).toBeUndefined()
  })
})

// A permission that offers no reject option takes the protocol's `cancelled` answer
// for a typed reason, and a stop that withdraws a request stores the same answer. The
// saved row states it rather than the vague "Responded".
describe('acpControlResponseSummary of a cancelled permission', () => {
  it('reads a cancelled outcome as Cancelled', () => {
    const cr: PersistedControlResponse = { claimToken: 'claim-1', requestId: '7', request: REQUEST, response: { result: { outcome: { outcome: 'cancelled' } } } }
    expect(acpControlResponseSummary(cr)).toEqual({ kind: 'label', text: 'Cancelled' })
  })
})
