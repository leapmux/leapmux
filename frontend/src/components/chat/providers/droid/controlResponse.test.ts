import type { PersistedControlResponse } from '../../persistedControlResponse'
import type { DroidConfirmationType, DroidPermissionOption } from '~/generated/contracts/droid-protocol'
import { describe, expect, it } from 'vitest'
import { DROID_CONFIRMATION_TYPE, DROID_PERMISSION_OPTION, DROID_TOOL } from '~/generated/contracts/droid-protocol'
import { resolveControlResponseSummary } from '../../persistedControlResponse'
import { droidControls } from './pluginControls'

/** The `result` of the answer to a `droid.request_permission` request. */
interface DroidPermissionResult {
  selectedOption: string
  /** Droid's reply schema accepts a comment beside the option. */
  comment?: string
}

/** One answer of the reply to a `droid.ask_user` request. */
interface DroidAskUserAnswer {
  index: number
  question: string
  answer: string
}

/** The `result` of the answer to a `droid.ask_user` request. */
interface DroidAskUserResult {
  cancelled: boolean
  answers: DroidAskUserAnswer[]
}

/** The one tool call that a stored permission request asks about. */
interface DroidToolUse {
  type: 'tool_use'
  id: string
  name: string
  input: Record<string, unknown>
}

/** One question of a stored question request. */
interface DroidQuestion {
  /** Droid's own number for the question, from 1. */
  index: number
  question: string
  options: string[]
  multiSelect: boolean
}

/**
 * The saved answer: the JSON-RPC response that the worker writes to Droid's stdin.
 * `droidResolveControlResponse` builds it, and the worker service stores the same bytes
 * as the answer row (backend/internal/worker/agent/providers/droid/control.go).
 */
function storedReply(rpcId: string, result: DroidPermissionResult | DroidAskUserResult): Record<string, unknown> {
  return replyEnvelope(rpcId, result)
}

/** The same envelope around a `result` that no worker writes, for the malformed cases. */
function replyEnvelope(rpcId: string, result: unknown): Record<string, unknown> {
  return { jsonrpc: '2.0', type: 'response', factoryApiVersion: '1.0.0', factoryProtocolVersion: '1.222.0', id: rpcId, result }
}

/** The permission request that the worker publishes for one tool call (`onRequestPermission`). */
function permissionRequest(toolUse: DroidToolUse, confirmationType: DroidConfirmationType, options: readonly DroidPermissionOption[]): Record<string, unknown> {
  return {
    type: 'permission_request',
    requestId: `droid-perm-${toolUse.id}`,
    rpcId: `rpc-${toolUse.id}`,
    toolUse,
    confirmationType,
    details: { type: confirmationType },
    options,
  }
}

/** The question request that the worker publishes (`onAskUser`). */
function questionRequest(questions: DroidQuestion[]): Record<string, unknown> {
  return { type: 'ask_user_request', requestId: 'droid-ask-call-1', rpcId: 'rpc-ask-1', toolCallId: 'call-1', questions }
}

/** One saved row, as `parsePersistedControlResponse` gives it to the renderer. */
function saved(request: Record<string, unknown> | undefined, response: Record<string, unknown>): PersistedControlResponse {
  return { requestId: 'droid-request', claimToken: 'claim-1', request, response }
}

/** What the transcript row and the scroll-rail dot both show for one saved row. */
function display(cr: PersistedControlResponse) {
  return resolveControlResponseSummary(cr, droidControls.controlResponseDisplay)
}

const ALL_OPTIONS: readonly DroidPermissionOption[] = Object.values(DROID_PERMISSION_OPTION)

