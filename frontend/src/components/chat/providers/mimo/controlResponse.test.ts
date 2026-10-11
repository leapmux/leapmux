import type { PersistedControlResponse } from '../../persistedControlResponse'
import { describe, expect, it } from 'vitest'
import { buildAllowResponse, buildDenyResponse } from '~/utils/controlResponse'
import { mimoControlResponseSummary } from './controlResponse'

function saved(request: Record<string, unknown> | undefined, response: Record<string, unknown> | undefined): PersistedControlResponse {
  return { requestId: 'r', claimToken: 't', request, response }
}

const PERMISSION = { type: 'permission.asked', properties: { id: 'per_1', permission: 'bash' }, request: { tool_name: 'bash' } }
const QUESTION = {
  type: 'question.asked',
  properties: { id: 'que_1', questions: [{ question: 'Which database?', header: 'Database' }, { question: 'Why?' }] },
  request: { tool_name: 'question' },
}
const PLAN = { type: 'question.asked', properties: { id: 'que_2', questions: [{ key: 'plan_exit' }] }, request: { tool_name: 'plan_exit' }, plan: '# Plan' }

describe('mimoControlResponseSummary', () => {
  it('states the permission option the reader chose', () => {
    const selected = (optionId: string) => ({ jsonrpc: '2.0', id: 'r', result: { outcome: { outcome: 'selected', optionId } } })
    expect(mimoControlResponseSummary(saved(PERMISSION, selected('once')))).toEqual({ kind: 'label', text: 'Allow once' })
    expect(mimoControlResponseSummary(saved(PERMISSION, selected('always')))).toEqual({ kind: 'label', text: 'Always allow' })
    expect(mimoControlResponseSummary(saved(PERMISSION, selected('reject')))).toEqual({ kind: 'label', text: 'Reject' })
    expect(mimoControlResponseSummary(saved(PERMISSION, { result: { outcome: { outcome: 'cancelled' } } }))).toEqual({ kind: 'label', text: 'Deny' })
    expect(mimoControlResponseSummary(saved(PERMISSION, selected('forever')))).toBeNull()
  })

  it('states each answer under its question', () => {
    expect(mimoControlResponseSummary(saved(QUESTION, { result: { answers: [['SQLite'], ['speed']] } }))).toEqual({
      kind: 'label',
      text: 'Database: SQLite\nWhy?: speed',
    })
  })

  it('states a dismissed question', () => {
    expect(mimoControlResponseSummary(saved(QUESTION, { result: { rejected: true } }))).toEqual({ kind: 'label', text: 'Dismissed' })
  })

  // The plan buttons send the shared allow/deny envelope. The envelope does not state
  // which control it answers, and the request does: a plan takes the plan words.
  it('states a plan answer in the plan words', () => {
    expect(mimoControlResponseSummary(saved(PLAN, buildAllowResponse('r', {})))).toEqual({ kind: 'label', text: 'Approve' })
    expect(mimoControlResponseSummary(saved(PLAN, buildDenyResponse('r')))).toEqual({ kind: 'label', text: 'Reject' })
    expect(mimoControlResponseSummary(saved(PLAN, buildDenyResponse('r', 'Split the migration')))).toEqual({ kind: 'feedback', message: 'Split the migration' })
    expect(mimoControlResponseSummary(saved(PLAN, { result: { rejected: true } }))).toEqual({ kind: 'label', text: 'Reject' })
  })

  // The composer's Send feedback answers a permission through the same envelope.
  it('states a permission answered through the envelope in the permission words', () => {
    expect(mimoControlResponseSummary(saved(PERMISSION, buildDenyResponse('r', 'Keep the file')))).toEqual({ kind: 'feedback', message: 'Keep the file' })
    expect(mimoControlResponseSummary(saved(PERMISSION, buildDenyResponse('r')))).toEqual({ kind: 'label', text: 'Deny' })
  })

  it('reads no answer that states no result', () => {
    expect(mimoControlResponseSummary(saved(PERMISSION, undefined))).toBeNull()
    expect(mimoControlResponseSummary(saved(PERMISSION, { jsonrpc: '2.0', id: 'r' }))).toBeNull()
  })

  // A permission answer with no chosen option is a refusal, whatever else it states.
  it('states a permission result with no outcome as a denial', () => {
    expect(mimoControlResponseSummary(saved(PERMISSION, { result: {} }))).toEqual({ kind: 'label', text: 'Deny' })
    expect(mimoControlResponseSummary(saved(PERMISSION, { result: { outcome: { outcome: 'selected' } } }))).toBeNull()
  })

  // The chosen option states its own answer, so a response whose request did not
  // survive still reads as that option.
  it('states a chosen option when the request is absent', () => {
    expect(mimoControlResponseSummary(saved(undefined, { result: { outcome: { outcome: 'selected', optionId: 'always' } } }))).toEqual({ kind: 'label', text: 'Always allow' })
  })

  it('labels an answer by its question when the question has no header, and by its position when it has neither', () => {
    const request = { type: 'question.asked', properties: { questions: [{ question: 'Why?' }, {}] }, request: { tool_name: 'question' } }
    expect(mimoControlResponseSummary(saved(request, { result: { answers: [['speed'], ['size']] } }))).toEqual({ kind: 'label', text: 'Why?: speed\nQuestion 2: size' })
  })

  // An answer beyond the questions the request kept still reaches the row, under its
  // position.
  it('labels an answer beyond the stated questions by its position', () => {
    expect(mimoControlResponseSummary(saved(QUESTION, { result: { answers: [['SQLite'], ['speed'], ['extra']] } }))).toEqual({
      kind: 'label',
      text: 'Database: SQLite\nWhy?: speed\nQuestion 3: extra',
    })
    expect(mimoControlResponseSummary(saved(undefined, { result: { answers: [['A']] } }))).toEqual({ kind: 'label', text: 'Question 1: A' })
  })

  it('trims each choice and skips an empty one, and skips a question with no answer', () => {
    expect(mimoControlResponseSummary(saved(QUESTION, { result: { answers: [['  SQLite ', '', 'Postgres'], []] } }))).toEqual({ kind: 'label', text: 'Database: SQLite, Postgres' })
  })

  it.each([
    ['no answer at all', { answers: [] }],
    ['only empty answers', { answers: [[], ['  ']] }],
    ['answers that are not a list', { answers: 'SQLite' }],
    ['no answers field', {}],
  ])('reads no answer from a question result with %s', (_name, result) => {
    expect(mimoControlResponseSummary(saved(QUESTION, { result }))).toBeNull()
  })

  // Only a rejection of a plan states a word of its own. Any other result names no
  // choice the plan buttons offer.
  it('states nothing for a plan result that is not a rejection', () => {
    expect(mimoControlResponseSummary(saved(PLAN, { result: { answers: [['Yes']] } }))).toBeNull()
    expect(mimoControlResponseSummary(saved(PLAN, { result: { rejected: false } }))).toBeNull()
  })
})
