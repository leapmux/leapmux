import type { PersistedControlResponse } from '../../persistedControlResponse'
import { describe, expect, it } from 'vitest'
import { ZCODE_METHOD, ZCODE_TOOL } from '~/generated/contracts/zcode-protocol'
import { zcodeQuestionsFromPayload } from './askUserQuestion'
import { zcodeControlResponseSummary } from './controlResponse'

/**
 * One saved answer, beside the request the worker stored for it.
 *
 * The stored request is a HYBRID, and the fixture carries both halves because the
 * derivation reads both: ZCode's own `params`, and the Claude-shaped header whose
 * `tool_name` states WHICH of the three prompts arrived (see `zcode_control.go`).
 */
function response(
  result: Record<string, unknown>,
  params: Record<string, unknown> = {},
  method: string = ZCODE_METHOD.RequestUserInput,
  toolName: string = ZCODE_TOOL.AskUserQuestion,
): PersistedControlResponse {
  return {
    requestId: 'request-1',
    claimToken: 'claim-1',
    request: { id: 7, method, params, request: { tool_name: toolName } },
    response: { id: 7, result },
  }
}

/** One saved answer to the plan approval, which the worker records as `ExitPlanMode`. */
function planResponse(result: Record<string, unknown>): PersistedControlResponse {
  return response(result, { schema: { interaction: 'plan_approval' } }, ZCODE_METHOD.RequestUserInput, ZCODE_TOOL.ExitPlanMode)
}

function questionRequest(...questions: string[]): Record<string, unknown> {
  return { input: { questions: questions.map(question => ({ question })) } }
}

