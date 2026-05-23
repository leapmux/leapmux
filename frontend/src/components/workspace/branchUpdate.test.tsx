/// <reference types="vitest/globals" />
/* eslint-disable solid/reactivity -- tests intentionally read memo values outside JSX */
import type { Tab } from '~/stores/tab.types'
import { createMemo, createRoot } from 'solid-js'
import { describe, expect, it } from 'vitest'
import { TabType } from '~/generated/leapmux/v1/workspace_pb'
import { createTabStore } from '~/stores/tab.store'
import { buildTree } from './WorkspaceTabTree'

// Pinpoint test for "after Change branch, the sidebar should reflect
// the new branch name". Routes through `stampBranchOnTabs` — the same
// helper AppShell.tsx's onBranchChanged handler calls — so any drift in
// the (workerId, gitToplevel) predicate / stale-write guard is caught
// here alongside the reactive buildTree round-trip.
describe('branchUpdate (change branch → sidebar reflects new label)', () => {
  function makeAgentTab(id: string, overrides: Partial<Tab> = {}): Tab {
    return {
      type: TabType.AGENT,
      id,
      title: id,
      tileId: 'tile-1',
      position: '0',
      workerId: 'w1',
      workingDir: '/repo',
      gitToplevel: '/repo',
      gitOriginUrl: 'https://github.com/o/r.git',
      gitBranch: 'A',
      ...overrides,
    } as Tab
  }

  it('stampBranchOnTabs restamps gitBranch on every tab in the (workerId, gitToplevel) group', () => {
    const ts = createTabStore()
    ts.addTab(makeAgentTab('a1'))
    ts.addTab(makeAgentTab('a2'))
    // A tab in a different group that must NOT be updated.
    ts.addTab(makeAgentTab('other', { workerId: 'w2', gitToplevel: '/other' }))

    ts.stampBranchOnTabs('w1', '/repo', 'B')

    expect(ts.getAgentTab('a1')?.gitBranch).toBe('B')
    expect(ts.getAgentTab('a2')?.gitBranch).toBe('B')
    expect(ts.getAgentTab('other')?.gitBranch).toBe('A')
  })

  it('buildTree re-runs reactively when a tab\'s gitBranch changes via stampBranchOnTabs', () => {
    createRoot((dispose) => {
      const ts = createTabStore()
      ts.addTab(makeAgentTab('a1'))
      ts.addTab(makeAgentTab('a2'))

      const tree = createMemo(() => buildTree(ts.state.tabs))

      // Initial: one group, branch label "A".
      expect(tree().groups).toHaveLength(1)
      expect(tree().groups[0].branches).toHaveLength(1)
      expect(tree().groups[0].branches[0].branchName).toBe('A')
      expect(tree().groups[0].branches[0].tabs.map(t => t.id).toSorted()).toEqual(['a1', 'a2'])

      // Switch both tabs to branch "B" the same way AppShell does.
      ts.stampBranchOnTabs('w1', '/repo', 'B')

      // The memo must have re-run and produced a single group with the
      // new branch label.
      expect(tree().groups).toHaveLength(1)
      expect(tree().groups[0].branches).toHaveLength(1)
      expect(tree().groups[0].branches[0].branchName).toBe('B')
      expect(tree().groups[0].branches[0].tabs.map(t => t.id).toSorted()).toEqual(['a1', 'a2'])

      dispose()
    })
  })

  it('rejects a stamp with an empty workingDir to avoid cross-repo leak', () => {
    // Regression guard: stampBranchOnTabs used to treat workingDir='' as
    // a wildcard via isSameRepo's `(t.gitToplevel ?? '') === ''`, so a
    // ChangeBranch on one unstamped repo silently re-labeled tabs in a
    // SIBLING unstamped repo on the same worker. The empty-workingDir
    // path is now a no-op; callers must resolve a real repo path first.
    createRoot((dispose) => {
      const ts = createTabStore()
      // Two tabs from DIFFERENT repos but neither has had its
      // gitToplevel stamped yet — pre-fix, a stamp on one would have
      // bled the new branch name onto the other.
      ts.addTab(makeAgentTab('a1', { gitToplevel: undefined, gitOriginUrl: 'https://github.com/o/r1.git' }))
      ts.addTab(makeAgentTab('a2', { gitToplevel: undefined, gitOriginUrl: 'https://github.com/o/r2.git' }))

      const wrote = ts.stampBranchOnTabs('w1', '', 'B')
      expect(wrote).toBe(false)
      expect(ts.getAgentTab('a1')?.gitBranch).toBe('A')
      expect(ts.getAgentTab('a2')?.gitBranch).toBe('A')
      dispose()
    })
  })
})
