import { describe, expect, it } from 'vitest'
import { KIMI_TOOL } from '~/generated/contracts/kimi-protocol'
import { kimiToolResult, kimiToolStart } from '~/test-support/kimiFixtures'
import { kimiPlanText } from './plan'

describe('kimiPlanText', () => {
  it('reads the plan an ExitPlanMode call proposes', () => {
    expect(kimiPlanText(kimiToolStart('c', KIMI_TOOL.ExitPlanMode, {}, { kind: 'plan_review', plan: '# Plan' }))).toBe('# Plan')
  })

  it('reads no plan elsewhere', () => {
    expect(kimiPlanText(kimiToolStart('c', KIMI_TOOL.ExitPlanMode, {}, { kind: 'plan_review', plan: '  ' }))).toBeNull()
    expect(kimiPlanText(kimiToolStart('c', KIMI_TOOL.ExitPlanMode, {}))).toBeNull()
    expect(kimiPlanText(kimiToolStart('c', KIMI_TOOL.Bash, {}, { kind: 'plan_review', plan: '# Plan' }))).toBeNull()
    expect(kimiPlanText(kimiToolResult('c', '# Plan'))).toBeNull()
  })
})
