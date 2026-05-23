import type { Tab } from './tab.types'
import { describe, expect, it } from 'vitest'
import { TabType } from '~/generated/leapmux/v1/workspace_pb'
import { gitTabFieldsDiffer, isSameRepo, preserveNonEmptyGitFields, tabDisplayLabel, toGitTabFields } from './tab.helpers'

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

// `isSameRepo` is the single source of truth for matching a
// (workerId, repoToplevel) pair against a Tab-shaped value. It backs
// the AppShell branch-changed routing AND tabStore.stampBranchOnTabs;
// every behavior listed here represents a contract those callers rely on.
describe('isSameRepo', () => {
  it('matches when workerId and gitToplevel both equal', () => {
    expect(isSameRepo({ workerId: 'w1', gitToplevel: '/repo' }, 'w1', '/repo')).toBe(true)
  })

  it('rejects when workerId differs (cross-worker leakage guard)', () => {
    // A branch change on worker A must never trigger a stamp on a tab
    // hosted by worker B even if both happen to share a repo path.
    expect(isSameRepo({ workerId: 'wA', gitToplevel: '/repo' }, 'wB', '/repo')).toBe(false)
  })

  it('rejects when gitToplevel differs (cross-repo guard)', () => {
    expect(isSameRepo({ workerId: 'w1', gitToplevel: '/repo-a' }, 'w1', '/repo-b')).toBe(false)
  })

  it('treats undefined workerId as empty string', () => {
    // A freshly-created tab may not have a workerId yet. Without the
    // ?? '' normalization, `undefined === ''` would be false and an
    // "empty/empty" query would also fail — but neither case should
    // match anything meaningful.
    expect(isSameRepo({ gitToplevel: '/repo' }, '', '/repo')).toBe(true)
    expect(isSameRepo({ gitToplevel: '/repo' }, 'w1', '/repo')).toBe(false)
  })

  it('treats undefined gitToplevel as empty string', () => {
    // A tab outside any git repo has gitToplevel=undefined. It must
    // only match an explicit empty-string query, never an arbitrary
    // path.
    expect(isSameRepo({ workerId: 'w1' }, 'w1', '')).toBe(true)
    expect(isSameRepo({ workerId: 'w1' }, 'w1', '/repo')).toBe(false)
  })

  it('returns false for null / undefined input', () => {
    expect(isSameRepo(null, 'w1', '/repo')).toBe(false)
    expect(isSameRepo(undefined, 'w1', '/repo')).toBe(false)
  })

  it('returns false when only one side is unset (no accidental empty-empty matches)', () => {
    // Worth pinning: an unset tab paired with an unset query DOES match
    // by the helper's spec, but mismatched cases must not. The two-arg
    // identity rule is symmetric.
    expect(isSameRepo({ workerId: 'w1' }, '', '/repo')).toBe(false)
    expect(isSameRepo({ gitToplevel: '/repo' }, 'w1', '')).toBe(false)
  })

  it('does not perform substring matching on gitToplevel', () => {
    // Regression guard: `/repo` must not match `/repo-other` even
    // though one is a prefix of the other.
    expect(isSameRepo({ workerId: 'w1', gitToplevel: '/repo-other' }, 'w1', '/repo')).toBe(false)
    expect(isSameRepo({ workerId: 'w1', gitToplevel: '/repo' }, 'w1', '/repo-other')).toBe(false)
  })

  it('accepts a full Tab object (the common production call shape)', () => {
    const tab: Tab = {
      type: TabType.AGENT,
      id: 'a1',
      workerId: 'w1',
      gitToplevel: '/repo',
    }
    expect(isSameRepo(tab, 'w1', '/repo')).toBe(true)
  })
})

