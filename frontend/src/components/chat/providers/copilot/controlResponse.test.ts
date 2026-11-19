import type { ControlPrompt, PermissionOption } from '../../model/controlPrompt'
import type { PersistedControlResponse } from '../../persistedControlResponse'
import { describe, expect, it } from 'vitest'
import { COPILOT_EVENT } from '~/generated/contracts/copilot-protocol'
import { resolveControlResponseSummary } from '../../persistedControlResponse'
import { copilotControlResponseSummary } from './controlResponse'

/** One native session event, as the runtime sends it. */
function sessionEvent(type: string, data: Record<string, unknown>): Record<string, unknown> {
  return {
    jsonrpc: '2.0',
    method: 'session.event',
    params: {
      sessionId: 'session-1',
      event: { id: 'event-1', type, data },
    },
  }
}

/** One native permission request, as the runtime sends it. */
function permissionRequest(request: Record<string, unknown>): Record<string, unknown> {
  return sessionEvent(COPILOT_EVENT.PermissionRequested, { permissionRequest: request })
}

/** The neutral envelope that the Worker stores, around one native answer. */
function stored(answer: unknown): Record<string, unknown> {
  return {
    type: 'control_response',
    response: {
      subtype: 'success',
      request_id: 'request-1',
      response: answer,
    },
  }
}

/** One stored answer to a tool's permission: the runtime's own decision word. */
function decided(kind: string, extra: Record<string, unknown> = {}): Record<string, unknown> {
  return stored({ kind, ...extra })
}

/** A permission prompt with these options, as `copilotPermissionPrompt` in Go states it. */
function permissionPrompt(options: PermissionOption[], title = 'Read the file'): ControlPrompt {
  return {
    kind: 'permission',
    permission: { title, options },
  }
}

const ALLOW_ONCE: PermissionOption = { optionId: 'once', kind: 'allow_once', name: 'Allow once' }
const ALLOW_SESSION: PermissionOption = { optionId: 'session', kind: 'allow_always', name: 'Allow for this session' }
const ALLOW_PROJECT: PermissionOption = { optionId: 'project', kind: 'allow_always', name: 'Allow for this project' }
const REJECT: PermissionOption = { optionId: 'reject', kind: 'reject_once', name: 'Reject' }

/** The prompt of a request that can express a session-wide approval rule. */
const SCOPED_PROMPT = permissionPrompt([
  ALLOW_ONCE,
  ALLOW_SESSION,
  ALLOW_PROJECT,
  REJECT,
])

/** The prompt of a request that offers the single approval alone. */
const SINGLE_PROMPT = permissionPrompt([ALLOW_ONCE, REJECT], 'Write the file')

/** The prompt of `auto_mode_switch.requested`. */
const AUTO_SWITCH_PROMPT: ControlPrompt = {
  kind: 'permission',
  permission: {
    title: 'Copilot asks to switch to Auto',
    options: [
      { optionId: 'once', kind: 'allow_once', name: 'Switch this turn' },
      { optionId: 'session', kind: 'allow_always', name: 'Always switch' },
      { optionId: 'reject', kind: 'reject_once', name: 'Decline' },
    ],
    independentOptions: true,
  },
}

/** The prompt of `sampling.requested`. */
const SAMPLING_PROMPT: ControlPrompt = {
  kind: 'permission',
  permission: {
    title: 'The MCP server docs asks the model to answer',
    input: {},
    options: [
      { optionId: 'once', kind: 'allow_once', name: 'Allow' },
      { optionId: 'reject', kind: 'reject_once', name: 'Deny' },
    ],
  },
}

/** One saved row. */
function row(request: Record<string, unknown> | undefined, response: Record<string, unknown>, prompt?: ControlPrompt): PersistedControlResponse {
  return {
    claimToken: 'claim-1',
    requestId: 'request-1',
    request,
    response,
    ...(prompt
      ? { prompt }
      : {}),
  }
}

