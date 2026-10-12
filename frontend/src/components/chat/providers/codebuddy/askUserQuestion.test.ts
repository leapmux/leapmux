import { describe, expect, it } from 'vitest'
import { codebuddyAskUserQuestions, codebuddyIsAskUserQuestion } from './askUserQuestion'

function payload(questions: unknown, toolName: string = 'AskUserQuestion') {
  return { request: { tool_name: toolName, input: { questions } } }
}

describe('codebuddyIsAskUserQuestion', () => {
  it('answers for the question tool', () => {
    expect(codebuddyIsAskUserQuestion(payload([]))).toBe(true)
  })

  it('refuses another tool and a payload with none', () => {
    expect(codebuddyIsAskUserQuestion(payload([], 'Bash'))).toBe(false)
    expect(codebuddyIsAskUserQuestion({})).toBe(false)
  })
})

describe('codebuddyAskUserQuestions', () => {
  it('reads each question and its options', () => {
    expect(codebuddyAskUserQuestions(payload([
      { question: 'Which one?', header: 'Pick', options: [{ label: 'A', description: 'first' }], multiSelect: true },
    ]))).toEqual([
      { question: 'Which one?', header: 'Pick', options: [{ label: 'A', description: 'first' }], multiSelect: true },
    ])
  })

  // The field is a boolean on the wire. A model that writes anything else must
  // not open the multiple-choice control.
  it('opens the multiple-choice control only for a real true', () => {
    expect(codebuddyAskUserQuestions(payload([
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
    expect(codebuddyAskUserQuestions(payload([null, 'plain text', 7, { question: 'Which one?', options: [] }])))
      .toEqual([{ question: 'Which one?', options: [] }])
  })

  it('answers an empty list for a payload that states no questions', () => {
    expect(codebuddyAskUserQuestions(payload(undefined))).toEqual([])
    expect(codebuddyAskUserQuestions(payload('nope'))).toEqual([])
    expect(codebuddyAskUserQuestions({})).toEqual([])
  })
})
