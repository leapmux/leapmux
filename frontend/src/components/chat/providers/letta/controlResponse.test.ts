import type { PersistedControlResponse } from '../../persistedControlResponse'
import { describe, expect, it } from 'vitest'
import { resolveControlResponseSummary } from '../../persistedControlResponse'
import { lettaControls } from './pluginControls'

/**
 * The saved answer of a permission: the flat `approval_response` payload that the
 * worker sends to the App Server (`lettaResolveControlResponse`), which the worker
 * service stores as the answer row's own bytes.
 */
function storedReply(decision: Record<string, unknown>): Record<string, unknown> {
  return { kind: 'approval_response', request_id: 'req-1', decision }
}

function storedCr(response: Record<string, unknown>, request?: Record<string, unknown>): PersistedControlResponse {
  return { requestId: 'req-1', claimToken: 'claim-1', response, request }
}

describe('lettaControlResponseSummary', () => {
  it('reads a saved approval as Allow and a saved denial as Deny with its reason', () => {
    expect(resolveControlResponseSummary(storedCr(storedReply({ behavior: 'allow' })), lettaControls.controlResponseDisplay))
      .toEqual({ kind: 'label', text: 'Allow' })
    expect(resolveControlResponseSummary(storedCr(storedReply({ behavior: 'deny', message: 'Not this file.' })), lettaControls.controlResponseDisplay))
      .toEqual({ kind: 'feedback', message: 'Not this file.' })
    expect(resolveControlResponseSummary(storedCr(storedReply({ behavior: 'deny' })), lettaControls.controlResponseDisplay))
      .toEqual({ kind: 'label', text: 'Deny' })
  })

  // The saved answer of a question is the response that the Worker sent to Letta
  // Code inside a task notification, not an `approval_response`.
  describe('a saved question response', () => {
    const request = {
      type: 'ask_user',
      requestId: 'letta-question-ask-1',
      tool_call_id: 'ask-1',
      tool_input: {
        questions: [
          { question: 'Which database?', header: 'Database', options: [{ label: 'Postgres', description: 'p' }, { label: 'SQLite', description: 's' }] },
          { question: 'Run migrations?', header: 'Migrations', options: [{ label: 'Yes', description: 'y' }, { label: 'No', description: 'n' }] },
        ],
      },
    }
    const questions = request.tool_input.questions

    it('reads the answers in the order the request asked its questions', () => {
      const response = {
        type: 'ask_user_question_response',
        version: 2,
        toolCallId: 'ask-1',
        questions,
        status: 'answered',
        // The answers map is keyed by question text, so its stored order identifies nothing.
        answers: { 'Run migrations?': 'Yes', 'Which database?': 'Postgres' },
      }
      expect(resolveControlResponseSummary(storedCr(response, request), lettaControls.controlResponseDisplay))
        .toEqual({ kind: 'label', text: 'Which database?: Postgres\nRun migrations?: Yes' })
    })

    it('reads a dismissal as Dismissed', () => {
      const response = { type: 'ask_user_question_response', version: 2, toolCallId: 'ask-1', questions, status: 'dismissed' }
      expect(resolveControlResponseSummary(storedCr(response, request), lettaControls.controlResponseDisplay))
        .toEqual({ kind: 'label', text: 'Dismissed' })
    })

    it('reads an answered response that states no answer as no answer', () => {
      const response = { type: 'ask_user_question_response', version: 2, toolCallId: 'ask-1', questions, status: 'answered', answers: {} }
      expect(resolveControlResponseSummary(storedCr(response, request), lettaControls.controlResponseDisplay))
        .toEqual({ kind: 'label', text: 'No answer' })
    })

    it('keeps the neutral display for a response of another type', () => {
      const response = { type: 'ask_user_question', version: 2, toolCallId: 'ask-1', questions, status: 'dismissed' }
      expect(resolveControlResponseSummary(storedCr(response, request), lettaControls.controlResponseDisplay))
        .toEqual({ kind: 'label', text: 'Responded' })
    })
  })

  it('leaves a reply that is no approval_response to the neutral display', () => {
    expect(resolveControlResponseSummary(storedCr({ behavior: 'allow' }), lettaControls.controlResponseDisplay))
      .toEqual({ kind: 'label', text: 'Responded' })
  })
})
