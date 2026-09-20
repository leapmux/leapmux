import { describe, expect, it } from 'vitest'
import { extractOpenCodeQuestions } from './askUserQuestion'

const ASKED = {
  properties: {
    questions: [{
      question: 'Which parser?',
      options: [{ label: 'Recursive descent' }, { label: 'Pratt' }],
    }],
  },
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

  // The daemon's own answer wins, and a `multiple` that is not a boolean states
  // nothing either way.
  it('keeps an explicit multiSelect and ignores a multiple that is not a boolean', () => {
    expect(extractOpenCodeQuestions({ properties: { questions: [{ question: 'Which?', options: [], multiSelect: false, multiple: true }] } })[0]?.multiSelect).toBe(false)
    expect(extractOpenCodeQuestions({ properties: { questions: [{ question: 'Which?', options: [], multiple: 'yes' }] } })[0]?.multiSelect).toBeUndefined()
  })
})
