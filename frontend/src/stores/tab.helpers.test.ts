import type { Tab } from './tab.types'
import { describe, expect, it } from 'vitest'
import { TabType } from '~/generated/leapmux/v1/workspace_pb'
import { tabDisplayLabel } from './tab.helpers'

// `tabDisplayLabel` is the shared "what should we render in the tab strip
// AND in the workspace tree?" helper. Three call sites depend on its
// fallback order (title → FILE basename → type-default), so each branch
// gets its own test to guard against silent drift.
function file(overrides: Partial<Extract<Tab, { type: TabType.FILE }>> = {}): Tab {
  return { type: TabType.FILE, id: 'f1', ...overrides }
}

function agent(overrides: Partial<Extract<Tab, { type: TabType.AGENT }>> = {}): Tab {
  return { type: TabType.AGENT, id: 'a1', ...overrides }
}

function terminal(overrides: Partial<Extract<Tab, { type: TabType.TERMINAL }>> = {}): Tab {
  return { type: TabType.TERMINAL, id: 't1', ...overrides }
}

describe('tabDisplayLabel', () => {
  it('prefers an explicit title over every fallback', () => {
    expect(tabDisplayLabel(file({ title: 'Renamed', filePath: '/repo/notes.txt' }))).toBe('Renamed')
    expect(tabDisplayLabel(agent({ title: 'My Agent' }))).toBe('My Agent')
    expect(tabDisplayLabel(terminal({ title: 'zsh' }))).toBe('zsh')
  })

  it('treats an empty-string title as no title (falls through to fallbacks)', () => {
    // Solid stores can briefly hold the empty string as a transitional
    // value; the helper must NOT show a blank label.
    expect(tabDisplayLabel(file({ title: '', filePath: '/repo/notes.txt' }))).toBe('notes.txt')
    expect(tabDisplayLabel(agent({ title: '' }))).toBe('Agent')
    expect(tabDisplayLabel(terminal({ title: '' }))).toBe('Terminal')
  })

  describe('file fallback', () => {
    it('uses basename(filePath) when no title is set', () => {
      expect(tabDisplayLabel(file({ filePath: '/repo/src/foo.ts' }))).toBe('foo.ts')
    })

    it('handles Windows-style paths', () => {
      expect(tabDisplayLabel(file({ filePath: 'C:\\users\\alice\\report.md' }))).toBe('report.md')
    })

    it('returns "File" when filePath is missing entirely', () => {
      // Pre-hydration projection — tab arrives without filePath. The
      // workspace tree must show *something*, not blank.
      expect(tabDisplayLabel(file({ filePath: undefined }))).toBe('File')
    })

    it('returns "File" when filePath is an empty string', () => {
      expect(tabDisplayLabel(file({ filePath: '' }))).toBe('File')
    })

    it('returns "File" when filePath is just a root separator (empty basename)', () => {
      // `basename('/')` returns '' (no segments after the root). The
      // helper's || 'File' fallback must catch that so we don't render
      // a blank label.
      expect(tabDisplayLabel(file({ filePath: '/' }))).toBe('File')
    })

    it('handles a bare filename with no separators', () => {
      expect(tabDisplayLabel(file({ filePath: 'standalone.md' }))).toBe('standalone.md')
    })
  })

  describe('agent / terminal fallback', () => {
    it('returns "Agent" for an unnamed agent tab', () => {
      expect(tabDisplayLabel(agent())).toBe('Agent')
    })

    it('returns "Terminal" for an unnamed terminal tab', () => {
      expect(tabDisplayLabel(terminal())).toBe('Terminal')
    })
  })
})
