import { describe, expect, it } from 'vitest'
import { KIMI_PLAN_FILE_CAPTURE, kimiPlanModeFromInstructions } from './planScenario'

describe('KIMI_PLAN_FILE_CAPTURE', () => {
  it('captures the plan file path and stops at its .md suffix', () => {
    const reminder = 'Plan file: /work/.kimi/plans/plan-7f3a.md</system-reminder>'
    expect(new RegExp(KIMI_PLAN_FILE_CAPTURE.planFile).exec(reminder)?.[1]).toBe('/work/.kimi/plans/plan-7f3a.md')
  })

  it('captures nothing for a reminder that states no plan file', () => {
    expect(new RegExp(KIMI_PLAN_FILE_CAPTURE.planFile).exec('Plan mode is active.</system-reminder>')).toBeNull()
  })
})

describe('kimiPlanModeFromInstructions', () => {
  it.each([
    ['', undefined],
    ['Plan mode is a read-only planning state.', undefined],
    ['Plan mode is active: the user turned it on.', true],
    ['Plan mode is no longer active. Restrictions no longer apply.', false],
    ['Plan mode is active: review first.\nPlan mode is no longer active.', false],
    ['Plan mode is no longer active.\nPlan mode is active: review again.', true],
  ])('reads the latest explicit disclosure in %j', (text, expected) => {
    expect(kimiPlanModeFromInstructions(text)).toBe(expected)
  })
})
