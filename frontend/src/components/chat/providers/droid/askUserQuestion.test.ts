import type { ControlAnswerSeed } from '../../controls/types'
import { describe, expect, it } from 'vitest'
import { createControlAnswerState } from '../../controls/types'
import { droidIsQuestionRequest, droidQuestionsFromPayload } from './askUserQuestion'
import { droidControls } from './pluginControls'

/** One question of a `droid.ask_user` request, as the worker publishes it (`onAskUser`). */
interface DroidQuestion {
  index?: unknown
  question: string
  options: string[]
  multiSelect?: boolean
}

/** The question request that the worker publishes for one `droid.ask_user` request. */
function questionRequest(questions: DroidQuestion[]): Record<string, unknown> {
  return { type: 'ask_user_request', requestId: 'droid-ask-call-1', rpcId: 'rpc-ask-1', toolCallId: 'call-1', questions }
}

/** Droid numbers its questions from 1, in order. */
const COLOR_AND_SIZES = questionRequest([
  { index: 1, question: 'Which color?', options: ['Blue', 'Red'], multiSelect: false },
  { index: 2, question: 'Which sizes?', options: ['Small', 'Medium', 'Large'], multiSelect: true },
])

/**
 * The answers of the decision that the plugin sends for one request, read back from
 * the bytes that the composer hands to the worker.
 */
async function sentAnswers(payload: Record<string, unknown>, seed: ControlAnswerSeed): Promise<unknown> {
  const handling = droidControls.askUserQuestion!
  const replies: Array<{ response: { request_id: string, response: Record<string, unknown> } }> = []
  await handling.sendAnswer(
    { requestId: 'droid-ask-call-1', agentId: 'agent-1', payload },
    async (bytes) => {
      replies.push(JSON.parse(new TextDecoder().decode(bytes)))
    },
    handling.extractQuestions(payload),
    createControlAnswerState(seed),
  )
  expect(replies).toHaveLength(1)
  const [reply] = replies
  expect(reply?.response.request_id).toBe('droid-ask-call-1')
  expect(reply?.response.response.behavior).toBe('allow')
  return reply?.response.response.answers
}

describe('droidQuestionsFromPayload', () => {
  it('reads each question with its options and its kind', () => {
    expect(droidQuestionsFromPayload(COLOR_AND_SIZES)).toStrictEqual([
      { question: 'Which color?', options: [{ value: 'Blue', label: 'Blue' }, { value: 'Red', label: 'Red' }], multiSelect: false },
      {
        question: 'Which sizes?',
        options: [{ value: 'Small', label: 'Small' }, { value: 'Medium', label: 'Medium' }, { value: 'Large', label: 'Large' }],
        multiSelect: true,
      },
    ])
  })

  it('skips a question that is not an object, and an option that is not text', () => {
    const payload = { ...questionRequest([]), questions: ['not a question', null, { index: 1, question: 'Q', options: ['A', 7, null] }] }
    expect(droidQuestionsFromPayload(payload)).toStrictEqual([{ question: 'Q', options: [{ value: 'A', label: 'A' }], multiSelect: false }])
    expect(droidQuestionsFromPayload({ ...questionRequest([]), questions: 'Q' })).toStrictEqual([])
  })

  it('recognizes a question request', () => {
    expect(droidIsQuestionRequest(COLOR_AND_SIZES)).toBe(true)
    expect(droidIsQuestionRequest({ type: 'permission_request' })).toBe(false)
    expect(droidIsQuestionRequest({})).toBe(false)
  })
})

