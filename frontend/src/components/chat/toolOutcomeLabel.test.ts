import { describe, expect, it } from 'vitest'
import { toolOutcomeLabel } from './toolOutcomeLabel'

describe('toolOutcomeLabel', () => {
  it('gives one word for each outcome', () => {
    expect(toolOutcomeLabel('succeeded')).toBe('Success')
    expect(toolOutcomeLabel('failed')).toBe('Error')
    expect(toolOutcomeLabel('interrupted')).toBe('Interrupted')
  })

  it('puts a qualifier in parentheses', () => {
    expect(toolOutcomeLabel('failed', 'exit 5')).toBe('Error (exit 5)')
  })

  it('drops an empty or absent qualifier rather than showing empty parentheses', () => {
    expect(toolOutcomeLabel('failed', '')).toBe('Error')
    expect(toolOutcomeLabel('failed', '   ')).toBe('Error')
    expect(toolOutcomeLabel('failed', null)).toBe('Error')
    expect(toolOutcomeLabel('failed', undefined)).toBe('Error')
  })
})
