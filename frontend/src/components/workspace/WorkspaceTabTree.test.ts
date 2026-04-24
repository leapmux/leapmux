import type { Tab } from '~/stores/tab.store'
import { describe, expect, it } from 'vitest'
import { SIDEBAR_TAB_PREFIX } from '~/components/shell/TabDragContext'
import { TabType } from '~/generated/leapmux/v1/workspace_pb'
import { buildTree, formatGitOriginUrl, LOCAL_REPO_KEY_PREFIX } from './WorkspaceTabTree'

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

  it('sorts agents before terminals within a branch', () => {
    const tabs = [
      makeTab({ id: 't1', type: TabType.TERMINAL, gitOriginUrl: 'https://github.com/org/repo.git', gitBranch: 'main' }),
      makeTab({ id: 'a1', type: TabType.AGENT, gitOriginUrl: 'https://github.com/org/repo.git', gitBranch: 'main' }),
    ]
    const tree = buildTree(tabs)
    expect(tree.groups[0].branches[0].tabs[0].type).toBe(TabType.AGENT)
    expect(tree.groups[0].branches[0].tabs[1].type).toBe(TabType.TERMINAL)
  })

  it('uses "(no branch)" for tabs without gitBranch', () => {
    const tabs = [
      makeTab({ id: 'a1', gitOriginUrl: 'https://github.com/org/repo.git' }),
    ]
    const tree = buildTree(tabs)
    expect(tree.groups[0].branches[0].branchName).toBe('(no branch)')
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
    expect(tree.groups[0].repoKey).toBe(`${LOCAL_REPO_KEY_PREFIX}/home/me/projects/alpha`)
    expect(tree.groups[1].repoKey).toBe(`${LOCAL_REPO_KEY_PREFIX}/home/me/projects/beta`)
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
