import { describe, expect, it } from 'vitest'
import { ZCODE_INTERACTION, ZCODE_METHOD, ZCODE_TOOL } from '~/generated/contracts/zcode-protocol'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { controlSurface } from '../../controls/controlSurface'
import { zcodeIsAskUserQuestion } from './askUserQuestion'
import { zcodeElicitation } from './elicitation'
import { zcodeControls } from './pluginControls'
import './plugin'

const schema = {
  type: 'object',
  properties: {
    count: {
      type: 'integer',
      title: 'Count',
      minimum: 0,
      maximum: 3,
    },
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
  required: ['count', 'enabled', 'color'],
}

/** A stored MCP form in the shape the worker publishes it: its own tool name, and ZCode's native params. */
function formPayload(params: Record<string, unknown>): Record<string, unknown> {
  return {
    type: 'control_request',
    request_id: 'mcp-elicit-1',
    id: 21,
    method: ZCODE_METHOD.RequestUserInput,
    request: {
      tool_name: 'McpElicitation',
      tool_use_id: 'mcp-elicit-1',
      input: {
        questions: [
          { question: 'Choose the probe settings.\n\nCount' },
        ],
      },
    },
    params,
  }
}

const nativeForm = {
  requestId: 'mcp-elicit-1',
  toolName: 'McpElicitation',
  prompt: 'The prompt of the request.',
  input: { serverName: 'input_server', message: 'Choose the probe settings.', requestedSchema: schema },
  schema: { interaction: ZCODE_INTERACTION.McpElicitation, serverName: 'form_probe', requestedSchema: schema },
}

describe('zcodeElicitation', () => {
  it('reads the server\'s own form, not the questions that flatten it', () => {
    expect(zcodeElicitation(formPayload(nativeForm))).toEqual({
      mode: 'form',
      message: 'Choose the probe settings.',
      server: 'form_probe',
      schema,
    })
  })

  it('reads the form of the input when the schema states no more than the interaction', () => {
    expect(zcodeElicitation(formPayload({
      prompt: 'The prompt of the request.',
      input: { serverName: 'input_server', requestedSchema: schema },
      schema: { interaction: ZCODE_INTERACTION.McpElicitation },
    }))).toEqual({
      mode: 'form',
      message: 'The prompt of the request.',
      server: 'input_server',
      schema,
    })
  })

  it.each([
    [
      'a question',
      {
        ...nativeForm,
        schema: { interaction: 'ask_user_question' },
      },
    ],
    [
      'a plan approval',
      {
        ...nativeForm,
        schema: { interaction: ZCODE_INTERACTION.PlanApproval },
      },
    ],
    [
      'a request without a schema',
      { requestId: 'mcp-elicit-1' },
    ],
  ])('reads no form from %s', (_name, params) => {
    expect(zcodeElicitation(formPayload(params))).toBeUndefined()
  })

  it('reads no form from another method or a payload without params', () => {
    expect(zcodeElicitation({
      ...formPayload(nativeForm),
      method: ZCODE_METHOD.RequestPermission,
    })).toBeUndefined()
    expect(zcodeElicitation({ method: ZCODE_METHOD.RequestUserInput })).toBeUndefined()
    expect(zcodeElicitation({})).toBeUndefined()
  })
})

describe('zcodeControls', () => {
  it('draws the stored MCP form as a form, never as questions', () => {
    const payload = formPayload(nativeForm)
    expect(zcodeIsAskUserQuestion(payload)).toBe(false)
    expect(controlSurface({ requestId: 'mcp-elicit-1', agentId: 'agent-1', payload }, AgentProvider.ZCODE, undefined)).toEqual({
      kind: 'elicitation',
      elicitation: {
        mode: 'form',
        message: 'Choose the probe settings.',
        server: 'form_probe',
        schema,
      },
    })
  })

  it('keeps a question on the question surface', () => {
    const payload = {
      ...formPayload({
        requestId: 'q-1',
        schema: {
          interaction: 'ask_user_question',
          questions: [
            {
              question: 'Which database?',
              options: [
                { label: 'Postgres' },
              ],
            },
          ],
        },
      }),
      request: {
        tool_name: ZCODE_TOOL.AskUserQuestion,
        input: {
          questions: [
            { question: 'Which database?' },
          ],
        },
      },
    }
    expect(zcodeIsAskUserQuestion(payload)).toBe(true)
    expect(controlSurface({ requestId: 'q-1', agentId: 'agent-1', payload }, AgentProvider.ZCODE, undefined)?.kind).toBe('question')
  })

  it('shows the saved answer of a form by its field titles and option labels', () => {
    const request = formPayload(nativeForm)
    const display = (response: Record<string, unknown>) => zcodeControls.controlResponseDisplay?.({
      requestId: 'mcp-elicit-1',
      claimToken: '',
      request,
      response,
    })
    expect(display({
      type: 'control_response',
      response: {
        response: {
          action: 'accept',
          content: { count: 0, enabled: false, color: 'b' },
        },
      },
    }))
      .toEqual({ kind: 'label', text: 'Approved\nCount: 0\nEnabled: No\nColor: Blue' })
    expect(display({
      type: 'control_response',
      response: {
        response: { action: 'decline' },
      },
    })).toEqual({ kind: 'label', text: 'Rejected' })
    expect(display({
      type: 'control_response',
      response: {
        response: { action: 'cancel' },
      },
    })).toEqual({ kind: 'label', text: 'Cancelled' })
  })
})
