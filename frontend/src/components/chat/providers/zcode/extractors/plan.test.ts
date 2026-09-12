import { describe, expect, it } from 'vitest'
import { zcodeControlPlanText } from './plan'

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
