import type { PersistedControlResponse } from '../../persistedControlResponse'
import { describe, expect, it } from 'vitest'
import { opencodeControlResponseDisplay, opencodeQuestionAnswersText } from './questionAnswers'

const QUESTION_REQUEST = {
  type: 'question.asked',
  properties: { questions: [{ header: 'Task', question: 'Pick a task' }, { header: 'Env', question: 'Pick an environment' }] },
}

describe('opencodequestionanswerstext', () => {
  it('renders header-labeled answer lines', () => {
    expect(opencodeQuestionAnswersText(QUESTION_REQUEST, { result: { answers: [['Build'], ['Dev']] } }))
      .toBe('Task: Build\nEnv: Dev')
  })

  it('labels a rejected answer as Reject', () => {
    expect(opencodeQuestionAnswersText(QUESTION_REQUEST, { result: { rejected: true } })).toBe('Reject')
  })

  it('falls back to question text then a positional label', () => {
    const request = { type: 'question.asked', properties: { questions: [{ question: 'Pick a task' }, {}] } }
    expect(opencodeQuestionAnswersText(request, { result: { answers: [['Build'], ['Dev']] } }))
      .toBe('Pick a task: Build\nQuestion 2: Dev')
  })

  it('drops answer groups whose values are all empty', () => {
    expect(opencodeQuestionAnswersText(QUESTION_REQUEST, { result: { answers: [['Build'], ['  ', '']] } }))
      .toBe('Task: Build')
  })

  it('returns null when nothing renders', () => {
    expect(opencodeQuestionAnswersText(QUESTION_REQUEST, { result: { answers: [] } })).toBeNull()
  })
})

describe('opencodecontrolresponsedisplay', () => {
  it('renders question answers for a question.asked request', () => {
    const cr: PersistedControlResponse = { provider: 'OPENCODE', requestId: 'q1', request: QUESTION_REQUEST, response: { result: { answers: [['Build']] } } }
    expect(opencodeControlResponseDisplay(cr)).toEqual({ kind: 'label', text: 'Task: Build' })
  })

  it('delegates to the ACP permission path for a non-question request', () => {
    const cr: PersistedControlResponse = {
      provider: 'OPENCODE',
      requestId: '7',
      request: { method: 'session/request_permission', params: { options: [{ optionId: 'proceed_once', name: 'Allow once' }] } },
      response: { result: { outcome: { optionId: 'proceed_once' } } },
    }
    expect(opencodeControlResponseDisplay(cr)).toEqual({ kind: 'label', text: 'Allow once' })
  })

  describe('request-gone (pruned request absent)', () => {
    // The pruned request type is absent, but a question answer is recognizable from the response
    // shape (answers array / rejected flag), so it renders -- labeled by position, not the missing
    // question headers -- instead of degrading to the generic label.
    it('recovers question answers from the response, labeled by position', () => {
      const cr: PersistedControlResponse = { provider: 'OPENCODE', requestId: 'q1', request: undefined, response: { result: { answers: [['Build'], ['Dev']] } } }
      expect(opencodeControlResponseDisplay(cr)).toEqual({ kind: 'label', text: 'Question 1: Build\nQuestion 2: Dev' })
    })

    it('recovers a rejected question from the response', () => {
      const cr: PersistedControlResponse = { provider: 'OPENCODE', requestId: 'q1', request: undefined, response: { result: { rejected: true } } }
      expect(opencodeControlResponseDisplay(cr)).toEqual({ kind: 'label', text: 'Reject' })
    })

    it('still resolves a request-gone permission via the response-based ACP path', () => {
      const cr: PersistedControlResponse = { provider: 'OPENCODE', requestId: '7', request: undefined, response: { result: { outcome: { optionId: 'proceed_once' } } } }
      expect(opencodeControlResponseDisplay(cr)).toEqual({ kind: 'label', text: 'Allow once' })
    })
  })
})
