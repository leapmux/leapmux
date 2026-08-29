import { describe, expect, it } from 'vitest'
import { repoGitView, repoKey } from '~/stores/repoGit'
import { createRepoGitStore } from '~/stores/repoGit.store'
import {
  branchKey,
  branchNameSegment,
  collapseKeyForBranch,
  isLocalRepoKey,
  repoKeyForLocal,
  repoKeyTooltip,
  tabBranchKey,
  tabGitToplevelForKey,
} from './branchKeys'

describe('branchNameSegment', () => {
  it('returns a sentinel for null', () => {
    const seg = branchNameSegment(null)
    expect(seg).not.toBe('')
    expect(seg).not.toBe('(no branch)')
  })

  it('does not collide with a real branch literally named "(no branch)"', () => {
    expect(branchNameSegment(null)).not.toBe(branchNameSegment('(no branch)'))
  })

  it('passes a real branch name through unchanged', () => {
    expect(branchNameSegment('main')).toBe('main')
    expect(branchNameSegment('feature/x')).toBe('feature/x')
  })
})

describe('branchKey', () => {
  it('keys distinct (branch, worker, toplevel) triples to distinct strings', () => {
    const a = branchKey('feature', 'w1', '/p')
    const b = branchKey('feature', 'w2', '/p')
    const c = branchKey('feature', 'w1', '/q')
    const d = branchKey('other', 'w1', '/p')
    const keys = new Set([a, b, c, d])
    expect(keys.size).toBe(4)
  })

  it('separates the null-branch bucket from a real branch literally named "(no branch)"', () => {
    expect(branchKey(null, 'w1', '/p')).not.toBe(branchKey('(no branch)', 'w1', '/p'))
  })

  it('keeps a colliding worker:path/branch:worker pair distinct', () => {
    const a = branchKey('feature', 'a:b', '/p')
    const b = branchKey('feature:a', 'b', '/p')
    expect(a).not.toBe(b)
  })
})

describe('repoKeyForLocal / isLocalRepoKey / repoKeyTooltip', () => {
  it('returns a local key that is recognised as local', () => {
    const key = repoKeyForLocal('/home/me/projects/alpha')
    expect(isLocalRepoKey(key)).toBe(true)
  })

  it('returns false for raw origin URLs', () => {
    expect(isLocalRepoKey('https://github.com/o/r.git')).toBe(false)
    expect(isLocalRepoKey('git@github.com:o/r.git')).toBe(false)
  })

  it('round-trips the toplevel path through the tooltip', () => {
    const toplevel = '/home/me/projects/alpha'
    expect(repoKeyTooltip(repoKeyForLocal(toplevel))).toBe(toplevel)
  })

  it('returns the origin URL unchanged for non-local keys', () => {
    expect(repoKeyTooltip('https://github.com/o/r.git')).toBe('https://github.com/o/r.git')
  })

  it('cannot collide with any real origin URL (control byte prefix)', () => {
    const local = repoKeyForLocal('/x')
    expect(local.charCodeAt(0)).toBeLessThan(0x20)
  })
})

describe('collapseKeyForBranch', () => {
  it('composes repo + branch into a unique key', () => {
    const a = collapseKeyForBranch('repoA', branchKey('feature', 'w1', '/p'))
    const b = collapseKeyForBranch('repoB', branchKey('feature', 'w1', '/p'))
    expect(a).not.toBe(b)
  })

  it('cannot collide across (repo, branch) split (sentinel separator)', () => {
    const a = collapseKeyForBranch('foo', branchKey('bar', 'w', '/p'))
    const b = collapseKeyForBranch('foo:bar', branchKey('', 'w', '/p'))
    expect(a).not.toBe(b)
  })
})

