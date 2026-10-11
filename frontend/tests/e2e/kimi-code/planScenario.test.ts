import { describe, expect, it } from 'vitest'
import { KIMI_PLAN_FILE_CAPTURE, occurrences } from './planScenario'

describe('KIMI_PLAN_FILE_CAPTURE', () => {
  it('captures the plan file path and stops at its .md suffix', () => {
    const reminder = 'Plan file: /work/.kimi/plans/plan-7f3a.md</system-reminder>'
    expect(new RegExp(KIMI_PLAN_FILE_CAPTURE.planFile).exec(reminder)?.[1]).toBe('/work/.kimi/plans/plan-7f3a.md')
  })

  it('captures nothing for a reminder that states no plan file', () => {
    expect(new RegExp(KIMI_PLAN_FILE_CAPTURE.planFile).exec('Plan mode is active.</system-reminder>')).toBeNull()
  })
})

describe('occurrences', () => {
  it.each([
    ['revise', 'revis', 1],
    ['revise and revision', 'revis', 2],
    ['no match', 'revis', 0],
    ['', 'revis', 0],
    ['aaaa', 'aa', 2],
  ])('counts the separate occurrences in %j of %j', (text, needle, count) => {
    expect(occurrences(text, needle)).toBe(count)
  })

  it('refuses an empty needle, which every position would match', () => {
    expect(() => occurrences('text', '')).toThrow('occurrences needs a needle that is not empty')
  })
})
