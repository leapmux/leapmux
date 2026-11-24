import { describe, expect, it } from 'vitest'
import { deepseekHarnessQuestions } from './askUserQuestion'

describe('deepseekHarnessQuestions', () => {
  it('preserves native question IDs and option labels', () => {
    const payload = {
      event: 'user-questions/request',
      request: {
        questions: [
          {
            id: 'native-question',
            question: 'Choose a result.',
            header: 'Result',
            multiSelect: true,
            options: [
              { label: 'First', description: 'Use the first result.' },
              { label: 'Second' },
            ],
          },
        ],
      },
    }
    expect(deepseekHarnessQuestions(payload)).toEqual([
      {
        id: 'native-question',
        question: 'Choose a result.',
        header: 'Result',
        multiSelect: true,
        options: [
          { label: 'First', value: 'First', description: 'Use the first result.' },
          { label: 'Second', value: 'Second' },
        ],
      },
    ])
  })

  it('keeps a free-text question and drops malformed records', () => {
    expect(deepseekHarnessQuestions({
      questions: [
        null,
        'invalid',
        { id: 'empty-options', question: 'Enter text.' },
        { question: 'Missing ID' },
        { id: 'missing-question' },
      ],
    })).toEqual([
      { id: 'empty-options', question: 'Enter text.', options: [] },
    ])
    expect(deepseekHarnessQuestions({ questions: null })).toEqual([])
  })
})
