import { describe, expect, it } from 'vitest'
import { copilotReadResult } from './readResult'

describe('copilot native read details', () => {
  it('recovers a large file without exceeding the JavaScript argument limit', () => {
    const count = 1_000_000
    const detailedContent = `--- a/project/large.txt\n+++ b/project/large.txt\n@@ -1,${count} +1,${count} @@\n${' line\n'.repeat(count)}`
    const result = copilotReadResult({ content: 'Shortened', detailedContent }, { path: '/project/large.txt' })
    expect(result.lines).toHaveLength(count)
    expect(result.lines?.at(-1)).toEqual({ num: count, text: 'line' })
  })

  it('retains empty file content and an explicit starting line', () => {
    const result = copilotReadResult({ content: '' }, { path: '/project/empty.txt', view_range: [2, -1] })
    expect(result.lines).toEqual([])
    expect(result.fallbackContent).toBe('')
  })

  it.each([0, -1, Number.MAX_SAFE_INTEGER + 1])('rejects an invalid native line position of %s', (position) => {
    const detailedContent = `--- a/project/sample.txt\n+++ b/project/sample.txt\n@@ -${position},1 +${position},1 @@\n invalid\n`
    const result = copilotReadResult({ content: 'Retained', detailedContent }, { path: '/project/sample.txt' })
    expect(result.fallbackContent).toBe('Retained')
  })
})
