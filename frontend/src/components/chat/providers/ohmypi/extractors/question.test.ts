import { describe, expect, it } from 'vitest'
import { ohMyPiQuestionAnswers, ohMyPiQuestionPrompts } from './question'

describe('ohMyPiQuestionPrompts', () => {
  it('keeps the native form context before its question', () => {
    const context = 'Policy  applies  everywhere\nKeep values.'
    const prompts = ohMyPiQuestionPrompts([
      {
        id: 'target',
        question: 'Choose target',
        context,
        multi: true,
        initialSelections: ['0'],
        options: [
          { value: '0', label: 'Alpha' },
        ],
      },
    ])
    expect(prompts[0]?.question).toBe('Choose target')
    expect(prompts[0]).toHaveProperty('context', context)
    expect(prompts[0]?.initialSelections).toEqual(['0'])
    expect(prompts[0]?.options).toEqual([
      { value: '0', label: 'Alpha' },
    ])
  })

  it('extracts rich labels and code previews from the retained native markup', () => {
    const prompts = ohMyPiQuestionPrompts([
      {
        id: 'style',
        question: 'Choose a style',
        allowCustomText: true,
        options: [
          {
            value: '0',
            label: 'Alpha Fast const x = 1; x',
            markup: '<label><input type="radio" name="style" value="alpha"><strong>Alpha</strong> <span>Fast</span> <pre>const x = 1;\nx</pre><script>DROP_SCRIPT_MARKER</script></label>',
          },
        ],
      },
    ])
    expect(prompts[0]?.options[0]).toMatchObject({ value: '0', label: 'Alpha', description: 'Fast' })
    expect(prompts[0]?.options[0]?.preview).toContain('const x = 1;\nx')
    expect(JSON.stringify(prompts)).not.toContain('DROP_SCRIPT_MARKER')
  })
  it('reads rich bridge metadata and ignores malformed options', () => {
    expect(ohMyPiQuestionPrompts([
      {
        id: 'db',
        question: 'Which database?',
        header: 'Storage',
        options: [
          { value: '0', label: 'SQLite Single file · Recommended', markup: '<label><strong>SQLite</strong> Single file · Recommended</label>' },
          { value: '1', label: 'PostgreSQL SELECT 1', markup: '<label><strong>PostgreSQL</strong><pre>SELECT 1</pre></label>' },
          { label: '' },
          'bad',
          null,
        ],
      },
    ])).toEqual([
      {
        id: 'db',
        question: 'Which database?',
        header: 'Storage',
        options: [
          { value: '0', label: 'SQLite', description: 'Single file · Recommended' },
          { value: '1', label: 'PostgreSQL', preview: '```\nSELECT 1\n```' },
        ],
      },
    ])
  })

  it('keeps a recommendation written in a label and omits a blank code preview', () => {
    expect(ohMyPiQuestionPrompts([
      {
        id: 'a',
        question: 'Q?',
        options: [
          { value: '0', label: 'x', markup: '<label>x<pre> \n </pre></label>' },
          { value: '1', label: 'y Recommended', markup: '<label><strong>y</strong> Recommended</label>' },
        ],
      },
    ])[0]?.options).toEqual([
      { value: '0', label: 'x' },
      { value: '1', label: 'y', description: 'Recommended' },
    ])
  })

  it('states an empty id for a question with none, and reads a multi flag only when it is `true`', () => {
    expect(ohMyPiQuestionPrompts([
      { question: 'Q?', multi: 'yes' },
    ])).toEqual([
      {
        id: '',
        question: 'Q?',
        options: [],
      },
    ])
  })

  it('retains native disambiguation of duplicate labels', () => {
    const questions = ohMyPiQuestionPrompts([
      {
        id: 'targets',
        question: 'Targets',
        options: [
          { value: '0', label: 'Shared', markup: '<label>Shared<input type=checkbox></label>' },
          { value: '1', label: 'Shared (2)', markup: '<label>Shared<input type=checkbox></label>' },
          { value: '2', label: 'Alpha Fast (2)', markup: '<label><strong>Alpha</strong> Fast</label>' },
        ],
      },
    ])
    expect(questions[0]?.options.map(option => option.label)).toEqual(['Shared', 'Shared (2)', 'Alpha (2)'])
    expect(questions[0]?.options.map(option => option.value)).toEqual(['0', '1', '2'])
  })

  it('skips a question with no text, and reads no list as none', () => {
    expect(ohMyPiQuestionPrompts([
      { id: 'a', options: [] },
    ])).toEqual([])
    expect(ohMyPiQuestionPrompts(undefined)).toEqual([])
    expect(ohMyPiQuestionPrompts({ questions: [] })).toEqual([])
  })
})

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
