import { describe, expect, it } from 'vitest'
import { createControlAnswerState } from '../../controls/types'
import {
  grokQuestionAnswerLines,
  grokQuestionReply,
  grokQuestions,
  isGrokQuestionPayload,
  sendGrokQuestionRejectResponse,
  sendGrokQuestionResponse,
} from './askUserQuestion'

/** Grok's question dialog as the probe recorded it. */
const PAYLOAD = {
  jsonrpc: '2.0',
  id: 0,
  method: '_x.ai/ask_user_question',
  params: {
    sessionId: 's',
    toolCallId: 'call_2_0',
    questions: [
      { question: 'Which database?', options: [{ label: 'Postgres (Recommended)', description: 'Relational', preview: 'CREATE TABLE t ()' }, { label: 'Redis', description: 'In-memory' }], multiSelect: null },
      { question: 'Which extras?', options: [{ label: 'Caching', description: 'Add a cache' }, { label: 'Metrics', description: 'Add metrics' }], multiSelect: true },
    ],
    mode: 'default',
  },
}

function answered(selections: Record<number, string[]>, customTexts: Record<number, string> = {}) {
  return createControlAnswerState({ selections, customTexts })
}

async function sentReply(send: (respond: (bytes: Uint8Array) => Promise<void>) => Promise<void>): Promise<unknown[]> {
  const sent: unknown[] = []
  await send(async (bytes) => {
    sent.push(JSON.parse(new TextDecoder().decode(bytes)))
  })
  return sent
}

describe('isGrokQuestionPayload', () => {
  it('matches the question method alone', () => {
    expect(isGrokQuestionPayload(PAYLOAD)).toBe(true)
    expect(isGrokQuestionPayload({ method: '_x.ai/exit_plan_mode' })).toBe(false)
    expect(isGrokQuestionPayload({})).toBe(false)
  })
})

describe('grokQuestions', () => {
  it('reads each question and folds a null multiSelect to false', () => {
    const questions = grokQuestions(PAYLOAD)
    expect(questions.map(question => question.multiSelect)).toEqual([false, true])
    expect(questions[0]?.options[0]).toEqual({ label: 'Postgres (Recommended)', description: 'Relational', preview: 'CREATE TABLE t ()' })
  })

  it('answers no question for a payload that states none', () => {
    expect(grokQuestions({ method: '_x.ai/ask_user_question' })).toEqual([])
    expect(grokQuestions({ params: { questions: 'text' } })).toEqual([])
  })
})

describe('grokQuestionReply', () => {
  const questions = grokQuestions(PAYLOAD)

  it('keys each answer by the question text, with the chosen labels', () => {
    expect(grokQuestionReply(questions, answered({ 1: ['Caching', 'Metrics'] }))).toEqual({
      outcome: 'accepted',
      answers: { 'Which extras?': ['Caching', 'Metrics'] },
    })
  })

  it('sends the notes beside a chosen option, and the preview of a single choice', () => {
    expect(grokQuestionReply(questions, answered({ 0: ['Postgres (Recommended)'] }, { 0: ' Use version 16 ' }))).toEqual({
      outcome: 'accepted',
      answers: { 'Which database?': ['Postgres (Recommended)'] },
      annotations: { 'Which database?': { preview: 'CREATE TABLE t ()', notes: 'Use version 16' } },
    })
  })

  it('answers a typed answer alone with the free-text label', () => {
    expect(grokQuestionReply(questions, answered({}, { 0: 'SQLite' }))).toEqual({
      outcome: 'accepted',
      answers: { 'Which database?': ['Other'] },
      annotations: { 'Which database?': { notes: 'SQLite' } },
    })
  })

  // A preview belongs to ONE choice, so a multi-select answer sends none, even with
  // one option chosen.
  it('sends no preview for a multi-select question', () => {
    const multi = grokQuestions({ params: { questions: [{ question: 'Which?', options: [{ label: 'A', preview: 'a()' }, { label: 'B' }], multiSelect: true }] } })
    expect(grokQuestionReply(multi, answered({ 0: ['A'] }))).toEqual({ outcome: 'accepted', answers: { 'Which?': ['A'] } })
  })

  it('sends no annotation for a single choice that carries no preview', () => {
    expect(grokQuestionReply(questions, answered({ 0: ['Redis'] }))).toEqual({ outcome: 'accepted', answers: { 'Which database?': ['Redis'] } })
  })

  it('omits a question the reader left empty, and whitespace is no answer', () => {
    expect(grokQuestionReply(questions, answered({}, { 1: '   ' }))).toEqual({ outcome: 'accepted', answers: {} })
  })

  // The keys are the model's own question text, and an assignment would set the
  // prototype for this one rather than add the answer.
  it('keeps an answer to a question worded like a prototype key', () => {
    const tricky = grokQuestions({ params: { questions: [{ question: '__proto__', options: [{ label: 'Yes' }] }] } })
    const reply = grokQuestionReply(tricky, answered({ 0: ['Yes'] }))
    expect(Object.hasOwn(reply.answers as object, '__proto__')).toBe(true)
    expect(JSON.parse(JSON.stringify(reply))).toEqual({ outcome: 'accepted', answers: { ['__proto__']: ['Yes'] } })
  })
})

