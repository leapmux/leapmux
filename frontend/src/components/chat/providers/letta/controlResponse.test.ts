import type { ControlPrompt } from '../../model/controlPrompt'
import type { PersistedControlResponse } from '../../persistedControlResponse'
import { describe, expect, it } from 'vitest'
import { GENERIC_PERMISSION_PROMPT } from '~/test-support/controlRequests'
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

/** One saved row: the stored answer, the native request, and the prompt that the Worker recorded. */
function storedCr(response: Record<string, unknown>, request?: Record<string, unknown>, prompt?: ControlPrompt): PersistedControlResponse {
  return {
    requestId: 'req-1',
    claimToken: 'claim-1',
    response,
    request,
    ...(prompt
      ? { prompt }
      : {}),
  }
}

describe('lettaControlResponseSummary', () => {
  it('reads a saved approval as Allow and a saved denial as Deny with its reason', () => {
    expect(resolveControlResponseSummary(storedCr(storedReply({ behavior: 'allow' }), undefined, GENERIC_PERMISSION_PROMPT), lettaControls.controlResponseDisplay))
      .toEqual({ kind: 'label', text: 'Allow' })
    expect(resolveControlResponseSummary(storedCr(storedReply({ behavior: 'deny', message: 'Not this file.' }), undefined, GENERIC_PERMISSION_PROMPT), lettaControls.controlResponseDisplay))
      .toEqual({ kind: 'feedback', message: 'Not this file.' })
    expect(resolveControlResponseSummary(storedCr(storedReply({ behavior: 'deny' }), undefined, GENERIC_PERMISSION_PROMPT), lettaControls.controlResponseDisplay))
      .toEqual({ kind: 'label', text: 'Deny' })
  })

  // A row that states no prompt cannot state a plan, so it takes the permission words.
  it('reads a saved permission whose row states no prompt with the permission words', () => {
    expect(resolveControlResponseSummary(storedCr(storedReply({ behavior: 'allow' })), lettaControls.controlResponseDisplay))
      .toEqual({ kind: 'label', text: 'Allow' })
    expect(resolveControlResponseSummary(storedCr(storedReply({ behavior: 'deny' })), lettaControls.controlResponseDisplay))
      .toEqual({ kind: 'label', text: 'Deny' })
  })

  // The recorded prompt is what the banner drew, so it decides the words. The Worker's
  // Letta Code reader records no plan prompt, so this case pins only that the words
  // come from the prompt.
  it('reads a saved answer to a plan prompt with the plan words', () => {
    expect(resolveControlResponseSummary(storedCr(storedReply({ behavior: 'allow' }), undefined, { kind: 'plan' }), lettaControls.controlResponseDisplay))
      .toEqual({ kind: 'label', text: 'Approve' })
    expect(resolveControlResponseSummary(storedCr(storedReply({ behavior: 'deny' }), undefined, { kind: 'plan' }), lettaControls.controlResponseDisplay))
      .toEqual({ kind: 'label', text: 'Reject' })
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
          {
            question: 'Which database?',
            header: 'Database',
            options: [
              { label: 'Postgres', description: 'p' },
              { label: 'SQLite', description: 's' },
            ],
          },
          {
            question: 'Run migrations?',
            header: 'Migrations',
            options: [
              { label: 'Yes', description: 'y' },
              { label: 'No', description: 'n' },
            ],
          },
        ],
      },
    }
    const questions = request.tool_input.questions
    /** The prompt that the Worker reads from the request: each question's text and option labels. */
    const prompt: ControlPrompt = {
      kind: 'question',
      questions: [
        {
          question: 'Which database?',
          options: [
            { value: 'Postgres', label: 'Postgres' },
            { value: 'SQLite', label: 'SQLite' },
          ],
        },
        {
          question: 'Run migrations?',
          options: [
            { value: 'Yes', label: 'Yes' },
            { value: 'No', label: 'No' },
          ],
        },
      ],
    }

    it('reads the answers in the order the prompt asked its questions', () => {
      const response = {
        type: 'ask_user_question_response',
        version: 2,
        toolCallId: 'ask-1',
        questions,
        status: 'answered',
        // The answers map is keyed by question text, so its stored order identifies nothing.
        answers: { 'Run migrations?': 'Yes', 'Which database?': 'Postgres' },
      }
      expect(resolveControlResponseSummary(storedCr(response, request, prompt), lettaControls.controlResponseDisplay))
        .toEqual({ kind: 'label', text: 'Which database?: Postgres\nRun migrations?: Yes' })
    })

    // The prompt, not the native request, states the questions and their order.
    it('reads the questions from the recorded prompt, not from the native request', () => {
      const response = {
        type: 'ask_user_question_response',
        version: 2,
        toolCallId: 'ask-1',
        questions,
        status: 'answered',
        answers: { 'Run migrations?': 'Yes', 'Which database?': 'Postgres' },
      }
      const reordered: ControlPrompt = {
        kind: 'question',
        questions: [
          { question: 'Run migrations?', options: [] },
          { question: 'Which database?', options: [] },
        ],
      }
      expect(resolveControlResponseSummary(storedCr(response, request, reordered), lettaControls.controlResponseDisplay))
        .toEqual({ kind: 'label', text: 'Run migrations?: Yes\nWhich database?: Postgres' })
    })

    it('reads an answered response whose row states no question prompt as no answer', () => {
      const response = {
        type: 'ask_user_question_response',
        version: 2,
        toolCallId: 'ask-1',
        questions,
        status: 'answered',
        answers: { 'Which database?': 'Postgres' },
      }
      expect(resolveControlResponseSummary(storedCr(response, request), lettaControls.controlResponseDisplay))
        .toEqual({ kind: 'label', text: 'No answer' })
      expect(resolveControlResponseSummary(storedCr(response, request, GENERIC_PERMISSION_PROMPT), lettaControls.controlResponseDisplay))
        .toEqual({ kind: 'label', text: 'No answer' })
    })

    it('reads a dismissal as Dismissed', () => {
      const response = {
        type: 'ask_user_question_response',
        version: 2,
        toolCallId: 'ask-1',
        questions,
        status: 'dismissed',
      }
      expect(resolveControlResponseSummary(storedCr(response, request, prompt), lettaControls.controlResponseDisplay))
        .toEqual({ kind: 'label', text: 'Dismissed' })
    })

    it('reads an answered response that states no answer as no answer', () => {
      const response = {
        type: 'ask_user_question_response',
        version: 2,
        toolCallId: 'ask-1',
        questions,
        status: 'answered',
        answers: {},
      }
      expect(resolveControlResponseSummary(storedCr(response, request, prompt), lettaControls.controlResponseDisplay))
        .toEqual({ kind: 'label', text: 'No answer' })
    })

    it('keeps the neutral display for a response of another type', () => {
      const response = {
        type: 'ask_user_question',
        version: 2,
        toolCallId: 'ask-1',
        questions,
        status: 'dismissed',
      }
      expect(resolveControlResponseSummary(storedCr(response, request, prompt), lettaControls.controlResponseDisplay))
        .toEqual({ kind: 'label', text: 'Responded' })
    })
  })

  it('leaves a reply that is no approval_response to the neutral display', () => {
    expect(resolveControlResponseSummary(storedCr({ behavior: 'allow' }), lettaControls.controlResponseDisplay))
      .toEqual({ kind: 'label', text: 'Responded' })
  })
})