// One request carries every option. The saved words depend on the option, and on
// whether the request is a plan review. They depend on nothing else in the request.
const CREATE_REQUEST = permissionRequest(
  { type: 'tool_use', id: 'create-1', name: DROID_TOOL.Create, input: { file_path: '/work/notes.txt', content: 'a' } },
  DROID_CONFIRMATION_TYPE.Create,
  ALL_OPTIONS,
)
const SPEC_REQUEST = permissionRequest(
  { type: 'tool_use', id: 'exit-1', name: DROID_TOOL.ExitSpecMode, input: { plan: '# Native plan' } },
  DROID_CONFIRMATION_TYPE.ExitSpecMode,
  ALL_OPTIONS,
)
const QUESTIONS = questionRequest([
  { index: 1, question: 'Which color?', options: ['Blue', 'Red'], multiSelect: false },
  { index: 2, question: 'Which size?', options: ['Small', 'Large'], multiSelect: false },
])

/** The words a permission answer shows, for each option that a tool permission offers. */
const PERMISSION_LABELS = {
  [DROID_PERMISSION_OPTION.ProceedOnce]: 'Allow',
  [DROID_PERMISSION_OPTION.ProceedAlways]: 'Allow always',
  [DROID_PERMISSION_OPTION.ProceedAlwaysFile]: 'Allow always for this file',
  [DROID_PERMISSION_OPTION.ProceedAlwaysTools]: 'Allow always for these MCP tools',
  [DROID_PERMISSION_OPTION.ProceedAlwaysServer]: 'Allow always for this MCP server',
  [DROID_PERMISSION_OPTION.ProceedAutoRun]: 'Allow and raise the autonomy level',
  [DROID_PERMISSION_OPTION.ProceedAutoRunLow]: 'Allow and set the autonomy level to Low',
  [DROID_PERMISSION_OPTION.ProceedAutoRunMedium]: 'Allow and set the autonomy level to Medium',
  [DROID_PERMISSION_OPTION.ProceedAutoRunHigh]: 'Allow and set the autonomy level to High',
  [DROID_PERMISSION_OPTION.ProceedReportFalsePositive]: 'Allow and report a false positive',
  [DROID_PERMISSION_OPTION.Cancel]: 'Deny',
} satisfies Partial<Record<DroidPermissionOption, string>>

/** The words a plan review answer shows, for each option that the spec review offers. */
const PLAN_LABELS = {
  [DROID_PERMISSION_OPTION.ProceedOnce]: 'Approve',
  [DROID_PERMISSION_OPTION.ProceedAutoRunLow]: 'Approve and set the autonomy level to Low',
  [DROID_PERMISSION_OPTION.ProceedAutoRunMedium]: 'Approve and set the autonomy level to Medium',
  [DROID_PERMISSION_OPTION.ProceedAutoRunHigh]: 'Approve and set the autonomy level to High',
  [DROID_PERMISSION_OPTION.ProceedNewSession]: 'Approve in a new session',
  [DROID_PERMISSION_OPTION.ProceedNewSessionLow]: 'Approve in a new session at autonomy level Low',
  [DROID_PERMISSION_OPTION.ProceedNewSessionMedium]: 'Approve in a new session at autonomy level Medium',
  [DROID_PERMISSION_OPTION.ProceedNewSessionHigh]: 'Approve in a new session at autonomy level High',
  [DROID_PERMISSION_OPTION.ProceedEdit]: 'Approve with an edited spec',
  [DROID_PERMISSION_OPTION.Cancel]: 'Reject',
} satisfies Partial<Record<DroidPermissionOption, string>>

