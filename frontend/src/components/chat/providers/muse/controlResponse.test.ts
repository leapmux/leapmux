import type { ControlPrompt } from '../../model/controlPrompt'
import type { ControlQuestion } from '../../model/question'
import type { PersistedControlResponse } from '../../persistedControlResponse'
import { GENERIC_PERMISSION_PROMPT } from '~/test-support/controlRequests'
import { museControlResponseSummary } from './controlResponse'

function questionRequest(questions: unknown): Record<string, unknown> {
  return {
    method: 'userInput/requested',
    params: { sessionId: 'session', userInputId: 'input', questions },
  }
}

/** The prompt that the Worker reads from a question request: its questions, in their order. */
function questionPrompt(questions: ControlQuestion[]): ControlPrompt {
  return { kind: 'question', questions, preservesSelectionNotes: true }
}

/** One single-choice question, as the Worker reads it from the native request. */
function singleChoice(id: string, question: string, header: string, labels: string[]): ControlQuestion {
  return {
    id,
    header,
    question,
    options: labels.map(label => ({ label, value: label })),
    multiSelect: false,
  }
}

function answered(answers: unknown): PersistedControlResponse {
  return {
    requestId: 'req-1',
    claimToken: 'claim',
    request: questionRequest([
      {
        id: 'q1',
        question: 'Choose the native style',
        header: 'Style',
        options: [
          { label: 'Alpha' },
          { label: 'Beta' },
        ],
        selection: { mode: 'single' },
      },
    ]),
    prompt: questionPrompt([
      singleChoice('q1', 'Choose the native style', 'Style', ['Alpha', 'Beta']),
    ]),
    response: {
      jsonrpc: '2.0',
      id: 1,
      method: 'userInput/answer',
      params: { sessionId: 'session', userInputId: 'input', answers },
    },
  }
}

/** The same row with no recorded prompt. */
function unprompted(cr: PersistedControlResponse): PersistedControlResponse {
  const { prompt: _prompt, ...rest } = cr
  return rest
}

describe('museControlResponseSummary', () => {
  it('shows the question and its selected answer', () => {
    expect(museControlResponseSummary(answered([
      { questionId: 'q1', selectedLabel: 'Beta' },
    ])))
      .toEqual({ kind: 'label', text: 'Choose the native style: Beta' })
  })

  it('joins a multiple selection and rides its note on the same line', () => {
    expect(museControlResponseSummary(answered([
      {
        questionId: 'q1',
        selectedLabels: ['Alpha', 'Beta'],
        note: 'Both fit.',
      },
    ])))
      .toEqual({ kind: 'label', text: 'Choose the native style: Alpha, Beta, Both fit.' })
  })

  it('shows a typed answer in place of the options', () => {
    expect(museControlResponseSummary(answered([
      { questionId: 'q1', freeText: 'A native typed answer' },
    ])))
      .toEqual({ kind: 'label', text: 'Choose the native style: A native typed answer' })
  })

  it('shows one line per answered question in the asked order', () => {
    const cr = answered([
      { questionId: 'q2', selectedLabel: 'Second' },
      { questionId: 'q1', selectedLabel: 'First' },
    ])
    cr.request = questionRequest([
      {
        id: 'q1',
        question: 'First choice',
        header: 'One',
        options: [
          { label: 'First' },
        ],
        selection: { mode: 'single' },
      },
      {
        id: 'q2',
        question: 'Second choice',
        header: 'Two',
        options: [
          { label: 'Second' },
        ],
        selection: { mode: 'single' },
      },
    ])
    cr.prompt = questionPrompt([
      singleChoice('q1', 'First choice', 'One', ['First']),
      singleChoice('q2', 'Second choice', 'Two', ['Second']),
    ])
    expect(museControlResponseSummary(cr)).toEqual({ kind: 'label', text: 'First choice: First\nSecond choice: Second' })
  })

  // The prompt, not the native request, states the questions and their order.
  it('reads the questions from the recorded prompt, not from the native request', () => {
    const cr = answered([
      { questionId: 'q1', selectedLabel: 'Beta' },
    ])
    cr.prompt = questionPrompt([
      singleChoice('q1', 'The recorded question', 'Style', ['Alpha', 'Beta']),
    ])
    expect(museControlResponseSummary(cr)).toEqual({ kind: 'label', text: 'The recorded question: Beta' })
  })

  it('skips a question that has no answer', () => {
    const cr = answered([
      { questionId: 'q2', selectedLabel: 'Second' },
    ])
    cr.prompt = questionPrompt([
      singleChoice('q1', 'First choice', 'One', ['First']),
      singleChoice('q2', 'Second choice', 'Two', ['Second']),
    ])
    expect(museControlResponseSummary(cr)).toEqual({ kind: 'label', text: 'Second choice: Second' })
  })

  it.each([
    [
      'an absent result',
      answered(undefined as unknown as unknown[]),
    ],
    [
      'a non-array result',
      answered({}),
    ],
    [
      'an unreadable entry',
      answered([null]),
    ],
    [
      'an answer to a question the prompt did not ask',
      answered([
        { questionId: 'q9', selectedLabel: 'Beta' },
      ]),
    ],
    [
      'an empty answer',
      answered([
        { questionId: 'q1' },
      ]),
    ],
    [
      'two answers to one question',
      answered([
        { questionId: 'q1', selectedLabel: 'Alpha' },
        { questionId: 'q1', selectedLabel: 'Beta' },
      ]),
    ],
    [
      'a row that states no prompt',
      unprompted(answered([
        { questionId: 'q1', selectedLabel: 'Beta' },
      ])),
    ],
    [
      'a row whose prompt is not a question',
      {
        ...answered([
          { questionId: 'q1', selectedLabel: 'Beta' },
        ]),
        prompt: GENERIC_PERMISSION_PROMPT,
      },
    ],
    [
      'an approval envelope',
      {
        requestId: 'r',
        claimToken: 'c',
        request: questionRequest([]),
        response: {
          jsonrpc: '2.0',
          id: 2,
          method: 'approval/decide',
          params: { sessionId: 'session', approvalId: 'approval', choiceId: 'deny' },
        },
      },
    ],
  ])('degrades the neutral label for %s', (_label, cr) => {
    expect(museControlResponseSummary(cr as PersistedControlResponse)).toBeNull()
  })
})

