import type { ControlPrompt } from '../../model/controlPrompt'
import type { PersistedControlResponse } from '../../persistedControlResponse'
import { describe, expect, it } from 'vitest'
import { GENERIC_PERMISSION_PROMPT } from '~/test-support/controlRequests'
import { buildAllowResponse, buildDenyResponse } from '~/test-support/controlResponses'
import { antigravityControlResponseSummary } from './controlResponse'

/** The native questions of one `askQuestion` interaction, as the language server states them. */
const NATIVE_QUESTIONS = [
  {
    question: 'First?',
    options: [
      { id: 'a', text: 'A' },
      { id: 'b', text: 'B' },
    ],
    isMultiSelect: false,
  },
  {
    question: 'Second?',
    options: [
      { id: 'x', text: 'X' },
      { id: 'y', text: 'Y' },
    ],
    isMultiSelect: true,
  },
]

/** The stored question request that the Worker publishes for the interaction. */
const QUESTION_REQUEST = {
  requestedInteraction: {
    askQuestion: { questions: NATIVE_QUESTIONS },
  },
}

/** The prompt that the Worker reads from the question request: each option's value is its id. */
const QUESTION_PROMPT: ControlPrompt = {
  kind: 'question',
  questions: [
    {
      question: 'First?',
      options: [
        { label: 'A', value: 'a' },
        { label: 'B', value: 'b' },
      ],
    },
    {
      question: 'Second?',
      options: [
        { label: 'X', value: 'x' },
        { label: 'Y', value: 'y' },
      ],
      multiSelect: true,
    },
  ],
}

/** The language server's own answer to one interaction, as the Worker stores it. */
function interactionAnswer(interaction: Record<string, unknown>): Record<string, unknown> {
  return {
    cascadeId: 'cascade',
    interaction: { trajectoryId: 'trajectory', stepIndex: 4, ...interaction },
  }
}

/** One saved row: the stored answer, the native request, and the prompt that the Worker recorded. */
function saved(response: Record<string, unknown>, request: Record<string, unknown>, prompt?: ControlPrompt): PersistedControlResponse {
  return {
    requestId: 'antigravity:cascade:trajectory:4',
    claimToken: 'claim',
    request,
    response,
    ...(prompt
      ? { prompt }
      : {}),
  }
}

/** A saved answer to the question interaction, with the given responses, under a question prompt. */
function answered(responses: unknown[], prompt: ControlPrompt = QUESTION_PROMPT): PersistedControlResponse {
  return saved(interactionAnswer({
    askQuestion: { responses },
  }), QUESTION_REQUEST, prompt)
}

