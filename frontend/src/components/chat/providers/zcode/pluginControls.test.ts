import type { PersistedControlResponse } from '../../persistedControlResponse'
import { describe, expect, it } from 'vitest'
import { WORKSPACE_TRUST_CONTROL_TYPE, WORKSPACE_TRUST_DECISION } from '~/generated/contracts/workspace-trust'
import { ZCODE_INTERACTION, ZCODE_METHOD } from '~/generated/contracts/zcode-protocol'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { extractChatRow, extractedRow } from '../../rowExtraction'
import { resolveMessageForRendering } from '../registry'
import { zcodeControls } from './pluginControls'
import './plugin'

// The Worker asks ZCode's hook-trust question itself, in LeapMux's neutral shape.
describe('zcodeControls', () => {
  const question = {
    type: WORKSPACE_TRUST_CONTROL_TYPE.Question,
    path: '/work/project',
    configuration: ['hooks'],
  }

  it.each([
    [WORKSPACE_TRUST_DECISION.Trust, 'Trusted this folder'],
    [WORKSPACE_TRUST_DECISION.Reject, 'Did not trust this folder'],
  ])('reads the saved answer %s', (decision, text) => {
    expect(zcodeControls.controlResponseDisplay?.({
      requestId: 'workspace-trust:session-1',
      claimToken: '',
      request: question,
      response: {
        type: 'control_response',
        response: {
          request_id: 'workspace-trust:session-1',
          response: { decision },
        },
      },
    })).toEqual({ kind: 'label', text })
  })
})

/*
 * ZCode forwards an MCP server's form as a user-input request. The Worker records the
 * form as the prompt of the answer, so the row reads a refused or a dismissed form in
 * the words of the form's own buttons, and not as the plan or the question that the
 * same RPC carries.
 */
describe('zcode saved form answer', () => {
  const schema = {
    type: 'object',
    properties: {
      count: { type: 'integer', title: 'Count' },
    },
  }

  /** The row of one saved form answer, as the Worker stores ZCode's own reply. */
  function savedForm(result: Record<string, unknown>): PersistedControlResponse {
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
    ['decline', 'Rejected'],
    ['cancel', 'Cancelled'],
  ])('reads a form answered with %s as %s', (action, text) => {
    expect(displayOf(savedForm({ action }))).toEqual({
      kind: 'control-response',
      display: { kind: 'label', text },
    })
  })
})
