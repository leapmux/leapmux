import { describe, expect, it } from 'vitest'
import { createControlAnswerState } from '../../controls/types'
import {
  isQwenQuestionPayload,
  qwenQuestionAnswerLines,
  qwenQuestionReply,
  qwenQuestions,
  sendQwenQuestionRejectResponse,
  sendQwenQuestionResponse,
} from './askUserQuestion'

const QUESTIONS = [
  { question: 'Which color do you want?', header: 'Color', options: [{ label: 'Red', description: 'The red one' }, { label: 'Blue', description: 'The blue one' }], multiSelect: false },
  { question: 'Which extras?', header: 'Extras', options: [{ label: 'Cache', description: 'c' }, { label: 'Metrics', description: 'm' }], multiSelect: true },
]

/** Qwen's question dialog as the probe recorded it, with a second question. */
const PAYLOAD = {
  jsonrpc: '2.0',
  id: 3,
  method: 'session/request_permission',
  params: {
    sessionId: 's',
    options: [{ optionId: 'proceed_once', name: 'Submit', kind: 'allow_once' }, { optionId: 'cancel', name: 'Cancel', kind: 'reject_once' }],
    toolCall: {
      toolCallId: 'call_dd960f1f5c',
      status: 'pending',
      title: 'Ask user 2 questions',
      kind: 'think',
      rawInput: { questions: QUESTIONS },
      _meta: { toolName: 'ask_user_question', qwenInteractionKind: 'user_question', qwenQuestions: QUESTIONS },
    },
  },
}

async function sentReply(send: (respond: (bytes: Uint8Array) => Promise<void>) => Promise<void>): Promise<unknown[]> {
  const sent: unknown[] = []
  await send(async (bytes) => {
    sent.push(JSON.parse(new TextDecoder().decode(bytes)))
  })
  return sent
}

describe('isQwenQuestionPayload', () => {
  it('matches the permission request that its _meta marks as a question', () => {
    expect(isQwenQuestionPayload(PAYLOAD)).toBe(true)
    expect(isQwenQuestionPayload({ params: { toolCall: { _meta: { toolName: 'ask_user_question' } } } })).toBe(false)
    expect(isQwenQuestionPayload({ params: {} })).toBe(false)
  })
})

describe('qwenQuestions', () => {
  it('reads the questions from _meta', () => {
    expect(qwenQuestions(PAYLOAD).map(question => [question.header, question.multiSelect])).toEqual([['Color', false], ['Extras', true]])
  })

  it('falls back to the tool call arguments', () => {
    const payload = { params: { toolCall: { rawInput: { questions: [QUESTIONS[0]] }, _meta: { qwenInteractionKind: 'user_question' } } } }
    expect(qwenQuestions(payload).map(question => question.question)).toEqual(['Which color do you want?'])
  })

  it('answers no question for a payload that states none', () => {
    expect(qwenQuestions({ params: { toolCall: {} } })).toEqual([])
  })

  // `_meta` is wire data. A copy there that is no list is no copy, and the tool
  // call's own arguments still state the questions.
  it('falls back to the arguments when the _meta copy is no list', () => {
    const payload = { params: { toolCall: { rawInput: { questions: [QUESTIONS[1]] }, _meta: { qwenInteractionKind: 'user_question', qwenQuestions: 'Which extras?' } } } }
    expect(qwenQuestions(payload).map(question => question.question)).toEqual(['Which extras?'])
  })

  // The dialog draws checkboxes only for `true`. A multiSelect that is absent or is
  // no boolean states a single choice.
  it('reads multiSelect as true only for true', () => {
    const payload = { params: { toolCall: { _meta: { qwenQuestions: [
      { question: 'A?', options: [] },
      { question: 'B?', options: [], multiSelect: 'yes' },
      { question: 'C?', options: [], multiSelect: true },
    ] } } } }
    expect(qwenQuestions(payload).map(question => question.multiSelect)).toEqual([false, false, true])
  })
})

describe('qwenQuestionReply', () => {
  const questions = qwenQuestions(PAYLOAD)

  it('selects the submit option and keys each answer by the question index', () => {
    const state = createControlAnswerState({ selections: { 0: ['Blue'], 1: ['Cache', 'Metrics'] } })
    expect(qwenQuestionReply(PAYLOAD, questions, state)).toEqual({
      outcome: { outcome: 'selected', optionId: 'proceed_once' },
      answers: { 0: 'Blue', 1: 'Cache, Metrics' },
    })
  })

  it('sends typed words as the answer, and omits an empty question', () => {
    const state = createControlAnswerState({ customTexts: { 0: '  Green  ', 1: '   ' } })
    expect(qwenQuestionReply(PAYLOAD, questions, state)).toEqual({
      outcome: { outcome: 'selected', optionId: 'proceed_once' },
      answers: { 0: 'Green' },
    })
  })

  it('selects Qwen\'s own submit id when the request states no option', () => {
    const payload = { params: { toolCall: { _meta: { qwenInteractionKind: 'user_question', qwenQuestions: QUESTIONS } } } }
    const reply = qwenQuestionReply(payload, qwenQuestions(payload), createControlAnswerState({ selections: { 0: ['Red'] } }))
    expect(reply.outcome).toEqual({ outcome: 'selected', optionId: 'proceed_once' })
  })

  // Qwen takes one string for each question, so a chosen option and typed words are
  // alternatives, and the choice wins.
  it('sends the chosen options over the typed words of the same question', () => {
    const state = createControlAnswerState({ selections: { 0: ['Red'] }, customTexts: { 0: 'Green' } })
    expect(qwenQuestionReply(PAYLOAD, questions, state).answers).toEqual({ 0: 'Red' })
  })

  it('sends an empty answer set when the reader answered nothing', () => {
    expect(qwenQuestionReply(PAYLOAD, questions, createControlAnswerState()).answers).toEqual({})
    expect(qwenQuestionReply(PAYLOAD, [], createControlAnswerState({ selections: { 0: ['Red'] } })).answers, 'an answer for a question the dialog did not ask').toEqual({})
  })

  // An option with an empty id is no answer Qwen can read, so the reply takes the
  // next allow option, and Qwen's own id when none is left.
  it('skips a submit option whose id is empty', () => {
    const withBlank = { params: { ...PAYLOAD.params, options: [{ optionId: '', kind: 'allow_once' }, { optionId: 'submit', kind: 'allow_once' }] } }
    expect(qwenQuestionReply(withBlank, questions, createControlAnswerState()).outcome).toEqual({ outcome: 'selected', optionId: 'submit' })
    const onlyBlank = { params: { ...PAYLOAD.params, options: [{ optionId: '', kind: 'allow_once' }] } }
    expect(qwenQuestionReply(onlyBlank, questions, createControlAnswerState()).outcome).toEqual({ outcome: 'selected', optionId: 'proceed_once' })
  })
})

