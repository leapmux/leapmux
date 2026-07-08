import type { GitFileStatusEntry } from '~/generated/leapmux/v1/common_pb'
import type { Tab } from '~/stores/tab.types'
import { createRoot } from 'solid-js'
import { describe, expect, it, vi } from 'vitest'
import { applyGitStatusToTabs, syncGitStatusToTabs } from '~/components/shell/syncGitStatusToTabs'
import { GitFileStatusCode } from '~/generated/leapmux/v1/common_pb'
import { TabType } from '~/generated/leapmux/v1/workspace_pb'
import { createGitFileStatusStore } from '~/stores/gitFileStatus.store'
import { tabKey } from '~/stores/tab.helpers'
import { createTabStore } from '~/stores/tab.store'

const mockGetGitFileStatus = vi.fn()
vi.mock('~/api/workerRpc', () => ({
  getGitFileStatus: (...args: unknown[]) => mockGetGitFileStatus(...args),
}))

function makeEntry(overrides: Partial<GitFileStatusEntry> & { path: string }): GitFileStatusEntry {
  return {
    $typeName: 'leapmux.v1.GitFileStatusEntry',
    stagedStatus: GitFileStatusCode.UNSPECIFIED,
    unstagedStatus: GitFileStatusCode.UNSPECIFIED,
    linesAdded: 0,
    linesDeleted: 0,
    stagedLinesAdded: 0,
    stagedLinesDeleted: 0,
    oldPath: '',
    ...overrides,
  }
}

