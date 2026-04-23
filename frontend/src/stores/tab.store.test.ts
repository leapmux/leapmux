import type { Tab } from './tab.store'
import { describe, expect, it } from 'vitest'
import { TabType } from '~/generated/leapmux/v1/workspace_pb'
import { preserveNonEmptyGitFields, resolveOptimisticGitInfo } from './tab.store'

function makeTab(overrides: Partial<Tab> & { id: string, type: TabType }): Tab {
  return {
    title: overrides.id,
    tileId: 'tile-1',
    position: '0',
    ...overrides,
  }
}

describe('resolveOptimisticGitInfo', () => {
  it('returns empty when there is no active tab', () => {
    const seed = resolveOptimisticGitInfo(null, { workingDir: '/repo' })
    expect(seed).toEqual({})
  })

  it('seeds git info from a terminal active tab with matching workingDir', () => {
    const activeTab = makeTab({
      id: 't1',
      type: TabType.TERMINAL,
      workingDir: '/repo',
      gitBranch: 'main',
      gitOriginUrl: 'https://github.com/org/repo.git',
    })
    const seed = resolveOptimisticGitInfo(activeTab, { workingDir: '/repo' })
    expect(seed).toEqual({
      gitBranch: 'main',
      gitOriginUrl: 'https://github.com/org/repo.git',
    })
  })

  it('seeds git info from an agent active tab with matching workingDir', () => {
    const activeTab = makeTab({
      id: 'a1',
      type: TabType.AGENT,
      workingDir: '/repo',
      gitBranch: 'feature',
      gitOriginUrl: 'https://github.com/org/repo.git',
    })
    const seed = resolveOptimisticGitInfo(activeTab, { workingDir: '/repo' })
    expect(seed).toEqual({
      gitBranch: 'feature',
      gitOriginUrl: 'https://github.com/org/repo.git',
    })
  })

  it('omits gitBranch when the active tab has an origin but no branch', () => {
    const activeTab = makeTab({
      id: 't1',
      type: TabType.TERMINAL,
      workingDir: '/repo',
      gitOriginUrl: 'https://github.com/org/repo.git',
    })
    const seed = resolveOptimisticGitInfo(activeTab, { workingDir: '/repo' })
    expect(seed).toEqual({
      gitOriginUrl: 'https://github.com/org/repo.git',
    })
    expect(seed.gitBranch).toBeUndefined()
  })

  it('refuses to seed when the active tab has no origin', () => {
    const activeTab = makeTab({
      id: 't1',
      type: TabType.TERMINAL,
      workingDir: '/repo',
      gitBranch: 'main',
    })
    const seed = resolveOptimisticGitInfo(activeTab, { workingDir: '/repo' })
    expect(seed).toEqual({})
  })

  it('refuses to seed when the active tab is a FILE tab', () => {
    const activeTab = makeTab({
      id: 'f1',
      type: TabType.FILE,
      workingDir: '/repo',
      gitBranch: 'main',
      gitOriginUrl: 'https://github.com/org/repo.git',
    })
    const seed = resolveOptimisticGitInfo(activeTab, { workingDir: '/repo' })
    expect(seed).toEqual({})
  })

  it('refuses to seed when workingDirs differ', () => {
    const activeTab = makeTab({
      id: 't1',
      type: TabType.TERMINAL,
      workingDir: '/repo-a',
      gitBranch: 'main',
      gitOriginUrl: 'https://github.com/org/repo-a.git',
    })
    const seed = resolveOptimisticGitInfo(activeTab, { workingDir: '/repo-b' })
    expect(seed).toEqual({})
  })

  it('refuses to seed when the new tab has no workingDir', () => {
    const activeTab = makeTab({
      id: 't1',
      type: TabType.TERMINAL,
      workingDir: '/repo',
      gitBranch: 'main',
      gitOriginUrl: 'https://github.com/org/repo.git',
    })
    const seed = resolveOptimisticGitInfo(activeTab, { workingDir: '' })
    expect(seed).toEqual({})
  })

  it('uses shellStartDir over workingDir when comparing', () => {
    // Active tab's git info was computed from shellStartDir (backend rule).
    // New tab with shellStartDir matching should seed; mismatched shellStartDir
    // should not, even if workingDirs happen to match.
    const activeTab = makeTab({
      id: 't1',
      type: TabType.TERMINAL,
      workingDir: '/home/me',
      shellStartDir: '/home/me/repo',
      gitBranch: 'main',
      gitOriginUrl: 'https://github.com/org/repo.git',
      gitToplevel: '/home/me/repo',
    })
    const matching = resolveOptimisticGitInfo(activeTab, {
      workingDir: '/home/me',
      shellStartDir: '/home/me/repo',
    })
    expect(matching).toEqual({
      gitBranch: 'main',
      gitOriginUrl: 'https://github.com/org/repo.git',
      gitToplevel: '/home/me/repo',
    })

    const mismatched = resolveOptimisticGitInfo(activeTab, {
      workingDir: '/home/me',
      shellStartDir: '/home/me/other',
    })
    expect(mismatched).toEqual({})
  })

  it('seeds toplevel for origin-less local repos', () => {
    // A fresh `git init` project: no origin, but we still have a toplevel.
    // Opening a new terminal from that tab should seed branch + toplevel
    // so the sidebar nests the new tab under the correct local repo group.
    const activeTab = makeTab({
      id: 't1',
      type: TabType.TERMINAL,
      workingDir: '/projects/fresh',
      gitBranch: 'main',
      gitToplevel: '/projects/fresh',
    })
    const seed = resolveOptimisticGitInfo(activeTab, { workingDir: '/projects/fresh' })
    expect(seed).toEqual({
      gitBranch: 'main',
      gitToplevel: '/projects/fresh',
    })
    expect(seed.gitOriginUrl).toBeUndefined()
  })

  it('refuses to seed when active tab has neither origin nor toplevel', () => {
    // branch-only activeTab is not enough signal; without a toplevel the
    // new tab would end up in the fallback `(local repo)` bucket with a
    // possibly-wrong branch association.
    const activeTab = makeTab({
      id: 't1',
      type: TabType.TERMINAL,
      workingDir: '/projects/fresh',
      gitBranch: 'main',
    })
    const seed = resolveOptimisticGitInfo(activeTab, { workingDir: '/projects/fresh' })
    expect(seed).toEqual({})
  })

  it('refuses to seed when the new tab opens in a subdir of the active repo', () => {
    // The active tab's origin was computed for /repo, but the new terminal
    // is starting in /repo/subdir. Even though both likely resolve to the
    // same repo, that is not guaranteed (submodules, nested repos), so the
    // helper is strict and leaves the seed to the authoritative phase-1
    // broadcast.
    const activeTab = makeTab({
      id: 't1',
      type: TabType.TERMINAL,
      workingDir: '/repo',
      gitBranch: 'main',
      gitOriginUrl: 'https://github.com/org/repo.git',
    })
    const seed = resolveOptimisticGitInfo(activeTab, { workingDir: '/repo/subdir' })
    expect(seed).toEqual({})
  })
})

