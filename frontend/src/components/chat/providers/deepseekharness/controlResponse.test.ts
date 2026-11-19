import type { ControlPrompt } from '../../model/controlPrompt'
import type { PersistedControlResponse } from '../../persistedControlResponse'
import { describe, expect, it } from 'vitest'
import { DEEPSEEK_HARNESS_CONTROL_EVENT } from '~/generated/contracts/deepseek-harness-protocol'
import { buildControlResponseEnvelope, buildDenyResponse } from '~/test-support/controlResponses'
import { deepseekHarnessControlResponseSummary } from './controlResponse'

/** The permission prompt that the Worker reads from a native approval. */
const APPROVAL_PROMPT: ControlPrompt = {
  kind: 'permission',
  permission: {
    title: 'bash',
    options: [
      { optionId: 'allow', kind: 'allow_once', name: 'Allow once' },
      { optionId: 'deny', kind: 'reject_once', name: 'Deny' },
    ],
  },
}

/** The plan prompt that the Worker reads from a plan-review question. */
const PLAN_PROMPT: ControlPrompt = {
  kind: 'plan',
  text: '1. Do it',
  choices: [
    { id: 'Approve', label: 'Approve', effect: 'approve' },
    { id: 'Keep planning', label: 'Keep planning', effect: 'refuse' },
  ],
}

/** The question prompt that the Worker reads from a user-questions request. */
const QUESTION_PROMPT: ControlPrompt = {
  kind: 'question',
  questions: [
    {
      id: 'route',
      question: 'Choose a route',
      options: [
        { label: 'First' },
        { label: 'Second' },
      ],
    },
    {
      id: 'speed',
      header: 'Speed',
      question: '',
      options: [
        { label: 'Fast' },
      ],
    },
  ],
  preservesSelectionNotes: true,
}

/** One saved answer, as the Worker built and stored it, with the prompt that it recorded. */
function saved(response: Record<string, unknown>, prompt: ControlPrompt | null = APPROVAL_PROMPT): PersistedControlResponse {
  return {
    requestId: 'native-event',
    claimToken: 'native-claim',
    request: {
      event: DEEPSEEK_HARNESS_CONTROL_EVENT.Approval,
      eventId: 'native-event',
      request: { toolName: 'bash', callId: 'native-call' },
    },
    response,
    ...(prompt
      ? { prompt }
      : {}),
  }
}

/** The allow envelope that answers the questions with these native answers. */
function answered(answers: unknown): Record<string, unknown> {
  return buildControlResponseEnvelope('native-event', { behavior: 'allow', answers })
}

describe('deepseekHarnessControlResponseSummary', () => {
  it('reads an allowed approval as the prompt option it selected', () => {
    expect(deepseekHarnessControlResponseSummary(saved(buildControlResponseEnvelope('native-event', { behavior: 'allow' }))))
      .toStrictEqual({ kind: 'label', text: 'Allow once' })
  })

  it('reads a bare denial as the word its button carried', () => {
    expect(deepseekHarnessControlResponseSummary(saved(buildDenyResponse('native-event'))))
      .toStrictEqual({ kind: 'label', text: 'Deny' })
  })

  // The native approval reply carries no reason. The Worker answers `rejected` alone,
  // and sends the reason as the reader's next message, which the transcript draws as
  // a separate row. The saved row states the decision alone to prevent duplicate
  // feedback.
  it('reads a denial with a typed reason as the decision alone', () => {
    expect(deepseekHarnessControlResponseSummary(saved(buildDenyResponse('native-event', 'Use a dry run.'))))
      .toStrictEqual({ kind: 'label', text: 'Deny' })
  })

  it('reads the shared words for a permission prompt that offers no option of the decision', () => {
    const bare: ControlPrompt = {
      kind: 'permission',
      permission: { options: [] },
    }
    expect(deepseekHarnessControlResponseSummary(saved(buildControlResponseEnvelope('native-event', { behavior: 'allow' }), bare)))
      .toStrictEqual({ kind: 'label', text: 'Allow' })
    expect(deepseekHarnessControlResponseSummary(saved(buildDenyResponse('native-event'), bare)))
      .toStrictEqual({ kind: 'label', text: 'Deny' })
  })

  it('reads a plan review with the plan words, and a refusal with its reason', () => {
    expect(deepseekHarnessControlResponseSummary(saved(buildControlResponseEnvelope('native-event', { behavior: 'allow' }), PLAN_PROMPT)))
      .toStrictEqual({ kind: 'label', text: 'Approve' })
    expect(deepseekHarnessControlResponseSummary(saved(buildDenyResponse('native-event', 'Split it'), PLAN_PROMPT)))
      .toStrictEqual({ kind: 'feedback', message: 'Split it' })
    expect(deepseekHarnessControlResponseSummary(saved(buildDenyResponse('native-event'), PLAN_PROMPT)))
      .toStrictEqual({ kind: 'label', text: 'Keep planning' })
  })

  it('reads each answered question through its ID, with the header for a question with no text', () => {
    expect(deepseekHarnessControlResponseSummary(saved(answered([
      {
        id: 'speed',
        selected: ['Fast'],
      },
      {
        id: 'route',
        selected: ['Second'],
        custom: 'via the bridge',
      },
    ]), QUESTION_PROMPT))).toStrictEqual({ kind: 'label', text: 'Choose a route: Second, via the bridge\nSpeed: Fast' })
  })

  it('reads an answer set with nothing chosen as no answer', () => {
    expect(deepseekHarnessControlResponseSummary(saved(answered([
      { id: 'route', selected: [] },
      { id: 'speed', selected: [] },
    ]), QUESTION_PROMPT))).toStrictEqual({ kind: 'label', text: 'No answer' })
  })

  it('reads a dismissed question with its reason', () => {
    expect(deepseekHarnessControlResponseSummary(saved(buildDenyResponse('native-event', 'Not now'), QUESTION_PROMPT)))
      .toStrictEqual({ kind: 'feedback', message: 'Not now' })
    expect(deepseekHarnessControlResponseSummary(saved(buildDenyResponse('native-event'), QUESTION_PROMPT)))
      .toStrictEqual({ kind: 'label', text: 'Dismissed' })
  })

  it.each([
    [
      'an answer that misses a question',
      [
        {
          id: 'route',
          selected: ['First'],
        },
      ],
    ],
    [
      'an answer that selects an option that is not offered',
      [
        {
          id: 'route',
          selected: ['Third'],
        },
        { id: 'speed', selected: [] },
      ],
    ],
    [
      'an answer with no ID',
      [
        {
          selected: ['First'],
        },
        { id: 'speed', selected: [] },
      ],
    ],
    [
      'two answers to one question',
      [
        { id: 'route', selected: [] },
        { id: 'route', selected: [] },
      ],
    ],
    [
      'an answer that is no object',
      [
        'route',
        { id: 'speed', selected: [] },
      ],
    ],
    ['no answer list', undefined],
  ])('reads nothing for %s', (_label, answers) => {
    expect(deepseekHarnessControlResponseSummary(saved(answered(answers), QUESTION_PROMPT))).toBeNull()
  })

  it('reads nothing for an answer to another request', () => {
    expect(deepseekHarnessControlResponseSummary(saved(buildDenyResponse('other-event')))).toBeNull()
  })

  it('reads nothing for a row that records no prompt', () => {
    expect(deepseekHarnessControlResponseSummary(saved(buildDenyResponse('native-event'), null))).toBeNull()
  })
})
