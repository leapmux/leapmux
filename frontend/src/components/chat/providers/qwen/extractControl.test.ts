import { describe, expect, it } from 'vitest'
import { isQwenPlanApproval, qwenExtractControl } from './extractControl'

/** Qwen's plan approval as the probe recorded it. */
const PLAN = {
  jsonrpc: '2.0',
  id: 5,
  method: 'session/request_permission',
  params: {
    sessionId: 's',
    options: [
      { optionId: 'restore_previous', name: 'Yes, restore previous mode (default)', kind: 'allow_once' },
      { optionId: 'proceed_always', name: 'Yes, and auto-accept edits', kind: 'allow_always' },
      { optionId: 'proceed_once', name: 'Yes, and manually approve edits', kind: 'allow_once' },
      { optionId: 'cancel', name: 'No, keep planning (esc)', kind: 'reject_once' },
    ],
    toolCall: { toolCallId: 'call_89a2ceb0ea', status: 'pending', title: 'Plan:', kind: 'switch_mode', rawInput: { plan: '1. Do X\n2. Do Y' }, _meta: { toolName: 'exit_plan_mode' } },
  },
}

describe('isQwenPlanApproval', () => {
  it('matches the permission request about exit_plan_mode', () => {
    expect(isQwenPlanApproval(PLAN)).toBe(true)
    expect(isQwenPlanApproval({ params: { toolCall: { _meta: { toolName: 'edit' } } } })).toBe(false)
    expect(isQwenPlanApproval({ params: {} })).toBe(false)
    expect(isQwenPlanApproval({})).toBe(false)
  })
})

describe('qwenExtractControl', () => {
  it('draws the plan approval as a plan with its text', () => {
    expect(qwenExtractControl({ payload: PLAN })).toEqual({ kind: 'plan', text: '1. Do X\n2. Do Y' })
  })

  it('draws a plan approval with no plan text', () => {
    expect(qwenExtractControl({ payload: { params: { toolCall: { _meta: { toolName: 'exit_plan_mode' } } } } })).toEqual({ kind: 'plan' })
  })

  // The plan is wire data. A plan that is empty or no string states no text, so the
  // plan surface draws no empty block.
  it('draws a plan approval whose plan is empty or no string with no text', () => {
    const planOf = (plan: unknown) => ({ params: { toolCall: { rawInput: { plan }, _meta: { toolName: 'exit_plan_mode' } } } })
    expect(qwenExtractControl({ payload: planOf('') })).toEqual({ kind: 'plan' })
    expect(qwenExtractControl({ payload: planOf(['1. X']) })).toEqual({ kind: 'plan' })
    expect(qwenExtractControl({ payload: planOf(5) })).toEqual({ kind: 'plan' })
  })

  // The mark is the tool name in `_meta`. A title or an ACP kind that reads like a
  // plan does not make a plan approval.
  it('reads only the tool name in _meta as the plan mark', () => {
    const lookalike = { params: { options: [{ optionId: 'proceed_once', kind: 'allow_once' }], toolCall: { toolCallId: 'p', title: 'exit_plan_mode', kind: 'switch_mode', rawInput: { plan: '1. X' } } } }
    expect(isQwenPlanApproval(lookalike)).toBe(false)
    expect(qwenExtractControl({ payload: lookalike })?.kind).toBe('permission')
  })

  it('reads every other request through the shared reader', () => {
    const shell = { method: 'session/request_permission', params: {
      options: [{ optionId: 'proceed_once', name: 'Allow', kind: 'allow_once' }, { optionId: 'cancel', name: 'Reject', kind: 'reject_once' }],
      toolCall: { toolCallId: 'c', kind: 'execute', rawInput: { command: 'touch x' }, _meta: { toolName: 'run_shell_command' } },
    } }
    const control = qwenExtractControl({ payload: shell })
    expect(control?.kind === 'permission' && control.permission.command).toBe('touch x')
    expect(qwenExtractControl({ payload: { method: 'x' } })).toBeNull()
  })
})