// `toGitTabFields` / `gitTabFieldsDiffer` carry the four-field git tuple
// (branch + originUrl + toplevel + isWorktree) onto every tab. The
// disposition was added when wires were already in place for the other
// three; pin its inclusion so a future "factor out branch+origin" refactor
// can't quietly drop it.
describe('toGitTabFields', () => {
  it('maps every input field, collapsing empty strings to undefined', () => {
    // Empty strings on the wire mean "not set"; the tab convention is
    // undefined so equality checks against tabs that never carried a
    // value don't churn on '' vs undefined.
    expect(toGitTabFields('', '', '', false)).toEqual({
      gitBranch: undefined,
      gitOriginUrl: undefined,
      gitToplevel: undefined,
      gitIsWorktree: undefined,
    })
  })

  it('carries every non-empty field through', () => {
    expect(toGitTabFields('main', 'https://example.com/r.git', '/repo', true)).toEqual({
      gitBranch: 'main',
      gitOriginUrl: 'https://example.com/r.git',
      gitToplevel: '/repo',
      gitIsWorktree: true,
    })
  })

  it('collapses isWorktree=false to undefined (proto default = "not a worktree")', () => {
    // A wire-default `false` is the most common case — keep it
    // undefined so the field doesn't surface as a meaningful "value
    // present" signal on tabs that haven't been resolved yet.
    expect(toGitTabFields('main', '', '/repo', false).gitIsWorktree).toBeUndefined()
  })
})

describe('gitTabFieldsDiffer', () => {
  const base = { gitBranch: 'main', gitOriginUrl: 'o', gitToplevel: '/r', gitIsWorktree: false }

  it('returns false for an identical tuple', () => {
    expect(gitTabFieldsDiffer(base, { ...base })).toBe(false)
  })

  it('detects a branch change', () => {
    expect(gitTabFieldsDiffer(base, { ...base, gitBranch: 'feature' })).toBe(true)
  })

  it('detects an originUrl change', () => {
    expect(gitTabFieldsDiffer(base, { ...base, gitOriginUrl: 'other' })).toBe(true)
  })

  it('detects a toplevel change', () => {
    expect(gitTabFieldsDiffer(base, { ...base, gitToplevel: '/other' })).toBe(true)
  })

  it('detects an isWorktree change (false → true)', () => {
    // Regression guard for the isWorktree plumbing: if a worker re-
    // probes and reports the path as a linked worktree where it
    // previously wasn't (or vice versa), the tab MUST update — the
    // sidebar's BranchGroup.isWorktree disposition is derived from
    // it and ChangeBranchDialog reads that to seed its path-info shape.
    expect(gitTabFieldsDiffer(base, { ...base, gitIsWorktree: true })).toBe(true)
  })

  it('treats undefined and false isWorktree as equal (no churn on proto-zero default)', () => {
    // Proto-zero `false` arrives from the wire as undefined in the tab
    // (toGitTabFields collapses), so the comparator must not flag a
    // false→undefined transition as a change — every refresh would
    // otherwise allocate a new tab object.
    expect(gitTabFieldsDiffer({ ...base, gitIsWorktree: undefined }, { ...base, gitIsWorktree: false })).toBe(false)
  })
})

describe('preserveNonEmptyGitFields', () => {
  it('carries gitIsWorktree forward when fresh has no toplevel', () => {
    // A transient probe failure (or a partial proto from a different
    // code path) leaves `fresh.gitToplevel` unset; the preserve helper
    // restores BOTH `gitToplevel` and `gitIsWorktree` from the prior
    // snapshot since they're co-derived.
    const out = preserveNonEmptyGitFields<{
      gitBranch?: string
      gitToplevel?: string
      gitIsWorktree?: boolean
    }>(
      { gitBranch: 'main' },
      { gitBranch: 'main', gitOriginUrl: 'o', gitToplevel: '/r', gitIsWorktree: true },
    )
    expect(out.gitToplevel).toBe('/r')
    expect(out.gitIsWorktree).toBe(true)
  })

  it('lets fresh override gitIsWorktree when fresh has a toplevel', () => {
    // The preserve helper must NOT mask a legitimate update from a fresh
    // probe — if the worker now reports a non-worktree where it used
    // to report a worktree, the new value wins.
    const out = preserveNonEmptyGitFields(
      { gitToplevel: '/r', gitIsWorktree: false },
      { gitToplevel: '/r', gitIsWorktree: true },
    )
    expect(out.gitIsWorktree).toBe(false)
  })
})
