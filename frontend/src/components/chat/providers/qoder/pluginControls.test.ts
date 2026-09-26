import { describe, expect, it } from 'vitest'
import { CONTROL_REJECTED_BY_USER_MESSAGE } from '~/utils/controlResponse'
import { qoderControls } from './pluginControls'

/** A stored can_use_tool request in the shape the worker publishes it. */
function approvalPayload(toolName: string, input: Record<string, unknown>): Record<string, unknown> {
  return { type: 'control_request', request_id: 'approval:ap1', request: { tool_name: toolName, tool_use_id: 'call-1', input } }
}

describe('qoderControls', () => {
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

  it('reads a shell command and an empty option list for the shared pair', () => {
    expect(qoderControls.extractControl?.({ payload: approvalPayload('Bash', { command: 'ls' }) }))
      .toStrictEqual({
        kind: 'permission',
        permission: { title: 'Bash', input: { command: 'ls' }, command: 'ls', options: [] },
      })
    expect(qoderControls.extractControl?.({ payload: approvalPayload('ExitPlanMode', {}) })?.kind).toBe('plan')
  })
})
