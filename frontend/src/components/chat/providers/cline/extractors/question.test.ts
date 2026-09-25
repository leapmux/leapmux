import { describe, expect, it } from 'vitest'
import { clineQuestionPrompt, clineQuestionRequest, clineQuestionResult } from './question'

describe('clineQuestionPrompt', () => {
  it('reads the question and the options that are text', () => {
    expect(clineQuestionPrompt({ question: 'Which?', options: ['A', 3, null, 'B'] })).toEqual({ question: 'Which?', options: [{ label: 'A' }, { label: 'B' }] })
  })

  it('reads an option list that is not a list as no option', () => {
    expect(clineQuestionPrompt({ question: 'Why?', options: 'A, B' })).toEqual({ question: 'Why?', options: [] })
  })
})

describe('clineQuestionRequest', () => {
  it('states one question for a call that asks one', () => {
    expect(clineQuestionRequest({ question: 'Which?', options: ['A'] })).toEqual({ questions: [{ question: 'Which?', options: [{ label: 'A' }] }] })
  })

  it('states no question for a call that asks none', () => {
    for (const args of [{}, { question: '' }, { question: 3, options: ['A'] }])
      expect(clineQuestionRequest(args), JSON.stringify(args)).toEqual({ questions: [] })
  })
})

describe('clineQuestionResult', () => {
  const request = clineQuestionRequest({ question: 'Which?', options: ['A', 'B'] })

  it('reads the answer under its question', () => {
    expect(clineQuestionResult(request, '  B  ')).toEqual({ answers: [{ header: 'Which?', answer: 'B' }] })
  })

  it('reads a blank or absent answer as no answer', () => {
    for (const output of ['', '  ', undefined, null])
      expect(clineQuestionResult(request, output), JSON.stringify(output)).toEqual({ answers: [{ header: 'Which?', answer: null }] })
  })

  // `0` is an answer. It must not read as an absent one.
  it('keeps an answer that is the number zero', () => {
    expect(clineQuestionResult(request, 0)).toEqual({ answers: [{ header: 'Which?', answer: '0' }] })
  })

  it('states the answer of a call that asked no question under an empty header', () => {
    expect(clineQuestionResult({ questions: [] }, 'B')).toEqual({ answers: [{ header: '', answer: 'B' }] })
  })
})
