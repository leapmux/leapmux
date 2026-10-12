import { describe, expect, it } from 'vitest'
import { CODEBUDDY_MODE } from '~/generated/contracts/codebuddy-protocol'
import { CONTROL_REJECTED_BY_USER_MESSAGE } from '~/utils/controlResponse'
import { createControlAnswerState } from '../../controls/types'
import { codebuddyControls } from './pluginControls'

/** A stored can_use_tool request in the shape the worker publishes it. */
function approvalPayload(toolName: string, input: Record<string, unknown>): Record<string, unknown> {
  return { type: 'control_request', request_id: 'approval:ap1', request: { tool_name: toolName, tool_use_id: 'call-1', input } }
}

describe('codebuddyControls', () => {
  it('allows a permission the composer sends with no reason', () => {
    expect(codebuddyControls.buildControlResponse?.(approvalPayload('Bash', { command: 'ls' }), '', 'approval:ap1'))
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
    expect(codebuddyControls.buildControlResponse?.(approvalPayload('Bash', { command: 'ls' }), 'Use a dry run.', 'approval:ap1'))
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
    expect(codebuddyControls.buildControlResponse?.(approvalPayload('ExitPlanMode', { plan: 'Do the thing.' }), 'Revise step 2.', 'approval:ap1'))
      .toMatchObject({ response: { response: { behavior: 'deny', message: 'Revise step 2.' } } })
    expect(codebuddyControls.buildControlResponse?.(approvalPayload('ExitPlanMode', { plan: 'Do the thing.' }), '', 'approval:ap1'))
      .toMatchObject({ response: { response: { behavior: 'deny', message: CONTROL_REJECTED_BY_USER_MESSAGE } } })
  })

  it('reads a shell command and an empty option list for the shared pair', () => {
    expect(codebuddyControls.extractControl?.({ payload: approvalPayload('Bash', { command: 'ls' }) }))
      .toStrictEqual({
        kind: 'permission',
        permission: { title: 'Bash', input: { command: 'ls' }, command: 'ls', options: [] },
      })
    expect(codebuddyControls.extractControl?.({ payload: approvalPayload('ExitPlanMode', {}) })?.kind).toBe('plan')
  })
})

describe('codebuddyControls permissionPresets', () => {
  // CodeBuddy's bypassPermissions is a real, advertised, live-switchable mode,
  // so the plus-menu and banner can switch the session onto it. There is no
  // smart mode to pair with it.
  it('offers a bypass shortcut and no smart one', () => {
    expect(codebuddyControls.permissionPresets?.bypass).toStrictEqual({
      sets: { permissionMode: CODEBUDDY_MODE.BypassPermissions },
    })
    expect(codebuddyControls.permissionPresets?.smart).toBeUndefined()
  })
})

describe('codebuddyControls askUserQuestion', () => {
  const questions = [{ question: 'Color?', header: 'Color', options: [{ label: 'Red', description: 'Warm' }, { label: 'Blue', description: 'Cool' }] }]

  it('recognizes a question by its tool name', () => {
    expect(codebuddyControls.askUserQuestion?.isRequest(approvalPayload('AskUserQuestion', { questions }))).toBe(true)
    expect(codebuddyControls.askUserQuestion?.isRequest(approvalPayload('Bash', {}))).toBe(false)
  })

  it('reads the questions the payload declares', () => {
    expect(codebuddyControls.askUserQuestion?.extractQuestions(approvalPayload('AskUserQuestion', { questions })))
      .toEqual(questions)
  })

  // The reply is the whole tool input with `answers` added: the worker folds
  // that object into CodeBuddy's `updatedInput`.
  it('answers with the neutral allow and the input that carries the answers', async () => {
    const request = { requestId: 'approval:ap1', agentId: 'agent-1', payload: approvalPayload('AskUserQuestion', { questions }) }
    const sent: string[] = []
    const sendControlResponse = async (content: Uint8Array) => {
      sent.push(new TextDecoder().decode(content))
      return undefined
    }
    const state = createControlAnswerState({ selections: { 0: ['Red'] } })

    await codebuddyControls.askUserQuestion?.sendAnswer(request, sendControlResponse, questions, state)

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

    await codebuddyControls.askUserQuestion?.sendReject(request, sendControlResponse, 'Ask again tomorrow.')

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
