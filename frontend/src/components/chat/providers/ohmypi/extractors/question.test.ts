import { describe, expect, it } from 'vitest'
import { ohMyPiQuestionAnswers, ohMyPiQuestionPrompts } from './question'

describe('ohMyPiQuestionPrompts', () => {
  it('reads omp\'s question record and marks the recommended option', () => {
    expect(ohMyPiQuestionPrompts([
      { id: 'db', question: 'Which database?', header: 'Storage', options: [{ label: 'SQLite', description: 'Single file' }, { label: 'PostgreSQL', preview: 'SELECT 1' }], recommended: 0 },
      { id: 'langs', question: 'Which languages?', multi: true, options: [{ label: 'Go' }, { label: '' }, 'bad'] },
    ])).toEqual([
      {
        id: 'db',
        question: 'Which database?',
        header: 'Storage',
        options: [{ label: 'SQLite', description: 'Single file · Recommended' }, { label: 'PostgreSQL', preview: 'SELECT 1' }],
        multiSelect: false,
      },
      { id: 'langs', question: 'Which languages?', options: [{ label: 'Go' }], multiSelect: true },
    ])
  })

  it('marks a recommended option that has no description', () => {
    expect(ohMyPiQuestionPrompts([{ id: 'a', question: 'Q?', options: [{ label: 'x' }, { label: 'y' }], recommended: 1 }])[0]?.options[1]).toEqual({ label: 'y', description: 'Recommended' })
  })

  it('leaves out a blank preview, and marks no option for a recommendation outside the list', () => {
    expect(ohMyPiQuestionPrompts([{ id: 'a', question: 'Q?', options: [{ label: 'x', preview: ' \n ' }, { label: 'y' }], recommended: 5 }])[0]?.options).toEqual([
      { label: 'x' },
      { label: 'y' },
    ])
    expect(ohMyPiQuestionPrompts([{ id: 'a', question: 'Q?', options: [{ label: 'x' }], recommended: -1 }])[0]?.options).toEqual([{ label: 'x' }])
  })

  it('marks the recommended option by its place in omp\'s list, which counts an option this build skips', () => {
    // omp's index counts every option it sent, so the mark stays on the option omp
    // meant although the list drops one with no label before it.
    expect(ohMyPiQuestionPrompts([{ id: 'a', question: 'Q?', options: [{ label: '' }, { label: 'y' }], recommended: 1 }])[0]?.options).toEqual([
      { label: 'y', description: 'Recommended' },
    ])
  })

  it('states an empty id for a question with none, and reads a multi flag only when it is `true`', () => {
    expect(ohMyPiQuestionPrompts([{ question: 'Q?', multi: 'yes' }])).toEqual([{ id: '', question: 'Q?', options: [], multiSelect: false }])
  })

  it('skips a question with no text, and reads no list as none', () => {
    expect(ohMyPiQuestionPrompts([{ id: 'a', options: [] }])).toEqual([])
    expect(ohMyPiQuestionPrompts(undefined)).toEqual([])
    expect(ohMyPiQuestionPrompts({ questions: [] })).toEqual([])
  })
})

describe('ohMyPiQuestionAnswers', () => {
  it('reads the answer of a single question', () => {
    // omp 18.2.11's own details (probe s2).
    expect(ohMyPiQuestionAnswers({ question: 'Which database?', options: ['SQLite', 'PostgreSQL'], multi: false, selectedOptions: ['SQLite'] })).toEqual([
      { header: 'Which database?', answer: 'SQLite' },
    ])
  })

  it('reads the answers of several questions, the typed text included', () => {
    // omp 18.2.11's own details (probe s2).
    expect(ohMyPiQuestionAnswers({
      results: [
        { id: 'name', question: 'Project name?', options: ['alpha', 'beta'], multi: false, selectedOptions: [], customInput: 'gamma (typed)' },
        { id: 'color', question: 'Color?', options: ['red', 'blue'], multi: false, selectedOptions: ['blue'] },
        { id: 'skipped', question: 'Skipped?', selectedOptions: [] },
      ],
    })).toEqual([
      { header: 'Project name?', answer: 'gamma (typed)' },
      { header: 'Color?', answer: 'blue' },
      { header: 'Skipped?', answer: null },
    ])
  })

  it('reads the typed text alone for a question of several that states it, as omp tells the model', () => {
    // The question bridge toggles each chosen label and then types the labels through
    // "Other", because omp offers no "Done" inside a call of several questions. omp's
    // `formatQuestionResult` then states the typed text alone.
    expect(ohMyPiQuestionAnswers({
      results: [
        { id: 'langs', question: 'Langs?', multi: true, selectedOptions: ['Go', 'Rust'], customInput: 'Go, Rust' },
        { id: 'more', question: 'More?', multi: true, selectedOptions: ['Go'], customInput: 'Go; Zig' },
        { id: 'blank', question: 'Blank?', multi: false, selectedOptions: [], customInput: '' },
      ],
    })).toEqual([
      { header: 'Langs?', answer: 'Go, Rust' },
      { header: 'More?', answer: 'Go; Zig' },
      { header: 'Blank?', answer: null },
    ])
  })

  it('reads both the chosen labels and the typed text of a single question, as omp tells the model', () => {
    // omp's `formatSingleQuestionResponse` states both parts.
    expect(ohMyPiQuestionAnswers({ question: 'Langs?', multi: true, selectedOptions: ['Go', 'Rust'], customInput: 'Zig' })).toEqual([
      { header: 'Langs?', answer: 'Go, Rust; Zig' },
    ])
  })

  it('answers null for a result that states no answer', () => {
    expect(ohMyPiQuestionAnswers({})).toBeNull()
    expect(ohMyPiQuestionAnswers({ results: [] })).toBeNull()
    expect(ohMyPiQuestionAnswers({ results: ['x', null] })).toBeNull()
  })

  it('reads a single question the reader left blank as no answer', () => {
    expect(ohMyPiQuestionAnswers({ question: 'Q?', selectedOptions: [], customInput: '  ' })).toEqual([{ header: 'Q?', answer: null }])
  })

  it('heads an answer of several by its id, else a word of its own, when it states no question', () => {
    expect(ohMyPiQuestionAnswers({ results: [{ id: 'db', selectedOptions: ['SQLite'] }, { selectedOptions: ['x'] }] })).toEqual([
      { header: 'db', answer: 'SQLite' },
      { header: 'Answer', answer: 'x' },
    ])
  })
})
