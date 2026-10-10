import { describe, expect, it, vi } from 'vitest'
import { QODER_MODE } from '~/generated/contracts/qoder-protocol'
import { CONTROL_REJECTED_BY_USER_MESSAGE } from '~/utils/controlResponse'
import { createControlAnswerState } from '../../controls/types'
import { qoderControls } from './pluginControls'

/** A stored can_use_tool request in the shape the worker publishes it. */
function approvalPayload(toolName: string, input: Record<string, unknown>): Record<string, unknown> {
  return { type: 'control_request', request_id: 'approval:ap1', request: { tool_name: toolName, tool_use_id: 'call-1', input } }
}

describe('qoderControls', () => {
  it('draws a native MCP elicitation as a form and keeps typed answer values', () => {
    const schema = { type: 'object', properties: { count: { type: 'integer', title: 'Count' } } }
    const payload = {
      type: 'control_request',
      request_id: 'form-1',
      request: { subtype: 'elicitation', mcp_server_name: 'form_probe', mode: 'form', message: 'Choose the probe settings.', requested_schema: schema },
    }
    expect(qoderControls.elicitation?.(payload)).toEqual({
      mode: 'form',
      message: 'Choose the probe settings.',
      server: 'form_probe',
      schema,
      url: '',
      title: '',
      description: '',
    })
    expect(qoderControls.elicitation?.({ request: { subtype: 'can_use_tool' } })).toBeUndefined()
    expect(qoderControls.controlResponseDisplay?.({
      requestId: 'form-1',
      claimToken: '',
      request: payload,
      response: { type: 'control_response', response: { response: { action: 'accept', content: { count: 0 } } } },
    })).toEqual({ kind: 'label', text: 'Approved\nCount: 0' })
  })

  it('allows a permission the composer sends with no reason', () => {
    expect(qoderControls.buildControlResponse?.(approvalPayload('Bash', { command: 'ls' }), '', 'approval:ap1'))
      .toStrictEqual({
        type: 'control_response',
        response: {
          subtype: 'success',
          request_id: 'approval:ap1',
          response: { behavior: 'allow', updatedInput: { command: 'ls' } },
        },
      })
  })

  it('denies a permission the composer answers with a reason', () => {
    expect(qoderControls.buildControlResponse?.(approvalPayload('Bash', { command: 'ls' }), 'Use a dry run.', 'approval:ap1'))
      .toStrictEqual({
        type: 'control_response',
        response: {
          subtype: 'success',
          request_id: 'approval:ap1',
          response: { behavior: 'deny', message: 'Use a dry run.' },
        },
      })
  })

  // An editor reply to a plan always rejects it: the dedicated approval button
  // owns the allow path, so a typed message is feedback and never an approve.
  it('denies a plan the composer answers, even with no reason', () => {
    expect(qoderControls.buildControlResponse?.(approvalPayload('ExitPlanMode', { plan: 'Do the thing.' }), 'Revise step 2.', 'approval:ap1'))
      .toMatchObject({ response: { response: { behavior: 'deny', message: 'Revise step 2.' } } })
    expect(qoderControls.buildControlResponse?.(approvalPayload('ExitPlanMode', { plan: 'Do the thing.' }), '', 'approval:ap1'))
      .toMatchObject({ response: { response: { behavior: 'deny', message: CONTROL_REJECTED_BY_USER_MESSAGE } } })
  })

  it('reads a shell command beside the offered scope options', () => {
    expect(qoderControls.extractControl?.({ payload: approvalPayload('Bash', { command: 'ls' }) }))
      .toStrictEqual({
        kind: 'permission',
        permission: {
          title: 'Bash',
          input: { command: 'ls' },
          command: 'ls',
          options: [
            { optionId: 'once', kind: 'allow_once', name: 'Allow once' },
            { optionId: 'session', kind: 'allow_always', name: 'Allow for this session' },
            { optionId: 'deny', kind: 'reject_once', name: 'Deny' },
          ],
        },
      })
    expect(qoderControls.extractControl?.({ payload: approvalPayload('ExitPlanMode', {}) })?.kind).toBe('plan')
  })
})

// Auto approves safe calls and asks about the rest. Do not offer bypass:
// Don't Ask denies calls that Qoder does not approve in advance.
describe('qoderControls permissionPresets', () => {
  it('maps Smart to Auto without offering bypass', () => {
    expect(qoderControls.permissionPresets).toEqual({ smart: { sets: { permissionMode: QODER_MODE.Auto } } })
  })
})

