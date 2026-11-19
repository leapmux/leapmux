import { describe, expect, it } from 'vitest'
import { qwenQuestionAnswerLines } from './askUserQuestion'

const QUESTIONS = [
  {
    question: 'Which color do you want?',
    header: 'Color',
    options: [
      { label: 'Red', description: 'The red one' },
      { label: 'Blue', description: 'The blue one' },
    ],
    multiSelect: false,
  },
  {
    question: 'Which extras?',
    header: 'Extras',
    options: [
      { label: 'Cache', description: 'c' },
      { label: 'Metrics', description: 'm' },
    ],
    multiSelect: true,
  },
]

/** Qwen's question dialog as the probe recorded it, with a second question. */
const PAYLOAD = {
  jsonrpc: '2.0',
  id: 3,
  method: 'session/request_permission',
  params: {
    sessionId: 's',
    options: [
      { optionId: 'proceed_once', name: 'Submit', kind: 'allow_once' },
      { optionId: 'cancel', name: 'Cancel', kind: 'reject_once' },
    ],
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

describe('qwenQuestionAnswerLines', () => {
  it('states one line for each answered question, headed by its header', () => {
    expect(qwenQuestionAnswerLines(PAYLOAD, {
      answers: { 1: 'Cache, Metrics', 0: 'Blue' },
    })).toBe('Color: Blue\nExtras: Cache, Metrics')
  })

  it('numbers the questions when the request is gone', () => {
    expect(qwenQuestionAnswerLines(undefined, {
      answers: { 0: 'Blue' },
    })).toBe('Question 1: Blue')
  })

  it('numbers the questions when the request states none', () => {
    expect(qwenQuestionAnswerLines({
      params: { toolCall: {} },
    }, {
      answers: { 0: 'Blue' },
    })).toBe('Question 1: Blue')
  })

  // Qwen states the questions in `_meta` and again as the tool call's arguments. The
  // `_meta` copy is the one it marks for a client, so it wins.
  it('labels the answers by the questions of _meta over the arguments', () => {
    const payload = {
      params: {
        toolCall: {
          rawInput: {
            questions: [
              { question: 'Argument question?', options: [] },
            ],
          },
          _meta: {
            qwenInteractionKind: 'user_question',
            qwenQuestions: [
              { question: 'Meta question?', options: [] },
            ],
          },
        },
      },
    }
    expect(qwenQuestionAnswerLines(payload, {
      answers: { 0: 'Blue' },
    })).toBe('Meta question?: Blue')
  })

  it('labels the answers by the tool call arguments when _meta states no questions', () => {
    const payload = {
      params: {
        toolCall: {
          rawInput: {
            questions: [QUESTIONS[0]],
          },
          _meta: { qwenInteractionKind: 'user_question' },
        },
      },
    }
    expect(qwenQuestionAnswerLines(payload, {
      answers: { 0: 'Blue' },
    })).toBe('Color: Blue')
  })

  // `_meta` is wire data. A copy there that is no list is no copy, and the tool
  // call's own arguments still state the questions.
  it('labels the answers by the arguments when the _meta copy is no list', () => {
    const payload = {
      params: {
        toolCall: {
          rawInput: {
            questions: [QUESTIONS[1]],
          },
          _meta: { qwenInteractionKind: 'user_question', qwenQuestions: 'Which extras?' },
        },
      },
    }
    expect(qwenQuestionAnswerLines(payload, {
      answers: { 0: 'Cache' },
    })).toBe('Extras: Cache')
  })

  it('answers null for a reply with no answers', () => {
    expect(qwenQuestionAnswerLines(PAYLOAD, {
      outcome: { outcome: 'selected', optionId: 'cancel' },
    })).toBeNull()
    expect(qwenQuestionAnswerLines(PAYLOAD, { answers: {} })).toBeNull()
  })

  it('answers null for an answers field that is no object', () => {
    expect(qwenQuestionAnswerLines(PAYLOAD, {
      answers: ['Blue'],
    })).toBeNull()
    expect(qwenQuestionAnswerLines(PAYLOAD, { answers: 'Blue' })).toBeNull()
  })

  // The label is the header, else the question, else the question's number. A
  // question the reader left out, or an answer that is blank or no string, states
  // no line.
  it('labels each line by what the question states, and skips a question with no answer', () => {
    const payload = {
      params: {
        toolCall: {
          _meta: {
            qwenInteractionKind: 'user_question',
            qwenQuestions: [
              { question: 'Which color?', options: [] },
              { header: 'Size', question: 'Which size?', options: [] },
              { options: [] },
              { question: 'Skipped?', options: [] },
              { question: 'Blank?', options: [] },
              { question: 'Number?', options: [] },
            ],
          },
        },
      },
    }
    expect(qwenQuestionAnswerLines(payload, {
      answers: {
        0: 'Blue',
        1: 'Large',
        2: 'Yes',
        4: '   ',
        5: 7,
      },
    }))
      .toBe('Which color?: Blue\nSize: Large\nQuestion 3: Yes')
  })

  // The request lists the questions the dialog asked, so an answer for an index it
  // never asked states nothing.
  it('drops an answer for a question the request does not ask', () => {
    expect(qwenQuestionAnswerLines(PAYLOAD, {
      answers: { 0: 'Blue', 7: 'Extra' },
    })).toBe('Color: Blue')
  })
})