/** The display of one saved permission decision against a prompt. */
function permissionDisplay(response: Record<string, unknown>, prompt: ControlPrompt = SCOPED_PROMPT) {
  return copilotControlResponseSummary(row(permissionRequest({ kind: 'read', canOfferSessionApproval: true }), response, prompt))
}

describe('copilotControlResponseSummary', () => {
  it.each([
    ['allow', 'Allow'],
    ['deny', 'Deny'],
  ])('reads the sampling %s decision', (decision, text) => {
    expect(copilotControlResponseSummary(row(
      sessionEvent(COPILOT_EVENT.SamplingRequested, {}),
      stored({ decision }),
      SAMPLING_PROMPT,
    ))).toEqual({ kind: 'label', text })
  })

  it.each([
    ['yes', 'Switched to Auto for this turn'],
    ['yes_always', 'Switched to Auto from now on'],
    ['no', 'Declined the switch to Auto'],
  ])('summarizes a saved %s answer to the switch to Auto', (answer, text) => {
    expect(copilotControlResponseSummary(row(
      sessionEvent(COPILOT_EVENT.AutoModeSwitchRequested, {}),
      stored(answer),
      AUTO_SWITCH_PROMPT,
    ))).toEqual({ kind: 'label', text })
  })

  // The saved row states the option that the decision selected, in the words of that
  // option, exactly as the Agent Client Protocol providers do.
  it.each([
    ['approve-once', 'Allow once'],
    ['approve-for-session', 'Allow for this session'],
    ['approve-for-location', 'Allow for this project'],
    ['reject', 'Reject'],
  ])('reads the saved decision %s in the words of the option that it selected', (kind, text) => {
    expect(permissionDisplay(decided(kind))).toEqual({ kind: 'label', text })
  })

  // The words come from the prompt that the banner drew, so a prompt whose options
  // state other words reads those, whatever the native request holds.
  it('reads the words of the recorded prompt, not of the native request', () => {
    const prompt = permissionPrompt([
      { ...ALLOW_ONCE, name: 'Run it this one time' },
      { ...REJECT, name: 'Refuse it' },
    ])
    expect(permissionDisplay(decided('approve-once'), prompt)).toEqual({ kind: 'label', text: 'Run it this one time' })
    expect(permissionDisplay(decided('reject'), prompt)).toEqual({ kind: 'label', text: 'Refuse it' })
  })

  // The refusal is found by its kind, so a prompt that spells the refusal's id
  // differently still reads its words.
  it('reads the refusal of the prompt by its kind', () => {
    const prompt = permissionPrompt([
      ALLOW_ONCE,
      { optionId: 'no', kind: 'reject_once', name: 'No thanks' },
    ])
    expect(permissionDisplay(decided('reject'), prompt)).toEqual({ kind: 'label', text: 'No thanks' })
  })

  it('reads the kind of an option that states no name of its own', () => {
    const prompt = permissionPrompt([
      { optionId: 'once', kind: 'allow_once' },
      REJECT,
    ])
    expect(permissionDisplay(decided('approve-once'), prompt)).toEqual({ kind: 'label', text: 'Allow once' })
  })

  it('shows the reason that a rejection carried', () => {
    expect(permissionDisplay(decided('reject', { feedback: 'Use the read-only tool' }))).toEqual({ kind: 'feedback', message: 'Use the read-only tool' })
  })

  it('shows the option words for an approval, whatever feedback it carries', () => {
    expect(permissionDisplay(decided('approve-once', { feedback: 'ignored' }))).toEqual({ kind: 'label', text: 'Allow once' })
  })

  // A request that offers no session-wide rule draws no session button, and the
  // Worker refuses to send a scope that the prompt does not offer.
  it('reads nothing for a scope that the prompt did not offer', () => {
    expect(permissionDisplay(decided('approve-for-session'), SINGLE_PROMPT)).toBeNull()
    expect(permissionDisplay(decided('reject'), permissionPrompt([ALLOW_ONCE]))).toBeNull()
  })

  // A request the runtime withdrew was answered by nobody.
  it('states a cancelled request rather than a decision', () => {
    expect(permissionDisplay(decided('cancelled'))).toEqual({ kind: 'label', text: 'Cancelled' })
  })

  it('answers null for a decision word this build does not know', () => {
    expect(permissionDisplay(decided('a-word-a-later-runtime-adds'))).toBeNull()
    expect(permissionDisplay(decided(''))).toBeNull()
    expect(permissionDisplay(stored({}))).toBeNull()
    expect(permissionDisplay(stored('not an answer'))).toBeNull()
  })

  // The decision word is wire data off a persisted row, so a member of
  // `Object.prototype` reaches the decision table. An index read answers a FUNCTION
  // for each of these four, which the option lookup then compares as an id.
  it.each([
    'toString',
    '__proto__',
    'constructor',
    'valueOf',
  ])('answers null for the prototype member %s', (decision) => {
    const read = () => permissionDisplay(decided(decision))
    expect(read).not.toThrow()
    expect(read()).toBeNull()
  })

  // The prompt lives in the answer row's own metadata, so a row whose request is gone
  // still reads its words.
  it('reads a decision whose native request is gone', () => {
    expect(copilotControlResponseSummary(row(undefined, decided('approve-for-location'), SCOPED_PROMPT))).toEqual({ kind: 'label', text: 'Allow for this project' })
  })

  it('reads the answer to a question, and an empty answer as one', () => {
    const prompt: ControlPrompt = {
      kind: 'question',
      questions: [
        { question: 'Which port?', options: [], allowEmpty: true },
      ],
    }
    const request = sessionEvent(COPILOT_EVENT.UserInputRequested, { question: 'Which port?', allowFreeform: true })
    expect(copilotControlResponseSummary(row(request, stored({ answer: '8080', wasFreeform: true }), prompt))).toEqual({ kind: 'label', text: '8080' })
    expect(copilotControlResponseSummary(row(request, stored({ answer: '', wasFreeform: true }), prompt))).toEqual({ kind: 'label', text: 'Answered with an empty response' })
    expect(copilotControlResponseSummary(row(request, stored('8080'), prompt))).toBeNull()
  })

  it('reads a plan decision in the words of the plan buttons', () => {
    const prompt: ControlPrompt = { kind: 'plan', text: '1. Do it' }
    const request = sessionEvent(COPILOT_EVENT.ExitPlanModeRequested, { planContent: '1. Do it' })
    expect(copilotControlResponseSummary(row(request, stored({ approved: true }), prompt))).toEqual({ kind: 'label', text: 'Approve' })
    expect(copilotControlResponseSummary(row(request, stored({ approved: false }), prompt))).toEqual({ kind: 'label', text: 'Reject' })
    expect(copilotControlResponseSummary(row(request, stored({ approved: false, feedback: 'Split step 2.' }), prompt))).toEqual({ kind: 'feedback', message: 'Split step 2.' })
    expect(copilotControlResponseSummary(row(request, stored(true), prompt))).toBeNull()
  })

  it.each([
    ['accept', 'Approve'],
    ['decline', 'Reject'],
    ['cancel', 'Cancel'],
  ])('reads the MCP form action %s in the words of its button', (action, text) => {
    const prompt: ControlPrompt = {
      kind: 'elicitation',
      elicitation: { mode: 'form', message: 'Choose', server: 'docs' },
    }
    const request = sessionEvent(COPILOT_EVENT.ElicitationRequested, { message: 'Choose' })
    expect(copilotControlResponseSummary(row(request, stored({ action }), prompt))).toEqual({ kind: 'label', text })
  })

  // The prompt states which control the answer belongs to. A row with none reads as
  // nothing here, and the shared chokepoint shows its generic label.
  it('reads nothing from a row that records no prompt', () => {
    const cr = row(permissionRequest({ kind: 'read' }), decided('approve-once'))
    expect(copilotControlResponseSummary(cr)).toBeNull()
    expect(resolveControlResponseSummary(cr, copilotControlResponseSummary)).toEqual({ kind: 'label', text: 'Responded' })
  })
})
