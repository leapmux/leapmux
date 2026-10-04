import { describe, expect, it } from 'vitest'
import { mimoIsQuestionRequest } from './askUserQuestion'

const QUESTION = {
  type: 'question.asked',
  properties: {
    id: 'que_1',
    questions: [
      { question: 'Which database?', header: 'Database', options: [{ label: 'SQLite' }, { label: 'Postgres' }], multiple: true },
      { question: 'Anything else?', options: [] },
    ],
  },
  request: { tool_name: 'question', tool_use_id: 'call-1' },
}

describe('mimoIsQuestionRequest', () => {
  it('recognizes a question and not a plan approval', () => {
    expect(mimoIsQuestionRequest(QUESTION)).toBe(true)
    expect(mimoIsQuestionRequest({ ...QUESTION, request: { tool_name: 'plan_exit' } })).toBe(false)
    expect(mimoIsQuestionRequest({ type: 'permission.asked' })).toBe(false)
  })

  // An MCP server's confirmation is a question on the wire, and the elicitation
  // form answers it. The question card must leave it to that form.
  it('leaves an MCP elicitation to the elicitation form', () => {
    const elicitation = { ...QUESTION, properties: { id: 'que_2', questions: [{ key: 'mcp_elicitation', header: 'docs', question: 'docs\n\nProceed?', options: [], custom: false }] } }
    expect(mimoIsQuestionRequest(elicitation)).toBe(false)
  })

  // The plan approval is the tool name the worker recorded. A question whose request
  // header states no tool name is still a question.
  it('recognizes a question whose request states no tool name', () => {
    expect(mimoIsQuestionRequest({ type: 'question.asked', properties: QUESTION.properties })).toBe(true)
    expect(mimoIsQuestionRequest({ ...QUESTION, request: {} })).toBe(true)
  })

  it('recognizes no request of another type', () => {
    expect(mimoIsQuestionRequest({ ...QUESTION, type: 'question.replied' })).toBe(false)
    expect(mimoIsQuestionRequest({})).toBe(false)
  })

  // Only the one question MiMo asks for an elicitation is one. A question that
  // carries the key beside another question is an ordinary question.
  it('recognizes a request of two questions that holds the key', () => {
    const mixed = { ...QUESTION, properties: { id: 'que_3', questions: [{ key: 'mcp_elicitation', question: 'A?' }, { question: 'B?' }] } }
    expect(mimoIsQuestionRequest(mixed)).toBe(true)
  })
})
