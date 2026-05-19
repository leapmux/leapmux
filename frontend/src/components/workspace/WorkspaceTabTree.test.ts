import type { Tab } from '~/stores/tab.types'
import { describe, expect, it } from 'vitest'
import { SIDEBAR_TAB_PREFIX } from '~/components/shell/TabDragContext'
import { TabType } from '~/generated/leapmux/v1/workspace_pb'
import { repoKeyForLocal } from './branchKeys'
import { buildTree, formatGitOriginUrl, sumDiffStatsFromTabs, tabBuildKey } from './WorkspaceTabTree'

describe('formatGitOriginUrl', () => {
  it('strips https protocol', () => {
    expect(formatGitOriginUrl('https://github.com/org/repo.git'))
      .toBe('github.com/org/repo')
  })

  it('strips http protocol', () => {
    expect(formatGitOriginUrl('http://github.com/org/repo.git'))
      .toBe('github.com/org/repo')
  })

  it('converts SSH format', () => {
    expect(formatGitOriginUrl('git@github.com:org/repo.git'))
      .toBe('github.com/org/repo')
  })

  it('strips trailing .git', () => {
    expect(formatGitOriginUrl('https://github.com/org/repo.git'))
      .toBe('github.com/org/repo')
  })

  it('handles URL without .git suffix', () => {
    expect(formatGitOriginUrl('https://github.com/org/repo'))
      .toBe('github.com/org/repo')
  })

  it('strips trailing slash', () => {
    expect(formatGitOriginUrl('https://github.com/org/repo/'))
      .toBe('github.com/org/repo')
  })

  it('returns empty string for empty input', () => {
    expect(formatGitOriginUrl('')).toBe('')
  })

  it('handles SSH with nested path', () => {
    expect(formatGitOriginUrl('git@gitlab.com:group/subgroup/repo.git'))
      .toBe('gitlab.com/group/subgroup/repo')
  })
})

function makeTab(overrides: Partial<Tab> & { id: string }): Tab {
  return {
    type: TabType.AGENT,
    title: overrides.id,
    tileId: 'tile-1',
    position: '0',
    ...overrides,
  }
}

