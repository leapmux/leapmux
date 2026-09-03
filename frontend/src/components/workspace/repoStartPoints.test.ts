import type { WorkerInfo } from '~/lib/workerInfoCache'
import type { Tab } from '~/stores/tab.types'
import { describe, expect, it } from 'vitest'
import { TabType } from '~/generated/proto/leapmux/v1/workspace_pb'
import { repoKey } from '~/stores/repoGit'
import { createRepoGitStore } from '~/stores/repoGit.store'
import { listRepoStartPoints } from './repoStartPoints'

interface RepoSeed {
  workerId?: string
  gitToplevel: string
  branch?: string
  originUrl?: string
  isWorktree?: boolean
}

function storeWith(seeds: readonly RepoSeed[]) {
  const store = createRepoGitStore()
  for (const seed of seeds) {
    const workerId = seed.workerId ?? 'w1'
    store.upsert(repoKey(workerId, seed.gitToplevel), {
      workerId,
      toplevel: seed.gitToplevel,
      branch: seed.branch ?? '',
      originUrl: seed.originUrl ?? '',
      isWorktree: seed.isWorktree ?? false,
    })
  }
  return store
}

let nextTabId = 0
function tab(fields: Partial<Tab> = {}): Tab {
  nextTabId += 1
  return {
    id: `t${nextTabId}`,
    workspaceId: 'ws-1',
    type: TabType.AGENT,
    workerId: 'w1',
    ...fields,
  } as Tab
}

const WORKERS: Record<string, WorkerInfo> = {
  w1: { name: 'mini', os: 'darwin', arch: 'arm64', homeDir: '/Users/me', version: '', commitHash: '', buildTime: '', updatedAt: 0 },
  w2: { name: 'devbox', os: 'linux', arch: 'amd64', homeDir: '/home/me', version: '', commitHash: '', buildTime: '', updatedAt: 0 },
}
const workerInfoFn = (id: string): WorkerInfo | null => WORKERS[id] ?? null

