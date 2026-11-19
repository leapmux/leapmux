import type { ControlPrompt } from '../../model/controlPrompt'
import type { PersistedControlResponse } from '../../persistedControlResponse'
import { describe, expect, it } from 'vitest'
import { resolveControlResponseSummary } from '../../persistedControlResponse'
import { openCodeControlResponseSummary } from './controlResponse'

/** The native form that the rows below answer. */
const FORM_REQUEST = {
  type: 'form.created',
  data: {
    form: {
      fields: [
        { key: 'choice', type: 'string', title: 'Choice' },
        { key: 'count', type: 'integer', title: 'Count' },
        { key: 'enabled', type: 'boolean', title: 'Enabled' },
      ],
    },
  },
}

/** The prompt that the Worker records for {@link FORM_REQUEST}. */
const FORM_PROMPT: ControlPrompt = {
  kind: 'elicitation',
  elicitation: {
    mode: 'form',
    message: '',
    schema: {
      type: 'object',
      properties: {
        choice: { title: 'Choice', type: 'string' },
        count: { title: 'Count', type: 'integer' },
        enabled: { title: 'Enabled', type: 'boolean' },
      },
      required: [],
    },
  },
}

/** One saved row of the resolved native answer, with the recorded prompt, or none for null. */
function response(native: Record<string, unknown>, prompt: ControlPrompt | null = FORM_PROMPT): PersistedControlResponse {
  return {
    claimToken: 'claim',
    requestId: 'form_1',
    request: FORM_REQUEST,
    response: { request_id: 'form_1', ...native },
    ...(prompt
      ? { prompt }
      : {}),
  }
}

describe('openCodeControlResponseSummary', () => {
  it('renders scalar native answers with their field titles', () => {
    expect(openCodeControlResponseSummary(response({
      answer: { choice: 'Green' },
    })))
      .toEqual({ kind: 'label', text: 'Choice: Green' })
  })

  it('retains zero, false, empty strings, and empty selection lists', () => {
    expect(openCodeControlResponseSummary(response({
      answer: {
        count: 0,
        enabled: false,
        choice: '',
        extras: [],
      },
    })))
      .toEqual({ kind: 'label', text: 'Count: 0\nEnabled: false\nChoice: ""\nextras: []' })
  })

  // A question of the question tool states its field's title as its header.
  it('labels a question answer with the header of the question whose id is the field key', () => {
    const prompt: ControlPrompt = {
      kind: 'question',
      questions: [
        {
          id: 'task',
          header: 'Pick a task',
          question: 'Pick a task',
          options: [],
        },
        { id: 'env', question: 'Where?', options: [] },
      ],
    }
    expect(openCodeControlResponseSummary(response({
      answer: {
        task: 'Build',
        env: ['Dev', 'Prod'],
      },
    }, prompt)))
      .toEqual({ kind: 'label', text: 'Pick a task: Build\nenv: Dev, Prod' })
  })

  // The titles come from the prompt that the banner drew, not from the stored request.
  it('labels the answers with the titles of the recorded prompt, not of the native request', () => {
    const prompt: ControlPrompt = {
      kind: 'elicitation',
      elicitation: {
        mode: 'form',
        message: '',
        schema: {
          type: 'object',
          properties: {
            choice: { title: 'Recorded' },
          },
        },
      },
    }
    expect(openCodeControlResponseSummary(response({
      answer: { choice: 'Green' },
    }, prompt)))
      .toEqual({ kind: 'label', text: 'Recorded: Green' })
  })

  it('labels each answer by its field key when the row records no prompt', () => {
    expect(openCodeControlResponseSummary(response({
      answer: { choice: 'Green' },
    }, null)))
      .toEqual({ kind: 'label', text: 'choice: Green' })
    expect(openCodeControlResponseSummary(response({
      answer: { choice: 'Green' },
    }, {
      kind: 'elicitation',
      elicitation: { mode: 'form', message: '', schema: 'not an object' },
    })))
      .toEqual({ kind: 'label', text: 'choice: Green' })
  })

  it.each([
    ['once', 'Allowed'],
    ['always', 'Always allowed'],
    ['reject', 'Denied'],
  ])('describes native permission decision %s', (optionId, text) => {
    expect(openCodeControlResponseSummary(response({
      decision: optionId,
    }))).toEqual({ kind: 'label', text })
  })

  it('describes cancellation and unanswered forms', () => {
    expect(openCodeControlResponseSummary(response({ cancel: true }))).toEqual({ kind: 'label', text: 'Cancelled' })
    expect(openCodeControlResponseSummary(response({ answer: {} }))).toEqual({ kind: 'label', text: 'Answered' })
    expect(openCodeControlResponseSummary(response({ unexpected: true }))).toBeNull()
  })

  // A row whose answer this display cannot read reads as the shared generic label.
  it('leaves an unreadable answer to the shared fallback', () => {
    const row = response({ unexpected: true })
    expect(resolveControlResponseSummary(row, openCodeControlResponseSummary)).toEqual({ kind: 'label', text: 'Responded' })
    expect(openCodeControlResponseSummary({ ...row, response: undefined })).toBeNull()
  })
})
