import type { ControlResponseSender } from '../controls/types'
import { describe, expect, it, vi } from 'vitest'
import { createControlAnswerState } from '../controls/types'
import { extractOpenCodeQuestions, sendOpenCodeQuestionRejectResponse, sendOpenCodeQuestionResponse } from './openCodeQuestions'

const ASKED = {
  properties: {
    questions: [{
      question: 'Which parser?',
      options: [{ label: 'Recursive descent' }, { label: 'Pratt' }],
    }],
  },
}

function sentBody(send: ReturnType<typeof vi.fn>): unknown {
  return JSON.parse(new TextDecoder().decode(send.mock.calls[0]?.[0]))
}

describe('extractOpenCodeQuestions', () => {
  it('reads the questions and the options the payload states', () => {
    expect(extractOpenCodeQuestions(ASKED)).toStrictEqual([{
      question: 'Which parser?',
      options: [{ label: 'Recursive descent' }, { label: 'Pratt' }],
    }])
  })

  /*
   * The only upstream guard tests `payload.type`, which says nothing about
   * `properties`. A `questions` that is truthy and not an array threw
   * `rawQuestions.map is not a function` and took the whole banner with it, because
   * `?? []` answers for `null` and `undefined` alone.
   */
  it.each([
    ['a string', 'Which parser?'],
    ['a number', 7],
    ['an object', { question: 'Which parser?' }],
    ['a boolean', true],
  ])('answers no question for a questions field that holds %s', (_shape, questions) => {
    expect(() => extractOpenCodeQuestions({ properties: { questions } })).not.toThrow()
    expect(extractOpenCodeQuestions({ properties: { questions } })).toStrictEqual([])
  })

  it.each([
    ['no properties at all', {}],
    ['a properties field that is not an object', { properties: 'questions' }],
    ['a properties object with no questions', { properties: {} }],
  ])('answers no question for %s', (_shape, payload) => {
    expect(extractOpenCodeQuestions(payload)).toStrictEqual([])
  })

  /*
   * An element the dialog cannot draw is dropped rather than spread. A bare string
   * spread one character per key, and `AskUserQuestionControl` then dereferenced a
   * `question` field the element never had and handed `options` to a `<For>`.
   */
  it('drops an element that is no object and keeps the ones that are', () => {
    const questions = extractOpenCodeQuestions({
      properties: { questions: [null, 'Which parser?', 42, { question: 'Which parser?', options: [] }] },
    })
    expect(questions).toStrictEqual([{ question: 'Which parser?', options: [] }])
  })

  // An element that states no options is a free-text question, which is a real one.
  it('keeps a question that states no options and reads a non-array options as none', () => {
    expect(extractOpenCodeQuestions({ properties: { questions: [{ question: 'Why?' }, { question: 'How?', options: 'many' }] } }))
      .toStrictEqual([{ question: 'Why?', options: [] }, { question: 'How?', options: [] }])
  })

  it('folds the legacy multiple field onto multiSelect', () => {
    const questions = extractOpenCodeQuestions({ properties: { questions: [{ question: 'Which?', options: [], multiple: true }] } })
    expect(questions[0]?.multiSelect).toBe(true)
  })

  // A false `multiple` is an answer too: the question takes one choice.
  it('folds a false multiple field onto a false multiSelect', () => {
    const questions = extractOpenCodeQuestions({ properties: { questions: [{ question: 'Which?', options: [], multiple: false }] } })
    expect(questions[0]?.multiSelect).toBe(false)
  })

  // The daemon's own answer wins, and a `multiple` that is not a boolean states
  // nothing either way.
  it('keeps an explicit multiSelect and ignores a multiple that is not a boolean', () => {
    expect(extractOpenCodeQuestions({ properties: { questions: [{ question: 'Which?', options: [], multiSelect: false, multiple: true }] } })[0]?.multiSelect).toBe(false)
    expect(extractOpenCodeQuestions({ properties: { questions: [{ question: 'Which?', options: [], multiple: 'yes' }] } })[0]?.multiSelect).toBeUndefined()
  })
})

describe('sendOpenCodeQuestionResponse', () => {
  const questions = extractOpenCodeQuestions({
    properties: {
      questions: [
        { question: 'Which database?', options: [{ label: 'SQLite' }, { label: 'Postgres' }], multiple: true },
        { question: 'Anything else?', options: [] },
        { question: 'Which parser?', options: [{ label: 'Pratt' }] },
      ],
    },
  })

  it('answers each question with its choices, or the typed words, in the question order', async () => {
    const send = vi.fn().mockResolvedValue(undefined)
    const state = createControlAnswerState({ selections: { 0: ['SQLite', 'Postgres'], 2: ['Pratt'] }, customTexts: { 1: '  more tests  ' } })
    await sendOpenCodeQuestionResponse(send as ControlResponseSender, 'que_1', questions, state)
    expect(sentBody(send)).toEqual({ jsonrpc: '2.0', id: 'que_1', result: { answers: [['SQLite', 'Postgres'], ['more tests'], ['Pratt']] } })
  })

  // A choice wins over typed words for the same question, because the two are
  // alternatives on the card.
  it('prefers the chosen options to the typed words', async () => {
    const send = vi.fn().mockResolvedValue(undefined)
    const state = createControlAnswerState({ selections: { 0: ['SQLite'] }, customTexts: { 0: 'Oracle' } })
    await sendOpenCodeQuestionResponse(send as ControlResponseSender, 'que_1', questions, state)
    expect(sentBody(send)).toEqual({ jsonrpc: '2.0', id: 'que_1', result: { answers: [['SQLite'], [], []] } })
  })

  it('answers an unanswered question, or one with blank words, with no choice', async () => {
    const send = vi.fn().mockResolvedValue(undefined)
    await sendOpenCodeQuestionResponse(send as ControlResponseSender, 'que_1', questions, createControlAnswerState({ customTexts: { 1: '   ' } }))
    expect(sentBody(send)).toEqual({ jsonrpc: '2.0', id: 'que_1', result: { answers: [[], [], []] } })
  })

  it('answers a request of no question with no answer', async () => {
    const send = vi.fn().mockResolvedValue(undefined)
    await sendOpenCodeQuestionResponse(send as ControlResponseSender, 'que_1', [], createControlAnswerState())
    expect(sentBody(send)).toEqual({ jsonrpc: '2.0', id: 'que_1', result: { answers: [] } })
  })

  // The caller reports a failed send, so the answer must not swallow it.
  it('passes a send failure to its caller', async () => {
    const failure = new Error('worker unreachable')
    const send = vi.fn().mockRejectedValue(failure)
    await expect(sendOpenCodeQuestionResponse(send as ControlResponseSender, 'que_1', questions, createControlAnswerState())).rejects.toBe(failure)
  })
})

describe('sendOpenCodeQuestionRejectResponse', () => {
  it('rejects the question', async () => {
    const send = vi.fn().mockResolvedValue(undefined)
    await sendOpenCodeQuestionRejectResponse(send as ControlResponseSender, 'que_1')
    expect(sentBody(send)).toEqual({ jsonrpc: '2.0', id: 'que_1', result: { rejected: true } })
  })

  it('passes a send failure to its caller', async () => {
    const failure = new Error('worker unreachable')
    const send = vi.fn().mockRejectedValue(failure)
    await expect(sendOpenCodeQuestionRejectResponse(send as ControlResponseSender, 'que_1')).rejects.toBe(failure)
  })
})
