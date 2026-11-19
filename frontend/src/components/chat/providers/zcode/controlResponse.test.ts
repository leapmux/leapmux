import type { ControlPrompt } from '../../model/controlPrompt'
import type { PersistedControlResponse } from '../../persistedControlResponse'
import { describe, expect, it } from 'vitest'
import { ZCODE_METHOD, ZCODE_TOOL } from '~/generated/contracts/zcode-protocol'
import { resolveControlResponseSummary } from '../../persistedControlResponse'
import { zcodeControlResponseSummary } from './controlResponse'

/**
 * The question prompt that the Worker records for a request: one question for each
 * text, with the header standing in for an absent question (`zcodeQuestions` in Go).
 */
function questionPrompt(...questions: string[]): ControlPrompt {
  return {
    kind: 'question',
    questions: questions.map(question => ({ question, options: [] })),
  }
}

/** The permission prompt that the Worker records for a request that offers no option. */
const PERMISSION_PROMPT: ControlPrompt = {
  kind: 'permission',
  permission: {
    title: ZCODE_TOOL.Bash,
    options: [
      { optionId: 'leapmux:allow', kind: 'allow_once' },
      { optionId: 'leapmux:deny', kind: 'reject_once' },
    ],
  },
}

/** The plan prompt that the Worker records for the plan approval. */
const PLAN_PROMPT: ControlPrompt = { kind: 'plan' }

/**
 * One saved answer, beside the request the worker stored for it and the prompt that
 * the Worker recorded with it.
 *
 * The stored request is a HYBRID: ZCode's own `params`, and the Claude-shaped header
 * whose `tool_name` states WHICH of the three prompts arrived (see
 * `providers/zcode/control.go`). The display reads neither: the prompt states the kind.
 */
function response(
  result: Record<string, unknown>,
  prompt: ControlPrompt | undefined,
  params: Record<string, unknown> = {},
  method: string = ZCODE_METHOD.RequestUserInput,
  toolName: string = ZCODE_TOOL.AskUserQuestion,
): PersistedControlResponse {
  return {
    requestId: 'request-1',
    claimToken: 'claim-1',
    request: {
      id: 7,
      method,
      params,
      request: { tool_name: toolName },
    },
    response: { id: 7, result },
    ...(prompt
      ? { prompt }
      : {}),
  }
}

/** One saved answer to a permission request. */
function permissionResponse(result: Record<string, unknown>): PersistedControlResponse {
  return response(result, PERMISSION_PROMPT, {}, ZCODE_METHOD.RequestPermission, ZCODE_TOOL.Bash)
}

/** One saved answer to the plan approval, which the worker records as `ExitPlanMode`. */
function planResponse(result: Record<string, unknown>): PersistedControlResponse {
  return response(result, PLAN_PROMPT, {
    schema: { interaction: 'plan_approval' },
  }, ZCODE_METHOD.RequestUserInput, ZCODE_TOOL.ExitPlanMode)
}

/** One saved answer to the questions of the prompt, with the native request carrying the same texts. */
function questionResponse(result: Record<string, unknown>, ...questions: string[]): PersistedControlResponse {
  return response(result, questionPrompt(...questions), {
    input: {
      questions: questions.map(question => ({ question })),
    },
  })
}