describe('listRepoStartPoints', () => {
  it('returns nothing for no tabs', () => {
    expect(listRepoStartPoints([], createRepoGitStore())).toEqual([])
  })

  it('collapses every tab of one checkout into a single entry', () => {
    const store = storeWith([{ gitToplevel: '/repo', branch: 'main' }])
    const tabs = [
      tab({ gitToplevel: '/repo' }),
      tab({ gitToplevel: '/repo' }),
      tab({ gitToplevel: '/repo' }),
    ]
    const out = listRepoStartPoints(tabs, store)
    expect(out).toHaveLength(1)
    expect(out[0].startPoint).toEqual({
      kind: 'repo',
      workerId: 'w1',
      gitToplevel: '/repo',
      isWorktree: false,
      currentBranch: 'main',
    })
  })

  it('drops a tab with no repository at all', () => {
    const store = createRepoGitStore()
    expect(listRepoStartPoints([tab({ gitToplevel: undefined })], store)).toEqual([])
  })

  it('drops a tab with no worker', () => {
    const store = storeWith([{ gitToplevel: '/repo' }])
    expect(listRepoStartPoints([tab({ workerId: undefined, gitToplevel: '/repo' })], store)).toEqual([])
  })

  describe('worktrees', () => {
    it('drops a worktree whose repository also has a normal checkout here', () => {
      // The dialog can create a worktree from the main checkout, so listing
      // both is one place to start too many.
      const store = storeWith([
        { gitToplevel: '/repo', originUrl: 'https://x/o/r.git' },
        { gitToplevel: '/wt/feature', originUrl: 'https://x/o/r.git', isWorktree: true },
      ])
      const out = listRepoStartPoints([
        tab({ gitToplevel: '/repo' }),
        tab({ gitToplevel: '/wt/feature' }),
      ], store)
      expect(out.map(e => e.startPoint.gitToplevel)).toEqual(['/repo'])
    })

    it('keeps a worktree when its repository has no normal checkout here', () => {
      // The only place anyone knows.
      const store = storeWith([
        { gitToplevel: '/wt/feature', originUrl: 'https://x/o/r.git', isWorktree: true },
      ])
      const out = listRepoStartPoints([tab({ gitToplevel: '/wt/feature' })], store)
      expect(out).toHaveLength(1)
      expect(out[0].startPoint.isWorktree).toBe(true)
    })

    it('keeps a worktree whose main checkout is on a DIFFERENT worker', () => {
      // One machine's main checkout says nothing about another machine's
      // worktrees, and neither can substitute for the other.
      const store = storeWith([
        { workerId: 'w1', gitToplevel: '/repo', originUrl: 'https://x/o/r.git' },
        { workerId: 'w2', gitToplevel: '/wt/feature', originUrl: 'https://x/o/r.git', isWorktree: true },
      ])
      const out = listRepoStartPoints([
        tab({ workerId: 'w1', gitToplevel: '/repo' }),
        tab({ workerId: 'w2', gitToplevel: '/wt/feature' }),
      ], store)
      expect(out).toHaveLength(2)
    })
  })

  it('keeps two clones of one origin as two entries', () => {
    // Same repository identity, two working copies. Both are real places to
    // start, and the dialog cannot substitute one for the other.
    const store = storeWith([
      { gitToplevel: '/a/leapmux', originUrl: 'https://x/o/r.git' },
      { gitToplevel: '/b/leapmux', originUrl: 'https://x/o/r.git' },
    ])
    const out = listRepoStartPoints([
      tab({ gitToplevel: '/a/leapmux' }),
      tab({ gitToplevel: '/b/leapmux' }),
    ], store)
    expect(out.map(e => e.startPoint.gitToplevel).toSorted()).toEqual(['/a/leapmux', '/b/leapmux'])
  })

  describe('order', () => {
    it('puts the most recently activated checkout first', () => {
      const store = storeWith([{ gitToplevel: '/a' }, { gitToplevel: '/b' }, { gitToplevel: '/c' }])
      const out = listRepoStartPoints([
        tab({ gitToplevel: '/a', mru: 3 }),
        tab({ gitToplevel: '/b', mru: 9 }),
        tab({ gitToplevel: '/c', mru: 5 }),
      ], store)
      expect(out.map(e => e.startPoint.gitToplevel)).toEqual(['/b', '/c', '/a'])
    })

    it('takes a checkout recency from its most recent tab', () => {
      const store = storeWith([{ gitToplevel: '/a' }, { gitToplevel: '/b' }])
      const out = listRepoStartPoints([
        tab({ gitToplevel: '/a', mru: 1 }),
        tab({ gitToplevel: '/a', mru: 20 }),
        tab({ gitToplevel: '/b', mru: 10 }),
      ], store)
      expect(out.map(e => e.startPoint.gitToplevel)).toEqual(['/a', '/b'])
    })

    it('breaks a no-activation tie by creation, newest first', () => {
      // `mru` is a session counter, so a workspace nobody touched this session
      // carries none at all.
      const store = storeWith([{ gitToplevel: '/a' }, { gitToplevel: '/b' }])
      const out = listRepoStartPoints([
        tab({ gitToplevel: '/a', createdAt: '2026-01-01T00:00:00Z' }),
        tab({ gitToplevel: '/b', createdAt: '2026-02-01T00:00:00Z' }),
      ], store)
      expect(out.map(e => e.startPoint.gitToplevel)).toEqual(['/b', '/a'])
    })

    it('puts an activated checkout ahead of a never-activated one', () => {
      const store = storeWith([{ gitToplevel: '/a' }, { gitToplevel: '/b' }])
      const out = listRepoStartPoints([
        tab({ gitToplevel: '/a', createdAt: '2099-01-01T00:00:00Z' }),
        tab({ gitToplevel: '/b', mru: 1 }),
      ], store)
      expect(out.map(e => e.startPoint.gitToplevel)).toEqual(['/b', '/a'])
    })

    it('falls back to the label, so the order is total and stable', () => {
      const store = storeWith([{ gitToplevel: '/zeta' }, { gitToplevel: '/alpha' }])
      const out = listRepoStartPoints([
        tab({ gitToplevel: '/zeta' }),
        tab({ gitToplevel: '/alpha' }),
      ], store)
      expect(out.map(e => e.label)).toEqual(['alpha', 'zeta'])
    })
  })

  it('keeps at most `limit` entries, most recent first', () => {
    const store = storeWith([{ gitToplevel: '/a' }, { gitToplevel: '/b' }, { gitToplevel: '/c' }])
    const out = listRepoStartPoints([
      tab({ gitToplevel: '/a', mru: 1 }),
      tab({ gitToplevel: '/b', mru: 2 }),
      tab({ gitToplevel: '/c', mru: 3 }),
    ], store, { limit: 2 })
    expect(out.map(e => e.startPoint.gitToplevel)).toEqual(['/c', '/b'])
  })

  describe('offline workers', () => {
    it('omits a repository whose worker is offline', () => {
      // The row opens a dialog that cannot probe the path, list its branches or
      // start an agent -- a trap rather than a shortcut.
      const store = storeWith([
        { workerId: 'w1', gitToplevel: '/on' },
        { workerId: 'w2', gitToplevel: '/off' },
      ])
      const out = listRepoStartPoints([
        tab({ workerId: 'w1', gitToplevel: '/on' }),
        tab({ workerId: 'w2', gitToplevel: '/off' }),
      ], store, { isWorkerOnline: id => id === 'w1' })
      expect(out.map(e => e.startPoint.gitToplevel)).toEqual(['/on'])
    })

    it('lists every repository when no liveness predicate is supplied', () => {
      const store = storeWith([{ workerId: 'w2', gitToplevel: '/off' }])
      const out = listRepoStartPoints([tab({ workerId: 'w2', gitToplevel: '/off' })], store)
      expect(out).toHaveLength(1)
    })

    it('does not let an offline worker keep its repository alive as a worktree anchor', () => {
      // The main checkout is filtered out BEFORE the worktree collapse, so the
      // reachable worktree survives rather than being dropped in favour of a
      // checkout nobody can reach.
      const store = storeWith([
        { workerId: 'w2', gitToplevel: '/repo', originUrl: 'https://x/o/r.git' },
        { workerId: 'w2', gitToplevel: '/wt', originUrl: 'https://x/o/r.git', isWorktree: true },
      ])
      const out = listRepoStartPoints([
        tab({ workerId: 'w2', gitToplevel: '/repo' }),
        tab({ workerId: 'w2', gitToplevel: '/wt' }),
      ], store, { isWorkerOnline: () => true })
      expect(out.map(e => e.startPoint.gitToplevel)).toEqual(['/repo'])
    })
  })

  describe('labels', () => {
    it('names an origin-backed repository the way the tree does', () => {
      const store = storeWith([{ gitToplevel: '/x', originUrl: 'git@github.com:org/leapmux.git' }])
      const out = listRepoStartPoints([tab({ gitToplevel: '/x' })], store)
      expect(out[0].label).toBe('github.com/org/leapmux')
    })

    it('names an origin-less repository by its directory', () => {
      const store = storeWith([{ gitToplevel: '/home/me/alpha' }])
      const out = listRepoStartPoints([tab({ gitToplevel: '/home/me/alpha' })], store)
      expect(out[0].label).toBe('alpha')
    })

    it('omits the worker while every entry is on one worker', () => {
      // On a solo desktop every row would carry the same prefix, which says
      // nothing and eats the width the repository name needs.
      const store = storeWith([{ gitToplevel: '/a' }, { gitToplevel: '/b' }])
      const out = listRepoStartPoints([
        tab({ gitToplevel: '/a' }),
        tab({ gitToplevel: '/b' }),
      ], store, { workerInfoFn })
      expect(out.map(e => e.label).toSorted()).toEqual(['a', 'b'])
    })

    it('names the worker once the list spans more than one', () => {
      const store = storeWith([
        { workerId: 'w1', gitToplevel: '/leapmux' },
        { workerId: 'w2', gitToplevel: '/api' },
      ])
      const out = listRepoStartPoints([
        tab({ workerId: 'w1', gitToplevel: '/leapmux' }),
        tab({ workerId: 'w2', gitToplevel: '/api' }),
      ], store, { workerInfoFn })
      expect(out.map(e => e.label).toSorted()).toEqual(['devbox · api', 'mini · leapmux'])
    })

    it('falls back to the worker id when its system info has not arrived', () => {
      const store = storeWith([
        { workerId: 'w1', gitToplevel: '/a' },
        { workerId: 'w9', gitToplevel: '/b' },
      ])
      const out = listRepoStartPoints([
        tab({ workerId: 'w1', gitToplevel: '/a' }),
        tab({ workerId: 'w9', gitToplevel: '/b' }),
      ], store, { workerInfoFn })
      expect(out.map(e => e.label).toSorted()).toEqual(['mini · a', 'w9 · b'])
    })

    it('appends a tilde-compressed path when two entries would read the same', () => {
      const store = storeWith([
        { gitToplevel: '/Users/me/a/leapmux' },
        { gitToplevel: '/Users/me/b/leapmux' },
      ])
      const out = listRepoStartPoints([
        tab({ gitToplevel: '/Users/me/a/leapmux' }),
        tab({ gitToplevel: '/Users/me/b/leapmux' }),
      ], store, { workerInfoFn })
      expect(out.map(e => e.label).toSorted())
        .toEqual(['leapmux (~/a/leapmux)', 'leapmux (~/b/leapmux)'])
    })

    it('leaves an unambiguous label alone', () => {
      const store = storeWith([
        { gitToplevel: '/Users/me/alpha' },
        { gitToplevel: '/Users/me/beta' },
      ])
      const out = listRepoStartPoints([
        tab({ gitToplevel: '/Users/me/alpha' }),
        tab({ gitToplevel: '/Users/me/beta' }),
      ], store, { workerInfoFn })
      expect(out.map(e => e.label).toSorted()).toEqual(['alpha', 'beta'])
    })

    it('gives every entry a distinct key', () => {
      const store = storeWith([
        { workerId: 'w1', gitToplevel: '/repo' },
        { workerId: 'w2', gitToplevel: '/repo' },
      ])
      const out = listRepoStartPoints([
        tab({ workerId: 'w1', gitToplevel: '/repo' }),
        tab({ workerId: 'w2', gitToplevel: '/repo' }),
      ], store, { workerInfoFn })
      expect(new Set(out.map(e => e.key)).size).toBe(2)
    })
  })
})
