import { isAbsolute, join } from 'node:path'
import { describe, expect, it } from 'vitest'
import { collectE2EFiles, e2eRoot } from '~/test-support/e2eFiles'

// Three e2e guards -- `noNetworkIdleWait`, `chatRowReads` and
// `visibleChatLocators` -- scan whatever this walk returns and then assert
// their offender list is empty. An empty walk is a silent pass for all three at
// once, so the non-emptiness is pinned HERE, one time, rather than in each of
// them.

describe('collectE2EFiles', () => {
  it('finds the files the e2e guards scan', () => {
    expect(collectE2EFiles().length, 'no e2e file found -- has tests/e2e/ moved?')
      .toBeGreaterThanOrEqual(50)
  })

  it('returns an absolute .ts path for every file', () => {
    const found = collectE2EFiles()

    expect(found.every(file => file.endsWith('.ts'))).toBe(true)
    expect(found.every(file => isAbsolute(file))).toBe(true)
  })

  it('includes a nested helper, not the specs alone', () => {
    // A helper runs inside the same page and breaks the same rules, which is
    // why the walk matches `.ts` rather than `.spec.ts`.
    const found = collectE2EFiles()

    expect(found).toContain(join(e2eRoot, 'helpers', 'ui.ts'))
    expect(found.some(file => file.endsWith('.spec.ts'))).toBe(true)
  })
})
