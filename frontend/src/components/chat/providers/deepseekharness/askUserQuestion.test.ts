import { describe, expect, it } from 'vitest'
import { createControlAnswerState } from '../../controls/types'
import { buildDeepseekHarnessAnswers, deepseekHarnessIsQuestionRequest, deepseekHarnessQuestions } from './askUserQuestion'

describe('deepseekHarnessQuestions', () => {
  it('preserves native question IDs and option labels', () => {
    const payload = { event: 'user-questions/request', request: { questions: [{ id: 'native-question', question: 'Choose a result.', header: 'Result', multiSelect: true, options: [{ label: 'First', description: 'Use the first result.' }, { label: 'Second' }] }] } }
    expect(deepseekHarnessIsQuestionRequest(payload)).toBe(true)
    expect(deepseekHarnessQuestions(payload)).toEqual([{ id: 'native-question', question: 'Choose a result.', header: 'Result', multiSelect: true, options: [{ label: 'First', value: 'First', description: 'Use the first result.' }, { label: 'Second', value: 'Second' }] }])
  })

  it('keeps a free-text question and drops malformed records', () => {
    expect(deepseekHarnessQuestions({ questions: [null, 'invalid', { id: 'empty-options', question: 'Enter text.' }, { question: 'Missing ID' }, { id: 'missing-question' }] })).toEqual([{ id: 'empty-options', question: 'Enter text.', options: [] }])
    expect(deepseekHarnessQuestions({ questions: null })).toEqual([])
  })

  it('sends native selections and custom text while refusing an unoffered option', () => {
    const questions = deepseekHarnessQuestions({ questions: [
      { id: 'single', question: 'Choose one.', options: [{ label: 'First' }, { label: 'Second' }] },
      { id: 'multi', question: 'Choose several.', multiSelect: true, options: [{ label: 'First' }, { label: 'Second' }] },
    ] })
    const state = createControlAnswerState({ selections: { 0: ['unknown', 'First', 'Second'], 1: ['First', 'Second'] }, customTexts: { 0: '  Native note.  ', 1: '' } })
    expect(buildDeepseekHarnessAnswers('request', questions, state)).toMatchObject({ response: { request_id: 'request', response: { behavior: 'allow', answers: [
      { id: 'single', selected: ['First'], custom: 'Native note.' },
      { id: 'multi', selected: ['First', 'Second'] },
    ] } } })
  })

  it('routes native plan-review questions through the plan approval surface', () => {
    const payload = { event: 'user-questions/request', request: { questions: [{ id: 'plan-review', question: 'Approve the plan?', intent: { kind: 'plan-review', approve: 'Approve', callId: 'native-plan' }, options: [] }] } }
    expect(deepseekHarnessIsQuestionRequest(payload)).toBe(false)
  })
})
