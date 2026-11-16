import type { MockModelRequestRecord } from '../helpers/mockModelScript'
import { describe, expect, it } from 'vitest'
import { OPENCODE_PLAN_REMINDER, openCodePlanIsActive } from './settingsScenario'

function request(...messages: string[]): MockModelRequestRecord {
  return {
    protocol: 'openai-chat-completions',
    path: '/v1/chat/completions',
    body: {
      messages: messages.map(content => ({ role: 'user', content })),
    },
  }
}

describe('openCodePlanIsActive', () => {
  it('reads the native reminder before the latest user prompt', () => {
    expect(openCodePlanIsActive(request(OPENCODE_PLAN_REMINDER, 'Reply once.'))).toBe(true)
  })

  it('honors the latest reminder while keeping the previous conversation', () => {
    const leave = 'You are NO LONGER in Plan mode.'
    expect(openCodePlanIsActive(request(OPENCODE_PLAN_REMINDER, leave, 'Reply once.'))).toBe(false)
    expect(openCodePlanIsActive(request(OPENCODE_PLAN_REMINDER, leave, OPENCODE_PLAN_REMINDER, 'Reply once.'))).toBe(true)
    expect(openCodePlanIsActive(request('Reply once.'))).toBe(false)
  })
})