describe('tabBranchKey', () => {
  const store = createRepoGitStore()
  const tab = (gitToplevel?: string, workerId?: string) => ({ gitToplevel, workerId })

  function seedBranch(workerId: string, gitToplevel: string, branch: string) {
    store.upsert(repoKey(workerId, gitToplevel), { workerId, toplevel: gitToplevel, branch })
  }

  it('agrees with branchKey for the same triple', () => {
    seedBranch('w1', '/repo', 'main')
    expect(tabBranchKey(tab('/repo', 'w1'), store)).toBe(branchKey('main', 'w1', '/repo'))
  })

  it('maps a tab with no branch to the no-branch bucket, not to an empty name', () => {
    seedBranch('w1', '/repo', '')
    expect(tabBranchKey(tab('/repo', 'w1'), store)).toBe(branchKey(null, 'w1', '/repo'))
  })

  it('keeps two clones of one repo on the same branch apart', () => {
    seedBranch('w1', '/a', 'main')
    seedBranch('w1', '/b', 'main')
    expect(tabBranchKey(tab('/a', 'w1'), store)).not.toBe(tabBranchKey(tab('/b', 'w1'), store))
    expect(tabBranchKey(tab('/a', 'w1'), store)).not.toBe(tabBranchKey(tab('/a', 'w2'), store))
  })

  it('treats absent workerId and toplevel as empty rather than throwing', () => {
    expect(tabBranchKey(tab('/repo', undefined), store)).toBe(branchKey(null, '', '/repo'))
    expect(tabBranchKey({}, store)).toBe(branchKey(null, '', ''))
  })

  it('uses store toplevel when tab gitToplevel is absent', () => {
    const local = createRepoGitStore()
    local.upsert(repoKey('w1', '/repo'), { workerId: 'w1', toplevel: '/repo', branch: 'feature' })
    expect(tabBranchKey({ workerId: 'w1', workingDir: '/repo/pkg' }, local))
      .toBe(branchKey('feature', 'w1', '/repo'))
  })

  /**
   * The worker answered "not a git repository" for this path, and the row's
   * `gitToplevel` is stale -- the repository was removed while the link was
   * down. Believing the row files the tab under a repository that is not there,
   * with no branch name, for the rest of the page.
   */
  it('believes an explicit non-repo answer over a stale row toplevel', () => {
    const local = createRepoGitStore()
    local.upsert(repoKey('w1', '/gone'), {
      workerId: 'w1',
      toplevel: '',
      errorHint: 'not a git repository',
      gitStatusSeen: true,
    })

    expect(tabGitToplevelForKey({ workerId: 'w1', gitToplevel: '/gone' }, local)).toBe('')
  })

  // No key resolves without a worker id, so there is no answer to believe and
  // the row is the only thing anyone knows.
  it('keeps the row toplevel when no store key resolves', () => {
    const local = createRepoGitStore()
    expect(tabGitToplevelForKey({ gitToplevel: '/repo' }, local)).toBe('/repo')
  })

  // An unprobed key is not an answer either.
  it('keeps the row toplevel when the store has no entry yet', () => {
    const local = createRepoGitStore()
    expect(tabGitToplevelForKey({ workerId: 'w1', gitToplevel: '/repo' }, local)).toBe('/repo')
  })
})

// Passing the already-resolved view must be equivalent to letting the helper
// resolve it: the tree hoists one resolution per tab and threads it through
// every key, and a divergence here would mis-key the sidebar's groups.
describe('branchKeys accept a precomputed view', () => {
  it('tabGitToplevelForKey returns the same answer with and without the view', () => {
    const store = createRepoGitStore()
    const tab = { workerId: 'w1', gitToplevel: '/repo', workingDir: '/repo' }
    const view = repoGitView(tab, store)
    expect(tabGitToplevelForKey(tab, store, view))
      .toBe(tabGitToplevelForKey(tab, store))
  })

  it('tabBranchKey returns the same key with and without the view', () => {
    const store = createRepoGitStore()
    const tab = { workerId: 'w1', gitToplevel: '/repo', workingDir: '/repo' }
    const view = repoGitView(tab, store)
    expect(tabBranchKey(tab, store, view))
      .toBe(tabBranchKey(tab, store))
  })
})
