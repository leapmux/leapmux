import { describe, expect, it } from 'vitest'
import { deepseekHarnessExtractControl } from './extractControl'

describe('deepseekHarnessExtractControl', () => {
  it('uses only the native approval choices', () => {
    expect(deepseekHarnessExtractControl({ payload: { event: 'approval/request', request: { toolName: 'bash', callId: 'native-call', reason: 'Approve wider file access.' } } })).toMatchObject({ kind: 'permission', permission: { title: 'bash', reason: 'Approve wider file access.', options: [{ optionId: 'allow' }, { optionId: 'deny' }] } })
  })

  it('prefers the display reason that the native client shows, as an Auto review denial states it', () => {
    const request = { toolName: 'bash', callId: 'native-call', reason: 'Auto review denied tool "bash": The removal is not authorized.', displayReason: { en: 'Auto review denied this call: The removal is not authorized.', zh: 'Auto review 拒绝了此调用：The removal is not authorized.' } }
    expect(deepseekHarnessExtractControl({ payload: { event: 'approval/request', request } })).toMatchObject({ kind: 'permission', permission: { title: 'bash', reason: 'Auto review denied this call: The removal is not authorized.' } })
  })

  it.each([
    ['no English display text', { zh: '允许本次操作' }],
    ['an empty English display text', { en: '' }],
    ['a display reason that is no object', 'Allow this operation.'],
  ])('falls back to the audit reason for %s', (_label, displayReason) => {
    const request = { toolName: 'bash', callId: 'native-call', reason: 'escalate sandbox to danger-full-access: wider access', displayReason }
    expect(deepseekHarnessExtractControl({ payload: { event: 'approval/request', request } })).toMatchObject({ kind: 'permission', permission: { reason: 'escalate sandbox to danger-full-access: wider access' } })
  })

  it('states no reason when the request holds neither text', () => {
    const control = deepseekHarnessExtractControl({ payload: { event: 'approval/request', request: { toolName: 'bash', callId: 'native-call', displayReason: { en: '' } } } })
    expect(control).toMatchObject({ kind: 'permission', permission: { title: 'bash' } })
    expect(control?.kind === 'permission' && 'reason' in control.permission).toBe(false)
  })

  it('reads the native plan and its exact choices', () => {
    expect(deepseekHarnessExtractControl({ payload: { event: 'user-questions/request', request: { questions: [{ id: 'plan-review', question: 'Approve?', detail: '# Native plan', intent: { kind: 'plan-review', approve: 'Approve', callId: 'native-plan' }, options: [{ label: 'Approve' }, { label: 'Keep planning' }] }] } } })).toEqual({ kind: 'plan', text: '# Native plan', choices: [{ id: 'Approve', label: 'Approve', approves: true }, { id: 'Keep planning', label: 'Keep planning', approves: false }] })
  })

  it.each([{ payload: {} }, { payload: { event: 'approval/request', request: null } }, { payload: { event: 'user-questions/request', request: { questions: [null] } } }, { payload: { event: 'other/request', request: { toolName: 'bash' } } }])('refuses a control that it cannot read: $payload', ({ payload }) => {
    expect(deepseekHarnessExtractControl({ payload })).toBeNull()
  })
})