describe('qoderControls askUserQuestion', () => {
  const questions = [{ question: 'Color?', header: 'Color', options: [{ label: 'Red', description: 'Warm' }, { label: 'Blue', description: 'Cool' }] }]

  it('recognizes a question by its tool name', () => {
    expect(qoderControls.askUserQuestion?.isRequest(approvalPayload('AskUserQuestion', { questions }))).toBe(true)
    expect(qoderControls.askUserQuestion?.isRequest(approvalPayload('WriteTodos', {}))).toBe(false)
  })

  it('reads the questions the payload declares', () => {
    expect(qoderControls.askUserQuestion?.extractQuestions(approvalPayload('AskUserQuestion', { questions })))
      .toEqual(questions)
  })

  // The reply is the whole tool input with `answers` added: the worker folds
  // that object into Qoder's `updatedInput`, and Qoder's own reader matches the
  // answers back to the questions by their text.
  it('answers with the neutral allow and the input that carries the answers', async () => {
    const request = { requestId: 'approval:ap1', agentId: 'agent-1', payload: approvalPayload('AskUserQuestion', { questions }) }
    const sent: string[] = []
    const sendControlResponse = async (content: Uint8Array) => {
      sent.push(new TextDecoder().decode(content))
      return undefined
    }
    const state = createControlAnswerState({ selections: { 0: ['Red'] } })

    await qoderControls.askUserQuestion?.sendAnswer(request, sendControlResponse, questions, state)

    expect(sent).toHaveLength(1)
    expect(JSON.parse(sent[0]!)).toStrictEqual({
      type: 'control_response',
      response: {
        subtype: 'success',
        request_id: 'approval:ap1',
        response: {
          behavior: 'allow',
          updatedInput: { questions, answers: { 'Color?': 'Red' } },
        },
      },
    })
  })

  it('rejects with the neutral deny and the reader\'s words', async () => {
    const request = { requestId: 'approval:ap1', agentId: 'agent-1', payload: approvalPayload('AskUserQuestion', { questions }) }
    const sent: string[] = []
    const sendControlResponse = async (content: Uint8Array) => {
      sent.push(new TextDecoder().decode(content))
      return undefined
    }

    await qoderControls.askUserQuestion?.sendReject(request, sendControlResponse, 'Ask again tomorrow.')

    expect(JSON.parse(sent[0]!)).toStrictEqual({
      type: 'control_response',
      response: {
        subtype: 'success',
        request_id: 'approval:ap1',
        response: { behavior: 'deny', message: 'Ask again tomorrow.' },
      },
    })
  })
})

describe('qoderControls permission options', () => {
  it('offers the once and session scopes the runtime accepts', () => {
    const extracted = qoderControls.extractControl?.({ payload: approvalPayload('Bash', { command: 'printf hi > out.txt' }) })
    expect(extracted && 'permission' in extracted ? extracted.permission.options : []).toEqual([
      { optionId: 'once', kind: 'allow_once', name: 'Allow once' },
      { optionId: 'session', kind: 'allow_always', name: 'Allow for this session' },
      { optionId: 'deny', kind: 'reject_once', name: 'Deny' },
    ])
  })

  it('sends the selected scope as the choice of an allow', async () => {
    const onRespond = vi.fn().mockResolvedValue(undefined)
    await qoderControls.sendPermissionOption?.(onRespond, 'approval:ap1', 'session')
    expect(onRespond).toHaveBeenCalledOnce()
    const sent = JSON.parse(new TextDecoder().decode(onRespond.mock.calls[0]?.[0] as Uint8Array))
    expect(sent).toMatchObject({ response: { response: { behavior: 'allow', choice: 'session' } } })

    await qoderControls.sendPermissionOption?.(onRespond, 'approval:ap1', 'once')
    sent.response.response.behavior = 'allow'
    expect(JSON.parse(new TextDecoder().decode(onRespond.mock.calls[1]?.[0] as Uint8Array)))
      .toMatchObject({ response: { response: { behavior: 'allow', choice: 'once' } } })

    // An id no pill offered answers deny, mirroring the runtime's fail-safe.
    await qoderControls.sendPermissionOption?.(onRespond, 'approval:ap1', 'persist')
    expect(JSON.parse(new TextDecoder().decode(onRespond.mock.calls[2]?.[0] as Uint8Array)))
      .toMatchObject({ response: { response: { behavior: 'deny', choice: 'persist' } } })
  })
})