describe('droidControls controlResponseDisplay', () => {
  it('reads a plan review answered with proceed_once as Approve', () => {
    expect(display(saved(SPEC_REQUEST, storedReply('rpc-exit-1', { selectedOption: DROID_PERMISSION_OPTION.ProceedOnce }))))
      .toEqual({ kind: 'label', text: 'Approve' })
  })

  it('reads a plan review answered with cancel as Reject', () => {
    expect(display(saved(SPEC_REQUEST, storedReply('rpc-exit-1', { selectedOption: DROID_PERMISSION_OPTION.Cancel }))))
      .toEqual({ kind: 'label', text: 'Reject' })
  })

  it('reads a permission answered with proceed_once as Allow', () => {
    expect(display(saved(CREATE_REQUEST, storedReply('rpc-create-1', { selectedOption: DROID_PERMISSION_OPTION.ProceedOnce }))))
      .toEqual({ kind: 'label', text: 'Allow' })
  })

  it('reads a permission answered with cancel as Deny', () => {
    expect(display(saved(CREATE_REQUEST, storedReply('rpc-create-1', { selectedOption: DROID_PERMISSION_OPTION.Cancel }))))
      .toEqual({ kind: 'label', text: 'Deny' })
  })

  it.each(Object.entries(PERMISSION_LABELS))('reads a permission answered with %s as %j', (option, text) => {
    expect(display(saved(CREATE_REQUEST, storedReply('rpc-create-1', { selectedOption: option }))))
      .toEqual({ kind: 'label', text })
  })

  it.each(Object.entries(PLAN_LABELS))('reads a plan review answered with %s as %j', (option, text) => {
    expect(display(saved(SPEC_REQUEST, storedReply('rpc-exit-1', { selectedOption: option }))))
      .toEqual({ kind: 'label', text })
  })

  it('words every option that the contract lists', () => {
    // A new option in contracts/droid-protocol.json must get its words here, or the
    // saved row falls back to the generic label.
    expect(new Set([...Object.keys(PERMISSION_LABELS), ...Object.keys(PLAN_LABELS)])).toEqual(new Set(ALL_OPTIONS))
  })

  // Droid discards a comment beside a cancel, so the worker sends the reason as the
  // reader's next message and writes no comment. The decision stays the label.
  it('keeps the decision visible when a comment accompanies a cancel', () => {
    expect(display(saved(CREATE_REQUEST, storedReply('rpc-create-1', { selectedOption: DROID_PERMISSION_OPTION.Cancel, comment: 'Use a dry run first.' }))))
      .toEqual({ kind: 'label', text: 'Deny' })
    expect(display(saved(SPEC_REQUEST, storedReply('rpc-exit-1', { selectedOption: DROID_PERMISSION_OPTION.Cancel, comment: 'Split the plan.' }))))
      .toEqual({ kind: 'label', text: 'Reject' })
  })

  it('keeps the decision visible when a comment accompanies an approval', () => {
    expect(display(saved(SPEC_REQUEST, storedReply('rpc-exit-1', { selectedOption: DROID_PERMISSION_OPTION.ProceedOnce, comment: 'Go ahead.' }))))
      .toEqual({ kind: 'label', text: 'Approve' })
  })

  it('uses the permission words when the stored request is absent', () => {
    expect(display(saved(undefined, storedReply('rpc-exit-1', { selectedOption: DROID_PERMISSION_OPTION.ProceedOnce }))))
      .toEqual({ kind: 'label', text: 'Allow' })
    expect(display(saved(undefined, storedReply('rpc-exit-1', { selectedOption: DROID_PERMISSION_OPTION.Cancel }))))
      .toEqual({ kind: 'label', text: 'Deny' })
  })

  // The worker writes the answers in the order of the questions, and Droid reports them
  // to the model in that order. The row shows them in that same order.
  it('lists question answers in their stored order', () => {
    const reply = storedReply('rpc-ask-1', {
      cancelled: false,
      answers: [
        { index: 1, question: 'Which color?', answer: 'Blue' },
        { index: 2, question: 'Which size?', answer: 'Large' },
      ],
    })
    expect(display(saved(QUESTIONS, reply))).toEqual({ kind: 'label', text: 'Which color?: Blue\nWhich size?: Large' })
    expect(display(saved(undefined, reply))).toEqual({ kind: 'label', text: 'Which color?: Blue\nWhich size?: Large' })
  })

  it('shows every pick of a multiple-choice answer', () => {
    const reply = storedReply('rpc-ask-1', {
      cancelled: false,
      answers: [{ index: 1, question: 'Which sizes?', answer: 'Small, Large' }],
    })
    expect(display(saved(undefined, reply))).toEqual({ kind: 'label', text: 'Which sizes?: Small, Large' })
  })

  it('trims each question and answer and skips an empty answer', () => {
    const reply = storedReply('rpc-ask-1', {
      cancelled: false,
      answers: [
        { index: 1, question: '  Which color?  ', answer: '  Blue  ' },
        { index: 2, question: 'Which size?', answer: '   ' },
      ],
    })
    expect(display(saved(QUESTIONS, reply))).toEqual({ kind: 'label', text: 'Which color?: Blue' })
  })

  it('reads a questionnaire with no non-empty answer as No answer', () => {
    const reply = storedReply('rpc-ask-1', { cancelled: false, answers: [{ index: 1, question: 'Which color?', answer: '' }] })
    expect(display(saved(QUESTIONS, reply))).toEqual({ kind: 'label', text: 'No answer' })
    expect(display(saved(QUESTIONS, storedReply('rpc-ask-1', { cancelled: false, answers: [] })))).toEqual({ kind: 'label', text: 'No answer' })
  })

  it('reads a cancelled questionnaire as Cancelled', () => {
    expect(display(saved(QUESTIONS, storedReply('rpc-ask-1', { cancelled: true, answers: [] }))))
      .toEqual({ kind: 'label', text: 'Cancelled' })
    expect(display(saved(undefined, storedReply('rpc-ask-1', { cancelled: true, answers: [] }))))
      .toEqual({ kind: 'label', text: 'Cancelled' })
  })

  it('skips an answer entry that is not an object', () => {
    const reply = replyEnvelope('rpc-ask-1', { cancelled: false, answers: ['Blue', null, { index: 2, question: 'Which size?', answer: 'Large' }] })
    expect(display(saved(QUESTIONS, reply))).toEqual({ kind: 'label', text: 'Which size?: Large' })
  })

  describe('falls back to the generic label', () => {
    it.each([
      ['an option outside the contract', 'proceed_sometimes'],
      ['an option that spells an Object.prototype member', 'toString'],
      ['an empty option', ''],
    ])('for %s', (_case, selectedOption) => {
      expect(display(saved(CREATE_REQUEST, storedReply('rpc-create-1', { selectedOption }))))
        .toEqual({ kind: 'label', text: 'Responded' })
    })

    it('for an option that is not a string', () => {
      const reply = replyEnvelope('rpc-create-1', { selectedOption: 42 })
      expect(display(saved(CREATE_REQUEST, reply))).toEqual({ kind: 'label', text: 'Responded' })
    })

    it('for a reply that carries no result', () => {
      const reply = { jsonrpc: '2.0', type: 'response', factoryApiVersion: '1.0.0', factoryProtocolVersion: '1.222.0', id: 'rpc-create-1', error: { code: -32603, message: 'Internal error' } }
      expect(display(saved(CREATE_REQUEST, reply))).toEqual({ kind: 'label', text: 'Responded' })
    })

    it('for a question reply whose answers are not a list', () => {
      const reply = replyEnvelope('rpc-ask-1', { cancelled: false, answers: 'Blue' })
      expect(display(saved(QUESTIONS, reply))).toEqual({ kind: 'label', text: 'Responded' })
    })

    it('for the decision of another provider', () => {
      expect(display(saved(CREATE_REQUEST, { jsonrpc: '2.0', id: 0, result: { decision: 'accept' } })))
        .toEqual({ kind: 'label', text: 'Responded' })
    })
  })

  // An empty request payload makes the worker store the browser's own decision
  // unchanged (`droidResolveControlResponse`), and the shared fallback reads that.
  it('keeps the words of the neutral envelope when the worker stored it unchanged', () => {
    const neutral = { response: { request_id: 'droid-perm-create-1', response: { behavior: 'allow' } } }
    expect(display(saved(undefined, neutral))).toEqual({ kind: 'label', text: 'Allow' })
  })
})
