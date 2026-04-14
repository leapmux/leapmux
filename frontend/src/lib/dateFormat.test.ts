import { describe, expect, it } from 'vitest'
import { formatLocalDateTime } from './dateFormat'

describe('formatLocalDateTime', () => {
  it('includes a timezone suffix', () => {
    const formatted = formatLocalDateTime(new Date('2026-04-14T10:20:30Z'))
    const parts = formatted.trim().split(/\s+/)
    expect(parts.length).toBeGreaterThanOrEqual(5)
    const last = parts[parts.length - 1]
    expect(last).not.toBe('AM')
    expect(last).not.toBe('PM')
  })
})
