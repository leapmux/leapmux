import type { ControlQuestion } from '../../model/question'
import { describe, expect, it } from 'vitest'
import { grokQuestionAnswerLines } from './askUserQuestion'

/**
 * The questions of Grok's question dialog as the probe recorded it, as the Worker reads
 * them into the prompt (`providerkit.QuestionsFromWire` in Go).
 */
const QUESTIONS: ControlQuestion[] = [
  {
    question: 'Which database?',
    options: [
      { label: 'Postgres (Recommended)', description: 'Relational', preview: 'CREATE TABLE t ()' },
      { label: 'Redis', description: 'In-memory' },
    ],
  },
  {
    question: 'Which extras?',
    options: [
      { label: 'Caching', description: 'Add a cache' },
      { label: 'Metrics', description: 'Add metrics' },
    ],
    multiSelect: true,
  },
]

describe('grokQuestionAnswerLines', () => {
  it('states one line for each answered question, in the order of the dialog', () => {
    const result = {
      outcome: 'accepted',
      answers: {
        'Which extras?': ['Caching', 'Metrics'],
        'Which database?': ['Postgres (Recommended)'],
      },
      annotations: {
        'Which database?': { notes: 'Use version 16' },
      },
    }
    expect(grokQuestionAnswerLines(QUESTIONS, result)).toBe('Which database?: Postgres (Recommended), Use version 16\nWhich extras?: Caching, Metrics')
  })

  it('shows the notes rather than the free-text label', () => {
    expect(grokQuestionAnswerLines(QUESTIONS, {
      answers: {
        'Which database?': ['Other'],
      },
      annotations: {
        'Which database?': { notes: 'SQLite' },
      },
    })).toBe('Which database?: SQLite')
  })

  it('keeps the label when the question offers an option of that name', () => {
    const questions: ControlQuestion[] = [
      {
        question: 'Pick',
        options: [
          { label: 'Other' },
          { label: 'This' },
        ],
      },
    ]
    expect(grokQuestionAnswerLines(questions, {
      answers: {
        Pick: ['Other'],
      },
    })).toBe('Pick: Other')
  })

  // The Worker reads no question from a dialog whose question list is absent or no
  // list, so the answers state their own order.
  it('reads the answers alone when the prompt states no question', () => {
    expect(grokQuestionAnswerLines([], {
      answers: {
        'Which extras?': ['Caching'],
      },
    })).toBe('Which extras?: Caching')
  })

  it('answers null when nothing was answered', () => {
    expect(grokQuestionAnswerLines(QUESTIONS, { outcome: 'accepted', answers: {} })).toBeNull()
    expect(grokQuestionAnswerLines(QUESTIONS, { outcome: 'cancelled' })).toBeNull()
  })

  it('states only the questions the dialog asked, and skips an answer that is no list', () => {
    const result = {
      answers: {
        'Which database?': 'Redis',
        'Which extras?': ['Metrics'],
        'Never asked': ['X'],
      },
    }
    expect(grokQuestionAnswerLines(QUESTIONS, result)).toBe('Which extras?: Metrics')
  })

  it('ignores an annotation that is no object', () => {
    expect(grokQuestionAnswerLines(QUESTIONS, {
      answers: {
        'Which extras?': ['Metrics'],
      },
      annotations: { 'Which extras?': 'loose note' },
    })).toBe('Which extras?: Metrics')
  })

  // The saved keys are the model's own text, so a key that spells a prototype member
  // must read as the answer the reply holds, and an absent key as no annotation.
  it('reads the answer of a question worded like a prototype member', () => {
    const questions: ControlQuestion[] = [
      {
        question: 'toString',
        options: [
          { label: 'A' },
        ],
      },
      {
        question: '__proto__',
        options: [
          { label: 'B' },
        ],
      },
    ]
    const result = JSON.parse('{"answers":{"toString":["A"],"__proto__":["B"]},"annotations":{}}') as Record<string, unknown>
    expect(grokQuestionAnswerLines(questions, result)).toBe('toString: A\n__proto__: B')
  })
})
