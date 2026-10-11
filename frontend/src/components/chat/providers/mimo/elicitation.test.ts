import { describe, expect, it } from 'vitest'
import { mimoElicitation } from './elicitation'

/**
 * MiMo's own question for an MCP server's confirmation, as MiMo HEAD asks it
 * (`mcp/elicitation.ts`): the server as the header, the server, the message and the
 * subtitle as the question, MiMo's three answers, and no free text.
 */
function elicitationQuestion(entry: Record<string, unknown> = {}): Record<string, unknown> {
  return {
    type: 'question.asked',
    properties: {
      id: 'que_1',
      sessionID: 'ses_1',
      questions: [{
        key: 'mcp_elicitation',
        header: 'docs',
        question: 'docs\n\nProceed with the lookup?\n\nThe server reads the public index.',
        options: [{ label: 'Accept', description: '' }, { label: 'Decline', description: '' }, { label: 'Cancel', description: '' }],
        multiple: false,
        custom: false,
        ...entry,
      }],
    },
    request: { tool_name: 'question' },
  }
}

describe('mimoElicitation', () => {
  it('reads the server and the message into an empty confirmation form', () => {
    expect(mimoElicitation(elicitationQuestion())).toEqual({
      mode: 'form',
      server: 'docs',
      message: 'Proceed with the lookup?\n\nThe server reads the public index.',
      schema: { type: 'object', properties: {} },
    })
  })

  // MiMo joins the parts it has, so a question that does not open with the server
  // is stated whole.
  it('states the whole question when it does not open with the server', () => {
    expect(mimoElicitation(elicitationQuestion({ question: 'Proceed?' }))).toMatchObject({ server: 'docs', message: 'Proceed?' })
    expect(mimoElicitation(elicitationQuestion({ header: '', question: 'Proceed?' }))).toEqual({
      mode: 'form',
      message: 'Proceed?',
      schema: { type: 'object', properties: {} },
    })
  })

  it('states the server when the question holds nothing else', () => {
    expect(mimoElicitation(elicitationQuestion({ question: 'docs' }))).toMatchObject({ server: 'docs', message: 'docs' })
  })

  // The server stands in front of the message only when a blank line follows it.
  // A message that merely opens with the server's name keeps that name.
  it('keeps a message that opens with the server name but no blank line', () => {
    expect(mimoElicitation(elicitationQuestion({ question: 'docs lookup: proceed?' }))).toMatchObject({ server: 'docs', message: 'docs lookup: proceed?' })
  })

  it('states an empty message when only the server and its blank line remain', () => {
    expect(mimoElicitation(elicitationQuestion({ question: 'docs\n\n' }))).toMatchObject({ server: 'docs', message: '' })
  })

  it('states an empty message when the question states no text', () => {
    expect(mimoElicitation(elicitationQuestion({ question: undefined }))).toEqual({
      mode: 'form',
      server: 'docs',
      message: '',
      schema: { type: 'object', properties: {} },
    })
  })

  it.each([
    ['a question with another key', elicitationQuestion({ key: 'plan_exit' })],
    ['a question with no key', elicitationQuestion({ key: undefined })],
    ['a permission', { type: 'permission.asked', properties: { questions: [{ key: 'mcp_elicitation' }] } }],
    ['two questions', { type: 'question.asked', properties: { questions: [{ key: 'mcp_elicitation' }, { key: 'mcp_elicitation' }] } }],
    ['a questions field that is not a list', { type: 'question.asked', properties: { questions: { key: 'mcp_elicitation' } } }],
    ['a question that is not an object', { type: 'question.asked', properties: { questions: ['mcp_elicitation'] } }],
    ['no properties', { type: 'question.asked' }],
  ])('reads no elicitation from %s', (_name, payload) => {
    expect(mimoElicitation(payload)).toBeUndefined()
  })
})
