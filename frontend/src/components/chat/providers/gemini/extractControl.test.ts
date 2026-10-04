import type { ParsedMessageContent } from '~/lib/messageParser'
import { describe, expect, it } from 'vitest'
import { geminiExtractControl } from './extractControl'

const path = '/private/gemini/session/plans/plan.md'
const tool = { toolCallId: 'exit_plan_mode__native-call', title: `Requesting plan approval for: ${path}`, kind: 'other', status: 'pending', content: [] }
const payload = { jsonrpc: '2.0', id: 4, method: 'session/request_permission', params: { sessionId: 'native-root', toolCall: tool, options: [{ optionId: 'proceed_once', name: 'Allow', kind: 'allow_once' }, { optionId: 'cancel', name: 'Reject', kind: 'reject_once' }] } }

function source(overrides: Record<string, unknown> = {}, supplement: unknown = { sessionUpdate: 'tool_call', toolCallId: tool.toolCallId, geminiPlanPath: path, geminiPlanContent: '# Complete native plan\n\n1. Inspect the source.' }): ParsedMessageContent {
  const frame = { sessionUpdate: 'tool_call', ...tool, ...overrides }
  return { rawText: JSON.stringify(frame), topLevel: frame, parentObject: frame, wrapper: null, supplementalContent: supplement }
}

describe('geminiExtractControl', () => {
  it('reads the complete plan from the exact persisted native source', () => {
    expect(geminiExtractControl({ payload, source: source() })).toEqual({ kind: 'plan', text: '# Complete native plan\n\n1. Inspect the source.' })
  })

  it('rejects missing foreign and malformed source data', () => {
    expect(geminiExtractControl({ payload })).toEqual({ kind: 'plan' })
    expect(geminiExtractControl({ payload, source: source({ toolCallId: 'exit_plan_mode__foreign' }) })).toEqual({ kind: 'plan' })
    expect(geminiExtractControl({ payload, source: source({ title: 'Requesting plan approval for: /other/plan.md' }) })).toEqual({ kind: 'plan' })
    for (const supplement of [null, [], {}, { toolCallId: tool.toolCallId, geminiPlanPath: '/other/plan.md', geminiPlanContent: 'foreign' }, { toolCallId: 'foreign', geminiPlanPath: path, geminiPlanContent: 'foreign' }, { toolCallId: tool.toolCallId, geminiPlanPath: path, geminiPlanContent: 0 }])
      expect(geminiExtractControl({ payload, source: source({}, supplement) })).toEqual({ kind: 'plan' })
  })

  it('uses the shared permission reader for ordinary native tools and lookalike titles', () => {
    const ordinary = { ...payload, params: { ...payload.params, toolCall: { ...tool, toolCallId: 'run_shell_command__native-call', kind: 'execute', rawInput: { command: 'printf native' } } } }
    expect(geminiExtractControl({ payload: ordinary })).toMatchObject({ kind: 'permission', permission: { command: 'printf native' } })
    expect(geminiExtractControl({ payload: { ...payload, params: { ...payload.params, toolCall: { ...tool, title: 'exit_plan_mode' } } } })?.kind).toBe('permission')
    expect(geminiExtractControl({ payload: { method: 'unknown' } })).toBeNull()
  })
})
