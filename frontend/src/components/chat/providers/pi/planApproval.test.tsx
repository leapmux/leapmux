import { readFileSync } from 'node:fs'
import { describe, expect, it } from 'vitest'
import { isPiPlanApproval } from './planApproval'

const fixture = JSON.parse(readFileSync('../testdata/pi_plan_control_conformance.json', 'utf8')) as {
  cases: Array<{ name: string, payload: Record<string, unknown>, expected: boolean }>
}

describe('pi plan approval conformance', () => {
  it.each(fixture.cases)('$name', ({ payload, expected }) => {
    const original = JSON.stringify(payload)
    expect(isPiPlanApproval(payload)).toBe(expected)
    expect(JSON.stringify(payload)).toBe(original)
  })
})
