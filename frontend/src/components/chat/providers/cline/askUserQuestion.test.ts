import type { ControlQuestion } from '../../model/question'
import { describe, expect, it } from 'vitest'
import { CLINE_QUESTION_ANSWER } from '~/generated/contracts/cline-protocol'
import { createControlAnswerState } from '../../controls/types'
import { buildClineAnswer, clineIsQuestionRequest, clineQuestionsFromPayload } from './askUserQuestion'

/** A stored question request whose executor states `args`. */
function question(args: unknown, capabilityName = 'tool_executor.askQuestion'): Record<string, unknown> {
  return {
    version: 'v1',
    event: 'capability.requested',
    sessionId: 's1',
    payload: { requestId: 'capreq_1', capabilityName, payload: { executor: 'askQuestion', args } },
  }
}

const COLORS: ControlQuestion[] = [{ question: 'Which color?', options: [{ value: 'Red', label: 'Red' }, { value: 'Blue', label: 'Blue' }] }]

/** The answer text of one control response. */
function answerOf(response: Record<string, unknown>): unknown {
  return ((response.response as Record<string, unknown>).response as Record<string, unknown>)[CLINE_QUESTION_ANSWER.Answer]
}

describe('clineIsQuestionRequest', () => {
  it('recognizes the question capability of a capability request alone', () => {
    expect(clineIsQuestionRequest(question(['Q?', []]))).toBe(true)
    expect(clineIsQuestionRequest(question(['Q?', []], 'tool_executor.other'))).toBe(false)
    expect(clineIsQuestionRequest({ version: 'v1', event: 'approval.requested', payload: { capabilityName: 'tool_executor.askQuestion' } })).toBe(false)
    expect(clineIsQuestionRequest({})).toBe(false)
  })
})

describe('clineQuestionsFromPayload', () => {
  it('trims the question, and keeps only the options that are text', () => {
    expect(clineQuestionsFromPayload(question(['  Which color?  ', ['Red', 3, null, 'Blue']]))).toEqual(COLORS)
  })

  it('reads a question with no option list as a question with no option', () => {
    expect(clineQuestionsFromPayload(question(['Why?']))).toEqual([{ question: 'Why?', options: [] }])
    expect(clineQuestionsFromPayload(question(['Why?', 'not a list']))).toEqual([{ question: 'Why?', options: [] }])
  })

  it('reads no question from arguments that are not the executor\'s list', () => {
    for (const args of [undefined, {}, 'Which color?', [], [3, ['Red']]])
      expect(clineQuestionsFromPayload(question(args)), JSON.stringify(args)).toEqual([])
  })

  it('reads no question from a request that is not a question', () => {
    expect(clineQuestionsFromPayload(question(['Q?', ['A']], 'tool_executor.other'))).toEqual([])
  })
})

describe('buildClineAnswer', () => {
  const build = (selected: string[] | undefined, typed: string | undefined, questions = COLORS) => buildClineAnswer(
    'capreq_1',
    questions,
    createControlAnswerState({ ...(selected ? { selections: { 0: selected } } : {}), ...(typed !== undefined ? { customTexts: { 0: typed } } : {}) }),
  )

  it('answers with the picked option when the typed words are blank', () => {
    expect(answerOf(build(['Blue'], '   '))).toBe('Blue')
  })

  // The question offers its options, and a value that no option states is not the
  // reader's answer. The first offered one wins when a state holds several.
  it('ignores a pick that the question does not offer', () => {
    expect(answerOf(build(['Green'], ''))).toBe('')
    expect(answerOf(build(['Green', 'Red', 'Blue'], ''))).toBe('Red')
  })

  it('answers with nothing when the reader picked nothing and typed nothing', () => {
    expect(answerOf(build(undefined, undefined))).toBe('')
    expect(answerOf(build([], ''))).toBe('')
  })

  it('answers with the typed words of a question that offers no option', () => {
    expect(answerOf(build(['Red'], ' Teal ', [{ question: 'Which color?', options: [] }]))).toBe('Teal')
    expect(answerOf(build(['Red'], '', [{ question: 'Which color?', options: [] }]))).toBe('')
  })
})