describe('antigravityControlResponseSummary', () => {
  it('reads an answer in the neutral behavior envelope through the shared reader', () => {
    expect(antigravityControlResponseSummary(saved(buildAllowResponse('antigravity-plan:review-1', {}), {})))
      .toEqual({ kind: 'label', text: 'Allow' })
    expect(antigravityControlResponseSummary(saved(buildDenyResponse('antigravity-plan:review-1', 'Revise the plan'), {})))
      .toEqual({ kind: 'feedback', message: 'Revise the plan' })
  })

  it('reads a saved plan review with the words of its plan buttons', () => {
    const plan: ControlPrompt = { kind: 'plan', text: '1. Do it' }
    expect(antigravityControlResponseSummary(saved(buildAllowResponse('antigravity-plan:review-1', {}), {}, plan)))
      .toEqual({ kind: 'label', text: 'Approve' })
    expect(antigravityControlResponseSummary(saved(buildDenyResponse('antigravity-plan:review-1', ''), {}, plan)))
      .toEqual({ kind: 'label', text: 'Reject' })
  })

  it('reads a native permission answer as Allow or Deny', () => {
    const request = {
      requestedInteraction: {
        permission: {
          resource: { action: 'command', target: 'ls' },
        },
      },
    }
    expect(antigravityControlResponseSummary(saved(interactionAnswer({
      permission: { allow: true, scope: 'PERMISSION_SCOPE_ONCE' },
    }), request, GENERIC_PERMISSION_PROMPT)))
      .toEqual({ kind: 'label', text: 'Allow' })
    expect(antigravityControlResponseSummary(saved(interactionAnswer({
      permission: { allow: false, scope: 'PERMISSION_SCOPE_ONCE', userDenyInstruction: 'Use another approach' },
    }), request, GENERIC_PERMISSION_PROMPT)))
      .toEqual({ kind: 'label', text: 'Deny' })
  })

  it('reads a cancelled question as Dismissed', () => {
    expect(antigravityControlResponseSummary(saved(interactionAnswer({
      askQuestion: { cancelled: true },
    }), QUESTION_REQUEST, QUESTION_PROMPT)))
      .toEqual({ kind: 'label', text: 'Dismissed' })
  })

  it('shows each answer with the question and the option labels of the recorded prompt', () => {
    expect(antigravityControlResponseSummary(answered([
      {
        ...NATIVE_QUESTIONS[0],
        selectedOptionIds: ['b'],
      },
      {
        ...NATIVE_QUESTIONS[1],
        selectedOptionIds: ['x', 'y'],
      },
    ])))
      .toEqual({ kind: 'label', text: 'First?: B\nSecond?: X, Y' })
  })

  it('shows typed text in place of the options, and a skipped question as Skipped', () => {
    expect(antigravityControlResponseSummary(answered([
      {
        ...NATIVE_QUESTIONS[0],
        selectedOptionIds: ['a'],
        writeInResponse: 'Something else',
      },
      { ...NATIVE_QUESTIONS[1], skipped: true },
    ])))
      .toEqual({ kind: 'label', text: 'First?: Something else\nSecond?: Skipped' })
  })

  // The prompt is what the banner drew, so its words label the answer, and the native
  // request does not.
  it('reads the question words from the recorded prompt, not from the native request', () => {
    const recorded: ControlPrompt = {
      kind: 'question',
      questions: [
        {
          question: 'The recorded question',
          options: [
            { label: 'The recorded B', value: 'b' },
          ],
        },
      ],
    }
    expect(antigravityControlResponseSummary(answered([
      {
        ...NATIVE_QUESTIONS[0],
        selectedOptionIds: ['b'],
      },
    ], recorded)))
      .toEqual({ kind: 'label', text: 'The recorded question: The recorded B' })
  })

  // Without a question prompt, the row still states the answer: each question by its
  // position, and each option by the id that the answer carries.
  it('numbers the questions and shows the option ids when the row states no question prompt', () => {
    const responses = [
      {
        ...NATIVE_QUESTIONS[0],
        selectedOptionIds: ['b'],
      },
      { ...NATIVE_QUESTIONS[1], writeInResponse: 'Typed' },
    ]
    const expected = { kind: 'label', text: 'Question 1: b\nQuestion 2: Typed' }
    const response = interactionAnswer({
      askQuestion: { responses },
    })
    expect(antigravityControlResponseSummary(saved(response, QUESTION_REQUEST))).toEqual(expected)
    expect(antigravityControlResponseSummary(saved(response, QUESTION_REQUEST, GENERIC_PERMISSION_PROMPT))).toEqual(expected)
  })

  it('shows an option id that the prompt does not offer as the id', () => {
    expect(antigravityControlResponseSummary(answered([
      {
        ...NATIVE_QUESTIONS[0],
        selectedOptionIds: ['z', 'a'],
      },
    ])))
      .toEqual({ kind: 'label', text: 'First?: z, A' })
  })

  it('skips a response that is not an object or that states nothing', () => {
    expect(antigravityControlResponseSummary(answered([
      null,
      {
        ...NATIVE_QUESTIONS[1],
        selectedOptionIds: ['y'],
      },
    ])))
      .toEqual({ kind: 'label', text: 'Second?: Y' })
    expect(antigravityControlResponseSummary(answered([
      { ...NATIVE_QUESTIONS[0] },
    ])))
      .toBeNull()
    expect(antigravityControlResponseSummary(answered([]))).toBeNull()
  })

  it('reads nothing from an answer that states no known interaction', () => {
    expect(antigravityControlResponseSummary(saved(interactionAnswer({}), QUESTION_REQUEST, QUESTION_PROMPT))).toBeNull()
    expect(antigravityControlResponseSummary(saved({}, QUESTION_REQUEST, QUESTION_PROMPT))).toBeNull()
  })
})
