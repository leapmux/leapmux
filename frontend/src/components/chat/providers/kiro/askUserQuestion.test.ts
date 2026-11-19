import { describe, expect, it } from 'vitest'
import { kiroUserInputQuestion, kiroUserInputSavedAnswer } from './askUserQuestion'

describe('kiroUserInputQuestion', () => {
  it('reads the question and its options, and leaves the sub-options out', () => {
    expect(kiroUserInputQuestion('Which DB?', [
      {
        title: 'Postgres',
        description: 'pg',
        recommended: true,
        subOptionsLabel: 'Extras',
        subOptions: [
          { title: 'PostGIS', description: 'geo' },
          { title: 'pgvector' },
        ],
      },
      { title: 'SQLite', recommended: false },
    ])).toEqual({
      question: 'Which DB?',
      options: [
        { value: 'Postgres', label: 'Postgres (recommended)', description: 'pg' },
        { value: 'SQLite', label: 'SQLite' },
      ],
      multiSelect: false,
    })
  })

  it('reads a free-text question with no option', () => {
    expect(kiroUserInputQuestion('Any name?', [])).toEqual({ question: 'Any name?', options: [], multiSelect: false })
    expect(kiroUserInputQuestion('Any name?', undefined)).toEqual({ question: 'Any name?', options: [], multiSelect: false })
    expect(kiroUserInputQuestion('Any name?', 'Yes')).toEqual({ question: 'Any name?', options: [], multiSelect: false })
  })

  it('reads an option that the model states as its title alone', () => {
    expect(kiroUserInputQuestion('Q', ['Yes', 'No']).options).toEqual([
      { value: 'Yes', label: 'Yes' },
      { value: 'No', label: 'No' },
    ])
  })

  it('drops an option that states no title', () => {
    expect(kiroUserInputQuestion('Q', [
      null,
      7,
      '',
      { description: 'd' },
      { title: 'Kept' },
    ]).options).toEqual([
      { value: 'Kept', label: 'Kept' },
    ])
  })

  it('marks only an option that Kiro states as recommended with true', () => {
    expect(kiroUserInputQuestion('Q', [
      { title: 'A', recommended: 'yes' },
      { title: 'B', recommended: 1 },
    ]).options).toEqual([
      { value: 'A', label: 'A' },
      { value: 'B', label: 'B' },
    ])
  })
})

describe('kiroUserInputSavedAnswer', () => {
  it('reads the answer of a saved reply', () => {
    expect(kiroUserInputSavedAnswer({ action: 'answered', answer: ' Postgres [PostGIS] ' })).toBe('Postgres [PostGIS]')
  })

  it('answers null for a dismissal and for an empty answer', () => {
    expect(kiroUserInputSavedAnswer({ action: 'dismissed' })).toBeNull()
    expect(kiroUserInputSavedAnswer({ action: 'answered', answer: '' })).toBeNull()
    expect(kiroUserInputSavedAnswer({ action: 'answered', answer: '   ' })).toBeNull()
    expect(kiroUserInputSavedAnswer({ action: 'answered', answer: 7 })).toBeNull()
    expect(kiroUserInputSavedAnswer({})).toBeNull()
  })
})
