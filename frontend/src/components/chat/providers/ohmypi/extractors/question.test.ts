import { describe, expect, it } from 'vitest'
import { ohMyPiQuestionAnswers } from './question'

describe('ohMyPiQuestionAnswers', () => {
  it('reads the native HTML form answers, including a valid empty answer', () => {
    expect(ohMyPiQuestionAnswers({
      answers: [
        { name: 'style', question: 'Choose a style', answer: 'Beta' },
        { name: 'count', question: 'How many?', answer: '0' },
        { name: 'sizes', question: 'Sizes', answer: '' },
      ],
    })).toEqual([
      { header: 'Choose a style', answer: 'Beta' },
      { header: 'How many?', answer: '0' },
      { header: 'Sizes', answer: '' },
    ])
  })
  it('reads the answer of a single field', () => {
    expect(ohMyPiQuestionAnswers({
      answers: [
        { name: 'db', question: 'Which database?', answer: 'SQLite' },
      ],
    })).toEqual([
      { header: 'Which database?', answer: 'SQLite' },
    ])
  })

  it('reads several fields with typed answers and skipped optional choices', () => {
    expect(ohMyPiQuestionAnswers({
      answers: [
        { name: 'name', question: 'Project name?', answer: 'gamma (typed)' },
        { name: 'color', question: 'Color?', answer: 'blue' },
        { name: 'skipped', question: 'Skipped?', answer: '' },
      ],
    })).toEqual([
      { header: 'Project name?', answer: 'gamma (typed)' },
      { header: 'Color?', answer: 'blue' },
      { header: 'Skipped?', answer: '' },
    ])
  })

  it('preserves native answer formatting without reconstructing selected labels', () => {
    expect(ohMyPiQuestionAnswers({
      answers: [
        { name: 'langs', question: 'Langs?', answer: 'Go, Rust' },
        { name: 'more', question: 'More?', answer: 'Go; Zig' },
        { name: 'blank', question: 'Blank?', answer: '  ' },
        { name: 'code', question: 'Code?', answer: '\nprint("雪")\n' },
      ],
    })).toEqual([
      { header: 'Langs?', answer: 'Go, Rust' },
      { header: 'More?', answer: 'Go; Zig' },
      { header: 'Blank?', answer: '  ' },
      { header: 'Code?', answer: '\nprint("雪")\n' },
    ])
  })

  it('returns null when no record has a string answer', () => {
    for (const details of [
      {},
      { answers: [] },
      { answers: 'answer' },
      {
        answers: [
          'x',
          null,
          {},
          { answer: null },
          { answer: 0 },
          { answer: false },
        ],
      },
    ]) {
      expect(ohMyPiQuestionAnswers(details)).toBeNull()
    }
  })

  it('ignores malformed records without discarding valid answers', () => {
    expect(ohMyPiQuestionAnswers({
      answers: [
        null,
        { answer: 0 },
        { name: 'db', answer: 'SQLite' },
        { answer: false },
      ],
    })).toEqual([
      { header: 'db', answer: 'SQLite' },
    ])
  })

  it('uses the field name or a fallback heading when the question is absent', () => {
    expect(ohMyPiQuestionAnswers({
      answers: [
        { name: 'db', answer: 'SQLite' },
        { name: '', question: '', answer: 'x' },
        { answer: '0' },
      ],
    })).toEqual([
      { header: 'db', answer: 'SQLite' },
      { header: 'Answer', answer: 'x' },
      { header: 'Answer', answer: '0' },
    ])
  })
})