describe('preserveNonEmptyGitFields', () => {
  it('returns fresh unchanged when there is no previous tab', () => {
    const fresh = { gitBranch: 'main', gitOriginUrl: 'https://github.com/org/repo.git' }
    expect(preserveNonEmptyGitFields(fresh, null)).toEqual(fresh)
    expect(preserveNonEmptyGitFields(fresh, undefined)).toEqual(fresh)
  })

  it('preserves both git fields when fresh has neither', () => {
    const fresh = { title: 'Terminal' }
    const previous = { gitBranch: 'main', gitOriginUrl: 'https://github.com/org/repo.git' }
    const result = preserveNonEmptyGitFields(fresh, previous)
    expect(result).toEqual({
      title: 'Terminal',
      gitBranch: 'main',
      gitOriginUrl: 'https://github.com/org/repo.git',
    })
  })

  it('preserves gitBranch when fresh clears it but leaves origin alone', () => {
    const fresh = { gitBranch: undefined, gitOriginUrl: 'https://github.com/org/repo.git' }
    const previous = { gitBranch: 'main', gitOriginUrl: 'https://github.com/org/repo.git' }
    const result = preserveNonEmptyGitFields(fresh, previous)
    expect(result.gitBranch).toBe('main')
    expect(result.gitOriginUrl).toBe('https://github.com/org/repo.git')
  })

  it('preserves gitOriginUrl when fresh clears it but leaves branch alone', () => {
    const fresh = { gitBranch: 'main', gitOriginUrl: undefined }
    const previous = { gitBranch: 'main', gitOriginUrl: 'https://github.com/org/repo.git' }
    const result = preserveNonEmptyGitFields(fresh, previous)
    expect(result.gitOriginUrl).toBe('https://github.com/org/repo.git')
    expect(result.gitBranch).toBe('main')
  })

  it('preserves gitToplevel when fresh clears it', () => {
    const fresh = { gitBranch: 'main', gitOriginUrl: undefined, gitToplevel: undefined }
    const previous = { gitBranch: 'main', gitOriginUrl: undefined, gitToplevel: '/projects/fresh' }
    const result = preserveNonEmptyGitFields(fresh, previous)
    expect(result.gitToplevel).toBe('/projects/fresh')
    expect(result.gitBranch).toBe('main')
  })

  it('does not overwrite non-empty fresh toplevel', () => {
    const fresh = { gitToplevel: '/projects/new' }
    const previous = { gitBranch: undefined, gitOriginUrl: undefined, gitToplevel: '/projects/old' }
    const result = preserveNonEmptyGitFields(fresh, previous)
    expect(result.gitToplevel).toBe('/projects/new')
  })

  it('does not overwrite non-empty fresh values', () => {
    const fresh = { gitBranch: 'feature', gitOriginUrl: 'https://github.com/org/repo-new.git' }
    const previous = { gitBranch: 'main', gitOriginUrl: 'https://github.com/org/repo-old.git' }
    const result = preserveNonEmptyGitFields(fresh, previous)
    expect(result).toEqual({
      gitBranch: 'feature',
      gitOriginUrl: 'https://github.com/org/repo-new.git',
    })
  })

  it('leaves empty when previous is also empty (non-git dir)', () => {
    const fresh = { gitBranch: undefined, gitOriginUrl: undefined, title: 'Terminal' }
    const previous = { gitBranch: undefined, gitOriginUrl: undefined }
    expect(preserveNonEmptyGitFields(fresh, previous)).toEqual({
      gitBranch: undefined,
      gitOriginUrl: undefined,
      title: 'Terminal',
    })
  })

  it('does not mutate the input objects', () => {
    const fresh = { gitBranch: undefined, gitOriginUrl: undefined }
    const previous = { gitBranch: 'main', gitOriginUrl: 'https://github.com/org/repo.git' }
    const result = preserveNonEmptyGitFields(fresh, previous)
    expect(fresh).toEqual({ gitBranch: undefined, gitOriginUrl: undefined })
    expect(previous).toEqual({ gitBranch: 'main', gitOriginUrl: 'https://github.com/org/repo.git' })
    expect(result).not.toBe(fresh)
  })
})
