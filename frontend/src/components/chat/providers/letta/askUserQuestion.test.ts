import type { ControlAnswerSeed } from '../../controls/types'
import { describe, expect, it } from 'vitest'
import { createControlAnswerState } from '../../controls/types'
import { lettaIsQuestionRequest, lettaQuestionReceiptMessage, lettaQuestionsFromPayload } from './askUserQuestion'
import { lettaControls } from './pluginControls'

/**
 * The question request that the Worker publishes for one Letta Code receipt. The
 * questions are the native `AskUserQuestion` arguments, verbatim.
 */
const QUESTION_REQUEST: Record<string, unknown> = {
  type: 'ask_user',
  requestId: 'letta-question-ask-1',
  tool_name: 'AskUserQuestion',
  tool_call_id: 'ask-1',
  tool_input: {
    questions: [
      { question: 'Which color do you prefer?', header: 'Color', options: [{ label: 'Blue', description: 'The color blue' }, { label: 'Red', description: 'The color red' }] },
      { question: 'Which sizes?', header: 'Sizes', multiSelect: true, options: [{ label: 'Small', description: 'S' }, { label: 'Medium', description: 'M' }, { label: 'Large', description: 'L' }] },
    ],
  },
  suggestions: null,
}

/** The neutral answer that the shared question control sends to the Worker. */
interface SentAnswer {
  response: {
    request_id: string
    response: { behavior: string, updatedInput: { questions: unknown, answers: Record<string, string> } }
  }
}

/** The bytes that the composer sends to the Worker for one answer state. */
async function sentAnswer(seed: ControlAnswerSeed): Promise<SentAnswer> {
  const handling = lettaControls.askUserQuestion!
  const replies: SentAnswer[] = []
  await handling.sendAnswer(
    { requestId: 'letta-question-ask-1', agentId: 'agent-1', payload: QUESTION_REQUEST },
    async (bytes) => {
      replies.push(JSON.parse(new TextDecoder().decode(bytes)))
    },
    handling.extractQuestions(QUESTION_REQUEST),
    createControlAnswerState(seed),
  )
  expect(replies).toHaveLength(1)
  return replies[0]!
}

describe('lettaIsQuestionRequest', () => {
  it('recognizes the question request that the Worker publishes', () => {
    expect(lettaIsQuestionRequest(QUESTION_REQUEST)).toBe(true)
  })

  it('leaves a permission request to the permission banner', () => {
    expect(lettaIsQuestionRequest({ type: 'permission', requestId: 'perm-1', tool_name: 'AskUserQuestion', tool_input: {} })).toBe(false)
  })
})

describe('lettaQuestionsFromPayload', () => {
  it('reads each question with its option labels and its kind', () => {
    expect(lettaQuestionsFromPayload(QUESTION_REQUEST)).toStrictEqual([
      { question: 'Which color do you prefer?', options: [{ value: 'Blue', label: 'Blue' }, { value: 'Red', label: 'Red' }], multiSelect: false },
      { question: 'Which sizes?', options: [{ value: 'Small', label: 'Small' }, { value: 'Medium', label: 'Medium' }, { value: 'Large', label: 'Large' }], multiSelect: true },
    ])
  })

  it('reads no question from a payload that holds none', () => {
    expect(lettaQuestionsFromPayload({ type: 'ask_user', tool_input: {} })).toStrictEqual([])
    expect(lettaQuestionsFromPayload({ type: 'ask_user' })).toStrictEqual([])
  })
})

describe('the Letta question answer', () => {
  it('allows the request and folds the answers into the input of the question call', async () => {
    const sent = await sentAnswer({ selections: { 0: ['Red'], 1: ['Small'] } })
    expect(sent.response.request_id).toBe('letta-question-ask-1')
    expect(sent.response.response.behavior).toBe('allow')
    expect(sent.response.response.updatedInput.questions).toEqual((QUESTION_REQUEST.tool_input as Record<string, unknown>).questions)
  })

  it('sends the option that the reader picked for a single-choice question', async () => {
    const sent = await sentAnswer({ selections: { 0: ['Red'], 1: ['Small'] } })
    expect(sent.response.response.updatedInput.answers).toEqual({ 'Which color do you prefer?': 'Red', 'Which sizes?': 'Small' })
  })

  it('sends every option that the reader picked for a multiple-choice question', async () => {
    // Letta Code joins the picks of one multiple-choice answer with a comma and a space.
    const sent = await sentAnswer({ selections: { 0: ['Red'], 1: ['Small', 'Large'] } })
    expect(sent.response.response.updatedInput.answers['Which sizes?']).toBe('Small, Large')
  })

  it('sends the text that the reader typed when no option is picked', async () => {
    const sent = await sentAnswer({ selections: { 1: ['Medium'] }, customTexts: { 0: 'Green' } })
    expect(sent.response.response.updatedInput.answers['Which color do you prefer?']).toBe('Green')
  })
})

describe('the Letta question dismissal', () => {
  it('denies the request and carries the reason that the reader typed', async () => {
    const handling = lettaControls.askUserQuestion!
    const replies: Array<{ response: { request_id: string, response: { behavior: string, message: string } } }> = []
    await handling.sendReject(
      { requestId: 'letta-question-ask-1', agentId: 'agent-1', payload: QUESTION_REQUEST },
      async (bytes) => {
        replies.push(JSON.parse(new TextDecoder().decode(bytes)))
      },
      'Ask me later.',
    )
    expect(replies).toHaveLength(1)
    expect(replies[0]!.response.request_id).toBe('letta-question-ask-1')
    expect(replies[0]!.response.response).toEqual({ behavior: 'deny', message: 'Ask me later.' })
  })
})

describe('lettaQuestionReceiptMessage', () => {
  // The receipt of a live Letta Code 0.34.2 run, verbatim.
  const MESSAGE = 'Questions posted. Answers or dismissal will arrive later in a task notification. You may continue working; do not assume an answer.'
  const RECEIPT = {
    type: 'ask_user_question',
    version: 2,
    toolCallId: 'ask-1',
    questions: [{ question: 'Which color do you prefer?', header: 'Color', options: [{ label: 'Blue', description: 'The color blue' }, { label: 'Red', description: 'The color red' }] }],
    message: MESSAGE,
  }

  it('reads the message of a receipt', () => {
    expect(lettaQuestionReceiptMessage(RECEIPT)).toBe(MESSAGE)
  })

  it.each([
    ['no object', null],
    ['an object with no type', { message: 'Successfully replaced 1 occurrence' }],
    ['a response, not a receipt', { ...RECEIPT, type: 'ask_user_question_response' }],
    ['a receipt with no message', { ...RECEIPT, message: undefined }],
    ['a receipt with an empty message', { ...RECEIPT, message: '' }],
    ['a receipt with a blank message', { ...RECEIPT, message: '  \n' }],
    ['a receipt with a message that is no text', { ...RECEIPT, message: 7 }],
  ])('reads no message from %s', (_name, returned) => {
    expect(lettaQuestionReceiptMessage(returned)).toBeUndefined()
  })
})