describe('buildTree', () => {
  it('returns empty tree for empty input', () => {
    const tree = buildTree([])
    expect(tree.groups).toHaveLength(0)
    expect(tree.ungrouped).toHaveLength(0)
  })

  it('puts tabs without git info into ungrouped', () => {
    const tabs = [
      makeTab({ id: 'a1' }),
      makeTab({ id: 'a2', type: TabType.TERMINAL }),
    ]
    const tree = buildTree(tabs)
    expect(tree.groups).toHaveLength(0)
    expect(tree.ungrouped).toHaveLength(2)
  })

  it('groups tabs by origin URL and branch', () => {
    const tabs = [
      makeTab({ id: 'a1', gitOriginUrl: 'https://github.com/org/repo.git', gitBranch: 'main' }),
      makeTab({ id: 'a2', gitOriginUrl: 'https://github.com/org/repo.git', gitBranch: 'main' }),
      makeTab({ id: 'a3', gitOriginUrl: 'https://github.com/org/repo.git', gitBranch: 'dev' }),
    ]
    const tree = buildTree(tabs)
    expect(tree.groups).toHaveLength(1)
    expect(tree.groups[0].branches).toHaveLength(2)
    // Branches sorted alphabetically
    expect(tree.groups[0].branches[0].branchName).toBe('dev')
    expect(tree.groups[0].branches[0].tabs).toHaveLength(1)
    expect(tree.groups[0].branches[1].branchName).toBe('main')
    expect(tree.groups[0].branches[1].tabs).toHaveLength(2)
  })

  it('separates different repos into different groups', () => {
    const tabs = [
      makeTab({ id: 'a1', gitOriginUrl: 'https://github.com/org/repo1.git', gitBranch: 'main' }),
      makeTab({ id: 'a2', gitOriginUrl: 'https://github.com/org/repo2.git', gitBranch: 'main' }),
    ]
    const tree = buildTree(tabs)
    expect(tree.groups).toHaveLength(2)
    // Groups sorted by formatted URL
    expect(tree.groups[0].repoLabel).toBe('github.com/org/repo1')
    expect(tree.groups[1].repoLabel).toBe('github.com/org/repo2')
  })

  it('sorts tabs by tile-layout order, then LexoRank position', () => {
    // Visual layout: two tiles. `tile-A` is the top-left tile, `tile-B`
    // sits to its right (top-left-first DFS order: A then B). Within
    // each tile, LexoRank position drives left-to-right order in the
    // tab bar.
    const tabs = [
      // Out-of-input-order on purpose; the sort must reorder them to
      // match the visual layout.
      makeTab({ id: 'b2', type: TabType.TERMINAL, tileId: 'tile-B', position: '0|', gitOriginUrl: 'https://github.com/org/repo.git', gitBranch: 'main' }),
      makeTab({ id: 'a2', type: TabType.TERMINAL, tileId: 'tile-A', position: '1|', gitOriginUrl: 'https://github.com/org/repo.git', gitBranch: 'main' }),
      makeTab({ id: 'b1', type: TabType.AGENT, tileId: 'tile-B', position: '0', gitOriginUrl: 'https://github.com/org/repo.git', gitBranch: 'main' }),
      makeTab({ id: 'a1', type: TabType.AGENT, tileId: 'tile-A', position: '0|', gitOriginUrl: 'https://github.com/org/repo.git', gitBranch: 'main' }),
    ]
    const tree = buildTree(tabs, ['tile-A', 'tile-B'])
    expect(tree.groups[0].branches[0].tabs.map(t => t.id)).toEqual(['a1', 'a2', 'b1', 'b2'])
  })

  it('falls back to position-then-id order when no tile order is provided', () => {
    // Without layout context (e.g. test harness, cold registry), every
    // tab gets the same primary rank → sort by `position` then `id`.
    const tabs = [
      makeTab({ id: 'z', tileId: 'tile-1', position: '1|', gitOriginUrl: 'https://github.com/org/repo.git', gitBranch: 'main' }),
      makeTab({ id: 'm', tileId: 'tile-1', position: '0', gitOriginUrl: 'https://github.com/org/repo.git', gitBranch: 'main' }),
      makeTab({ id: 'a', tileId: 'tile-1', position: '0', gitOriginUrl: 'https://github.com/org/repo.git', gitBranch: 'main' }),
    ]
    const tree = buildTree(tabs)
    // `a` and `m` share position '0'; id breaks the tie. `z` at
    // position '1|' sorts after both.
    expect(tree.groups[0].branches[0].tabs.map(t => t.id)).toEqual(['a', 'm', 'z'])
  })

  it('applies the tile-order sort to the ungrouped bucket too', () => {
    // Non-git tabs (no origin/toplevel) land in `ungrouped`. They still
    // need to track the visual layout — e.g. a terminal in the left
    // tile should appear above a terminal in the right tile even when
    // neither carries git metadata.
    const tabs = [
      makeTab({ id: 'right', type: TabType.TERMINAL, tileId: 'tile-B', position: '0|' }),
      makeTab({ id: 'left', type: TabType.TERMINAL, tileId: 'tile-A', position: '0|' }),
    ]
    const tree = buildTree(tabs, ['tile-A', 'tile-B'])
    expect(tree.groups).toHaveLength(0)
    expect(tree.ungrouped.map(t => t.id)).toEqual(['left', 'right'])
  })

  it('orders tabs independently inside each branch of the same repo', () => {
    // Two branches under the same remote, each containing tabs from
    // both tiles. The sort must apply per-branch — branch A's order
    // must not leak into branch B's.
    const tabs = [
      makeTab({ id: 'feat-B', tileId: 'tile-B', position: '0', gitOriginUrl: 'https://github.com/org/repo.git', gitBranch: 'feature' }),
      makeTab({ id: 'main-B', tileId: 'tile-B', position: '0', gitOriginUrl: 'https://github.com/org/repo.git', gitBranch: 'main' }),
      makeTab({ id: 'feat-A', tileId: 'tile-A', position: '0', gitOriginUrl: 'https://github.com/org/repo.git', gitBranch: 'feature' }),
      makeTab({ id: 'main-A', tileId: 'tile-A', position: '0', gitOriginUrl: 'https://github.com/org/repo.git', gitBranch: 'main' }),
    ]
    const tree = buildTree(tabs, ['tile-A', 'tile-B'])
    const branches = tree.groups[0].branches
    expect(branches.map(b => b.branchName)).toEqual(['feature', 'main'])
    expect(branches[0].tabs.map(t => t.id)).toEqual(['feat-A', 'feat-B'])
    expect(branches[1].tabs.map(t => t.id)).toEqual(['main-A', 'main-B'])
  })

  it('interleaves FILE tabs with AGENT / TERMINAL tabs by position within the same tile', () => {
    // No type-based bucketing anymore — the layout's `position` is the
    // single source of left-to-right order. A FILE tab opened between
    // two agent tabs must show up between them.
    const tabs = [
      makeTab({ id: 'agent-right', type: TabType.AGENT, tileId: 'tile-A', position: '2', gitOriginUrl: 'https://github.com/org/repo.git', gitBranch: 'main' }),
      makeTab({ id: 'file', type: TabType.FILE, tileId: 'tile-A', position: '1', gitOriginUrl: 'https://github.com/org/repo.git', gitBranch: 'main' }),
      makeTab({ id: 'agent-left', type: TabType.AGENT, tileId: 'tile-A', position: '0', gitOriginUrl: 'https://github.com/org/repo.git', gitBranch: 'main' }),
    ]
    const tree = buildTree(tabs, ['tile-A'])
    expect(tree.groups[0].branches[0].tabs.map(t => t.id))
      .toEqual(['agent-left', 'file', 'agent-right'])
  })

  it('tolerates an empty tileOrder for the active branch (legacy/test harness)', () => {
    // Empty tileOrder ⇒ every tile gets the same Infinity rank, so the
    // sort effectively becomes position-then-id. Real-world equivalent:
    // a workspace whose layout hasn't been hydrated yet but whose tabs
    // already arrived via the CRDT projection.
    const tabs = [
      makeTab({ id: 'b', tileId: 'tile-x', position: '1', gitOriginUrl: 'https://github.com/org/repo.git', gitBranch: 'main' }),
      makeTab({ id: 'a', tileId: 'tile-y', position: '0', gitOriginUrl: 'https://github.com/org/repo.git', gitBranch: 'main' }),
    ]
    const tree = buildTree(tabs, [])
    // 'a' has earlier position, comes first; 'b' second — tile-x vs
    // tile-y irrelevant under empty tileOrder.
    expect(tree.groups[0].branches[0].tabs.map(t => t.id)).toEqual(['a', 'b'])
  })

  it('sinks tabs with unknown tile ids to the end of their branch', () => {
    // A tab whose `tileId` isn't in `tileOrder` (layout/registry race,
    // or a never-assigned ghost) appears after every known-tile tab
    // but stays grouped with other unknown-tile tabs by position/id.
    const tabs = [
      makeTab({ id: 'ghost', tileId: 'tile-gone', position: '0|', gitOriginUrl: 'https://github.com/org/repo.git', gitBranch: 'main' }),
      makeTab({ id: 'real', tileId: 'tile-A', position: '0|', gitOriginUrl: 'https://github.com/org/repo.git', gitBranch: 'main' }),
    ]
    const tree = buildTree(tabs, ['tile-A'])
    expect(tree.groups[0].branches[0].tabs.map(t => t.id)).toEqual(['real', 'ghost'])
  })

  it('represents tabs without gitBranch as null branchName and renders "(no branch)" as displayLabel', () => {
    const tabs = [
      makeTab({ id: 'a1', gitOriginUrl: 'https://github.com/org/repo.git' }),
    ]
    const tree = buildTree(tabs)
    // Internal: null so it can't collide with a real branch literally
    // named "(no branch)".
    expect(tree.groups[0].branches[0].branchName).toBeNull()
    // User-visible: the fallback label.
    expect(tree.groups[0].branches[0].displayLabel).toBe('(no branch)')
  })

  it('keeps a real branch named "(no branch)" distinct from the null sentinel', () => {
    const tabs = [
      makeTab({
        id: 'a1',
        gitOriginUrl: 'https://github.com/org/repo.git',
        gitToplevel: '/r',
        workerId: 'w-1',
        gitBranch: '(no branch)',
      }),
      makeTab({
        id: 'a2',
        gitOriginUrl: 'https://github.com/org/repo.git',
        gitToplevel: '/r',
        workerId: 'w-1',
        // No gitBranch → null sentinel.
      }),
    ]
    const tree = buildTree(tabs)
    // Two distinct branch groups: one with the real string and one with null.
    const names = tree.groups[0].branches.map(b => b.branchName)
    expect(names).toContain('(no branch)')
    expect(names).toContain(null)
    expect(tree.groups[0].branches).toHaveLength(2)
  })

  it('handles mix of grouped and ungrouped tabs', () => {
    const tabs = [
      makeTab({ id: 'a1', gitOriginUrl: 'https://github.com/org/repo.git', gitBranch: 'main' }),
      makeTab({ id: 'a2' }),
    ]
    const tree = buildTree(tabs)
    expect(tree.groups).toHaveLength(1)
    expect(tree.ungrouped).toHaveLength(1)
  })

  it('uses first tab with diff stats for branch (no summing)', () => {
    const tabs = [
      makeTab({ id: 'a1', gitOriginUrl: 'https://github.com/org/repo.git', gitBranch: 'main', gitDiffAdded: 10, gitDiffDeleted: 3 }),
      makeTab({ id: 'a2', gitOriginUrl: 'https://github.com/org/repo.git', gitBranch: 'main', gitDiffAdded: 5, gitDiffDeleted: 2 }),
    ]
    const tree = buildTree(tabs)
    // All tabs on same branch share git state; use first non-zero tab's values
    expect(tree.groups[0].branches[0].diffAdded).toBe(10)
    expect(tree.groups[0].branches[0].diffDeleted).toBe(3)
  })

  it('sums branch diff stats into repo group', () => {
    const tabs = [
      makeTab({ id: 'a1', gitOriginUrl: 'https://github.com/org/repo.git', gitBranch: 'main', gitDiffAdded: 10, gitDiffDeleted: 3 }),
      makeTab({ id: 'a2', gitOriginUrl: 'https://github.com/org/repo.git', gitBranch: 'dev', gitDiffAdded: 5, gitDiffDeleted: 2 }),
    ]
    const tree = buildTree(tabs)
    expect(tree.groups[0].diffAdded).toBe(15)
    expect(tree.groups[0].diffDeleted).toBe(5)
  })

  it('defaults diff stats to zero when tabs have no counts', () => {
    const tabs = [
      makeTab({ id: 'a1', gitOriginUrl: 'https://github.com/org/repo.git', gitBranch: 'main' }),
    ]
    const tree = buildTree(tabs)
    expect(tree.groups[0].branches[0].diffAdded).toBe(0)
    expect(tree.groups[0].branches[0].diffDeleted).toBe(0)
  })

  it('keeps tabs with a branch but no origin or toplevel in ungrouped', () => {
    // Without a toplevel, we can't reliably disambiguate local repos, so
    // branch-only tabs fall through to the flat ungrouped bucket.
    const tabs = [
      makeTab({ id: 'a1', gitBranch: 'main' }),
      makeTab({ id: 'a2', gitBranch: 'main', type: TabType.TERMINAL }),
    ]
    const tree = buildTree(tabs)
    expect(tree.groups).toHaveLength(0)
    expect(tree.ungrouped).toHaveLength(2)
  })

  it('keeps tabs with neither origin nor branch in ungrouped', () => {
    const tabs = [makeTab({ id: 'a1' })]
    const tree = buildTree(tabs)
    expect(tree.groups).toHaveLength(0)
    expect(tree.ungrouped).toHaveLength(1)
  })

  it('separates distinct local repos by toplevel path', () => {
    // Two `git init` projects in the same workspace should show up as two
    // groups, each labelled by its toplevel's basename, not collapse into
    // a single "(local repo)" bucket.
    const tabs = [
      makeTab({ id: 't1', type: TabType.TERMINAL, gitBranch: 'main', gitToplevel: '/home/me/projects/alpha' }),
      makeTab({ id: 't2', type: TabType.TERMINAL, gitBranch: 'main', gitToplevel: '/home/me/projects/beta' }),
      makeTab({ id: 'a1', type: TabType.AGENT, gitBranch: 'feature', gitToplevel: '/home/me/projects/alpha' }),
    ]
    const tree = buildTree(tabs)
    expect(tree.groups).toHaveLength(2)
    expect(tree.groups.map(g => g.repoLabel)).toEqual(['alpha', 'beta'])
    expect(tree.groups[0].repoKey).toBe(repoKeyForLocal('/home/me/projects/alpha'))
    expect(tree.groups[1].repoKey).toBe(repoKeyForLocal('/home/me/projects/beta'))
    // alpha has both branches (main + feature); beta just main.
    expect(tree.groups[0].branches.map(b => b.branchName)).toEqual(['feature', 'main'])
    expect(tree.groups[1].branches.map(b => b.branchName)).toEqual(['main'])
  })

  it('orders remotes before per-toplevel locals', () => {
    // Mixed set: one remote and two distinct local-with-toplevel repos.
    // Remotes sort first (alphabetical), then locals (alphabetical by
    // toplevel basename).
    const tabs = [
      makeTab({ id: 'r1', gitOriginUrl: 'https://github.com/org/repo.git', gitBranch: 'main' }),
      makeTab({ id: 'l1', gitBranch: 'main', gitToplevel: '/home/me/zulu' }),
      makeTab({ id: 'l2', gitBranch: 'main', gitToplevel: '/home/me/alpha' }),
    ]
    const tree = buildTree(tabs)
    expect(tree.groups).toHaveLength(3)
    expect(tree.groups.map(g => g.repoLabel)).toEqual([
      'github.com/org/repo',
      'alpha',
      'zulu',
    ])
  })

  it('falls back to the toplevel path when basename would be empty', () => {
    // Edge case: a toplevel like "/" has no basename. We fall back to the
    // raw path rather than rendering an empty label.
    const tabs = [
      makeTab({ id: 't1', gitBranch: 'main', gitToplevel: '/' }),
    ]
    const tree = buildTree(tabs)
    expect(tree.groups).toHaveLength(1)
    expect(tree.groups[0].repoLabel).toBe('/')
  })

  it('keeps single-occurrence branch labels unsuffixed', () => {
    const tabs = [
      makeTab({ id: 'a', workerId: 'w1', gitOriginUrl: 'https://github.com/o/r.git', gitBranch: 'main', gitToplevel: '/repo' }),
    ]
    const tree = buildTree(tabs)
    expect(tree.groups[0].branches[0].displayLabel).toBe('main')
  })

  it('splits same-branch-different-worker into separate groups with worker-name suffix', () => {
    const lookup = (id: string) => ({
      name: id === 'w1' ? 'worker-a' : 'worker-b',
      os: 'linux',
      arch: 'arm64',
      homeDir: '/home/user',
      version: '0',
      commitHash: '',
      buildTime: '',
      updatedAt: 0,
    })
    const tabs = [
      makeTab({ id: 'a1', workerId: 'w1', gitOriginUrl: 'https://github.com/o/r.git', gitBranch: 'main', gitToplevel: '/repo-a' }),
      makeTab({ id: 'a2', workerId: 'w2', gitOriginUrl: 'https://github.com/o/r.git', gitBranch: 'main', gitToplevel: '/repo-b' }),
    ]
    const tree = buildTree(tabs, undefined, lookup)
    expect(tree.groups).toHaveLength(1)
    expect(tree.groups[0].branches).toHaveLength(2)
    const labels = tree.groups[0].branches.map(b => b.displayLabel).toSorted()
    expect(labels[0]).toMatch(/worker-a/)
    expect(labels[1]).toMatch(/worker-b/)
  })

  it('appends only the path when toplevel differs but worker is the same', () => {
    const lookup = (id: string) => ({
      name: id === 'w1' ? 'worker-a' : 'worker-b',
      os: 'linux',
      arch: 'arm64',
      homeDir: '/home/user',
      version: '0',
      commitHash: '',
      buildTime: '',
      updatedAt: 0,
    })
    const tabs = [
      makeTab({ id: 'a', workerId: 'w1', gitOriginUrl: 'https://github.com/o/r.git', gitBranch: 'main', gitToplevel: '/home/user/Workspaces/foo' }),
      makeTab({ id: 'b', workerId: 'w1', gitOriginUrl: 'https://github.com/o/r.git', gitBranch: 'main', gitToplevel: '/home/user/Workspaces/bar' }),
    ]
    const tree = buildTree(tabs, undefined, lookup)
    expect(tree.groups[0].branches).toHaveLength(2)
    const labels = tree.groups[0].branches.map(b => b.displayLabel)
    // No worker name (single worker), tildified path.
    expect(labels).toEqual(
      expect.arrayContaining([
        'main (~/Workspaces/bar)',
        'main (~/Workspaces/foo)',
      ]),
    )
    for (const label of labels)
      expect(label).not.toMatch(/worker-/)
  })

  it('appends both worker name and path when both dimensions vary', () => {
    const lookup = (id: string) => ({
      name: id === 'w1' ? 'worker-a' : 'worker-b',
      os: 'linux',
      arch: 'arm64',
      homeDir: id === 'w1' ? '/home/alice' : '/home/bob',
      version: '0',
      commitHash: '',
      buildTime: '',
      updatedAt: 0,
    })
    const tabs = [
      makeTab({ id: '1', workerId: 'w1', gitOriginUrl: 'https://github.com/o/r.git', gitBranch: 'main', gitToplevel: '/home/alice/Workspaces/foo' }),
      makeTab({ id: '2', workerId: 'w1', gitOriginUrl: 'https://github.com/o/r.git', gitBranch: 'main', gitToplevel: '/home/alice/Workspaces/bar' }),
      makeTab({ id: '3', workerId: 'w2', gitOriginUrl: 'https://github.com/o/r.git', gitBranch: 'main', gitToplevel: '/home/bob/Workspaces/foo' }),
    ]
    const tree = buildTree(tabs, undefined, lookup)
    const labels = tree.groups[0].branches.map(b => b.displayLabel)
    for (const label of labels) {
      expect(label).toMatch(/worker-[ab]/)
      expect(label).toMatch(/~\/Workspaces\//)
    }
  })

  it('tildifies paths with the worker\'s OS flavor (win32 home + backslashes)', () => {
    // Auto-detect would land on POSIX for a Windows path because the
    // first char isn't `\\` and there's no drive letter on the home
    // segment we strip — so without flavorFromOs(os), `C:\Users\u\foo`
    // would not tildify against `C:\Users\u`. Verify the win32 branch
    // emits a `~`-prefixed label and keeps the native separator.
    const lookup = () => ({
      name: 'w-win',
      os: 'windows',
      arch: 'x86_64',
      homeDir: 'C:\\Users\\u',
      version: '0',
      commitHash: '',
      buildTime: '',
      updatedAt: 0,
    })
    const tabs = [
      makeTab({ id: '1', workerId: 'w1', gitOriginUrl: 'https://github.com/o/r.git', gitBranch: 'main', gitToplevel: 'C:\\Users\\u\\Workspaces\\foo' }),
      makeTab({ id: '2', workerId: 'w1', gitOriginUrl: 'https://github.com/o/r.git', gitBranch: 'main', gitToplevel: 'C:\\Users\\u\\Workspaces\\bar' }),
    ]
    const tree = buildTree(tabs, undefined, lookup)
    const labels = tree.groups[0].branches.map(b => b.displayLabel)
    expect(labels).toEqual(
      expect.arrayContaining([
        'main (~\\Workspaces\\foo)',
        'main (~\\Workspaces\\bar)',
      ]),
    )
  })

  it('falls back to workerId and absolute path when no lookup is provided', () => {
    const tabs = [
      makeTab({ id: '1', workerId: 'w1', gitOriginUrl: 'https://github.com/o/r.git', gitBranch: 'main', gitToplevel: '/home/u/a' }),
      makeTab({ id: '2', workerId: 'w2', gitOriginUrl: 'https://github.com/o/r.git', gitBranch: 'main', gitToplevel: '/home/u/b' }),
    ]
    const tree = buildTree(tabs)
    const labels = tree.groups[0].branches.map(b => b.displayLabel)
    // Both worker AND path vary across the two groups → both appear.
    expect(labels.every(l => l.includes('w1') || l.includes('w2'))).toBe(true)
    expect(labels.every(l => l.includes('/home/u/'))).toBe(true)
  })
})

describe('buildTree branchByKey', () => {
  // The per-RepoGroup branchByKey Map is built once during buildTree
  // (vs. the previous per-row inner memo that rebuilt one Map per
  // <For> row per reactive tick). Pin the contract so a regression
  // that drops the Map sends every render-time `branch().branchByKey.get(...)`
  // lookup back to `branches.find(...)`.

  it('exposes a branchByKey Map keyed by composite branch key for each repo group', () => {
    const tabs = [
      makeTab({ id: 'a1', workerId: 'w1', gitOriginUrl: 'https://github.com/o/r.git', gitBranch: 'main', gitToplevel: '/x' }),
      makeTab({ id: 'a2', workerId: 'w1', gitOriginUrl: 'https://github.com/o/r.git', gitBranch: 'dev', gitToplevel: '/x' }),
    ]
    const tree = buildTree(tabs)
    const group = tree.groups[0]
    expect(group.branchByKey).toBeInstanceOf(Map)
    // The Map's size matches the branch count, and every key resolves
    // to the same object reference as the corresponding branches[] entry.
    expect(group.branchByKey.size).toBe(group.branches.length)
    for (const b of group.branches) {
      const key = `${b.branchName}\x00${b.workerId}\x00${b.gitToplevel}`
      expect(group.branchByKey.get(key)).toBe(b)
    }
  })

  it('separates same-named branches by workerId in the lookup map', () => {
    // Two clones of the same repo on the same branch must land in
    // separate buckets — the composite key includes workerId, so the
    // Map must too. A regression that keys by branchName alone would
    // collapse them.
    const tabs = [
      makeTab({ id: 'a1', workerId: 'w1', gitOriginUrl: 'https://github.com/o/r.git', gitBranch: 'main', gitToplevel: '/h1' }),
      makeTab({ id: 'a2', workerId: 'w2', gitOriginUrl: 'https://github.com/o/r.git', gitBranch: 'main', gitToplevel: '/h2' }),
    ]
    const tree = buildTree(tabs)
    const group = tree.groups[0]
    expect(group.branches).toHaveLength(2)
    expect(group.branchByKey.size).toBe(2)
    // Both entries are independently addressable; identity match.
    for (const b of group.branches) {
      const key = `${b.branchName}\x00${b.workerId}\x00${b.gitToplevel}`
      expect(group.branchByKey.get(key)).toBe(b)
    }
  })

  it('represents the "(no branch)" bucket distinctly in the lookup map', () => {
    // Tabs without gitBranch land in the (null) bucket. The composite
    // key uses a sentinel so it cannot collide with a real branch
    // literally named "(no branch)" — pin that the Map preserves the
    // distinction.
    const tabs = [
      makeTab({ id: 'a1', workerId: 'w1', gitOriginUrl: 'https://github.com/o/r.git', gitBranch: '', gitToplevel: '/x' }),
      makeTab({ id: 'a2', workerId: 'w1', gitOriginUrl: 'https://github.com/o/r.git', gitBranch: '(no branch)', gitToplevel: '/x' }),
    ]
    const tree = buildTree(tabs)
    const group = tree.groups[0]
    // Two distinct buckets — one with branchName=null, one with the literal string.
    expect(group.branches).toHaveLength(2)
    expect(group.branchByKey.size).toBe(2)
    const nullEntry = group.branches.find(b => b.branchName === null)
    const literalEntry = group.branches.find(b => b.branchName === '(no branch)')
    expect(nullEntry).toBeDefined()
    expect(literalEntry).toBeDefined()
    expect(nullEntry).not.toBe(literalEntry)
  })
})

describe('tabLeaf draggable ID format', () => {
  // TabLeaf creates draggable IDs with the format:
  //   `${SIDEBAR_TAB_PREFIX}${workspaceId}:${tabType}:${tabId}`
  // TabDragContext parses these IDs to extract workspaceId and tabKey.
  // These tests verify the encoding/parsing roundtrip is correct.

  function encodeDraggableId(workspaceId: string, tabType: TabType, tabId: string): string {
    return `${SIDEBAR_TAB_PREFIX}${workspaceId}:${tabType}:${tabId}`
  }

  function parseDraggableId(id: string): { workspaceId: string, tabKey: string } | null {
    if (!id.startsWith(SIDEBAR_TAB_PREFIX))
      return null
    const rest = id.slice(SIDEBAR_TAB_PREFIX.length)
    const colonIdx = rest.indexOf(':')
    if (colonIdx < 0)
      return null
    return {
      workspaceId: rest.slice(0, colonIdx),
      tabKey: rest.slice(colonIdx + 1),
    }
  }

  it('roundtrips agent tab ID', () => {
    const id = encodeDraggableId('ws-abc', TabType.AGENT, 'agent-123')
    const parsed = parseDraggableId(id)
    expect(parsed).not.toBeNull()
    expect(parsed!.workspaceId).toBe('ws-abc')
    expect(parsed!.tabKey).toBe(`${TabType.AGENT}:agent-123`)
  })

  it('roundtrips terminal tab ID', () => {
    const id = encodeDraggableId('ws-xyz', TabType.TERMINAL, 'term-456')
    const parsed = parseDraggableId(id)
    expect(parsed).not.toBeNull()
    expect(parsed!.workspaceId).toBe('ws-xyz')
    expect(parsed!.tabKey).toBe(`${TabType.TERMINAL}:term-456`)
  })

  it('handles workspace ID with hyphens and UUIDs', () => {
    const wsId = '550e8400-e29b-41d4-a716-446655440000'
    const id = encodeDraggableId(wsId, TabType.AGENT, 'a1')
    const parsed = parseDraggableId(id)
    expect(parsed!.workspaceId).toBe(wsId)
  })

  it('returns null for non-sidebar-tab IDs', () => {
    expect(parseDraggableId('1:agent-1')).toBeNull()
    expect(parseDraggableId('ws-drop:ws-1')).toBeNull()
    expect(parseDraggableId('')).toBeNull()
  })

  it('returns null for malformed sidebar-tab ID without colon', () => {
    expect(parseDraggableId(`${SIDEBAR_TAB_PREFIX}nocolon`)).toBeNull()
  })
})

describe('sumDiffStatsFromTabs', () => {
  it('returns all-zero on empty input', () => {
    expect(sumDiffStatsFromTabs([])).toEqual({ added: 0, deleted: 0, untracked: 0 })
  })

  it('skips tabs without origin URL or toplevel (ungrouped bucket)', () => {
    const tabs = [
      makeTab({ id: 'a1', gitDiffAdded: 5, gitDiffDeleted: 2, gitDiffUntracked: 1 }),
    ]
    expect(sumDiffStatsFromTabs(tabs)).toEqual({ added: 0, deleted: 0, untracked: 0 })
  })

  it('counts a branch group once even when many tabs share its git state', () => {
    // buildTree picks the "first tab with non-zero stats" per branch
    // group because every tab in the group shares the same git state.
    // sumDiffStatsFromTabs must apply the same dedup or it would
    // over-count an N-tab branch group N times.
    const shared = {
      gitOriginUrl: 'https://github.com/org/repo.git',
      gitBranch: 'main',
      workerId: 'w-1',
      gitToplevel: '/repos/repo',
      gitDiffAdded: 10,
      gitDiffDeleted: 3,
      gitDiffUntracked: 2,
    }
    const tabs = [
      makeTab({ id: 'a1', ...shared }),
      makeTab({ id: 'a2', ...shared }),
      makeTab({ id: 'a3', ...shared, type: TabType.TERMINAL }),
    ]
    expect(sumDiffStatsFromTabs(tabs)).toEqual({ added: 10, deleted: 3, untracked: 2 })
  })

  it('sums distinct branch groups across the same repo', () => {
    const tabs = [
      makeTab({
        id: 'a1',
        gitOriginUrl: 'https://github.com/org/repo.git',
        gitBranch: 'main',
        workerId: 'w-1',
        gitToplevel: '/repos/repo',
        gitDiffAdded: 5,
        gitDiffDeleted: 1,
        gitDiffUntracked: 0,
      }),
      makeTab({
        id: 'a2',
        gitOriginUrl: 'https://github.com/org/repo.git',
        gitBranch: 'feature',
        workerId: 'w-1',
        gitToplevel: '/repos/repo',
        gitDiffAdded: 2,
        gitDiffDeleted: 0,
        gitDiffUntracked: 3,
      }),
    ]
    expect(sumDiffStatsFromTabs(tabs)).toEqual({ added: 7, deleted: 1, untracked: 3 })
  })

  it('treats different workers / toplevels on the same branch as separate groups', () => {
    const tabs = [
      makeTab({
        id: 'a1',
        gitOriginUrl: 'https://github.com/org/repo.git',
        gitBranch: 'main',
        workerId: 'w-1',
        gitToplevel: '/clone-a',
        gitDiffAdded: 4,
      }),
      makeTab({
        id: 'a2',
        gitOriginUrl: 'https://github.com/org/repo.git',
        gitBranch: 'main',
        workerId: 'w-1',
        gitToplevel: '/clone-b',
        gitDiffAdded: 6,
      }),
      makeTab({
        id: 'a3',
        gitOriginUrl: 'https://github.com/org/repo.git',
        gitBranch: 'main',
        workerId: 'w-2',
        gitToplevel: '/clone-a',
        gitDiffAdded: 1,
      }),
    ]
    expect(sumDiffStatsFromTabs(tabs)).toEqual({ added: 11, deleted: 0, untracked: 0 })
  })

  it('groups local repos by toplevel when origin URL is absent', () => {
    const tabs = [
      makeTab({
        id: 'a1',
        gitToplevel: '/local/repo',
        gitBranch: 'main',
        workerId: 'w-1',
        gitDiffAdded: 3,
        gitDiffDeleted: 4,
      }),
      makeTab({
        id: 'a2',
        gitToplevel: '/local/repo',
        gitBranch: 'main',
        workerId: 'w-1',
        gitDiffAdded: 3,
        gitDiffDeleted: 4,
      }),
    ]
    expect(sumDiffStatsFromTabs(tabs)).toEqual({ added: 3, deleted: 4, untracked: 0 })
  })

  it('ignores all-zero tabs even when they belong to a group with stats', () => {
    // The first non-zero tab in a group is the one that contributes.
    // An all-zero tab in the same group must not knock the group out
    // of the seen-set before a real non-zero tab arrives.
    const groupKey = {
      gitOriginUrl: 'https://github.com/org/repo.git',
      gitBranch: 'main',
      workerId: 'w-1',
      gitToplevel: '/repos/repo',
    }
    const tabs = [
      makeTab({ id: 'a1', ...groupKey, gitDiffAdded: 0, gitDiffDeleted: 0, gitDiffUntracked: 0 }),
      makeTab({ id: 'a2', ...groupKey, gitDiffAdded: 7, gitDiffDeleted: 2, gitDiffUntracked: 1 }),
    ]
    expect(sumDiffStatsFromTabs(tabs)).toEqual({ added: 7, deleted: 2, untracked: 1 })
  })

  it('produces the same sum as the buildTree-based projection for a mixed workspace', () => {
    // Equivalence check: the old call site computed
    //   tree.groups.reduce((s, g) => s + g.diff*, 0)
    // for added/deleted/untracked. The new helper must produce the
    // same triple on any input shape; this is the regression guard
    // against the helper drifting from buildTree semantics.
    const tabs = [
      makeTab({
        id: 'a1',
        gitOriginUrl: 'https://github.com/org/repo.git',
        gitBranch: 'main',
        workerId: 'w-1',
        gitToplevel: '/repos/repo',
        gitDiffAdded: 5,
        gitDiffDeleted: 1,
        gitDiffUntracked: 2,
      }),
      makeTab({
        id: 'a2',
        gitOriginUrl: 'https://github.com/org/repo.git',
        gitBranch: 'feature',
        workerId: 'w-1',
        gitToplevel: '/repos/repo',
        gitDiffAdded: 3,
        gitDiffDeleted: 0,
        gitDiffUntracked: 0,
      }),
      makeTab({
        id: 'a3',
        gitToplevel: '/local/other',
        gitBranch: 'main',
        workerId: 'w-1',
        gitDiffAdded: 1,
        gitDiffDeleted: 1,
        gitDiffUntracked: 1,
      }),
      makeTab({ id: 'a4' }), // ungrouped, must be skipped
    ]
    const tree = buildTree(tabs)
    const expected = {
      added: tree.groups.reduce((s, g) => s + g.diffAdded, 0),
      deleted: tree.groups.reduce((s, g) => s + g.diffDeleted, 0),
      untracked: tree.groups.reduce((s, g) => s + g.diffUntracked, 0),
    }
    expect(sumDiffStatsFromTabs(tabs)).toEqual(expected)
  })

  // Regression guard: both call sites (sumDiffStatsFromTabs and
  // buildTree) compute the same composite branch key for the same tab.
  // A drift here would cause the per-branch counts surfaced in the
  // sidebar header to disagree with the aggregate the workspace card
  // shows — easy to introduce by tweaking the "(no branch)" fallback
  // in one site and forgetting the other.
  it('aggregates the same set of branch keys that buildTree does for "(no branch)" tabs', () => {
    const tabs = [
      // gitBranch omitted on both: must collapse onto one key ("(no branch)" +
      // same worker + same toplevel), counted once.
      makeTab({
        id: 'a1',
        gitOriginUrl: 'https://github.com/org/repo.git',
        gitToplevel: '/r',
        workerId: 'w-1',
        gitDiffAdded: 4,
        gitDiffDeleted: 0,
        gitDiffUntracked: 0,
      }),
      makeTab({
        id: 'a2',
        gitOriginUrl: 'https://github.com/org/repo.git',
        gitToplevel: '/r',
        workerId: 'w-1',
        gitDiffAdded: 9,
        gitDiffDeleted: 0,
        gitDiffUntracked: 0,
      }),
      // Different toplevel ⇒ different bucket ⇒ added separately.
      makeTab({
        id: 'a3',
        gitOriginUrl: 'https://github.com/org/repo.git',
        gitToplevel: '/r2',
        workerId: 'w-1',
        gitDiffAdded: 2,
        gitDiffDeleted: 0,
        gitDiffUntracked: 0,
      }),
    ]
    const tree = buildTree(tabs)
    // buildTree represents the missing-branch case as null internally;
    // the display layer renders it as "(no branch)".
    const branchNames = tree.groups.flatMap(g => g.branches.map(b => b.branchName))
    expect(branchNames).toContain(null)
    const displayLabels = tree.groups.flatMap(g => g.branches.map(b => b.displayLabel))
    // disambiguated with "(~/r)" / "(~/r2)" because two toplevels collide
    expect(displayLabels.some(l => l.startsWith('(no branch)'))).toBe(true)
    // Same key contract → sumDiffStatsFromTabs collapses the two same-bucket
    // tabs onto one count and adds the distinct-toplevel tab separately.
    expect(sumDiffStatsFromTabs(tabs).added).toBe(
      tree.groups.reduce((s, g) => s + g.diffAdded, 0),
    )
  })
})

describe('tabBuildKey', () => {
  // tabBuildKey backs WorkspaceTabTree's tabsProjection memo. The
  // contract: every field buildTree consumes must influence the
  // fingerprint, and unrelated fields (title, runtime status, etc.)
  // must NOT — otherwise a WatchEvents push that flips an unrelated
  // tab field reruns buildTree on every keystroke.

  it('is stable across identical inputs', () => {
    const t = makeTab({
      id: 'a1',
      workerId: 'w',
      gitBranch: 'main',
      gitToplevel: '/r',
      gitOriginUrl: 'g',
      gitDiffAdded: 1,
      gitDiffDeleted: 2,
      gitDiffUntracked: 3,
      tileId: 'tile',
      position: '1',
    })
    expect(tabBuildKey(t)).toBe(tabBuildKey({ ...t }))
  })

  it('changes when any tracked field changes', () => {
    const base = makeTab({
      id: 'a1',
      workerId: 'w',
      gitBranch: 'main',
      gitToplevel: '/r',
      gitOriginUrl: 'g',
      gitDiffAdded: 1,
      gitDiffDeleted: 2,
      gitDiffUntracked: 3,
      tileId: 'tile',
      position: '1',
    })
    const baseKey = tabBuildKey(base)
    const tracked: Array<Partial<Tab>> = [
      { workerId: 'w2' },
      { gitBranch: 'dev' },
      { gitToplevel: '/r2' },
      { gitOriginUrl: 'g2' },
      { gitDiffAdded: 99 },
      { gitDiffDeleted: 99 },
      { gitDiffUntracked: 99 },
      { tileId: 'tile2' },
      { position: '2' },
    ]
    for (const override of tracked)
      expect(tabBuildKey({ ...base, ...override })).not.toBe(baseKey)
  })

  it('ignores fields buildTree does not read', () => {
    const base = makeTab({ id: 'a1', gitBranch: 'main' })
    const baseKey = tabBuildKey(base)
    // title is rendered but never reaches buildTree's grouping logic,
    // so toggling it must not bust the projection's dedup.
    expect(tabBuildKey({ ...base, title: 'renamed' })).toBe(baseKey)
  })

  it('disambiguates empty-vs-zero in adjacent fields', () => {
    // The `|` delimiter has to survive on each side; a naive
    // string concat (e.g. join('')) would let an empty branch with
    // numeric diff stats collide with a different shaped tab.
    const a = makeTab({ id: 'a', gitBranch: '', gitDiffAdded: 12 })
    const b = makeTab({ id: 'a', gitBranch: '1', gitDiffAdded: 2 })
    expect(tabBuildKey(a)).not.toBe(tabBuildKey(b))
  })

  it('treats missing optional fields as empty', () => {
    // Tabs come from the registry with optional fields undefined when
    // git info hasn't landed. The key must still be stable so the
    // projection doesn't churn between undefined and "".
    const tab1 = makeTab({ id: 'a1' })
    const tab2 = makeTab({ id: 'a1', workerId: undefined, gitBranch: undefined })
    expect(tabBuildKey(tab1)).toBe(tabBuildKey(tab2))
  })
})
