import { describe, expect, it } from 'bun:test'
import { needsNotice, NOTICE_INPUTS } from './notice-inputs.mjs'

describe('needsNotice', () => {
  it('selects every dependency source and shared notice input', () => {
    for (const path of NOTICE_INPUTS.filter(path => !path.includes('*')))
      expect(needsNotice([path])).toBe(true)
    for (const path of ['backend/go.mod', 'desktop/go/go.sum', 'scripts/license-overrides/js/vinxi/LICENSE'])
      expect(needsNotice([path])).toBe(true)
  })
  it('skips empty and unrelated changes', () => {
    expect(needsNotice([])).toBe(false)
    expect(needsNotice(['backend/main.go', 'README.md'])).toBe(false)
  })
})
