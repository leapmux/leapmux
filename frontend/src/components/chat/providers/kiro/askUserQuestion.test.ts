import { describe, expect, it } from 'vitest'
import { createControlAnswerState } from '../../controls/types'
import {
  isKiroUserInputPayload,
  kiroUserInputAnswer,
  kiroUserInputQuestions,
  kiroUserInputReply,
  kiroUserInputRequestQuestions,
  kiroUserInputSavedAnswer,
  sendKiroUserInputDismissal,
  sendKiroUserInputResponse,
} from './askUserQuestion'

/** Kiro's question as the probe recorded it. */
const PAYLOAD = {
  jsonrpc: '2.0',
  id: 4,
  method: '_kiro/userInput',
  params: {
    sessionId: 's',
    toolCallId: 't_q',
    question: 'Which DB?',
    options: [
      { title: 'Postgres', description: 'pg', recommended: true, subOptionsLabel: 'Extras', subOptions: [{ title: 'PostGIS', description: 'geo' }, { title: 'pgvector' }] },
      { title: 'SQLite', recommended: false },
    ],
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

describe('isKiroUserInputPayload', () => {
  it('matches the question method alone', () => {
    expect(isKiroUserInputPayload(PAYLOAD)).toBe(true)
    expect(isKiroUserInputPayload({ method: '_kiro/mcp/elicitation' })).toBe(false)
    expect(isKiroUserInputPayload({})).toBe(false)
  })
})

describe('kiroUserInputQuestions', () => {
  it('reads the question, its options and one question for each sub-option list', () => {
    expect(kiroUserInputRequestQuestions(PAYLOAD)).toEqual([
      {
        question: 'Which DB?',
        options: [
          { value: 'Postgres', label: 'Postgres (recommended)', description: 'pg' },
          { value: 'SQLite', label: 'SQLite' },
        ],
        multiSelect: false,
      },
      {
        header: 'Postgres',
        question: 'Extras for Postgres: select any to leave out',
        options: [{ value: 'PostGIS', label: 'PostGIS', description: 'geo' }, { value: 'pgvector', label: 'pgvector' }],
        multiSelect: true,
        allowEmpty: true,
      },
    ])
  })

  it('reads a free-text question with no option', () => {
    expect(kiroUserInputQuestions('Any name?', [])).toEqual([{ question: 'Any name?', options: [], multiSelect: false }])
    expect(kiroUserInputQuestions('Any name?', undefined)).toEqual([{ question: 'Any name?', options: [], multiSelect: false }])
  })

  it('reads an option that the model states as its title alone', () => {
    expect(kiroUserInputQuestions('Q', ['Yes', 'No'])[0]?.options).toEqual([{ value: 'Yes', label: 'Yes' }, { value: 'No', label: 'No' }])
  })

  it('drops an option that states no title', () => {
    expect(kiroUserInputQuestions('Q', [null, 7, '', { description: 'd' }, { title: 'Kept' }])[0]?.options).toEqual([{ value: 'Kept', label: 'Kept' }])
  })

  it('marks only an option that Kiro states as recommended with true', () => {
    expect(kiroUserInputQuestions('Q', [{ title: 'A', recommended: 'yes' }, { title: 'B', recommended: 1 }])[0]?.options).toEqual([{ value: 'A', label: 'A' }, { value: 'B', label: 'B' }])
  })

  it('titles a sub-option list that states no label', () => {
    expect(kiroUserInputQuestions('Q', [{ title: 'A', subOptions: [{ title: 'a1' }] }])[1]?.question).toBe('Choices for A: select any to leave out')
  })
})

describe('kiroUserInputAnswer', () => {
  const questions = kiroUserInputRequestQuestions(PAYLOAD)

  it('answers the title of a chosen option', () => {
    expect(kiroUserInputAnswer(questions, answered({ 0: ['SQLite'] }))).toBe('SQLite')
  })

  it('states the sub-options that the reader kept in brackets, as Kiro does', () => {
    expect(kiroUserInputAnswer(questions, answered({ 0: ['Postgres'], 1: ['PostGIS'] }))).toBe('Postgres [pgvector]')
  })

  it('states every sub-option when the reader left none out, which is Kiro\'s own start', () => {
    // The page starts with nothing selected, which is what the answer means.
    expect(kiroUserInputAnswer(questions, answered({ 0: ['Postgres'] }))).toBe('Postgres [PostGIS, pgvector]')
    expect(kiroUserInputAnswer(questions, answered({ 0: ['Postgres'], 1: [] }))).toBe('Postgres [PostGIS, pgvector]')
  })

  it('ignores a left-out value that the page does not list', () => {
    expect(kiroUserInputAnswer(questions, answered({ 0: ['Postgres'], 1: ['Not a sub-option'] }))).toBe('Postgres [PostGIS, pgvector]')
  })

  it('states no sub-option when the reader left every one out', () => {
    expect(kiroUserInputAnswer(questions, answered({ 0: ['Postgres'], 1: ['PostGIS', 'pgvector'] }))).toBe('Postgres []')
  })

  it('reads no sub-option page of an option that the reader did not choose', () => {
    expect(kiroUserInputAnswer(questions, answered({ 0: ['SQLite'], 1: ['PostGIS'] }))).toBe('SQLite')
  })

  it('lets a typed answer win over a chosen option', () => {
    expect(kiroUserInputAnswer(questions, answered({ 0: ['SQLite'] }, { 0: ' MySQL ' }))).toBe('MySQL')
  })

  it('reads a typed answer on a sub-option page, where the dialog saves it', () => {
    // The dialog advances to the page of Postgres, and the reader types there.
    expect(kiroUserInputAnswer(questions, answered({ 0: ['Postgres'] }, { 1: 'Use MySQL instead' }))).toBe('Use MySQL instead')
  })

  it('reads the typed answer of the first page that holds one', () => {
    expect(kiroUserInputAnswer(questions, answered({}, { 0: '  ', 1: 'Later page', 2: 'Unused' }))).toBe('Later page')
  })

  it('answers null when the reader chose and typed nothing', () => {
    expect(kiroUserInputAnswer(questions, answered({}, { 0: '  ' }))).toBeNull()
    expect(kiroUserInputAnswer([], answered({ 0: ['x'] }))).toBeNull()
  })
})

describe('kiroUserInputReply', () => {
  const questions = kiroUserInputRequestQuestions(PAYLOAD)

  it('answers with the action and the text', () => {
    expect(kiroUserInputReply(questions, answered({ 0: ['SQLite'] }))).toEqual({ action: 'answered', answer: 'SQLite' })
  })

  it('dismisses a dialog that the reader answered nothing in', () => {
    expect(kiroUserInputReply(questions, answered({}))).toEqual({ action: 'dismissed' })
  })
})

describe('sendKiroUserInputResponse', () => {
  it('sends the reply under the worker request id', async () => {
    const questions = kiroUserInputRequestQuestions(PAYLOAD)
    const sent = await sentReply(respond => sendKiroUserInputResponse(respond, 'jsonrpc:4', questions, answered({ 0: ['SQLite'] })))
    expect(sent).toEqual([{ jsonrpc: '2.0', id: 'jsonrpc:4', result: { action: 'answered', answer: 'SQLite' } }])
  })
})

describe('sendKiroUserInputDismissal', () => {
  it('sends the dismissal, which carries no reason', async () => {
    const sent = await sentReply(respond => sendKiroUserInputDismissal(respond, 'jsonrpc:4'))
    expect(sent).toEqual([{ jsonrpc: '2.0', id: 'jsonrpc:4', result: { action: 'dismissed' } }])
  })
})

describe('kiroUserInputSavedAnswer', () => {
  it('reads the answer of a saved reply', () => {
    expect(kiroUserInputSavedAnswer({ action: 'answered', answer: ' Postgres [PostGIS] ' })).toBe('Postgres [PostGIS]')
  })

  it('answers null for a dismissal and for an empty answer', () => {
    expect(kiroUserInputSavedAnswer({ action: 'dismissed' })).toBeNull()
    expect(kiroUserInputSavedAnswer({ action: 'answered', answer: '' })).toBeNull()
    expect(kiroUserInputSavedAnswer({ action: 'answered', answer: '   ' })).toBeNull()
    expect(kiroUserInputSavedAnswer({ action: 'answered', answer: 7 })).toBeNull()
    expect(kiroUserInputSavedAnswer({})).toBeNull()
  })
})
