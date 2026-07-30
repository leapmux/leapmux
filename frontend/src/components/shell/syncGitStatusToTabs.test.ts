import type { GitFileStatusEntry } from '~/generated/leapmux/v1/common_pb'
import type { Tab } from '~/stores/tab.types'
import type { TabMetadata } from '~/stores/tabMetadata.store'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { applyGitStatusToTabs, syncGitStatusToTabs } from '~/components/shell/syncGitStatusToTabs'
import { GitFileStatusCode } from '~/generated/leapmux/v1/common_pb'
import { TabType } from '~/generated/leapmux/v1/workspace_pb'
import { setCRDTBridge } from '~/lib/crdt'
import { createGitFileStatusStore } from '~/stores/gitFileStatus.store'
import { emitAddTab, emitSetTabPosition } from '~/stores/tabOps'
import { withTestBridge } from '~/test-support/crdtBridge'
import { createTestTabStores } from '~/test-support/tabStores'

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

afterEach(() => setCRDTBridge(null))

let nextPosition = 0

/**
 * Mount the reactive effect over a real bridge and hand the body a small
 * add/read surface.
 *
 * Placement (`tileId`, `position`) comes from the CRDT projection and metadata
 * (`workingDir`, `filePath`, the git fields) from `tabMetadata`, so `add` has to
 * write both halves — that split IS the thing under test here, since the effect
 * reads containment paths off the join and writes git fields back to metadata.
 */
async function withSync(
  body: (ctx: {
    add: (type: TabType, id: string, meta?: TabMetadata) => void
    get: (type: TabType, id: string) => Tab | undefined
    /** Tab ids written since the last `resetPatches()`, in call order. */
    patched: () => string[]
    resetPatches: () => void
    refresh: (workerId: string, dir: string) => Promise<unknown>
    setPosition: (type: TabType, id: string, position: string) => void
  }) => Promise<void>,
): Promise<void> {
  await withTestBridge(async (harness) => {
    const { view, metadata } = createTestTabStores(harness.workspaceId)
    const realPatchMatching = metadata.patchMatching.bind(metadata)
    const gitFileStatusStore = createGitFileStatusStore()
    // Recording which tabs get written is how "already-stamped tabs aren't
    // rewritten" is observable now. The join assembles a fresh `Tab` object per
    // read, so the old `expect(after).toBe(before)` proxy-identity check can no
    // longer see a redundant write.
    //
    // BOTH write paths must be observed. The stamp goes through `patchMatching`
    // (one `produce` for the whole sweep); `patch` is only how this harness
    // seeds a tab's metadata. Watching `patch` alone would let every
    // "the stamp must actually write" assertion pass on the seed write while
    // the stamp itself did nothing.
    const stamped: string[] = []
    const patch = vi.spyOn(metadata, 'patch')
    vi.spyOn(metadata, 'patchMatching').mockImplementation((predicate, fields) => {
      // The production predicate closes over a fixed id set, so evaluating it
      // here against the live rows names exactly the tabs the sweep will write.
      for (const [tabId, meta] of Object.entries(metadata.state.byTabId)) {
        if (predicate(meta, tabId))
          stamped.push(tabId)
      }
      return realPatchMatching(predicate, fields)
    })

    syncGitStatusToTabs({ gitFileStatusStore, view, metadata })

    await body({
      // Every tab lands on `worker1`, which is the worker each test refreshes
      // against. Repo identity is `(workerId, toplevel)` -- a status read from
      // one worker must not stamp an identically-pathed repo on another -- so a
      // tab with no worker matches nothing.
      add: (type, id, meta) => {
        nextPosition += 1
        emitAddTab({ type, id, tileId: harness.rootTileId, position: `p${nextPosition}`, workerId: 'worker1' })
        if (meta)
          metadata.patch(id, meta)
      },
      get: (type, id) => view.getById(type, id),
      patched: () => [...patch.mock.calls.map(c => c[0]), ...stamped],
      resetPatches: () => {
        patch.mockClear()
        stamped.length = 0
      },
      refresh: (workerId, dir) => gitFileStatusStore.refresh(workerId, dir),
      setPosition: (type, id, position) => emitSetTabPosition(type, id, position),
    })
  })
}

