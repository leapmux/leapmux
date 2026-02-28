import { describe, expect, it } from 'vitest'
import { computeSeparator } from './editorRef.store'

describe('computeSeparator', () => {
  it('returns empty string when current is empty (block)', () => {
    expect(computeSeparator('', 'block')).toBe('')
  })

  it('returns empty string when current is empty (inline)', () => {
    expect(computeSeparator('', 'inline')).toBe('')
  })

  it('returns \\n\\n for block mode with existing content', () => {
    expect(computeSeparator('hello', 'block')).toBe('\n\n')
  })

  it('returns space for inline mode with existing content', () => {
    expect(computeSeparator('hello', 'inline')).toBe(' ')
  })

  it('returns empty string for inline mode when current ends with newline', () => {
    expect(computeSeparator('hello\n', 'inline')).toBe('')
  })

  it('returns \\n\\n for block mode even when current ends with newline', () => {
    expect(computeSeparator('hello\n', 'block')).toBe('\n\n')
  })
})
