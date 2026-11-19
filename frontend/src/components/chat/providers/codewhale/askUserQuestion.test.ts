import { describe, expect, it } from 'vitest'
import { CODEWHALE_TOOL } from '~/generated/contracts/codewhale-protocol'
import { codewhaleAnswerValues, codewhaleQuestionRecords, codewhaleQuestionsFromToolInput, codewhaleQuestionText } from './askUserQuestion'
import { questionPayload, QUESTIONS } from './controls.fixtures'

describe('codewhaleQuestionRecords', () => {
  it('reads the questions of the header the worker stored', () => {
    expect(codewhaleQuestionRecords(questionPayload())).toStrictEqual(QUESTIONS)
  })

  it('falls back to the stored event when the header lost the questions', () => {
    const payload = {
      ...questionPayload(),
      request: { tool_name: CODEWHALE_TOOL.RequestUserInput, input: {} },
    }
    expect(codewhaleQuestionRecords(payload)).toStrictEqual(QUESTIONS)
    expect(codewhaleQuestionRecords({})).toStrictEqual([])
  })

  // The header is what the worker stored for the question, so it wins over the event
  // whenever it states a list, even an empty one.
  it('reads the header\'s list over the stored event\'s', () => {
    const header = [
      { id: 'only', question: 'Only this?' },
    ]
    const payload = {
      ...questionPayload(),
      request: {
        tool_name: CODEWHALE_TOOL.RequestUserInput,
        input: { questions: header },
      },
    }
    expect(codewhaleQuestionRecords(payload)).toStrictEqual(header)
    const emptied = {
      ...questionPayload(),
      request: {
        tool_name: CODEWHALE_TOOL.RequestUserInput,
        input: { questions: [] },
      },
    }
    expect(codewhaleQuestionRecords(emptied)).toStrictEqual([])
  })

  it('drops a record that is not an object, and reads a list in another shape as none', () => {
    expect(codewhaleQuestionRecords(questionPayload([QUESTIONS[0], null, 7, 'x']))).toStrictEqual([QUESTIONS[0]])
    const payload = {
      request: {
        tool_name: CODEWHALE_TOOL.RequestUserInput,
        input: { questions: 'Which color?' },
      },
    }
    expect(codewhaleQuestionRecords(payload)).toStrictEqual([])
  })
})

describe('codewhaleQuestionsFromToolInput', () => {
  it('reads the questions a tool call asked', () => {
    expect(codewhaleQuestionsFromToolInput({ questions: QUESTIONS })).toStrictEqual([
      {
        header: 'Color',
        question: 'Which color?',
        options: [
          { label: 'Red', description: 'Warm' },
          { label: 'Blue', description: 'Cool' },
        ],
      },
      {
        header: 'Sizes',
        question: 'Which sizes?',
        options: [
          { label: 'S' },
          { label: 'M' },
        ],
      },
    ])
    expect(codewhaleQuestionsFromToolInput({})).toStrictEqual([])
  })

  it('drops an option with no label and a question with no text, and states a header only when it adds words', () => {
    const questions = [
      {
        header: 'Header only',
        options: [
          { description: 'no label' },
          { label: 'Yes' },
        ],
      },
      { header: 'Same', question: 'Same' },
      {
        options: [
          { label: 'Orphan' },
        ],
      },
    ]
    expect(codewhaleQuestionsFromToolInput({ questions })).toStrictEqual([
      {
        question: 'Header only',
        options: [
          { label: 'Yes' },
        ],
      },
      { question: 'Same', options: [] },
    ])
  })
})

describe('codewhaleQuestionText', () => {
  it('reads the sentence before the header', () => {
    expect(codewhaleQuestionText({ question: 'Which color?', header: 'Color' })).toBe('Which color?')
  })

  it('reads the header of a record that states only that', () => {
    expect(codewhaleQuestionText({ header: 'Header only' })).toBe('Header only')
  })

  it('answers an empty text for a record nothing can answer', () => {
    expect(codewhaleQuestionText({ id: 'x' })).toBe('')
  })
})

describe('codewhaleAnswerValues', () => {
  it('reads every value one question received', () => {
    const answers = [
      { id: 'a', label: 'X', value: 'X' },
      { id: 'b', value: ' Y ' },
      { id: 'a', value: '' },
      'z',
    ]
    expect(codewhaleAnswerValues(answers, 'a')).toStrictEqual(['X'])
    expect(codewhaleAnswerValues(answers, 'b')).toStrictEqual(['Y'])
    expect(codewhaleAnswerValues('x', 'a')).toStrictEqual([])
  })
})