describe('zcodeControlResponseSummary', () => {
  it.each([
    ['allow', 'Allow'],
    ['deny', 'Deny'],
    ['escalate', 'Escalated'],
    ['modify', 'Modified'],
  ])('renders the native permission decision %s', (decision, text) => {
    expect(zcodeControlResponseSummary(permissionResponse({ decision }))).toEqual({ kind: 'label', text })
  })

  it('preserves a native permission rejection reason without attributing it to the user', () => {
    expect(zcodeControlResponseSummary(permissionResponse({ decision: 'deny', reason: 'No offered option allows this operation.' })))
      .toEqual({ kind: 'label', text: 'Deny\nNo offered option allows this operation.' })
  })

  it('renders native answers in the order of the recorded prompt', () => {
    expect(zcodeControlResponseSummary(questionResponse({
      action: 'accept',
      content: {
        answers: { Second: 'b', First: 'a' },
        answer_0: 'wrong',
        answer_1: 'wrong',
      },
    }, 'First', 'Second')))
      .toEqual({ kind: 'label', text: 'First: a\nSecond: b' })
  })

  it('reads a single answer to the one question of the prompt', () => {
    expect(zcodeControlResponseSummary(questionResponse({
      action: 'accept',
      content: { answer: 'Postgres' },
    }, 'Database?')))
      .toEqual({ kind: 'label', text: 'Database?: Postgres' })
  })

  it('uses positional answers when keyed answers are absent', () => {
    expect(zcodeControlResponseSummary(questionResponse({
      action: 'accept',
      content: { answer_0: 'a', answer_1: 'b', answer: 'ignored' },
    }, 'First', 'Second')))
      .toEqual({ kind: 'label', text: 'First: a\nSecond: b' })
  })

  it('keeps the final native value when question text repeats', () => {
    expect(zcodeControlResponseSummary(questionResponse({
      action: 'accept',
      content: { answer_0: 'first', answer_1: 'last' },
    }, 'Repeated', 'Repeated')))
      .toEqual({ kind: 'label', text: 'Repeated: last' })
  })

  it('uses a single answer only for one question', () => {
    expect(zcodeControlResponseSummary(questionResponse({
      action: 'accept',
      content: { answer: 'a' },
    }, 'One')))
      .toEqual({ kind: 'label', text: 'One: a' })
    expect(zcodeControlResponseSummary(questionResponse({
      action: 'accept',
      content: { answer: 'a' },
    }, 'One', 'Two'))).toBeNull()
  })

  it('normalizes strings and string arrays as the native consumer does', () => {
    expect(zcodeControlResponseSummary(questionResponse({
      action: 'accept',
      content: {
        answer_0: '  value  ',
        answer_1: [' a ', false, '', ' b '],
      },
    }, 'One', 'Two')))
      .toEqual({ kind: 'label', text: 'One: value\nTwo: a, b' })
  })

  it('does not show answers that the native consumer discards', () => {
    expect(zcodeControlResponseSummary(questionResponse({
      action: 'accept',
      content: { answers: {}, answer_0: 'ignored' },
    }, 'One'))).toBeNull()
    expect(zcodeControlResponseSummary(questionResponse({
      action: 'accept',
      content: {
        answers: { Unrelated: 'ignored' },
      },
    }, 'One'))).toBeNull()
    expect(zcodeControlResponseSummary(questionResponse({
      action: 'accept',
      content: { answer_0: 0, answer_1: false, answer_2: ' ' },
    }, 'One', 'Two', 'Three'))).toBeNull()
  })

  it.each([
    ['decline', 'Reject'],
    ['cancel', 'Cancel'],
  ])('renders %s without claiming that its ignored reason reached the model', (action, text) => {
    expect(zcodeControlResponseSummary(questionResponse({ action, reason: 'The native mapper drops this field.' }, 'One')))
      .toEqual({ kind: 'label', text })
  })

  it.each([
    { answer: 'approve' },
    { answer_0: 'approve' },
    {
      answers: { 'Review this implementation plan.': 'approve' },
    },
  ])('recognizes the native plan approval answer %j', (content) => {
    expect(zcodeControlResponseSummary(planResponse({ action: 'accept', content }))).toEqual({ kind: 'label', text: 'Approve' })
  })

  it('preserves native plan feedback and refuses to infer approval from an empty accept', () => {
    expect(zcodeControlResponseSummary(planResponse({
      action: 'accept',
      content: { answer: '  Add tests.  ' },
    })))
      .toEqual({ kind: 'feedback', message: 'Add tests.' })
    expect(zcodeControlResponseSummary(planResponse({ action: 'accept', content: {} }))).toEqual({ kind: 'label', text: 'Reject' })
    expect(zcodeControlResponseSummary(planResponse({
      action: 'accept',
      content: {
        answers: { 'Review this implementation plan.': '' },
        answer_0: 'approve',
      },
    })))
      .toEqual({ kind: 'label', text: 'Reject' })
  })

  it.each([
    ['decline', 'Reject'],
    ['cancel', 'Cancel'],
  ])('renders a plan reply that states %s', (action, text) => {
    expect(zcodeControlResponseSummary(planResponse({ action }))).toEqual({ kind: 'label', text })
  })

  /*
   * The saved row and the banner must agree about WHICH control arrived. The prompt
   * that the Worker recorded with the answer decides, because the banner drew that
   * prompt. Neither the tool name nor `schema.interaction` of the stored request
   * decides: a request that carried one field and not the other once read as a plan on
   * one surface and as a question on the other.
   */
  it('reads the plan by the recorded prompt, with no schema beside it', () => {
    expect(zcodeControlResponseSummary(response({
      action: 'accept',
      content: { answer: 'approve' },
    }, PLAN_PROMPT, {}, ZCODE_METHOD.RequestUserInput, ZCODE_TOOL.ExitPlanMode))).toEqual({ kind: 'label', text: 'Approve' })
  })

  it('reads a question as a question although its params carry the plan schema', () => {
    expect(zcodeControlResponseSummary(response(
      {
        action: 'accept',
        content: {
          answers: { 'Which database?': 'Postgres' },
        },
      },
      questionPrompt('Which database?'),
      {
        schema: { interaction: 'plan_approval' },
        input: {
          questions: [
            { question: 'Which database?' },
          ],
        },
      },
    ))).toEqual({ kind: 'label', text: 'Which database?: Postgres' })
  })

  it('reads a permission by the recorded prompt, whatever method the stored request states', () => {
    expect(zcodeControlResponseSummary(response({ decision: 'allow' }, PERMISSION_PROMPT))).toEqual({ kind: 'label', text: 'Allow' })
    expect(zcodeControlResponseSummary(response({
      action: 'accept',
      content: { answer: 'a' },
    }, PERMISSION_PROMPT, {}, ZCODE_METHOD.RequestPermission))).toBeNull()
  })

  // The questions come from the prompt that the banner drew, never from the stored
  // request, whose lists the Worker read in its own precedence.
  it('reads the question texts from the recorded prompt, not from the native request', () => {
    expect(zcodeControlResponseSummary(response(
      {
        action: 'accept',
        content: { answer: 'yes' },
      },
      questionPrompt('From the prompt'),
      {
        schema: {
          questions: [
            { question: 'From the schema' },
          ],
        },
        input: {
          questions: [
            { question: 'From the input' },
          ],
        },
      },
    ))).toEqual({ kind: 'label', text: 'From the prompt: yes' })
  })

  // The prompt states a header-only question by its header, and the Worker keys the
  // answer map by that text, so the display looks it up under the same text.
  it('reads a header-only question back under its header', () => {
    expect(zcodeControlResponseSummary(response({
      action: 'accept',
      content: {
        answers: { Databases: 'Postgres' },
      },
    }, questionPrompt('Databases'), {
      input: {
        questions: [
          { header: 'Databases' },
        ],
      },
    })))
      .toEqual({ kind: 'label', text: 'Databases: Postgres' })
  })

  // The prompt leaves out a record that nothing can answer. The Worker keys the
  // answer of every later question by its text, and positions it in the native list,
  // so the keyed answer reads the right question whatever its position.
  it('reads the keyed answer of a question after a record that the prompt left out', () => {
    expect(zcodeControlResponseSummary(response({
      action: 'accept',
      content: {
        answers: { Second: 'kept' },
        answer_1: 'kept',
      },
    }, questionPrompt('Second'), {
      input: {
        questions: [
          {},
          { question: 'Second' },
        ],
      },
    })))
      .toEqual({ kind: 'label', text: 'Second: kept' })
  })

  it('reads a question whose text spells a prototype member as its own answer', () => {
    expect(zcodeControlResponseSummary(questionResponse({
      action: 'accept',
      content: {
        answers: JSON.parse('{"toString":"a","__proto__":"b"}') as Record<string, unknown>,
      },
    }, 'toString', '__proto__', 'constructor')))
      .toEqual({ kind: 'label', text: 'toString: a\n__proto__: b' })
  })

  it('returns no derived label for an unknown or corrupt native response', () => {
    expect(zcodeControlResponseSummary(questionResponse({ action: 'unknown' }, 'One'))).toBeNull()
    expect(zcodeControlResponseSummary(permissionResponse({ decision: 'unknown' }))).toBeNull()
    expect(zcodeControlResponseSummary({
      ...questionResponse({ action: 'accept' }, 'One'),
      response: undefined,
    })).toBeNull()
    expect(zcodeControlResponseSummary(questionResponse({
      action: 'accept',
      content: { answer: 'ignored' },
    }))).toBeNull()
  })

  // The prompt lives in the answer row's own metadata, so a row whose request is gone
  // still reads its questions.
  it('reads an answer whose native request is gone', () => {
    expect(zcodeControlResponseSummary({
      ...questionResponse({
        action: 'accept',
        content: {
          answers: { One: 'a' },
        },
      }, 'One'),
      request: undefined,
    })).toEqual({ kind: 'label', text: 'One: a' })
  })

  // The prompt states which control the answer belongs to. A row with none reads as
  // nothing here, whatever its tool name, and the shared chokepoint shows its generic
  // label.
  it('reads nothing from a row that records no prompt, whatever its tool name', () => {
    const plan = response({
      action: 'accept',
      content: { answer: 'approve' },
    }, undefined, {}, ZCODE_METHOD.RequestUserInput, ZCODE_TOOL.ExitPlanMode)
    expect(zcodeControlResponseSummary(plan)).toBeNull()
    const permission = response({ decision: 'allow' }, undefined, {}, ZCODE_METHOD.RequestPermission, ZCODE_TOOL.Bash)
    expect(zcodeControlResponseSummary(permission)).toBeNull()
    expect(resolveControlResponseSummary(permission, zcodeControlResponseSummary)).toEqual({ kind: 'label', text: 'Responded' })
  })

  it('reads nothing for a prompt that the reply cannot answer', () => {
    const elicitation: ControlPrompt = {
      kind: 'elicitation',
      elicitation: { mode: 'form', message: 'Choose' },
    }
    expect(zcodeControlResponseSummary(response({
      action: 'accept',
      content: { answer_0: 'a' },
    }, elicitation))).toBeNull()
  })
})
