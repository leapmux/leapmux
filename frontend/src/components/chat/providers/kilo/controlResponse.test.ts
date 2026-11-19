import type { ControlPrompt } from '../../model/controlPrompt'
import type { ControlQuestion } from '../../model/question'
import type { PersistedControlResponse } from '../../persistedControlResponse'
import { describe, expect, it } from 'vitest'
import { resolveControlResponseSummary } from '../../persistedControlResponse'
import { kiloControlResponseSummary, kiloQuestionAnswersText } from './controlResponse'

const QUESTION_REQUEST = {
  type: 'question.asked',
  properties: {
    questions: [
      { header: 'Task', question: 'Pick a task' },
      { header: 'Env', question: 'Pick an environment' },
    ],
  },
}

/** One question as `providerkit.QuestionsFromWire` in Go reads it. */
function question(fields: Partial<ControlQuestion>): ControlQuestion {
  return { question: '', options: [], ...fields }
}

/** The questions that the Worker reads from {@link QUESTION_REQUEST}. */
const QUESTIONS: ControlQuestion[] = [
  question({ header: 'Task', question: 'Pick a task' }),
  question({ header: 'Env', question: 'Pick an environment' }),
]

/** The prompt that the Worker records for {@link QUESTION_REQUEST}. */
const QUESTION_PROMPT: ControlPrompt = { kind: 'question', questions: QUESTIONS }

const PERMISSION_REQUEST = {
  method: 'session/request_permission',
  params: {
    options: [
      { optionId: 'proceed_once', name: 'Allow once' },
    ],
  },
}

/** The prompt that the Worker records for {@link PERMISSION_REQUEST}. */
const PERMISSION_PROMPT: ControlPrompt = {
  kind: 'permission',
  permission: {
    options: [
      { optionId: 'proceed_once', kind: 'other', name: 'Allow once' },
      { optionId: 'cancel', kind: 'reject_once', name: 'Cancel' },
    ],
  },
}

/** One saved row. */
function row(request: Record<string, unknown> | undefined, response: Record<string, unknown>, prompt?: ControlPrompt): PersistedControlResponse {
  return {
    claimToken: 'claim-1',
    requestId: 'q1',
    request,
    response,
    ...(prompt
      ? { prompt }
      : {}),
  }
}

describe('kiloQuestionAnswersText', () => {
  it('renders header-labeled answer lines', () => {
    expect(kiloQuestionAnswersText(QUESTIONS, {
      result: {
        answers: [
          ['Build'],
          ['Dev'],
        ],
      },
    }))
      .toBe('Task: Build\nEnv: Dev')
  })

  it('labels a rejected answer as Reject', () => {
    expect(kiloQuestionAnswersText(QUESTIONS, {
      result: { rejected: true },
    })).toBe('Reject')
  })

  it('falls back to question text then a positional label', () => {
    expect(kiloQuestionAnswersText([
      question({ question: 'Pick a task' }),
      question({}),
    ], {
      result: {
        answers: [
          ['Build'],
          ['Dev'],
        ],
      },
    }))
      .toBe('Pick a task: Build\nQuestion 2: Dev')
  })

  it('labels an answer group beyond the questions by its position', () => {
    expect(kiloQuestionAnswersText([], {
      result: {
        answers: [
          ['Build'],
        ],
      },
    }))
      .toBe('Question 1: Build')
  })

  it('drops answer groups whose values are all empty', () => {
    expect(kiloQuestionAnswersText(QUESTIONS, {
      result: {
        answers: [
          ['Build'],
          ['  ', ''],
        ],
      },
    }))
      .toBe('Task: Build')
  })

  it('returns null when nothing renders', () => {
    expect(kiloQuestionAnswersText(QUESTIONS, {
      result: { answers: [] },
    })).toBeNull()
    expect(kiloQuestionAnswersText(QUESTIONS, {
      result: { answers: 'Build' },
    })).toBeNull()
  })
})

describe('kiloControlResponseSummary', () => {
  it('renders question answers for a question prompt', () => {
    expect(kiloControlResponseSummary(row(QUESTION_REQUEST, {
      result: {
        answers: [
          ['Build'],
        ],
      },
    }, QUESTION_PROMPT))).toEqual({ kind: 'label', text: 'Task: Build' })
  })

  // The question headers come from the prompt that the banner drew, not from the
  // stored request.
  it('labels question answers with the questions of the recorded prompt, not of the native request', () => {
    const prompt: ControlPrompt = {
      kind: 'question',
      questions: [
        question({ header: 'Recorded' }),
      ],
    }
    expect(kiloControlResponseSummary(row(QUESTION_REQUEST, {
      result: {
        answers: [
          ['Build'],
        ],
      },
    }, prompt))).toEqual({ kind: 'label', text: 'Recorded: Build' })
  })

  it('delegates to the ACP permission path for a permission prompt', () => {
    expect(kiloControlResponseSummary(row(PERMISSION_REQUEST, {
      result: {
        outcome: { optionId: 'proceed_once' },
      },
    }, PERMISSION_PROMPT))).toEqual({ kind: 'label', text: 'Allow once' })
  })

  // The prompt lives in the answer row's own metadata, so a row whose request is gone
  // still reads the questions and their headers.
  describe('request-gone (pruned request absent)', () => {
    it('renders question answers under the headers of the recorded prompt', () => {
      expect(kiloControlResponseSummary(row(undefined, {
        result: {
          answers: [
            ['Build'],
            ['Dev'],
          ],
        },
      }, QUESTION_PROMPT))).toEqual({ kind: 'label', text: 'Task: Build\nEnv: Dev' })
    })

    it('renders a rejected question', () => {
      expect(kiloControlResponseSummary(row(undefined, {
        result: { rejected: true },
      }, QUESTION_PROMPT))).toEqual({ kind: 'label', text: 'Reject' })
    })

    it('still resolves a permission via the response-based ACP path', () => {
      expect(kiloControlResponseSummary(row(undefined, {
        result: {
          outcome: { optionId: 'proceed_once' },
        },
      }, PERMISSION_PROMPT))).toEqual({ kind: 'label', text: 'Allow once' })
    })
  })

  // The prompt states which request the answer belongs to. A question answer whose row
  // records none reads as nothing here, and the shared chokepoint shows its generic
  // label.
  it('reads nothing from a question answer whose row records no prompt', () => {
    const cr = row(QUESTION_REQUEST, {
      result: {
        answers: [
          ['Build'],
        ],
      },
    })
    expect(kiloControlResponseSummary(cr)).toBeNull()
    expect(resolveControlResponseSummary(cr, kiloControlResponseSummary)).toEqual({ kind: 'label', text: 'Responded' })
  })
})
