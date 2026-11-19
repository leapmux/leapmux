import { describe, expect, it } from 'vitest'
import { codewhaleQuestionsFromToolInput, codewhaleQuestionText } from './askUserQuestion'
import { QUESTIONS } from './controls.fixtures'

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