describe('syncGitStatusToTabs', () => {
  it('stamps git fields on terminal tabs whose workingDir sits under repoRoot', async () => {
    await createRoot(async (dispose) => {
      const tabStore = createTabStore()
      const gitFileStatusStore = createGitFileStatusStore()

      tabStore.addTab({ type: TabType.TERMINAL, id: 't1', workingDir: '/repo/sub' })
      tabStore.addTab({ type: TabType.TERMINAL, id: 't2', workingDir: '/elsewhere' })

      syncGitStatusToTabs({ gitFileStatusStore, tabStore })

      mockGetGitFileStatus.mockResolvedValueOnce({
        repoRoot: '/repo',
        originUrl: 'git@example.com:org/repo.git',
        currentBranch: 'main',
        files: [
          makeEntry({ path: 'a.ts', unstagedStatus: GitFileStatusCode.MODIFIED, linesAdded: 5, linesDeleted: 2 }),
          makeEntry({ path: 'b.ts', unstagedStatus: GitFileStatusCode.UNTRACKED }),
        ],
      })

      await gitFileStatusStore.refresh('worker1', '/repo')

      const t1 = tabStore.getTabByKey(tabKey({ type: TabType.TERMINAL, id: 't1' }))
      expect(t1?.gitDiffAdded).toBe(5)
      expect(t1?.gitDiffDeleted).toBe(2)
      expect(t1?.gitDiffUntracked).toBe(1)
      expect(t1?.gitOriginUrl).toBe('git@example.com:org/repo.git')
      expect(t1?.gitBranch).toBe('main')

      // Tab outside the repo isn't touched.
      const t2 = tabStore.getTabByKey(tabKey({ type: TabType.TERMINAL, id: 't2' }))
      expect(t2?.gitDiffAdded).toBeUndefined()
      expect(t2?.gitOriginUrl).toBeUndefined()

      dispose()
    })
  })

  // Per-agent metadata lives on the Tab record now, so the agent's
  // workingDir comes off the tab directly — no separate agentStore.
  it('reads agent workingDir from the tab record itself', async () => {
    await createRoot(async (dispose) => {
      const tabStore = createTabStore()
      const gitFileStatusStore = createGitFileStatusStore()

      tabStore.addTab({ type: TabType.AGENT, id: 'a1', workingDir: '/repo/agent-cwd' })

      syncGitStatusToTabs({ gitFileStatusStore, tabStore })

      mockGetGitFileStatus.mockResolvedValueOnce({
        repoRoot: '/repo',
        originUrl: '',
        currentBranch: '',
        files: [
          makeEntry({ path: 'a.ts', unstagedStatus: GitFileStatusCode.MODIFIED, linesAdded: 1, linesDeleted: 0 }),
        ],
      })

      await gitFileStatusStore.refresh('worker1', '/repo')

      const a1 = tabStore.getTabByKey(tabKey({ type: TabType.AGENT, id: 'a1' }))
      expect(a1?.gitDiffAdded).toBe(1)

      dispose()
    })
  })

  it('skips no-op writes when fields already match the resolved git stats', async () => {
    await createRoot(async (dispose) => {
      const tabStore = createTabStore()
      const gitFileStatusStore = createGitFileStatusStore()

      tabStore.addTab({ type: TabType.TERMINAL, id: 't1', workingDir: '/repo' })

      syncGitStatusToTabs({ gitFileStatusStore, tabStore })

      mockGetGitFileStatus.mockResolvedValue({
        repoRoot: '/repo',
        originUrl: '',
        currentBranch: 'main',
        files: [
          makeEntry({ path: 'a.ts', unstagedStatus: GitFileStatusCode.MODIFIED, linesAdded: 3, linesDeleted: 0 }),
        ],
      })

      await gitFileStatusStore.refresh('worker1', '/repo')
      const after1 = tabStore.getTabByKey(tabKey({ type: TabType.TERMINAL, id: 't1' }))
      const refBefore = after1
      expect(after1?.gitDiffAdded).toBe(3)

      // Second refresh with identical files: predicate filters out matching
      // tabs so no `updateMatchingTabs` write fires, leaving the same proxy
      // reference in place.
      await gitFileStatusStore.refresh('worker1', '/repo')
      const after2 = tabStore.getTabByKey(tabKey({ type: TabType.TERMINAL, id: 't1' }))
      expect(after2?.gitDiffAdded).toBe(3)
      expect(after2).toBe(refBefore)

      dispose()
    })
  })

  it('does not stamp the focused repo onto a nested tab that belongs to a different repo', async () => {
    // Sidebar grouping bug: tab A is rooted at /parent (its own git repo),
    // tab B's working dir is /parent/sub but it belongs to a *different*
    // git repo whose toplevel is /parent/sub. When tab A is the focused
    // repo the directory tree publishes A's repoRoot. The sync effect
    // must not overwrite tab B's identity just because B's path is
    // lexically inside A's tree — otherwise the workspace tab tree
    // groups them under one repo.
    await createRoot(async (dispose) => {
      const tabStore = createTabStore()
      const gitFileStatusStore = createGitFileStatusStore()

      tabStore.addTab({
        type: TabType.TERMINAL,
        id: 'a',
        workingDir: '/parent',
        gitOriginUrl: 'https://example.com/a.git',
        gitToplevel: '/parent',
        gitBranch: 'main',
      })
      tabStore.addTab({
        type: TabType.TERMINAL,
        id: 'b',
        workingDir: '/parent/sub',
        gitOriginUrl: 'https://example.com/b.git',
        gitToplevel: '/parent/sub',
        gitBranch: 'feature',
      })

      syncGitStatusToTabs({ gitFileStatusStore, tabStore })

      mockGetGitFileStatus.mockResolvedValueOnce({
        repoRoot: '/parent',
        originUrl: 'https://example.com/a.git',
        currentBranch: 'main',
        files: [],
      })
      await gitFileStatusStore.refresh('worker1', '/parent')

      const tabB = tabStore.getTabByKey(tabKey({ type: TabType.TERMINAL, id: 'b' }))
      expect(tabB?.gitOriginUrl).toBe('https://example.com/b.git')
      expect(tabB?.gitToplevel).toBe('/parent/sub')
      expect(tabB?.gitBranch).toBe('feature')

      dispose()
    })
  })

  it('stamps git fields on a FILE tab whose filePath sits under repoRoot', async () => {
    // FILE tabs hydrated after a refresh come back with only `filePath`
    // (the CRDT projection doesn't carry workingDir, and the path
    // hydrator only fills filePath). syncGitStatusToTabs must use
    // filePath as a stand-in for the containment check — otherwise the
    // workspace tree groups the tab under the wrong repo (or in the
    // ungrouped bucket).
    await createRoot(async (dispose) => {
      const tabStore = createTabStore()
      const gitFileStatusStore = createGitFileStatusStore()

      tabStore.addTab({ type: TabType.FILE, id: 'f1', filePath: '/repo/src/foo.ts' })

      syncGitStatusToTabs({ gitFileStatusStore, tabStore })

      mockGetGitFileStatus.mockResolvedValueOnce({
        repoRoot: '/repo',
        originUrl: 'https://example.com/repo.git',
        currentBranch: 'main',
        files: [
          makeEntry({ path: 'foo.ts', unstagedStatus: GitFileStatusCode.MODIFIED, linesAdded: 7, linesDeleted: 1 }),
        ],
      })

      await gitFileStatusStore.refresh('worker1', '/repo')

      const f1 = tabStore.getTabByKey(tabKey({ type: TabType.FILE, id: 'f1' }))
      expect(f1?.gitOriginUrl).toBe('https://example.com/repo.git')
      expect(f1?.gitBranch).toBe('main')
      expect(f1?.gitToplevel).toBe('/repo')
      expect(f1?.gitDiffAdded).toBe(7)
      expect(f1?.gitDiffDeleted).toBe(1)

      dispose()
    })
  })

  it('does not stamp a FILE tab whose filePath lives outside repoRoot', async () => {
    await createRoot(async (dispose) => {
      const tabStore = createTabStore()
      const gitFileStatusStore = createGitFileStatusStore()

      // FILE tab outside the focused repo's tree — must stay unstamped.
      tabStore.addTab({ type: TabType.FILE, id: 'outside', filePath: '/other-repo/src/x.ts' })
      // Sanity reference: an inside tab so we can assert the effect did fire.
      tabStore.addTab({ type: TabType.FILE, id: 'inside', filePath: '/repo/y.ts' })

      syncGitStatusToTabs({ gitFileStatusStore, tabStore })

      mockGetGitFileStatus.mockResolvedValueOnce({
        repoRoot: '/repo',
        originUrl: 'https://example.com/repo.git',
        currentBranch: 'main',
        files: [],
      })
      await gitFileStatusStore.refresh('worker1', '/repo')

      const outside = tabStore.getTabByKey(tabKey({ type: TabType.FILE, id: 'outside' }))
      expect(outside?.gitOriginUrl).toBeUndefined()
      expect(outside?.gitToplevel).toBeUndefined()

      const inside = tabStore.getTabByKey(tabKey({ type: TabType.FILE, id: 'inside' }))
      expect(inside?.gitOriginUrl).toBe('https://example.com/repo.git')
      expect(inside?.gitToplevel).toBe('/repo')

      dispose()
    })
  })

  it('prefers workingDir over filePath on a FILE tab that has both', async () => {
    // Live-session FILE tabs (those opened via useTabOperations.openFile)
    // carry both `workingDir` and `filePath`. The containment check uses
    // workingDir first; we verify by giving the FILE tab a workingDir
    // OUTSIDE the focused repo and a filePath INSIDE — the tab must not
    // be stamped, proving workingDir won.
    await createRoot(async (dispose) => {
      const tabStore = createTabStore()
      const gitFileStatusStore = createGitFileStatusStore()

      tabStore.addTab({
        type: TabType.FILE,
        id: 'f1',
        filePath: '/repo/inside.ts',
        workingDir: '/elsewhere',
      })

      syncGitStatusToTabs({ gitFileStatusStore, tabStore })

      mockGetGitFileStatus.mockResolvedValueOnce({
        repoRoot: '/repo',
        originUrl: 'https://example.com/repo.git',
        currentBranch: 'main',
        files: [],
      })
      await gitFileStatusStore.refresh('worker1', '/repo')

      const f1 = tabStore.getTabByKey(tabKey({ type: TabType.FILE, id: 'f1' }))
      expect(f1?.gitOriginUrl).toBeUndefined()
      expect(f1?.gitToplevel).toBeUndefined()

      dispose()
    })
  })

  it('skips a FILE tab that has neither workingDir nor a filePath yet', async () => {
    // Pre-hydration: the path hydrator hasn't filled filePath in yet,
    // and the CRDT projection didn't carry workingDir. There's nothing
    // to compare against repoRoot, so the effect must leave the tab
    // alone (no false positives).
    await createRoot(async (dispose) => {
      const tabStore = createTabStore()
      const gitFileStatusStore = createGitFileStatusStore()

      tabStore.addTab({ type: TabType.FILE, id: 'f1' })

      syncGitStatusToTabs({ gitFileStatusStore, tabStore })

      mockGetGitFileStatus.mockResolvedValueOnce({
        repoRoot: '/repo',
        originUrl: 'https://example.com/repo.git',
        currentBranch: 'main',
        files: [],
      })
      await gitFileStatusStore.refresh('worker1', '/repo')

      const f1 = tabStore.getTabByKey(tabKey({ type: TabType.FILE, id: 'f1' }))
      expect(f1?.gitOriginUrl).toBeUndefined()
      expect(f1?.gitToplevel).toBeUndefined()
      expect(f1?.gitBranch).toBeUndefined()

      dispose()
    })
  })

  it('stamps a tab that is added AFTER the git store already has data', async () => {
    // Regression: when the user opens a new file in the same focused
    // repo, the git store state doesn't change (same files, branch,
    // repoRoot). Before the fix, the effect tracked only store state,
    // so the new FILE tab arrived after the last store-state change
    // and never got stamped — the workspace tab tree placed it under
    // "Ungrouped" until a page refresh re-ran the restore→refresh
    // sequence in the right order.
    await createRoot(async (dispose) => {
      const tabStore = createTabStore()
      const gitFileStatusStore = createGitFileStatusStore()

      syncGitStatusToTabs({ gitFileStatusStore, tabStore })

      mockGetGitFileStatus.mockResolvedValueOnce({
        repoRoot: '/repo',
        originUrl: 'https://example.com/repo.git',
        currentBranch: 'main',
        files: [
          makeEntry({ path: 'foo.ts', unstagedStatus: GitFileStatusCode.MODIFIED, linesAdded: 4, linesDeleted: 2 }),
        ],
      })
      // Populate the store first. No tabs exist yet, so the effect
      // runs and finds nothing to stamp.
      await gitFileStatusStore.refresh('worker1', '/repo')

      // Now the user opens a file — new FILE tab arrives after the
      // store already settled. The effect must still notice and stamp
      // it on the next reactive flush.
      tabStore.addTab({ type: TabType.FILE, id: 'newly-opened', filePath: '/repo/foo.ts', workingDir: '/repo' })
      await Promise.resolve()

      const tab = tabStore.getTabByKey(tabKey({ type: TabType.FILE, id: 'newly-opened' }))
      expect(tab?.gitOriginUrl).toBe('https://example.com/repo.git')
      expect(tab?.gitBranch).toBe('main')
      expect(tab?.gitToplevel).toBe('/repo')
      expect(tab?.gitDiffAdded).toBe(4)
      expect(tab?.gitDiffDeleted).toBe(2)

      dispose()
    })
  })

  it('order-independent signature: reordering tabs without changing the set does not refire the effect', async () => {
    // Regression guard for the Set-equality memo. Drag-reorder mutates
    // tab `position` and re-sorts the underlying array without changing
    // the (type,id,workingDir,gitToplevel) tuples we sign. If the
    // signature were order-sensitive the effect would refire on every
    // drag, churning store identities for nothing.
    await createRoot(async (dispose) => {
      const tabStore = createTabStore()
      const gitFileStatusStore = createGitFileStatusStore()

      tabStore.addTab({ type: TabType.TERMINAL, id: 't1', workingDir: '/repo/a' })
      tabStore.addTab({ type: TabType.TERMINAL, id: 't2', workingDir: '/repo/b' })

      syncGitStatusToTabs({ gitFileStatusStore, tabStore })

      mockGetGitFileStatus.mockResolvedValue({
        repoRoot: '/repo',
        originUrl: '',
        currentBranch: 'main',
        files: [],
      })
      await gitFileStatusStore.refresh('worker1', '/repo')

      // Both tabs stamped — record their post-stamp identities.
      const t1Before = tabStore.getTabByKey(tabKey({ type: TabType.TERMINAL, id: 't1' }))
      const t2Before = tabStore.getTabByKey(tabKey({ type: TabType.TERMINAL, id: 't2' }))
      expect(t1Before?.gitToplevel).toBe('/repo')
      expect(t2Before?.gitToplevel).toBe('/repo')

      // Reorder via the public API: `position` is NOT one of the signed
      // tuple fields, so the unstampedTabsSignature set is unchanged.
      tabStore.setTabPosition(tabKey({ type: TabType.TERMINAL, id: 't1' }), 'z')
      tabStore.setTabPosition(tabKey({ type: TabType.TERMINAL, id: 't2' }), 'a')
      await Promise.resolve()
      await Promise.resolve()

      // Same proxy identity → no syncGitStatusToTabs-triggered write. If
      // the memo were order-sensitive, the effect would have refired and
      // (even with no-op fields) would have replaced the row.
      const t1After = tabStore.getTabByKey(tabKey({ type: TabType.TERMINAL, id: 't1' }))
      const t2After = tabStore.getTabByKey(tabKey({ type: TabType.TERMINAL, id: 't2' }))
      expect(t1After).toBe(t1Before)
      expect(t2After).toBe(t2Before)

      dispose()
    })
  })

  describe('applyGitStatusToTabs (cross-workspace / registry-snapshot stamping)', () => {
    // applyGitStatusToTabs replaces the inlined effect body so that
    // inactive workspace snapshots can use the same containment +
    // aggregation rules as the active tabStore. The reactive
    // syncGitStatusToTabs effect routes through this helper too, so
    // these tests cover the active path indirectly; the assertions
    // below pin the cross-workspace shape — a snapshot-shaped
    // TabStampTarget with a custom update fn that rewrites a plain
    // array (mimicking how AppShell stamps inactive registry
    // snapshots).
    it('stamps a snapshot-shaped target without going through tabStore', () => {
      let tabs: Tab[] = [
        { type: TabType.TERMINAL, id: 't1', workingDir: '/repo' } as Tab,
        { type: TabType.TERMINAL, id: 't2', workingDir: '/elsewhere' } as Tab,
      ]
      applyGitStatusToTabs({
        get tabs() {
          return tabs
        },
        update: (predicate, fields) => {
          tabs = tabs.map(t => predicate(t) ? { ...t, ...fields } as Tab : t)
        },
      }, {
        repoRoot: '/repo',
        toplevel: '/repo',
        originUrl: 'git@example.com:org/repo.git',
        currentBranch: 'feature',
        files: [
          makeEntry({ path: 'a.ts', unstagedStatus: GitFileStatusCode.MODIFIED, linesAdded: 9, linesDeleted: 4 }),
          makeEntry({ path: 'b/', unstagedStatus: GitFileStatusCode.UNTRACKED }),
        ],
      })
      const t1 = tabs.find(t => t.id === 't1')!
      expect(t1.gitToplevel).toBe('/repo')
      expect(t1.gitBranch).toBe('feature')
      expect(t1.gitOriginUrl).toBe('git@example.com:org/repo.git')
      expect(t1.gitDiffAdded).toBe(9)
      expect(t1.gitDiffDeleted).toBe(4)
      expect(t1.gitDiffUntracked).toBe(1)
      // Snapshot's other tab stayed untouched — outside the repo path,
      // outside the predicate.
      const t2 = tabs.find(t => t.id === 't2')!
      expect(t2.gitToplevel).toBeUndefined()
      expect(t2.gitBranch).toBeUndefined()
    })

    it('is a no-op when no tab matches — does not call update()', () => {
      const update = vi.fn()
      const tabs: Tab[] = [{ type: TabType.TERMINAL, id: 't1', workingDir: '/elsewhere' } as Tab]
      applyGitStatusToTabs({ tabs, update }, {
        repoRoot: '/repo',
        toplevel: '/repo',
        originUrl: '',
        currentBranch: 'main',
        files: [],
      })
      expect(update).not.toHaveBeenCalled()
    })

    it('is a no-op when status.toplevel is empty (no working tree to anchor)', () => {
      const update = vi.fn()
      const tabs: Tab[] = [{ type: TabType.TERMINAL, id: 't1', workingDir: '/repo' } as Tab]
      applyGitStatusToTabs({ tabs, update }, {
        repoRoot: '/repo',
        toplevel: '',
        originUrl: '',
        currentBranch: '',
        files: [],
      })
      expect(update).not.toHaveBeenCalled()
    })

    it('stamps the worktree variant onto only the worktree tab, NOT main-tree tabs sharing repoRoot', () => {
      // Regression: the bug this fix exists for. Before the toplevel
      // split, syncGitStatusToTabs matched containment against the
      // CANONICAL repo root. A worktree query returns
      //   { repoRoot: '/repo', toplevel: '/repo-wts/feature', isWorktree: true,
      //     currentBranch: 'feature' }
      // and the old logic stamped the worktree's branch onto every tab
      // whose gitToplevel == '/repo' — i.e. the entire main tree.
      // After the fix: only tabs whose gitToplevel == toplevel match.
      let tabs: Tab[] = [
        // Main-tree tab: gitToplevel === repo_root. Must KEEP its branch.
        {
          type: TabType.AGENT,
          id: 'main',
          workingDir: '/repo',
          gitToplevel: '/repo',
          gitBranch: 'trunk',
        } as Tab,
        // Worktree tab: gitToplevel === worktree root. SHOULD pick up new branch.
        {
          type: TabType.AGENT,
          id: 'wt',
          workingDir: '/repo-wts/feature',
          gitToplevel: '/repo-wts/feature',
          gitBranch: 'trunk', // pre-stamp stale label
        } as Tab,
      ]
      applyGitStatusToTabs({
        get tabs() {
          return tabs
        },
        update: (predicate, fields) => {
          tabs = tabs.map(t => predicate(t) ? { ...t, ...fields } as Tab : t)
        },
      }, {
        repoRoot: '/repo',
        toplevel: '/repo-wts/feature',
        originUrl: '',
        currentBranch: 'feature',
        files: [],
      })
      const main = tabs.find(t => t.id === 'main')!
      const wt = tabs.find(t => t.id === 'wt')!
      // Main tree tab keeps its branch — the worktree refresh must not
      // touch it.
      expect(main.gitBranch).toBe('trunk')
      expect(main.gitToplevel).toBe('/repo')
      // Worktree tab gets the worktree's branch.
      expect(wt.gitBranch).toBe('feature')
      expect(wt.gitToplevel).toBe('/repo-wts/feature')
    })

    it('migrates a pre-PR worktree tab whose gitToplevel was stamped as repoRoot', () => {
      // Pre-PR worker code stamped gitToplevel = repoRoot for BOTH
      // main-tree and worktree tabs (the worktree-aware `toplevel`
      // field didn't exist yet). After upgrade, the new worker reports
      // toplevel = worktreeDir, so the exact-toplevel-match check
      // would permanently skip the persisted worktree tab and freeze
      // its branch/diff badges at the pre-upgrade values. The
      // migration branch detects this exact case (tab.gitToplevel
      // equals the new status.repoRoot, AND the tab's containment
      // path sits under the new toplevel) and re-stamps once. After
      // the re-stamp, subsequent refreshes hit the exact-match path.
      let tabs: Tab[] = [
        // Pre-PR worktree tab: containment path is inside the
        // worktree, but gitToplevel still holds the (stale) repoRoot.
        {
          type: TabType.AGENT,
          id: 'wt-legacy',
          workingDir: '/repo-wts/feature/cmd',
          gitToplevel: '/repo', // stale pre-PR stamp
          gitBranch: 'trunk', // stale pre-PR branch label
        } as Tab,
        // Pre-PR main-tree tab: same stale gitToplevel, but
        // containment path is NOT under the worktree — must NOT be
        // re-stamped with the worktree's branch.
        {
          type: TabType.AGENT,
          id: 'main-legacy',
          workingDir: '/repo/cmd',
          gitToplevel: '/repo',
          gitBranch: 'trunk',
        } as Tab,
      ]
      applyGitStatusToTabs({
        get tabs() {
          return tabs
        },
        update: (predicate, fields) => {
          tabs = tabs.map(t => predicate(t) ? { ...t, ...fields } as Tab : t)
        },
      }, {
        repoRoot: '/repo',
        toplevel: '/repo-wts/feature',
        originUrl: '',
        currentBranch: 'feature',
        files: [],
      })

      const wt = tabs.find(t => t.id === 'wt-legacy')!
      const main = tabs.find(t => t.id === 'main-legacy')!
      // Worktree tab migrated to the worktree-aware toplevel + branch.
      expect(wt.gitToplevel).toBe('/repo-wts/feature')
      expect(wt.gitBranch).toBe('feature')
      // Main-tree tab untouched — containment guard rejects it
      // (containmentPath is not under the worktree's toplevel).
      expect(main.gitToplevel).toBe('/repo')
      expect(main.gitBranch).toBe('trunk')
    })

    it('does NOT migrate when the status is itself a main-tree refresh', () => {
      // The migration only fires for worktree-shaped status (toplevel
      // != repoRoot). A main-tree refresh has toplevel === repoRoot,
      // and pre-PR main-tree tabs already carry gitToplevel ===
      // repoRoot — these hit the exact-match branch and re-stamp via
      // tabAlreadyMatches' normal path. Verify that the same input
      // doesn't trip the migration sub-branch for a sibling-repo tab
      // that just happens to share the repoRoot value.
      let tabs: Tab[] = [
        // A tab in a SIBLING repo at /other-repo whose gitToplevel
        // legitimately matches /repo (pathological alias case — same
        // string value, different actual directories). With the
        // toplevel == repoRoot check + containment guard, this stays
        // safely skipped.
        {
          type: TabType.AGENT,
          id: 'sibling',
          workingDir: '/other-repo/cmd',
          gitToplevel: '/repo',
          gitBranch: 'trunk',
        } as Tab,
      ]
      applyGitStatusToTabs({
        get tabs() {
          return tabs
        },
        update: (predicate, fields) => {
          tabs = tabs.map(t => predicate(t) ? { ...t, ...fields } as Tab : t)
        },
      }, {
        repoRoot: '/repo',
        toplevel: '/repo-wts/feature',
        originUrl: '',
        currentBranch: 'feature',
        files: [],
      })

      const sib = tabs.find(t => t.id === 'sibling')!
      // Containment guard saved it: /other-repo/cmd is not under
      // /repo-wts/feature, so the migration branch's second condition
      // (`relativeUnder(containmentPath, toplevel) !== null`) rejects.
      expect(sib.gitToplevel).toBe('/repo')
      expect(sib.gitBranch).toBe('trunk')
    })

    it('does NOT over-stamp a nested-repo tab via the migration branch', () => {
      // The migration branch requires `tab.gitToplevel ===
      // status.repoRoot`. A nested-repo tab carries the INNER repo's
      // toplevel (e.g. /repo/vendor/inner), NOT the parent's
      // repoRoot, so the migration condition is false and the
      // authoritative exact-match check rejects it.
      let tabs: Tab[] = [
        {
          type: TabType.AGENT,
          id: 'nested',
          workingDir: '/repo/vendor/inner/cmd',
          gitToplevel: '/repo/vendor/inner', // nested repo's own toplevel
          gitBranch: 'nested-branch',
        } as Tab,
      ]
      applyGitStatusToTabs({
        get tabs() {
          return tabs
        },
        update: (predicate, fields) => {
          tabs = tabs.map(t => predicate(t) ? { ...t, ...fields } as Tab : t)
        },
      }, {
        repoRoot: '/repo',
        toplevel: '/repo',
        originUrl: '',
        currentBranch: 'parent-main',
        files: [],
      })

      const nested = tabs.find(t => t.id === 'nested')!
      // Nested repo's stamp survives the parent's refresh.
      expect(nested.gitToplevel).toBe('/repo/vendor/inner')
      expect(nested.gitBranch).toBe('nested-branch')
    })
  })

  it('seeds gitToplevel on a tab that has not yet learned its toplevel', async () => {
    // First-sync fallback: a freshly-created tab has no gitToplevel yet,
    // so the path-prefix check is the best we can do. After the first
    // sync, the tab carries its authoritative gitToplevel for subsequent
    // runs to compare against.
    await createRoot(async (dispose) => {
      const tabStore = createTabStore()
      const gitFileStatusStore = createGitFileStatusStore()

      tabStore.addTab({ type: TabType.TERMINAL, id: 't1', workingDir: '/repo/nested' })

      syncGitStatusToTabs({ gitFileStatusStore, tabStore })

      mockGetGitFileStatus.mockResolvedValueOnce({
        repoRoot: '/repo',
        originUrl: 'https://example.com/repo.git',
        currentBranch: 'main',
        files: [],
      })
      await gitFileStatusStore.refresh('worker1', '/repo')

      const t1 = tabStore.getTabByKey(tabKey({ type: TabType.TERMINAL, id: 't1' }))
      expect(t1?.gitOriginUrl).toBe('https://example.com/repo.git')
      expect(t1?.gitToplevel).toBe('/repo')
      expect(t1?.gitBranch).toBe('main')

      dispose()
    })
  })
})