describe('syncGitStatusToTabs', () => {
  it('stamps git fields on terminal tabs whose workingDir sits under repoRoot', async () => {
    await withSync(async ({ add, get, refresh }) => {
      add(TabType.TERMINAL, 't1', { workingDir: '/repo/sub' })
      add(TabType.TERMINAL, 't2', { workingDir: '/elsewhere' })

      mockGetGitFileStatus.mockResolvedValueOnce({
        repoRoot: '/repo',
        toplevel: '/repo',
        originUrl: 'git@example.com:org/repo.git',
        currentBranch: 'main',
        files: [
          makeEntry({ path: 'a.ts', unstagedStatus: GitFileStatusCode.MODIFIED, linesAdded: 5, linesDeleted: 2 }),
          makeEntry({ path: 'b.ts', unstagedStatus: GitFileStatusCode.UNTRACKED }),
        ],
      })

      await refresh('worker1', '/repo')

      const t1 = get(TabType.TERMINAL, 't1')
      expect(t1?.gitDiffAdded).toBe(5)
      expect(t1?.gitDiffDeleted).toBe(2)
      expect(t1?.gitDiffUntracked).toBe(1)
      expect(t1?.gitOriginUrl).toBe('git@example.com:org/repo.git')
      expect(t1?.gitBranch).toBe('main')

      // Tab outside the repo isn't touched.
      const t2 = get(TabType.TERMINAL, 't2')
      expect(t2?.gitDiffAdded).toBeUndefined()
      expect(t2?.gitOriginUrl).toBeUndefined()
    })
  })

  // Per-agent metadata lives on the Tab record now, so the agent's
  // workingDir comes off the tab directly — no separate agentStore.
  it('reads agent workingDir from the tab record itself', async () => {
    await withSync(async ({ add, get, refresh }) => {
      add(TabType.AGENT, 'a1', { workingDir: '/repo/agent-cwd' })

      mockGetGitFileStatus.mockResolvedValueOnce({
        repoRoot: '/repo',
        toplevel: '/repo',
        originUrl: '',
        currentBranch: '',
        files: [
          makeEntry({ path: 'a.ts', unstagedStatus: GitFileStatusCode.MODIFIED, linesAdded: 1, linesDeleted: 0 }),
        ],
      })

      await refresh('worker1', '/repo')

      const a1 = get(TabType.AGENT, 'a1')
      expect(a1?.gitDiffAdded).toBe(1)
    })
  })

  it('skips no-op writes when fields already match the resolved git stats', async () => {
    await withSync(async ({ add, get, patched, refresh, resetPatches }) => {
      add(TabType.TERMINAL, 't1', { workingDir: '/repo' })

      mockGetGitFileStatus.mockResolvedValue({
        repoRoot: '/repo',
        toplevel: '/repo',
        originUrl: '',
        currentBranch: 'main',
        files: [
          makeEntry({ path: 'a.ts', unstagedStatus: GitFileStatusCode.MODIFIED, linesAdded: 3, linesDeleted: 0 }),
        ],
      })

      await refresh('worker1', '/repo')
      expect(get(TabType.TERMINAL, 't1')?.gitDiffAdded).toBe(3)
      expect(patched(), 'the first refresh must actually write').toContain('t1')

      // Adding a tab changes `unstampedTabsSignature`, so the effect genuinely
      // re-runs and re-walks BOTH tabs. `tabAlreadyMatches` is what keeps the
      // already-correct t1 out of the write -- without it, every new tab in a
      // repo would rewrite every existing tab's git fields, invalidating the
      // sidebar's memos across the whole account.
      resetPatches()
      add(TabType.TERMINAL, 't2', { workingDir: '/repo' })
      await Promise.resolve()
      await Promise.resolve()

      expect(get(TabType.TERMINAL, 't2')?.gitDiffAdded, 'the new tab is stamped').toBe(3)
      expect(patched()).not.toContain('t1')
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
    await withSync(async ({ add, get, refresh }) => {
      add(TabType.TERMINAL, 'a', { workingDir: '/parent', gitOriginUrl: 'https://example.com/a.git', gitToplevel: '/parent', gitBranch: 'main' })
      add(TabType.TERMINAL, 'b', { workingDir: '/parent/sub', gitOriginUrl: 'https://example.com/b.git', gitToplevel: '/parent/sub', gitBranch: 'feature' })

      mockGetGitFileStatus.mockResolvedValueOnce({
        repoRoot: '/parent',
        toplevel: '/parent',
        originUrl: 'https://example.com/a.git',
        currentBranch: 'main',
        files: [],
      })
      await refresh('worker1', '/parent')

      const tabB = get(TabType.TERMINAL, 'b')
      expect(tabB?.gitOriginUrl).toBe('https://example.com/b.git')
      expect(tabB?.gitToplevel).toBe('/parent/sub')
      expect(tabB?.gitBranch).toBe('feature')
    })
  })

  it('stamps git fields on a FILE tab whose filePath sits under repoRoot', async () => {
    // FILE tabs hydrated after a refresh come back with only `filePath`
    // (the CRDT projection doesn't carry workingDir, and the path
    // hydrator only fills filePath). syncGitStatusToTabs must use
    // filePath as a stand-in for the containment check — otherwise the
    // workspace tree groups the tab under the wrong repo (or in the
    // ungrouped bucket).
    await withSync(async ({ add, get, refresh }) => {
      add(TabType.FILE, 'f1', { filePath: '/repo/src/foo.ts' })

      mockGetGitFileStatus.mockResolvedValueOnce({
        repoRoot: '/repo',
        toplevel: '/repo',
        originUrl: 'https://example.com/repo.git',
        currentBranch: 'main',
        files: [
          makeEntry({ path: 'foo.ts', unstagedStatus: GitFileStatusCode.MODIFIED, linesAdded: 7, linesDeleted: 1 }),
        ],
      })

      await refresh('worker1', '/repo')

      const f1 = get(TabType.FILE, 'f1')
      expect(f1?.gitOriginUrl).toBe('https://example.com/repo.git')
      expect(f1?.gitBranch).toBe('main')
      expect(f1?.gitToplevel).toBe('/repo')
      expect(f1?.gitDiffAdded).toBe(7)
      expect(f1?.gitDiffDeleted).toBe(1)
    })
  })

  // `workingDir: ''` is the NORMAL state of a just-opened file tab, not a
  // corner case: the local open path seeds it from `getCurrentTabContext()`,
  // which answers `''` until worker hydration lands. A nullish-coalescing
  // fallback passes that empty string through as the containment path, and the
  // `if (!containmentPath) continue` guard then drops the tab from the match
  // entirely -- so it renders with no branch group and no diff badge until the
  // worker echo arrives, instead of falling back to its own path meanwhile.
  it('stamps a FILE tab whose workingDir is the empty string', async () => {
    await withSync(async ({ add, get, refresh }) => {
      add(TabType.FILE, 'seeded', { filePath: '/repo/src/foo.ts', workingDir: '' })

      mockGetGitFileStatus.mockResolvedValueOnce({
        repoRoot: '/repo',
        toplevel: '/repo',
        originUrl: 'https://example.com/repo.git',
        currentBranch: 'main',
        files: [
          makeEntry({ path: 'foo.ts', unstagedStatus: GitFileStatusCode.MODIFIED, linesAdded: 3, linesDeleted: 0 }),
        ],
      })

      await refresh('worker1', '/repo')

      const t = get(TabType.FILE, 'seeded')
      expect(t?.gitBranch).toBe('main')
      expect(t?.gitToplevel).toBe('/repo')
      expect(t?.gitDiffAdded).toBe(3)
    })
  })

  it('does not stamp a FILE tab whose filePath lives outside repoRoot', async () => {
    await withSync(async ({ add, get, refresh }) => {
      // FILE tab outside the focused repo's tree — must stay unstamped.
      add(TabType.FILE, 'outside', { filePath: '/other-repo/src/x.ts' })
      // Sanity reference: an inside tab so we can assert the effect did fire.
      add(TabType.FILE, 'inside', { filePath: '/repo/y.ts' })

      mockGetGitFileStatus.mockResolvedValueOnce({
        repoRoot: '/repo',
        toplevel: '/repo',
        originUrl: 'https://example.com/repo.git',
        currentBranch: 'main',
        files: [],
      })
      await refresh('worker1', '/repo')

      const outside = get(TabType.FILE, 'outside')
      expect(outside?.gitOriginUrl).toBeUndefined()
      expect(outside?.gitToplevel).toBeUndefined()

      const inside = get(TabType.FILE, 'inside')
      expect(inside?.gitOriginUrl).toBe('https://example.com/repo.git')
      expect(inside?.gitToplevel).toBe('/repo')
    })
  })

  it('prefers workingDir over filePath on a FILE tab that has both', async () => {
    // Live-session FILE tabs (those opened via useTabOperations.openFile)
    // carry both `workingDir` and `filePath`. The containment check uses
    // workingDir first; we verify by giving the FILE tab a workingDir
    // OUTSIDE the focused repo and a filePath INSIDE — the tab must not
    // be stamped, proving workingDir won.
    await withSync(async ({ add, get, refresh }) => {
      add(TabType.FILE, 'f1', { filePath: '/repo/inside.ts', workingDir: '/elsewhere' })

      mockGetGitFileStatus.mockResolvedValueOnce({
        repoRoot: '/repo',
        toplevel: '/repo',
        originUrl: 'https://example.com/repo.git',
        currentBranch: 'main',
        files: [],
      })
      await refresh('worker1', '/repo')

      const f1 = get(TabType.FILE, 'f1')
      expect(f1?.gitOriginUrl).toBeUndefined()
      expect(f1?.gitToplevel).toBeUndefined()
    })
  })

  it('skips a FILE tab that has neither workingDir nor a filePath yet', async () => {
    // Pre-hydration: the path hydrator hasn't filled filePath in yet,
    // and the CRDT projection didn't carry workingDir. There's nothing
    // to compare against repoRoot, so the effect must leave the tab
    // alone (no false positives).
    await withSync(async ({ add, get, refresh }) => {
      add(TabType.FILE, 'f1')

      mockGetGitFileStatus.mockResolvedValueOnce({
        repoRoot: '/repo',
        toplevel: '/repo',
        originUrl: 'https://example.com/repo.git',
        currentBranch: 'main',
        files: [],
      })
      await refresh('worker1', '/repo')

      const f1 = get(TabType.FILE, 'f1')
      expect(f1?.gitOriginUrl).toBeUndefined()
      expect(f1?.gitToplevel).toBeUndefined()
      expect(f1?.gitBranch).toBeUndefined()
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
    await withSync(async ({ add, get, refresh }) => {
      mockGetGitFileStatus.mockResolvedValueOnce({
        repoRoot: '/repo',
        toplevel: '/repo',
        originUrl: 'https://example.com/repo.git',
        currentBranch: 'main',
        files: [
          makeEntry({ path: 'foo.ts', unstagedStatus: GitFileStatusCode.MODIFIED, linesAdded: 4, linesDeleted: 2 }),
        ],
      })
      // Populate the store first. No tabs exist yet, so the effect
      // runs and finds nothing to stamp.
      await refresh('worker1', '/repo')

      // Now the user opens a file — new FILE tab arrives after the
      // store already settled. The effect must still notice and stamp
      // it on the next reactive flush.
      add(TabType.FILE, 'newly-opened', { filePath: '/repo/foo.ts', workingDir: '/repo' })
      await Promise.resolve()

      const tab = get(TabType.FILE, 'newly-opened')
      expect(tab?.gitOriginUrl).toBe('https://example.com/repo.git')
      expect(tab?.gitBranch).toBe('main')
      expect(tab?.gitToplevel).toBe('/repo')
      expect(tab?.gitDiffAdded).toBe(4)
      expect(tab?.gitDiffDeleted).toBe(2)
    })
  })

  it('order-independent signature: reordering tabs without changing the set does not refire the effect', async () => {
    // Regression guard for the Set-equality memo. Drag-reorder mutates
    // tab `position` and re-sorts the underlying array without changing
    // the (type,id,workingDir,gitToplevel) tuples we sign. If the
    // signature were order-sensitive the effect would refire on every
    // drag, churning store identities for nothing.
    await withSync(async ({ add, get, patched, refresh, resetPatches, setPosition }) => {
      add(TabType.TERMINAL, 't1', { workingDir: '/repo/a' })
      add(TabType.TERMINAL, 't2', { workingDir: '/repo/b' })

      mockGetGitFileStatus.mockResolvedValue({
        repoRoot: '/repo',
        toplevel: '/repo',
        originUrl: '',
        currentBranch: 'main',
        files: [],
      })
      await refresh('worker1', '/repo')

      // Both tabs stamped.
      expect(get(TabType.TERMINAL, 't1')?.gitToplevel).toBe('/repo')
      expect(get(TabType.TERMINAL, 't2')?.gitToplevel).toBe('/repo')
      expect(patched(), 'the initial stamp must actually write').toContain('t1')
      resetPatches()

      // Reorder by emitting position ops: `position` is NOT one of the signed
      // tuple fields, so the unstampedTabsSignature set is unchanged even
      // though the projection ticks and re-sorts the tab list.
      setPosition(TabType.TERMINAL, 't1', 'z')
      setPosition(TabType.TERMINAL, 't2', 'a')
      await Promise.resolve()
      await Promise.resolve()

      // No further writes. If the signature were order-sensitive the effect
      // would have refired on the reorder; the recorded writes are what expose
      // that now that each read assembles a fresh `Tab`.
      expect(patched()).toEqual([])
      expect(get(TabType.TERMINAL, 't1')?.gitToplevel).toBe('/repo')
      expect(get(TabType.TERMINAL, 't2')?.gitToplevel).toBe('/repo')
    })
  })

  describe('applyGitStatusToTabs (direct, non-reactive stamping)', () => {
    // applyGitStatusToTabs is the effect body, extracted so the same
    // containment +
    // aggregation rules as the active tabStore. The reactive
    // syncGitStatusToTabs effect routes through this helper too, so
    // these tests cover the active path indirectly; the assertions
    // below drive it through a plain-array TabStampTarget, with no CRDT
    // bridge and no reactive root -- which is what the seam is for.
    it('stamps a plain-array target with no store behind it', () => {
      let tabs: Tab[] = [
        { type: TabType.TERMINAL, id: 't1', workerId: 'w1', workingDir: '/repo' } as Tab,
        { type: TabType.TERMINAL, id: 't2', workerId: 'w1', workingDir: '/elsewhere' } as Tab,
      ]
      applyGitStatusToTabs({
        get tabs() {
          return tabs
        },
        update: (tabIds, fields) => {
          tabs = tabs.map(t => tabIds.has(t.id) ? { ...t, ...fields } as Tab : t)
        },
      }, {
        workerId: 'w1',
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
      const tabs: Tab[] = [{ type: TabType.TERMINAL, id: 't1', workerId: 'w1', workingDir: '/elsewhere' } as Tab]
      applyGitStatusToTabs({ tabs, update }, {
        workerId: 'w1',
        toplevel: '/repo',
        originUrl: '',
        currentBranch: 'main',
        files: [],
      })
      expect(update).not.toHaveBeenCalled()
    })

    // Repo identity is (workerId, toplevel). The same absolute path on two
    // workers is the normal case -- two dev boxes, two identically-provisioned
    // containers -- and this stamp now reaches every workspace in the account,
    // so matching on path alone smears one worker's branch and diff badges
    // across the other's tabs.
    it('does not stamp a tab on a DIFFERENT worker with the same repo path', () => {
      let tabs: Tab[] = [
        { type: TabType.TERMINAL, id: 'mine', workerId: 'w1', workingDir: '/repo' } as Tab,
        { type: TabType.TERMINAL, id: 'theirs', workerId: 'w2', workingDir: '/repo' } as Tab,
      ]
      applyGitStatusToTabs({
        get tabs() {
          return tabs
        },
        update: (tabIds, fields) => {
          tabs = tabs.map(t => tabIds.has(t.id) ? { ...t, ...fields } as Tab : t)
        },
      }, {
        workerId: 'w1',
        toplevel: '/repo',
        originUrl: '',
        currentBranch: 'feature',
        files: [],
      })

      expect(tabs.find(t => t.id === 'mine')?.gitBranch).toBe('feature')
      expect(tabs.find(t => t.id === 'theirs')?.gitBranch, 'a different worker is a different repo').toBeUndefined()
    })

    it('is a no-op when the status carries no worker (nothing to anchor to)', () => {
      const update = vi.fn()
      const tabs: Tab[] = [{ type: TabType.TERMINAL, id: 't1', workerId: 'w1', workingDir: '/repo' } as Tab]
      applyGitStatusToTabs({ tabs, update }, {
        workerId: '',
        toplevel: '/repo',
        originUrl: '',
        currentBranch: 'main',
        files: [],
      })
      expect(update).not.toHaveBeenCalled()
    })

    // `metadata.patch` SKIPS undefined, so a cleared branch has to be sent as
    // `''`. Sending undefined left a detached HEAD showing its old branch
    // forever, and re-patched every tab on every refresh without converging.
    it('clears the branch when the repo no longer has one', () => {
      let tabs: Tab[] = [
        { type: TabType.TERMINAL, id: 't1', workerId: 'w1', workingDir: '/repo', gitBranch: 'old', gitToplevel: '/repo' } as Tab,
      ]
      applyGitStatusToTabs({
        get tabs() {
          return tabs
        },
        update: (tabIds, fields) => {
          tabs = tabs.map(t => tabIds.has(t.id) ? { ...t, ...fields } as Tab : t)
        },
      }, {
        workerId: 'w1',
        toplevel: '/repo',
        originUrl: '',
        currentBranch: '',
        files: [],
      })
      expect(tabs[0].gitBranch, 'a detached HEAD must not keep the stale label').toBe('')
    })

    it('is a no-op when status.toplevel is empty (no working tree to anchor)', () => {
      const update = vi.fn()
      const tabs: Tab[] = [{ type: TabType.TERMINAL, id: 't1', workerId: 'w1', workingDir: '/repo' } as Tab]
      applyGitStatusToTabs({ tabs, update }, {
        workerId: 'w1',
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
          workerId: 'w1',
          workingDir: '/repo',
          gitToplevel: '/repo',
          gitBranch: 'trunk',
        } as Tab,
        // Worktree tab: gitToplevel === worktree root. SHOULD pick up new branch.
        {
          type: TabType.AGENT,
          id: 'wt',
          workerId: 'w1',
          workingDir: '/repo-wts/feature',
          gitToplevel: '/repo-wts/feature',
          gitBranch: 'trunk', // pre-stamp stale label
        } as Tab,
      ]
      applyGitStatusToTabs({
        get tabs() {
          return tabs
        },
        update: (tabIds, fields) => {
          tabs = tabs.map(t => tabIds.has(t.id) ? { ...t, ...fields } as Tab : t)
        },
      }, {
        workerId: 'w1',
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

    it('does NOT stamp a sibling-repo tab whose gitToplevel aliases another repo path', () => {
      // `gitToplevel` is matched EXACTLY, so a tab whose stamp happens to
      // equal some other repo's path is still rejected: the worktree
      // refresh's toplevel is /repo-wts/feature and the tab's is /repo.
      // The value-equality alias must not be enough to claim the tab.
      let tabs: Tab[] = [
        // A tab in a SIBLING repo at /other-repo whose gitToplevel
        // legitimately matches /repo (pathological alias case — same
        // string value, different actual directories). With the
        // toplevel == repoRoot check + containment guard, this stays
        // safely skipped.
        {
          type: TabType.AGENT,
          id: 'sibling',
          workerId: 'w1',
          workingDir: '/other-repo/cmd',
          gitToplevel: '/repo',
          gitBranch: 'trunk',
        } as Tab,
      ]
      applyGitStatusToTabs({
        get tabs() {
          return tabs
        },
        update: (tabIds, fields) => {
          tabs = tabs.map(t => tabIds.has(t.id) ? { ...t, ...fields } as Tab : t)
        },
      }, {
        workerId: 'w1',
        toplevel: '/repo-wts/feature',
        originUrl: '',
        currentBranch: 'feature',
        files: [],
      })

      const sib = tabs.find(t => t.id === 'sibling')!
      // Rejected by the exact-toplevel check: '/repo' is not
      // '/repo-wts/feature', so the tab is never a stamping target.
      expect(sib.gitToplevel).toBe('/repo')
      expect(sib.gitBranch).toBe('trunk')
    })

    it('does NOT over-stamp a nested-repo tab whose path sits under the outer repo', () => {
      // A nested repo's tab carries the INNER repo's toplevel
      // (/repo/vendor/inner). Its containment path DOES sit under the
      // outer repo, so a path-based rule would claim it; the exact
      // toplevel match is what keeps the two repos' badges independent.
      let tabs: Tab[] = [
        {
          type: TabType.AGENT,
          id: 'nested',
          workerId: 'w1',
          workingDir: '/repo/vendor/inner/cmd',
          gitToplevel: '/repo/vendor/inner', // nested repo's own toplevel
          gitBranch: 'nested-branch',
        } as Tab,
      ]
      applyGitStatusToTabs({
        get tabs() {
          return tabs
        },
        update: (tabIds, fields) => {
          tabs = tabs.map(t => tabIds.has(t.id) ? { ...t, ...fields } as Tab : t)
        },
      }, {
        workerId: 'w1',
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
    await withSync(async ({ add, get, refresh }) => {
      add(TabType.TERMINAL, 't1', { workingDir: '/repo/nested' })

      mockGetGitFileStatus.mockResolvedValueOnce({
        workerId: 'w1',
        toplevel: '/repo',
        originUrl: 'https://example.com/repo.git',
        currentBranch: 'main',
        files: [],
      })
      await refresh('worker1', '/repo')

      const t1 = get(TabType.TERMINAL, 't1')
      expect(t1?.gitOriginUrl).toBe('https://example.com/repo.git')
      expect(t1?.gitToplevel).toBe('/repo')
      expect(t1?.gitBranch).toBe('main')
    })
  })
})