describe('museControlResponseSummary of an approval', () => {
  /** The permission prompt that the Worker reads from a Muse approval: one option per native choice. */
  const APPROVAL_PROMPT: ControlPrompt = {
    kind: 'permission',
    permission: {
      title: 'Run the command',
      options: [
        { optionId: 'allow-once', kind: 'allow_once', name: 'Allow once' },
        {
          optionId: 'allow-session',
          kind: 'allow_always',
          name: 'Allow for this session',
          scope: 'session',
        },
        { optionId: 'deny', kind: 'reject_once', name: 'Deny' },
      ],
    },
  }

  /** A saved `approval/decide` answer with these params. */
  function decided(params: Record<string, unknown>): PersistedControlResponse {
    return {
      requestId: 'approval',
      claimToken: 'claim',
      request: {
        method: 'approval/requested',
        params: { sessionId: 'session', approvalId: 'approval' },
      },
      response: {
        jsonrpc: '2.0',
        id: 2,
        method: 'approval/decide',
        params: {
          sessionId: 'session',
          approvalId: 'approval',
          requirementId: 'r',
          ...params,
        },
      },
      prompt: APPROVAL_PROMPT,
    }
  }

  it.each([
    ['allow-once', 'Allow once'],
    ['allow-session', 'Allow for this session'],
    ['deny', 'Deny'],
  ])('shows the label of the chosen option %s', (choiceId, text) => {
    expect(museControlResponseSummary(decided({ choiceId }))).toEqual({ kind: 'label', text })
  })

  it('shows the typed feedback of a refusal', () => {
    expect(museControlResponseSummary(decided({ choiceId: 'deny', feedback: '  Use the safer command  ' }))).toEqual({ kind: 'feedback', message: 'Use the safer command' })
  })

  it('shows the label for feedback that is only white space', () => {
    expect(museControlResponseSummary(decided({ choiceId: 'deny', feedback: '   ' }))).toEqual({ kind: 'label', text: 'Deny' })
  })

  it.each([
    [
      'a choice that the prompt does not offer',
      { choiceId: 'later' },
    ],
    ['no choice', {}],
    [
      'an empty choice',
      { choiceId: '' },
    ],
    [
      'a choice that is no string',
      { choiceId: 7 },
    ],
  ])('reads nothing for %s', (_label, params) => {
    expect(museControlResponseSummary(decided(params))).toBeNull()
  })
})
