import type { ControlPrompt } from '../../model/controlPrompt'
import type { PersistedControlResponse } from '../../persistedControlResponse'
import { describe, expect, it } from 'vitest'
import { GENERIC_PERMISSION_PROMPT } from '~/test-support/controlRequests'
import { codexAsyncQuestionSummary } from './asyncQuestion'

const REQUEST_ID = 'codex-async-question-call_abc-0'

/**
 * One async question request, as the worker publishes it: the tool name is
 * LeapMux's own, and the input carries the Codex question identity beside the
 * title and the options.
 */
const ASYNC_REQUEST = {
  request: {
    tool_name: 'CodexAsyncQuestion',
    input: {
      question_item_id: '["request_user_input_async","call_abc",0]',
      title: 'Which color should I use?',
      options: ['Blue', 'Red'],
    },
  },
}

/** The prompt that the Worker reads from {@link ASYNC_REQUEST} (`codexAsyncQuestions` in Go). */
const ASYNC_PROMPT: ControlPrompt = {
  kind: 'question',
  questions: [
    {
      question: 'Which color should I use?',
      options: [
        { label: 'Blue' },
        { label: 'Red' },
      ],
    },
  ],
  preservesSelectionNotes: true,
}

/** The neutral envelope, as the Worker answers the question and persists the answer. */
function envelope(inner: Record<string, unknown>): Record<string, unknown> {
  return {
    type: 'control_response',
    response: { subtype: 'success', request_id: REQUEST_ID, response: inner },
  }
}

/** One saved row. `null` states a row that records no prompt, or whose request is gone. */
function row(response: Record<string, unknown> | undefined, prompt: ControlPrompt | null = ASYNC_PROMPT, request: Record<string, unknown> | null = ASYNC_REQUEST): PersistedControlResponse {
  return {
    requestId: REQUEST_ID,
    claimToken: 'claim-1',
    request: request ?? undefined,
    response,
    ...(prompt
      ? { prompt }
      : {}),
  }
}

describe('codexAsyncQuestionSummary', () => {
  it.each(['allow', 'deny'])('normalizes a padded %s behavior before formatting the question', (behavior) => {
    const expected = behavior === 'allow'
      ? 'Which color should I use?: Red'
      : 'Skipped: Which color should I use?'
    expect(codexAsyncQuestionSummary(row(envelope({ behavior: ` ${behavior}\t`, answer: 'Red' }))))
      .toEqual({ kind: 'label', text: expected })
  })

  it('reads a skip under the title of the skipped question', () => {
    expect(codexAsyncQuestionSummary(row(envelope({ behavior: 'deny', message: 'Rejected by user.' }))))
      .toEqual({ kind: 'label', text: 'Skipped: Which color should I use?' })
  })

  it('reads an answer as the question with its answer', () => {
    expect(codexAsyncQuestionSummary(row(envelope({ behavior: 'allow', answer: 'Red' }))))
      .toEqual({ kind: 'label', text: 'Which color should I use?: Red' })
  })

  // The title is the question of the prompt that the banner drew, not the input of
  // the stored request.
  it('reads the title from the recorded prompt, not from the native request', () => {
    const prompt: ControlPrompt = {
      kind: 'question',
      questions: [
        { question: 'The recorded title', options: [] },
      ],
    }
    expect(codexAsyncQuestionSummary(row(envelope({ behavior: 'allow', answer: 'Red' }), prompt)))
      .toEqual({ kind: 'label', text: 'The recorded title: Red' })
  })

  // The prompt lives in the answer row's own metadata, so a row whose request is gone
  // still reads its title.
  it('reads the title of a question whose native request is gone', () => {
    expect(codexAsyncQuestionSummary(row(envelope({ behavior: 'deny' }), ASYNC_PROMPT, null)))
      .toEqual({ kind: 'label', text: 'Skipped: Which color should I use?' })
  })

  // The Worker reads no question from a request with no title, so the prompt states
  // none, and a reader could only skip it.
  it('reads a question with no title as the skip or the answer alone', () => {
    const untitled: ControlPrompt = { kind: 'question', questions: [], preservesSelectionNotes: true }
    expect(codexAsyncQuestionSummary(row(envelope({ behavior: 'deny' }), untitled))).toEqual({ kind: 'label', text: 'Skipped' })
    expect(codexAsyncQuestionSummary(row(envelope({ behavior: 'allow', answer: 'Red' }), untitled))).toEqual({ kind: 'label', text: 'Red' })
    expect(codexAsyncQuestionSummary(row(envelope({ behavior: 'allow', answer: '' }), untitled))).toBeNull()
  })

  it('answers nothing for a prompt that is no question', () => {
    expect(codexAsyncQuestionSummary(row(envelope({ behavior: 'allow', answer: 'Red' }), GENERIC_PERMISSION_PROMPT, {
      request: { tool_name: 'Bash', input: {} },
    })))
      .toBeNull()
    expect(codexAsyncQuestionSummary(row(envelope({ behavior: 'allow' }), { kind: 'plan' }))).toBeNull()
  })

  // requestUserInput is a question too, and the Worker answers it with a JSON-RPC
  // result rather than the neutral envelope.
  it('answers nothing for a question that the answer states as a JSON-RPC result', () => {
    expect(codexAsyncQuestionSummary(row({ result: {} }))).toBeNull()
    expect(codexAsyncQuestionSummary(row({
      result: {
        answers: {
          q0: {
            answers: ['Red'],
          },
        },
      },
    }))).toBeNull()
  })

  it('answers nothing for a row that records no prompt', () => {
    expect(codexAsyncQuestionSummary(row(envelope({ behavior: 'allow', answer: 'Red' }), null))).toBeNull()
  })
})
