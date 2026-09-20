import { describe, expect, it } from 'vitest'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { providerRow } from '~/test-support/toolCallFixture'
import { zcodeControlPlanText, zcodePlanText } from './plan'
import '../plugin'
import '../../testMocks'

const request = (params: Record<string, unknown>) => ({ method: 'interaction/requestUserInput', params: { schema: { interaction: 'plan_approval' }, ...params } })

describe('zcode control plan source', () => {
  it('prefers the native tool input over context and prompt', () => {
    expect(zcodeControlPlanText(request({ input: { plan: 'Input plan' }, context: { plan: 'Context plan' }, prompt: 'Prompt' }))).toBe('Input plan')
  })

  it('uses context when the input plan is blank', () => {
    expect(zcodeControlPlanText(request({ input: { plan: ' ' }, context: { plan: 'Context plan' } }))).toBe('Context plan')
  })

  it('keeps the prompt fallback and handles absent fields', () => {
    expect(zcodeControlPlanText(request({ prompt: 'Plan from prompt' }))).toBe('Plan from prompt')
    expect(zcodeControlPlanText(request({}))).toBe('')
  })

  it.each([null, {}, { method: 'interaction/requestPermission' }, { method: 'interaction/requestUserInput', params: { schema: { interaction: 'question' } } }])('rejects other message shapes', (value) => {
    expect(zcodeControlPlanText(value)).toBeNull()
  })
})

describe('zcodePlanText', () => {
  const scheduled = (inputValue: Record<string, unknown>) => ({ type: 'tool.updated', payload: { kind: 'scheduled', toolCallId: 'plan', toolName: 'ExitPlanMode', ...inputValue } })

  it('reads the plan the frame itself carries', () => {
    expect(zcodePlanText(scheduled({ input: { plan: '# In the frame' } }), 'ExitPlanMode')).toBe('# In the frame')
  })

  // The daemon persists streamed arguments in the supplement, outside the frame:
  // the frame states the tool and carries only the prompts, while the plan -- the
  // one field a plan row exists to state -- arrives beside it. Both pipeline
  // layers classify through this reader, so without reading it here the row fell
  // to the switch-mode tool call and the transcript never stated the plan.
  it('reads the plan from the supplemental stream input when the frame carries none', () => {
    expect(zcodePlanText(scheduled({ input: { allowedPrompts: [] } }), 'ExitPlanMode', { type: 'tool.updated', payload: { kind: 'scheduled', toolCallId: 'plan', input: { plan: '# Streamed plan' } } })).toBe('# Streamed plan')
  })

  it('classifies the streamed-plan row as the plan the transcript draws', () => {
    const row = providerRow(AgentProvider.ZCODE, scheduled({ input: { allowedPrompts: [] } }), {
      spanType: 'ExitPlanMode',
      supplementalContent: { type: 'tool.updated', payload: { kind: 'scheduled', toolCallId: 'plan', input: { plan: '# Streamed plan' } } },
    })
    expect(row).toEqual({ kind: 'assistant-plan', text: '# Streamed plan' })
  })

  it('answers null for a call that is not the plan tool', () => {
    expect(zcodePlanText({ type: 'tool.updated', payload: { kind: 'scheduled', toolCallId: 'read', toolName: 'Read', input: { file_path: '/a.ts' } } }, 'Read')).toBeNull()
  })
})
