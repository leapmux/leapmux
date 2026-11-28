import type { PersistedControlResponse } from '../../persistedControlResponse'
import { create } from '@bufbuild/protobuf'
import { describe, expect, it } from 'vitest'
import { ZCODE_INTERACTION, ZCODE_METHOD } from '~/generated/contracts/zcode-protocol'
import { ControlDecisionSchema, ElicitationAction } from '~/generated/proto/leapmux/v1/agent_control_pb'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { extractChatRow, extractedRow } from '../../rowExtraction'
import { resolveMessageForRendering } from '../registry'
import { zcodeControls } from './pluginControls'
import './plugin'

describe('zcodeControls', () => {
  // The Worker reads each request, builds each answer, and records the decision that
  // words a saved answer, so the browser keeps only the presets.
  it('registers only the presets', () => {
    expect(Object.keys(zcodeControls)).toEqual(['permissionPresets'])
  })
})

/*
 * ZCode forwards an MCP server's form as a user-input request. The Worker records the
 * form as the prompt of the answer, so the row reads a refused or a dismissed form in
 * the words of the form, and not as the plan or the question that the same RPC
 * carries.
 */
describe('zcode saved form answer', () => {
  const schema = {
    type: 'object',
    properties: {
      count: { type: 'integer', title: 'Count' },
    },
  }

  /** The row of one saved form answer: ZCode's own reply, and the Worker's prompt and decision. */
  function savedForm(result: Record<string, unknown>, action: ElicitationAction, contentJson = ''): PersistedControlResponse {
    return {
      requestId: 'mcp-elicit-1',
      claimToken: 'claim-1',
      request: {
        id: 21,
        method: ZCODE_METHOD.RequestUserInput,
        request: { tool_name: 'McpElicitation', input: {} },
        params: {
          schema: { interaction: ZCODE_INTERACTION.McpElicitation, serverName: 'form_probe', requestedSchema: schema },
        },
      },
      response: { id: 21, result },
      prompt: {
        kind: 'elicitation',
        elicitation: {
          mode: 'form',
          message: 'Choose the probe settings.',
          server: 'form_probe',
          schema,
        },
      },
      decision: create(ControlDecisionSchema, {
        kind: {
          case: 'elicitation',
          value: { action, contentJson },
        },
      }),
    }
  }

  function displayOf(record: PersistedControlResponse) {
    const parsed = resolveMessageForRendering({
      rawText: '',
      topLevel: {},
      parentObject: {},
      wrapper: null,
    }, AgentProvider.ZCODE)
    return extractedRow(extractChatRow(AgentProvider.ZCODE, parsed, { kind: 'control_response', response: record }))
  }

  it.each([
    ['decline', ElicitationAction.DECLINE, 'Rejected'],
    ['cancel', ElicitationAction.CANCEL, 'Cancelled'],
  ])('reads a form answered with %s', (wire, action, text) => {
    expect(displayOf(savedForm({ action: wire }, action))).toEqual({
      kind: 'control-response',
      display: { kind: 'label', text },
    })
  })

  // ZCode's reply keys the form positionally (`answer_0`), and the decision keys it by
  // the fields of the form's schema, so the row reads the field's title.
  it('reads an accepted form under the titles of its fields', () => {
    expect(displayOf(savedForm({
      action: 'accept',
      content: { answer_0: '3' },
    }, ElicitationAction.ACCEPT, '{"count":3}'))).toEqual({
      kind: 'control-response',
      display: { kind: 'label', text: 'Approved\nCount: 3' },
    })
  })
})
