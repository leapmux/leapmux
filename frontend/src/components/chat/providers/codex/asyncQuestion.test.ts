import type { ControlAnswerSeed } from '../../controls/types'
import type { PersistedControlResponse } from '../../persistedControlResponse'
import { describe, expect, it } from 'vitest'
import { createControlAnswerState } from '../../controls/types'
import { codexAsyncQuestionSummary, isCodexAsyncQuestion } from './asyncQuestion'
import { codexControls } from './pluginControls'

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

/** A question with no `options` key: the reader answers it in text. */
const FREE_TEXT_REQUEST = {
  request: {
    tool_name: 'CodexAsyncQuestion',
    input: {
      question_item_id: '["request_user_input_async","call_abc",1]',
      title: 'Which port should I use?',
    },
  },
}

/** The neutral envelope, as the browser sends it and the worker persists it. */
function envelope(inner: Record<string, unknown>): Record<string, unknown> {
  return { type: 'control_response', response: { subtype: 'success', request_id: REQUEST_ID, response: inner } }
}

describe('isCodexAsyncQuestion', () => {
  it('accepts a request that carries the LeapMux async question tool name', () => {
    expect(isCodexAsyncQuestion(ASYNC_REQUEST)).toBe(true)
    expect(isCodexAsyncQuestion(FREE_TEXT_REQUEST)).toBe(true)
  })

  it('refuses a request of another tool', () => {
    expect(isCodexAsyncQuestion({ request: { tool_name: 'CodexPlanModePrompt', input: {} } })).toBe(false)
    expect(isCodexAsyncQuestion({ method: 'item/tool/requestUserInput', params: { questions: [] } })).toBe(false)
    expect(isCodexAsyncQuestion({})).toBe(false)
  })
})

describe('codexControls askUserQuestion', () => {
  const ask = codexControls.askUserQuestion!

  it('recognizes the async question and the synchronous question alike', () => {
    expect(ask.isRequest(ASYNC_REQUEST)).toBe(true)
    expect(ask.isRequest({ method: 'item/tool/requestUserInput', params: { questions: [] } })).toBe(true)
    expect(ask.isRequest({ request: { tool_name: 'CodexPlanModePrompt', input: {} } })).toBe(false)
  })

  it('extracts the question with its options', () => {
    expect(ask.extractQuestions(ASYNC_REQUEST)).toStrictEqual([
      { question: 'Which color should I use?', options: [{ label: 'Blue' }, { label: 'Red' }] },
    ])
  })

  it('extracts a question without options as one that takes typed text', () => {
    expect(ask.extractQuestions(FREE_TEXT_REQUEST)).toStrictEqual([
      { question: 'Which port should I use?', options: [] },
    ])
  })

  it('extracts no question from a malformed payload', () => {
    // A missing title is malformed: rendering an untitled question would show
    // options for a question the reader cannot see.
    expect(ask.extractQuestions({ request: { tool_name: 'CodexAsyncQuestion', input: {} } })).toStrictEqual([])
    expect(ask.extractQuestions({ request: { tool_name: 'CodexAsyncQuestion', input: 'not an object' } })).toStrictEqual([])
    expect(ask.extractQuestions({})).toStrictEqual([])
  })

  /** Answer the request and read back the one envelope the send produced. */
  async function sentAnswer(seed: ControlAnswerSeed): Promise<unknown> {
    const replies: Array<Record<string, unknown>> = []
    await ask.sendAnswer(
      { requestId: REQUEST_ID, agentId: 'agent-1', payload: ASYNC_REQUEST },
      async (bytes) => {
        replies.push(JSON.parse(new TextDecoder().decode(bytes)))
      },
      ask.extractQuestions(ASYNC_REQUEST),
      createControlAnswerState(seed),
    )
    expect(replies).toHaveLength(1)
    return replies[0]
  }

  it('sends the selected option as the answer', async () => {
    expect(await sentAnswer({ selections: { 0: ['Red'] } })).toEqual(
      envelope({ behavior: 'allow', answer: 'Red' }),
    )
  })

  it('sends the trimmed note as the answer when the reader typed one', async () => {
    expect(await sentAnswer({ customTexts: { 0: '  A light shade  ' } })).toEqual(
      envelope({ behavior: 'allow', answer: 'A light shade' }),
    )
  })

  // Codex sets `preservesSelectionNotes`, so a selection and its note coexist
  // and both must ride the one message Codex keeps no state for.
  it('sends the selection and the note together', async () => {
    expect(await sentAnswer({ selections: { 0: ['Red'] }, customTexts: { 0: 'A light shade' } })).toEqual(
      envelope({ behavior: 'allow', answer: 'Red\n\nA light shade' }),
    )
  })

  it('sends the automatic YOLO text as the answer', async () => {
    expect(await sentAnswer({ customTexts: { 0: 'Go with the recommended option.' } })).toEqual(
      envelope({ behavior: 'allow', answer: 'Go with the recommended option.' }),
    )
  })

  it('sends a bare deny when the reader skips the question', async () => {
    const replies: Array<Record<string, unknown>> = []
    await ask.sendReject(
      { requestId: REQUEST_ID, agentId: 'agent-1', payload: ASYNC_REQUEST },
      async (bytes) => {
        replies.push(JSON.parse(new TextDecoder().decode(bytes)))
      },
      'A typed reason the skip sends on to nobody',
    )
    expect(replies).toHaveLength(1)
    // The empty reason becomes the placeholder: Codex keeps no pending question
    // state, and the skip sends nothing on to it at all.
    expect(replies[0]).toEqual(envelope({ behavior: 'deny', message: 'Rejected by user.' }))
  })
})

describe('codexAsyncQuestionSummary', () => {
  function row(response: Record<string, unknown> | undefined, request: Record<string, unknown> = ASYNC_REQUEST): PersistedControlResponse {
    return { requestId: REQUEST_ID, claimToken: 'claim-1', request, response }
  }

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

  it('answers nothing for another request', () => {
    expect(codexAsyncQuestionSummary(row(envelope({ behavior: 'allow', answer: 'Red' }), { request: { tool_name: 'Bash', input: {} } })))
      .toBeNull()
  })

  it('answers nothing when the response carries no recognizable behavior', () => {
    expect(codexAsyncQuestionSummary(row({ result: {} }))).toBeNull()
  })
})
