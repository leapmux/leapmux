import { describe, expect, it } from 'vitest'
import { deepseekHarnessExtractControl } from './extractControl'

describe('deepseekHarnessExtractControl', () => {
  it('uses only the native approval choices', () => {
    expect(deepseekHarnessExtractControl({ payload: { event: 'approval/request', request: { toolName: 'bash', callId: 'native-call', reason: 'Approve wider file access.' } } })).toMatchObject({ kind: 'permission', permission: { title: 'bash', reason: 'Approve wider file access.', options: [{ optionId: 'allow' }, { optionId: 'deny' }] } })
  })

  it('reads the native plan and its exact choices', () => {
    expect(deepseekHarnessExtractControl({ payload: { event: 'user-questions/request', request: { questions: [{ id: 'plan-review', question: 'Approve?', detail: '# Native plan', intent: { kind: 'plan-review', approve: 'Approve', callId: 'native-plan' }, options: [{ label: 'Approve' }, { label: 'Keep planning' }] }] } } })).toEqual({ kind: 'plan', text: '# Native plan', choices: [{ id: 'Approve', label: 'Approve', approves: true }, { id: 'Keep planning', label: 'Keep planning', approves: false }] })
  })

  it.each([{ payload: {} }, { payload: { event: 'approval/request', request: null } }, { payload: { event: 'user-questions/request', request: { questions: [null] } } }, { payload: { event: 'other/request', request: { toolName: 'bash' } } }])('refuses a control that it cannot read: $payload', ({ payload }) => {
    expect(deepseekHarnessExtractControl({ payload })).toBeNull()
  })
})
