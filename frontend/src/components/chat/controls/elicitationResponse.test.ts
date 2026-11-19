import type { ControlPrompt } from '../model/controlPrompt'
import type { PersistedControlResponse } from '../persistedControlResponse'
import { describe, expect, it } from 'vitest'
import { elicitationResponseSummary } from './elicitationResponse'

const schema = {
  type: 'object',
  required: ['count', 'enabled', 'color'],
  properties: {
    count: { type: 'integer', title: 'Count' },
    enabled: { type: 'boolean', title: 'Enabled' },
    color: {
      type: 'string',
      title: 'Color',
      oneOf: [
        { const: 'b', title: 'Blue' },
        { const: 'r', title: 'Red' },
      ],
    },
  },
}

const FORM: ControlPrompt = {
  kind: 'elicitation',
  elicitation: { mode: 'form', message: 'Choose the settings.', schema },
}

/** A saved answer to `prompt`, with `response` as the runtime's answer. A null prompt records none. */
function saved(response: Record<string, unknown> | undefined, prompt: ControlPrompt | null = FORM): PersistedControlResponse {
  return {
    requestId: 'form',
    claimToken: 'claim-1',
    request: { method: 'elicitation/create' },
    response,
    ...(prompt
      ? { prompt }
      : {}),
  }
}

/** The answer in a JSON-RPC result, as the Agent Client Protocol and Codex take it. */
function jsonRpc(result: Record<string, unknown>): Record<string, unknown> {
  return { jsonrpc: '2.0', id: 3, result }
}

/** The answer in the neutral control response, as Claude Code takes it. */
function controlResponse(result: Record<string, unknown>): Record<string, unknown> {
  return {
    type: 'control_response',
    response: { subtype: 'success', request_id: 'form', response: result },
  }
}

describe('elicitationResponseSummary', () => {
  it.each([
    ['a JSON-RPC result', jsonRpc],
    ['the neutral control response', controlResponse],
  ])('reads an accept in %s with the field titles and the option labels', (_envelope, wrap) => {
    expect(elicitationResponseSummary(saved(wrap({
      action: 'accept',
      content: { count: 0, enabled: false, color: 'b' },
    })))).toEqual({
      kind: 'label',
      text: 'Approved\nCount: 0\nEnabled: No\nColor: Blue',
    })
  })

  it('reads an accept with no content as a bare approval', () => {
    expect(elicitationResponseSummary(saved(jsonRpc({ action: 'accept' })))).toEqual({ kind: 'label', text: 'Approved' })
  })

  it('reads a field that the form does not state by its key, and a structured value as JSON', () => {
    expect(elicitationResponseSummary(saved(jsonRpc({
      action: 'accept',
      content: {
        extra: { a: 1 },
        note: 'hi',
      },
    })))).toEqual({
      kind: 'label',
      text: 'Approved\nextra: {"a": 1}\nnote: hi',
    })
  })

  it('reads a decline and a cancel', () => {
    expect(elicitationResponseSummary(saved(jsonRpc({ action: 'decline' })))).toEqual({ kind: 'label', text: 'Rejected' })
    expect(elicitationResponseSummary(saved(controlResponse({ action: 'cancel' })))).toEqual({ kind: 'label', text: 'Cancelled' })
  })

  it('reads no summary from an answer of an unknown action, or from no answer', () => {
    expect(elicitationResponseSummary(saved(jsonRpc({ action: 'later' })))).toBeNull()
    expect(elicitationResponseSummary(saved(jsonRpc({})))).toBeNull()
    expect(elicitationResponseSummary(saved(undefined))).toBeNull()
  })

  it('leaves a row whose recorded prompt is no elicitation to the provider', () => {
    const accept = jsonRpc({
      action: 'accept',
      content: { count: 0 },
    })
    expect(elicitationResponseSummary(saved(accept, { kind: 'plan', text: 'the plan' }))).toBeNull()
    expect(elicitationResponseSummary(saved(accept, null))).toBeNull()
  })
})