describe('zcodeControlResponseSummary', () => {
  it.each([
    ['allow', 'Allow'],
    ['deny', 'Deny'],
    ['escalate', 'Escalated'],
    ['modify', 'Modified'],
  ])('renders the native permission decision %s', (decision, text) => {
    expect(zcodeControlResponseSummary(response({ decision }, {}, ZCODE_METHOD.RequestPermission))).toEqual({ kind: 'label', text })
  })

  it('preserves a native permission rejection reason without attributing it to the user', () => {
    expect(zcodeControlResponseSummary(response({ decision: 'deny', reason: 'No offered option allows this operation.' }, {}, ZCODE_METHOD.RequestPermission)))
      .toEqual({ kind: 'label', text: 'Deny\nNo offered option allows this operation.' })
  })

  it('renders native answers in the complete request order', () => {
    expect(zcodeControlResponseSummary(response({ action: 'accept', content: { answers: { Second: 'b', First: 'a' }, answer_0: 'wrong', answer_1: 'wrong' } }, questionRequest('First', 'Second'))))
      .toEqual({ kind: 'label', text: 'First: a\nSecond: b' })
  })

  it('recovers question labels from native request display fields', () => {
    expect(zcodeControlResponseSummary(response({ action: 'accept', content: { answer: 'Postgres' } }, { questions: [{ question: 'Database?' }] })))
      .toEqual({ kind: 'label', text: 'Database?: Postgres' })
  })

  it('uses positional answers when keyed answers are absent', () => {
    expect(zcodeControlResponseSummary(response({ action: 'accept', content: { answer_0: 'a', answer_1: 'b', answer: 'ignored' } }, questionRequest('First', 'Second'))))
      .toEqual({ kind: 'label', text: 'First: a\nSecond: b' })
  })

  it('keeps the final native value when question text repeats', () => {
    expect(zcodeControlResponseSummary(response({ action: 'accept', content: { answer_0: 'first', answer_1: 'last' } }, questionRequest('Repeated', 'Repeated'))))
      .toEqual({ kind: 'label', text: 'Repeated: last' })
  })

  it('uses a single answer only for one question', () => {
    expect(zcodeControlResponseSummary(response({ action: 'accept', content: { answer: 'a' } }, questionRequest('One'))))
      .toEqual({ kind: 'label', text: 'One: a' })
    expect(zcodeControlResponseSummary(response({ action: 'accept', content: { answer: 'a' } }, questionRequest('One', 'Two')))).toBeNull()
  })

  it('normalizes strings and string arrays as the native consumer does', () => {
    expect(zcodeControlResponseSummary(response({ action: 'accept', content: { answer_0: '  value  ', answer_1: [' a ', false, '', ' b '] } }, questionRequest('One', 'Two'))))
      .toEqual({ kind: 'label', text: 'One: value\nTwo: a, b' })
  })

  it('does not show answers that the native consumer discards', () => {
    expect(zcodeControlResponseSummary(response({ action: 'accept', content: { answers: {}, answer_0: 'ignored' } }, questionRequest('One')))).toBeNull()
    expect(zcodeControlResponseSummary(response({ action: 'accept', content: { answers: { Unrelated: 'ignored' } } }, questionRequest('One')))).toBeNull()
    expect(zcodeControlResponseSummary(response({ action: 'accept', content: { answer_0: 0, answer_1: false, answer_2: ' ' } }, questionRequest('One', 'Two', 'Three')))).toBeNull()
  })

  it.each([
    ['decline', 'Reject'],
    ['cancel', 'Cancel'],
  ])('renders %s without claiming that its ignored reason reached the model', (action, text) => {
    expect(zcodeControlResponseSummary(response({ action, reason: 'The native mapper drops this field.' }, questionRequest('One'))))
      .toEqual({ kind: 'label', text })
  })

  it.each([
    { answer: 'approve' },
    { answer_0: 'approve' },
    { answers: { 'Review this implementation plan.': 'approve' } },
  ])('recognizes the native plan approval answer %j', (content) => {
    expect(zcodeControlResponseSummary(planResponse({ action: 'accept', content }))).toEqual({ kind: 'label', text: 'Approve' })
  })

  it('preserves native plan feedback and refuses to infer approval from an empty accept', () => {
    expect(zcodeControlResponseSummary(planResponse({ action: 'accept', content: { answer: '  Add tests.  ' } })))
      .toEqual({ kind: 'feedback', message: 'Add tests.' })
    expect(zcodeControlResponseSummary(planResponse({ action: 'accept', content: {} }))).toEqual({ kind: 'label', text: 'Reject' })
    expect(zcodeControlResponseSummary(planResponse({ action: 'accept', content: { answers: { 'Review this implementation plan.': '' }, answer_0: 'approve' } })))
      .toEqual({ kind: 'label', text: 'Reject' })
  })

  /*
   * The saved row and the banner must agree about WHICH control arrived, and the tool
   * name is what decides -- the worker records it for exactly that (`zcode_control.go`),
   * and the banner reads nothing else. This display used to read `schema.interaction`
   * instead, so a request that carried one field and not the other read as a plan on one
   * surface and a question on the other.
   */
  it('reads the plan by the tool name the banner reads, with no schema beside it', () => {
    const bare = response({ action: 'accept', content: { answer: 'approve' } }, {}, ZCODE_METHOD.RequestUserInput, ZCODE_TOOL.ExitPlanMode)
    expect(zcodeControlResponseSummary(bare)).toEqual({ kind: 'label', text: 'Approve' })
  })

  it('reads a question as a question although its params carry the plan schema', () => {
    const question = response(
      { action: 'accept', content: { answers: { 'Which database?': 'Postgres' } } },
      { schema: { interaction: 'plan_approval' }, input: { questions: [{ question: 'Which database?' }] } },
    )
    expect(zcodeControlResponseSummary(question)).toEqual({ kind: 'label', text: 'Which database?: Postgres' })
  })

  // `zcodeQuestionRecords` is the provider's ONE question list. The reader answers it and
  // this display reads the saved answer back through it, so the two cannot disagree about
  // which question an answer belongs to.
  it('reads the same question list the reader answered', () => {
    const params = { schema: { questions: [{ question: 'From the schema' }] }, input: { questions: [{ question: 'From the input' }] } }
    expect(zcodeQuestionsFromPayload({ params }).map(question => question.question)).toEqual(['From the schema'])
    expect(zcodeControlResponseSummary(response({ action: 'accept', content: { answer: 'yes' } }, params)))
      .toEqual({ kind: 'label', text: 'From the schema: yes' })
  })

  // The shared control shows a header-only question by its header and keys the answer map
  // by that text, so the display must look it up under the same text.
  it('reads a header-only question back under its header', () => {
    expect(zcodeControlResponseSummary(response({ action: 'accept', content: { answers: { Databases: 'Postgres' } } }, { input: { questions: [{ header: 'Databases' }] } })))
      .toEqual({ kind: 'label', text: 'Databases: Postgres' })
  })

  // The worker's positional `answer_<index>` keys count EVERY question the request
  // declared, so a question nothing can answer still holds its place in the list.
  it('keeps the positional index of a question with no text of its own', () => {
    expect(zcodeControlResponseSummary(response({ action: 'accept', content: { answer_0: 'dropped', answer_1: 'kept' } }, { input: { questions: [{}, { question: 'Second' }] } })))
      .toEqual({ kind: 'label', text: 'Second: kept' })
  })

  it('returns no derived label for an unknown or corrupt native response', () => {
    expect(zcodeControlResponseSummary(response({ action: 'unknown' }))).toBeNull()
    expect(zcodeControlResponseSummary(response({ decision: 'unknown' }, {}, ZCODE_METHOD.RequestPermission))).toBeNull()
    expect(zcodeControlResponseSummary({ ...response({ action: 'accept' }), response: undefined })).toBeNull()
    expect(zcodeControlResponseSummary({ ...response({ action: 'accept' }), request: undefined })).toBeNull()
    expect(zcodeControlResponseSummary(response({ action: 'accept', content: { answer: 'ignored' } }, { questions: [null, 0, {}] }))).toBeNull()
  })
})