describe('droidControls askUserQuestion sendAnswer', () => {
  // Droid identifies each answer by the `index` of its question. The worker pairs
  // each answer with its question by that number alone.
  it('answers each question by its native index, in the order of the request', async () => {
    expect(await sentAnswers(COLOR_AND_SIZES, { selections: { 0: ['Red'], 1: ['Large'] } })).toStrictEqual([
      { index: 1, answer: 'Red' },
      { index: 2, answer: 'Large' },
    ])
  })

  it('keeps a native index that does not start at one', async () => {
    const payload = questionRequest([
      { index: 7, question: 'Seventh?', options: ['Yes', 'No'] },
      { index: 3, question: 'Third?', options: ['Yes', 'No'] },
    ])
    expect(await sentAnswers(payload, { selections: { 0: ['Yes'], 1: ['No'] } })).toStrictEqual([
      { index: 7, answer: 'Yes' },
      { index: 3, answer: 'No' },
    ])
  })

  // Droid's answer is one string. Its own TUI joins the chosen options in the order
  // of the options, then the typed text, with ", ".
  it('sends every pick of a multiple-choice question in the order of the options', async () => {
    expect(await sentAnswers(COLOR_AND_SIZES, { selections: { 0: ['Blue'], 1: ['Large', 'Small'] } })).toStrictEqual([
      { index: 1, answer: 'Blue' },
      { index: 2, answer: 'Small, Large' },
    ])
  })

  it('sends the typed text after the picks of a multiple-choice question', async () => {
    expect(await sentAnswers(COLOR_AND_SIZES, { selections: { 0: ['Blue'], 1: ['Medium'] }, customTexts: { 1: '  Extra large  ' } })).toStrictEqual([
      { index: 1, answer: 'Blue' },
      { index: 2, answer: 'Medium, Extra large' },
    ])
  })

  it('sends the typed text when nothing is picked', async () => {
    expect(await sentAnswers(COLOR_AND_SIZES, { customTexts: { 0: ' Green ', 1: 'Huge' } })).toStrictEqual([
      { index: 1, answer: 'Green' },
      { index: 2, answer: 'Huge' },
    ])
  })

  // The control keeps a choice and typed text exclusive for a single-choice
  // question, as Droid's own TUI does. When both still arrive, the choice is the
  // answer.
  it('answers a single-choice question with its choice when text arrives beside it', async () => {
    expect(await sentAnswers(COLOR_AND_SIZES, { selections: { 0: ['Red'], 1: ['Small'] }, customTexts: { 0: 'Green' } })).toStrictEqual([
      { index: 1, answer: 'Red' },
      { index: 2, answer: 'Small' },
    ])
  })

  it('drops a pick that the question does not offer', async () => {
    expect(await sentAnswers(COLOR_AND_SIZES, { selections: { 0: ['Purple', 'Blue'], 1: ['Huge', 'Medium'] } })).toStrictEqual([
      { index: 1, answer: 'Blue' },
      { index: 2, answer: 'Medium' },
    ])
  })

  // Droid's own TUI sends "" for a question that holds no choice.
  it('sends an empty answer for a question with no pick and no text', async () => {
    expect(await sentAnswers(COLOR_AND_SIZES, { selections: { 0: ['Blue'] }, customTexts: { 1: '   ' } })).toStrictEqual([
      { index: 1, answer: 'Blue' },
      { index: 2, answer: '' },
    ])
  })

  // A question with no number cannot identify its answer. The answer goes without
  // one, and the worker refuses the set, so the reader sees the refusal.
  it('sends no index for a question whose index is not a whole number', async () => {
    const payload = questionRequest([
      { question: 'No number?', options: ['Yes'] },
      { index: '2', question: 'Text number?', options: ['Yes'] },
      { index: 1.5, question: 'Fraction?', options: ['Yes'] },
    ])
    expect(await sentAnswers(payload, { selections: { 0: ['Yes'], 1: ['Yes'], 2: ['Yes'] } })).toStrictEqual([
      { answer: 'Yes' },
      { answer: 'Yes' },
      { answer: 'Yes' },
    ])
  })

  it('sends an empty answer list for a request with no question', async () => {
    expect(await sentAnswers(questionRequest([]), {})).toStrictEqual([])
  })
})
