import { describe, expect, it } from 'vitest'
import { isReasonixPlanApproval, reasonixExtractControl } from './extractControl'

const PLAN = {
  jsonrpc: '2.0',
  id: 3,
  method: 'session/request_permission',
  params: {
    sessionId: 'plan-session',
    toolCall: {
      toolCallId: 'gate-1',
      title: 'exit_plan_mode',
      kind: 'other',
      status: 'pending',
      _meta: { 'reasonix.io': { approvalId: '1', fresh: false, subject: '', tool: 'exit_plan_mode' } },
    },
    options: [
      { optionId: 'allow_once', name: 'Allow', kind: 'allow_once' },
      { optionId: 'reject_once', name: 'Reject', kind: 'reject_once' },
    ],
  },
}

describe('isReasonixPlanApproval', () => {
  it('matches only the exact native metadata and method', () => {
    expect(isReasonixPlanApproval(PLAN)).toBe(true)
    expect(isReasonixPlanApproval({ ...PLAN, method: '_x/other' })).toBe(false)
    expect(isReasonixPlanApproval({ params: { toolCall: { title: 'exit_plan_mode', _meta: { 'reasonix.io': { tool: 'bash' } } } } })).toBe(false)
    expect(isReasonixPlanApproval({ params: { toolCall: { title: 'exit_plan_mode' } } })).toBe(false)
    expect(isReasonixPlanApproval({})).toBe(false)
  })
})

describe('reasonixExtractControl', () => {
  it('draws the native gate as a plan without duplicating transcript text', () => {
    expect(reasonixExtractControl({ payload: PLAN })).toEqual({ kind: 'plan' })
  })

  it('reads a lookalike title as an ordinary permission', () => {
    const control = reasonixExtractControl({ payload: { ...PLAN, params: { ...PLAN.params, toolCall: { ...PLAN.params.toolCall, _meta: {} } } } })
    expect(control?.kind).toBe('permission')
  })

  it('reads every other request through the shared reader', () => {
    const shell = { method: 'session/request_permission', params: {
      options: [{ optionId: 'allow_once', name: 'Allow', kind: 'allow_once' }],
      toolCall: { toolCallId: 'c', kind: 'execute', rawInput: { command: 'touch x' } },
    } }
    const control = reasonixExtractControl({ payload: shell })
    expect(control?.kind === 'permission' && control.permission.command).toBe('touch x')
    expect(reasonixExtractControl({ payload: { method: 'x' } })).toBeNull()
  })
})
