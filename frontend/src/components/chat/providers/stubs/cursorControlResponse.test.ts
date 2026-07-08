import type { PersistedControlResponse } from '../../persistedControlResponse'
import { describe, expect, it } from 'vitest'
import { cursorControlResponseDisplay } from './cursorControlResponse'

function cr(request: Record<string, unknown> | undefined, response: Record<string, unknown> | undefined): PersistedControlResponse {
  return { provider: 'CURSOR', requestId: '7', request, response }
}

const QUESTION_REQUEST = {
  method: 'cursor/ask_question',
  params: {
    questions: [
      { id: 'q1', prompt: 'Pick a color', options: [{ id: 'o1', label: 'Red' }, { id: 'o2', label: 'Blue' }] },
      { id: 'q2', prompt: 'Pick a size', options: [{ id: 's1', label: 'Large' }] },
    ],
  },
}

function questionOutcome(outcome: Record<string, unknown>): Record<string, unknown> {
  return { result: { outcome } }
}

describe('cursorcontrolresponsedisplay', () => {
  describe('ask_question', () => {
    it('maps selected option ids to labels in request order', () => {
      const response = questionOutcome({
        outcome: 'answered',
        answers: [
          { questionId: 'q1', selectedOptionIds: ['o1', 'o2'] },
          { questionId: 'q2', selectedOptionIds: ['s1'] },
        ],
      })
      expect(cursorControlResponseDisplay(cr(QUESTION_REQUEST, response)))
        .toEqual({ kind: 'label', text: 'Pick a color: Red, Blue\nPick a size: Large' })
    })

    it('passes unknown option ids through and drops empty selections', () => {
      const response = questionOutcome({
        outcome: 'answered',
        answers: [
          { questionId: 'q1', selectedOptionIds: ['o1', 'unknown'] },
          { questionId: 'q2', selectedOptionIds: [] },
        ],
      })
      expect(cursorControlResponseDisplay(cr(QUESTION_REQUEST, response)))
        .toEqual({ kind: 'label', text: 'Pick a color: Red, unknown' })
    })

    it('renders a cancellation reason as feedback, else the Cancel label', () => {
      expect(cursorControlResponseDisplay(cr(QUESTION_REQUEST, questionOutcome({ outcome: 'cancelled', reason: 'changed mind' }))))
        .toEqual({ kind: 'feedback', message: 'changed mind' })
      expect(cursorControlResponseDisplay(cr(QUESTION_REQUEST, questionOutcome({ outcome: 'skipped' }))))
        .toEqual({ kind: 'label', text: 'Cancel' })
    })
  })

  describe('create_plan', () => {
    const request = { method: 'cursor/create_plan' }

    it('labels an accepted plan', () => {
      expect(cursorControlResponseDisplay(cr(request, questionOutcome({ outcome: 'accepted' }))))
        .toEqual({ kind: 'label', text: 'Accept' })
    })

    it('renders a rejection reason as feedback, else Reject', () => {
      expect(cursorControlResponseDisplay(cr(request, questionOutcome({ outcome: 'rejected', reason: 'Needs tests.' }))))
        .toEqual({ kind: 'feedback', message: 'Needs tests.' })
      expect(cursorControlResponseDisplay(cr(request, questionOutcome({ outcome: 'rejected' }))))
        .toEqual({ kind: 'label', text: 'Reject' })
    })
  })

  it('delegates to the ACP permission path for a plain permission selection', () => {
    const request = { method: 'session/request_permission', params: { options: [{ optionId: 'proceed_once', name: 'Allow once' }] } }
    const response = { result: { outcome: { optionId: 'proceed_once' } } }
    expect(cursorControlResponseDisplay(cr(request, response))).toEqual({ kind: 'label', text: 'Allow once' })
  })

  describe('request-gone (pruned request absent)', () => {
    // The pruned request (and its method) is absent, but a create_plan / permission outcome is
    // recoverable from result.outcome alone, so it renders instead of degrading to "Responded".
    it('recovers a create_plan decision from the response', () => {
      expect(cursorControlResponseDisplay(cr(undefined, questionOutcome({ outcome: 'accepted' }))))
        .toEqual({ kind: 'label', text: 'Accept' })
      expect(cursorControlResponseDisplay(cr(undefined, questionOutcome({ outcome: 'rejected', reason: 'Needs tests.' }))))
        .toEqual({ kind: 'feedback', message: 'Needs tests.' })
      expect(cursorControlResponseDisplay(cr(undefined, questionOutcome({ outcome: 'cancelled', reason: 'changed mind' }))))
        .toEqual({ kind: 'feedback', message: 'changed mind' })
    })

    it('still resolves a permission selection via the response-based ACP path', () => {
      expect(cursorControlResponseDisplay(cr(undefined, { result: { outcome: { optionId: 'proceed_once' } } })))
        .toEqual({ kind: 'label', text: 'Allow once' })
    })
  })
})
