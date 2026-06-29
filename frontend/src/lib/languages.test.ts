import { describe, expect, it } from 'vitest'
import { LANGUAGES } from './languages'
import { resolveBundledLang } from './shikiLazyHighlighter'

describe('lANGUAGES', () => {
  it('lists Plain Text first, then all bundled grammars', () => {
    expect(LANGUAGES[0]).toEqual({ id: 'plaintext', label: 'Plain Text' })
    // ~235 bundled grammars + plaintext, far beyond the old 140-entry curated list.
    expect(LANGUAGES.length).toBeGreaterThan(200)
  })

  it('includes common languages, each with a non-empty label', () => {
    const ids = new Set(LANGUAGES.map(l => l.id))
    for (const id of ['typescript', 'python', 'ruby', 'go', 'rust', 'swift'])
      expect(ids.has(id), id).toBe(true)
    expect(LANGUAGES.every(l => l.label.length > 0)).toBe(true)
  })

  it('maps every non-plaintext id to a real Shiki bundled grammar', () => {
    for (const lang of LANGUAGES) {
      if (lang.id === 'plaintext')
        continue
      expect(resolveBundledLang(lang.id), lang.id).toBeDefined()
    }
  })

  it('is sorted by label after Plain Text', () => {
    const labels = LANGUAGES.slice(1).map(l => l.label)
    const sorted = [...labels].sort((a, b) => a.localeCompare(b))
    expect(labels).toEqual(sorted)
  })
})