describe('sendQwenQuestionResponse', () => {
  it('sends the reply under the worker request id', async () => {
    const sent = await sentReply(respond => sendQwenQuestionResponse(respond, 'jsonrpc:3', PAYLOAD, qwenQuestions(PAYLOAD), createControlAnswerState({ selections: { 0: ['Red'] } })))
    expect(sent).toEqual([{ jsonrpc: '2.0', id: 'jsonrpc:3', result: { outcome: { outcome: 'selected', optionId: 'proceed_once' }, answers: { 0: 'Red' } } }])
  })
})

describe('sendQwenQuestionRejectResponse', () => {
  it('selects the cancel option', async () => {
    const sent = await sentReply(respond => sendQwenQuestionRejectResponse(respond, 'jsonrpc:3', PAYLOAD))
    expect(sent).toEqual([{ jsonrpc: '2.0', id: 'jsonrpc:3', result: { outcome: { outcome: 'selected', optionId: 'cancel' } } }])
  })

  it('selects the reject option the request offers under an id of its own', async () => {
    const payload = { params: { ...PAYLOAD.params, options: [{ optionId: 'submit', kind: 'allow_once' }, { optionId: 'dismiss', kind: 'reject_once' }] } }
    const sent = await sentReply(respond => sendQwenQuestionRejectResponse(respond, 'jsonrpc:3', payload))
    expect(sent).toEqual([{ jsonrpc: '2.0', id: 'jsonrpc:3', result: { outcome: { outcome: 'selected', optionId: 'dismiss' } } }])
  })

  it('selects Qwen\'s own cancel id when the request states no reject option', async () => {
    const payload = { params: { ...PAYLOAD.params, options: [{ optionId: 'submit', kind: 'allow_once' }] } }
    const sent = await sentReply(respond => sendQwenQuestionRejectResponse(respond, 'jsonrpc:3', payload))
    expect(sent).toEqual([{ jsonrpc: '2.0', id: 'jsonrpc:3', result: { outcome: { outcome: 'selected', optionId: 'cancel' } } }])
  })
})

describe('qwenQuestionAnswerLines', () => {
  it('states one line for each answered question, headed by its header', () => {
    expect(qwenQuestionAnswerLines(PAYLOAD, { answers: { 1: 'Cache, Metrics', 0: 'Blue' } })).toBe('Color: Blue\nExtras: Cache, Metrics')
  })

  it('numbers the questions when the request is gone', () => {
    expect(qwenQuestionAnswerLines(undefined, { answers: { 0: 'Blue' } })).toBe('Question 1: Blue')
  })

  it('answers null for a reply with no answers', () => {
    expect(qwenQuestionAnswerLines(PAYLOAD, { outcome: { outcome: 'selected', optionId: 'cancel' } })).toBeNull()
    expect(qwenQuestionAnswerLines(PAYLOAD, { answers: {} })).toBeNull()
  })

  it('answers null for an answers field that is no object', () => {
    expect(qwenQuestionAnswerLines(PAYLOAD, { answers: ['Blue'] })).toBeNull()
    expect(qwenQuestionAnswerLines(PAYLOAD, { answers: 'Blue' })).toBeNull()
  })

  // The label is the header, else the question, else the question's number. A
  // question the reader left out, or an answer that is blank or no string, states
  // no line.
  it('labels each line by what the question states, and skips a question with no answer', () => {
    const payload = { params: { toolCall: { _meta: { qwenInteractionKind: 'user_question', qwenQuestions: [
      { question: 'Which color?', options: [] },
      { header: 'Size', question: 'Which size?', options: [] },
      { options: [] },
      { question: 'Skipped?', options: [] },
      { question: 'Blank?', options: [] },
      { question: 'Number?', options: [] },
    ] } } } }
    expect(qwenQuestionAnswerLines(payload, { answers: { 0: 'Blue', 1: 'Large', 2: 'Yes', 4: '   ', 5: 7 } }))
      .toBe('Which color?: Blue\nSize: Large\nQuestion 3: Yes')
  })

  // The request lists the questions the dialog asked, so an answer for an index it
  // never asked states nothing.
  it('drops an answer for a question the request does not ask', () => {
    expect(qwenQuestionAnswerLines(PAYLOAD, { answers: { 0: 'Blue', 7: 'Extra' } })).toBe('Color: Blue')
  })
})