describe('sendGrokQuestionResponse', () => {
  it('sends the reply under the worker request id', async () => {
    const questions = grokQuestions(PAYLOAD)
    const sent = await sentReply(respond => sendGrokQuestionResponse(respond, 'jsonrpc:0', questions, answered({ 1: ['Metrics'] })))
    expect(sent).toEqual([{ jsonrpc: '2.0', id: 'jsonrpc:0', result: { outcome: 'accepted', answers: { 'Which extras?': ['Metrics'] } } }])
  })
})

describe('sendGrokQuestionRejectResponse', () => {
  it('sends the cancelled outcome, which carries no reason', async () => {
    const sent = await sentReply(respond => sendGrokQuestionRejectResponse(respond, 'jsonrpc:0'))
    expect(sent).toEqual([{ jsonrpc: '2.0', id: 'jsonrpc:0', result: { outcome: 'cancelled' } }])
  })
})

describe('grokQuestionAnswerLines', () => {
  it('states one line for each answered question, in the order of the dialog', () => {
    const result = {
      outcome: 'accepted',
      answers: { 'Which extras?': ['Caching', 'Metrics'], 'Which database?': ['Postgres (Recommended)'] },
      annotations: { 'Which database?': { notes: 'Use version 16' } },
    }
    expect(grokQuestionAnswerLines(PAYLOAD, result)).toBe('Which database?: Postgres (Recommended), Use version 16\nWhich extras?: Caching, Metrics')
  })

  it('shows the notes rather than the free-text label', () => {
    expect(grokQuestionAnswerLines(PAYLOAD, { answers: { 'Which database?': ['Other'] }, annotations: { 'Which database?': { notes: 'SQLite' } } })).toBe('Which database?: SQLite')
  })

  it('keeps the label when the question offers an option of that name', () => {
    const request = { params: { questions: [{ question: 'Pick', options: [{ label: 'Other' }, { label: 'This' }] }] } }
    expect(grokQuestionAnswerLines(request, { answers: { Pick: ['Other'] } })).toBe('Pick: Other')
  })

  it('reads the answers alone when the request is gone', () => {
    expect(grokQuestionAnswerLines(undefined, { answers: { 'Which extras?': ['Caching'] } })).toBe('Which extras?: Caching')
  })

  it('answers null when nothing was answered', () => {
    expect(grokQuestionAnswerLines(PAYLOAD, { outcome: 'accepted', answers: {} })).toBeNull()
    expect(grokQuestionAnswerLines(PAYLOAD, { outcome: 'cancelled' })).toBeNull()
  })

  it('states only the questions the dialog asked, and skips an answer that is no list', () => {
    const result = { answers: { 'Which database?': 'Redis', 'Which extras?': ['Metrics'], 'Never asked': ['X'] } }
    expect(grokQuestionAnswerLines(PAYLOAD, result)).toBe('Which extras?: Metrics')
  })

  it('ignores an annotation that is no object', () => {
    expect(grokQuestionAnswerLines(PAYLOAD, { answers: { 'Which extras?': ['Metrics'] }, annotations: { 'Which extras?': 'loose note' } })).toBe('Which extras?: Metrics')
  })

  // The saved keys are the model's own text, so a key that spells a prototype member
  // must read as the answer the reply holds, and an absent key as no annotation.
  it('reads the answer of a question worded like a prototype member', () => {
    const request = { params: { questions: [{ question: 'toString', options: [{ label: 'A' }] }, { question: '__proto__', options: [{ label: 'B' }] }] } }
    const result = JSON.parse('{"answers":{"toString":["A"],"__proto__":["B"]},"annotations":{}}') as Record<string, unknown>
    expect(grokQuestionAnswerLines(request, result)).toBe('toString: A\n__proto__: B')
  })
})
