import { describe, expect, it } from 'vitest'
import { CODEWHALE_TOOL } from '~/generated/contracts/codewhale-protocol'
import { createControlAnswerState } from '../../controls/types'
import { codewhaleAnswers, codewhaleAnswerValues, codewhaleIsQuestionRequest, codewhaleQuestionRecords, codewhaleQuestionsFromPayload, codewhaleQuestionsFromToolInput } from './askUserQuestion'
import { approvalPayload, questionPayload, QUESTIONS } from './controls.fixtures'

describe('codewhaleIsQuestionRequest', () => {
  it('recognizes a stored question by its event and its tool', () => {
    expect(codewhaleIsQuestionRequest(questionPayload())).toBe(true)
  })

  it('refuses an approval, even of the question tool', () => {
    expect(codewhaleIsQuestionRequest(approvalPayload(CODEWHALE_TOOL.Bash, {}))).toBe(false)
    expect(codewhaleIsQuestionRequest(approvalPayload(CODEWHALE_TOOL.RequestUserInput, {}))).toBe(false)
    expect(codewhaleIsQuestionRequest({ ...questionPayload(), request: { tool_name: CODEWHALE_TOOL.Bash } })).toBe(false)
  })
})

describe('codewhaleQuestionsFromPayload', () => {
  it('reads each question with its id, its options and its multiple choice', () => {
    expect(codewhaleQuestionsFromPayload(questionPayload())).toStrictEqual([
      { id: 'color', question: 'Which color?', options: [{ label: 'Red', description: 'Warm' }, { label: 'Blue', description: 'Cool' }], header: 'Color' },
      { id: 'sizes', question: 'Which sizes?', options: [{ label: 'S' }, { label: 'M' }], header: 'Sizes', multiSelect: true },
    ])
  })

  it('drops a question the runtime could never match an answer to', () => {
    expect(codewhaleQuestionsFromPayload(questionPayload([{ question: 'No id?' }, { id: 'x' }, 'x', { id: 'h', header: 'Header only', options: [{ description: 'no label' }] }])))
      .toStrictEqual([{ id: 'h', question: 'Header only', options: [] }])
  })

  it('falls back to the stored event when the header lost the questions', () => {
    const payload = { ...questionPayload(), request: { tool_name: CODEWHALE_TOOL.RequestUserInput, input: {} } }
    expect(codewhaleQuestionRecords(payload)).toStrictEqual(QUESTIONS)
    expect(codewhaleQuestionRecords({})).toStrictEqual([])
  })

  // The header is what the worker stored for the control to answer, so it wins
  // over the event whenever it states a list, even an empty one.
  it('reads the header\'s list over the stored event\'s', () => {
    const header = [{ id: 'only', question: 'Only this?' }]
    const payload = { ...questionPayload(), request: { tool_name: CODEWHALE_TOOL.RequestUserInput, input: { questions: header } } }
    expect(codewhaleQuestionRecords(payload)).toStrictEqual(header)
    const emptied = { ...questionPayload(), request: { tool_name: CODEWHALE_TOOL.RequestUserInput, input: { questions: [] } } }
    expect(codewhaleQuestionRecords(emptied)).toStrictEqual([])
  })

  it('drops a record that is not an object, and reads a list in another shape as none', () => {
    expect(codewhaleQuestionRecords(questionPayload([QUESTIONS[0], null, 7, 'x']))).toStrictEqual([QUESTIONS[0]])
    const payload = { request: { tool_name: CODEWHALE_TOOL.RequestUserInput, input: { questions: 'Which color?' } } }
    expect(codewhaleQuestionRecords(payload)).toStrictEqual([])
  })
})

describe('codewhaleQuestionsFromToolInput', () => {
  it('reads the questions a tool call asked', () => {
    expect(codewhaleQuestionsFromToolInput({ questions: QUESTIONS })).toStrictEqual([
      { header: 'Color', question: 'Which color?', options: [{ label: 'Red', description: 'Warm' }, { label: 'Blue', description: 'Cool' }] },
      { header: 'Sizes', question: 'Which sizes?', options: [{ label: 'S' }, { label: 'M' }] },
    ])
    expect(codewhaleQuestionsFromToolInput({})).toStrictEqual([])
  })

  it('drops an option with no label and a question with no text, and states a header only when it adds words', () => {
    const questions = [
      { header: 'Header only', options: [{ description: 'no label' }, { label: 'Yes' }] },
      { header: 'Same', question: 'Same' },
      { options: [{ label: 'Orphan' }] },
    ]
    expect(codewhaleQuestionsFromToolInput({ questions })).toStrictEqual([
      { question: 'Header only', options: [{ label: 'Yes' }] },
      { question: 'Same', options: [] },
    ])
  })
})

describe('codewhaleAnswers', () => {
  const questions = codewhaleQuestionsFromPayload(questionPayload())

  it('sends one entry for each chosen option, and free text as Other', () => {
    const state = createControlAnswerState({ selections: { 1: ['S', 'M'] }, customTexts: { 0: ' Green ' } })
    expect(codewhaleAnswers(questions, state)).toStrictEqual([
      { id: 'color', label: 'Other', value: 'Green' },
      { id: 'sizes', label: 'S', value: 'S' },
      { id: 'sizes', label: 'M', value: 'M' },
    ])
  })

  it('prefers a choice over the text beside it, and skips an unanswered question', () => {
    const state = createControlAnswerState({ selections: { 0: ['Red'] }, customTexts: { 0: 'ignored', 1: '  ' } })
    expect(codewhaleAnswers(questions, state)).toStrictEqual([{ id: 'color', label: 'Red', value: 'Red' }])
  })

  it('answers nothing for a question without an id', () => {
    expect(codewhaleAnswers([{ question: 'No id', options: [] }], createControlAnswerState({ customTexts: { 0: 'x' } }))).toStrictEqual([])
  })
})

describe('codewhaleAnswerValues', () => {
  it('reads every value one question received', () => {
    const answers = [{ id: 'a', label: 'X', value: 'X' }, { id: 'b', value: ' Y ' }, { id: 'a', value: '' }, 'z']
    expect(codewhaleAnswerValues(answers, 'a')).toStrictEqual(['X'])
    expect(codewhaleAnswerValues(answers, 'b')).toStrictEqual(['Y'])
    expect(codewhaleAnswerValues('x', 'a')).toStrictEqual([])
  })
})
