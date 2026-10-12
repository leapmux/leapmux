import { describe, expect, it } from 'vitest'
import { qoderAskUserQuestions, qoderIsAskUserQuestion } from './askUserQuestion'

function payload(questions: unknown, toolName: string = 'AskUserQuestion') {
  return { request: { tool_name: toolName, input: { questions } } }
}

describe('qoderIsAskUserQuestion', () => {
  it('answers for the question tool', () => {
    expect(qoderIsAskUserQuestion(payload([]))).toBe(true)
  })

  it('refuses another tool and a payload with none', () => {
    expect(qoderIsAskUserQuestion(payload([], 'WriteTodos'))).toBe(false)
    expect(qoderIsAskUserQuestion({})).toBe(false)
  })
})

describe('qoderAskUserQuestions', () => {
  it('reads each question and its options', () => {
    expect(qoderAskUserQuestions(payload([
      { question: 'Which one?', header: 'Pick', options: [{ label: 'A', description: 'first' }], multiSelect: true },
    ]))).toEqual([
      { question: 'Which one?', header: 'Pick', options: [{ label: 'A', description: 'first' }], multiSelect: true },
    ])
  })

  // The field is a boolean on the wire. A model that writes anything else must
  // not open the multiple-choice control.
  it('opens the multiple-choice control only for a real true', () => {
    expect(qoderAskUserQuestions(payload([
      { question: 'One?', options: [], multiSelect: false },
      { question: 'Two?', options: [], multiSelect: 'yes' },
      { question: 'Three?', options: [] },
    ]))).toEqual([
      { question: 'One?', options: [] },
      { question: 'Two?', options: [] },
      { question: 'Three?', options: [] },
    ])
  })

  // An `Array.isArray` on the OUTER array says nothing about the elements, and
  // the control surface dereferences `question` and hands `options` to a
  // `<For>` -- so a null or a bare string among them threw the whole banner
  // away.
  it('drops an element the control surface cannot draw', () => {
    expect(qoderAskUserQuestions(payload([null, 'plain text', 7, { question: 'Which one?', options: [] }])))
      .toEqual([{ question: 'Which one?', options: [] }])
  })

  it('answers an empty list for a payload that states no questions', () => {
    expect(qoderAskUserQuestions(payload(undefined))).toEqual([])
    expect(qoderAskUserQuestions(payload('nope'))).toEqual([])
    expect(qoderAskUserQuestions({})).toEqual([])
  })
})
