import { describe, expect, it } from 'vitest'
import { claudeAskUserQuestions, claudeIsAskUserQuestion } from './askUserQuestion'
import { CLAUDE_TOOL_NAMES } from './toolNames'

function payload(questions: unknown, toolName: string = CLAUDE_TOOL_NAMES.ASK_USER_QUESTION) {
  return { request: { tool_name: toolName, input: { questions } } }
}

describe('claudeIsAskUserQuestion', () => {
  it('answers for both spellings of the question tool', () => {
    expect(claudeIsAskUserQuestion(payload([]))).toBe(true)
    expect(claudeIsAskUserQuestion(payload([], 'request_user_input'))).toBe(true)
  })

  it('refuses another tool', () => {
    expect(claudeIsAskUserQuestion(payload([], CLAUDE_TOOL_NAMES.BASH))).toBe(false)
    expect(claudeIsAskUserQuestion({})).toBe(false)
  })
})

describe('claudeAskUserQuestions', () => {
  it('reads each question and its options', () => {
    expect(claudeAskUserQuestions(payload([
      { question: 'Which one?', header: 'Pick', options: [{ label: 'A', description: 'first' }] },
    ]))).toEqual([
      { question: 'Which one?', header: 'Pick', options: [{ label: 'A', description: 'first' }] },
    ])
  })

  // An `Array.isArray` on the OUTER array says nothing about the elements, and the
  // control surface dereferences `question` and hands `options` to a `<For>` -- so a
  // null or a bare string among them threw the whole banner away.
  it('drops an element the control surface cannot draw', () => {
    expect(claudeAskUserQuestions(payload([null, 'plain text', 7, { question: 'Which one?', options: [] }])))
      .toEqual([{ question: 'Which one?', options: [] }])
  })

  it('answers an empty list for a payload that states no questions', () => {
    expect(claudeAskUserQuestions(payload(undefined))).toEqual([])
    expect(claudeAskUserQuestions(payload('nope'))).toEqual([])
    expect(claudeAskUserQuestions({})).toEqual([])
  })
})
