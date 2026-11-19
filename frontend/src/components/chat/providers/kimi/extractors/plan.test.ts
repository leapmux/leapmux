import { describe, expect, it } from 'vitest'
import { KIMI_TOOL } from '~/generated/contracts/kimi-protocol'
import { kimiConversationToolRecord } from '~/test-support/kimiFixtures'
import { kimiPlanText } from './plan'

describe('kimiPlanText', () => {
  it('reads the plan an ExitPlanMode call proposes', () => {
    expect(kimiPlanText(kimiConversationToolRecord('request', {
      name: KIMI_TOOL.ExitPlanMode,
      input: {},
      display: { kind: 'plan_review', plan: '# Plan' },
    }))).toBe('# Plan')
  })

  it('reads no plan elsewhere', () => {
    expect(kimiPlanText(kimiConversationToolRecord('request', {
      name: KIMI_TOOL.ExitPlanMode,
      input: {},
      display: { kind: 'plan_review', plan: '  ' },
    }))).toBeNull()
    expect(kimiPlanText(kimiConversationToolRecord('request', { name: KIMI_TOOL.ExitPlanMode, input: {} }))).toBeNull()
    expect(kimiPlanText(kimiConversationToolRecord('request', {
      name: KIMI_TOOL.Bash,
      input: {},
      display: { kind: 'plan_review', plan: '# Plan' },
    }))).toBeNull()
    expect(kimiPlanText(kimiConversationToolRecord('result', {
      name: KIMI_TOOL.ExitPlanMode,
      input: {},
      status: 'ended',
      outcome: 'completed',
      output: '# Plan',
    }))).toBeNull()
  })
})
