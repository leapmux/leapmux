/// <reference types="vitest/globals" />
/* eslint-disable solid/reactivity -- tests intentionally read memo values outside JSX */
import type { TabStampTarget } from '~/components/shell/syncGitStatusToTabs'
import type { Tab } from '~/stores/tab.types'
import { createMemo, createRoot } from 'solid-js'
import { createStore, produce } from 'solid-js/store'
import { describe, expect, it } from 'vitest'
import { stampBranchOnTabs } from '~/components/shell/stampBranchOnTabs'
import { TabType } from '~/generated/leapmux/v1/workspace_pb'
import { tabKey } from '~/stores/tab.helpers'
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
      workspaceId: 'ws-1',
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

  /**
   * A reactive {@link TabStampTarget} over a plain tab list.
   *
   * In production the target is `tabStampTarget(tabView, tabMetadata)`, which
   * reads placement from the CRDT projection and writes into the metadata
   * store. Neither half is what these tests are about: the questions here are
   * which tabs the predicate selects and whether a write propagates to
   * `buildTree`. A store-backed list answers both without a bridge.
   */
  function target(initial: Tab[]) {
    const [state, setState] = createStore<{ tabs: Tab[] }>({ tabs: initial })
    const stamp: TabStampTarget = {
      get tabs() {
        return state.tabs
      },
      update: (tabIds, fields) => {
        setState(produce((s) => {
          for (const t of s.tabs) {
            if (tabIds.has(t.id))
              Object.assign(t, fields)
          }
        }))
      },
    }
    return { state, stamp }
  }

  function branchOf(state: { tabs: Tab[] }, id: string) {
    return state.tabs.find(t => t.id === id)?.gitBranch
  }

  it('restamps gitBranch on every tab in the (workerId, gitToplevel) group', () => {
    const { state, stamp } = target([
      makeAgentTab('a1'),
      makeAgentTab('a2'),
      // A tab in a different group that must NOT be updated.
      makeAgentTab('other', { workerId: 'w2', gitToplevel: '/other' }),
    ])

    expect(stampBranchOnTabs(stamp, 'w1', '/repo', 'B')).toBe(true)

    expect(branchOf(state, 'a1')).toBe('B')
    expect(branchOf(state, 'a2')).toBe('B')
    expect(branchOf(state, 'other')).toBe('A')
  })

  it('reports false and writes nothing when every tab already holds the branch', () => {
    // The `t.gitBranch !== newBranch` half of the predicate. Without it every
    // no-op stamp would still call `update`, churning the metadata store and
    // invalidating the sidebar's memos on each poll.
    const { state, stamp } = target([makeAgentTab('a1'), makeAgentTab('a2')])
    let updates = 0
    const counting: TabStampTarget = {
      get tabs() {
        return stamp.tabs
      },
      update: (p, f) => {
        updates++
        stamp.update(p, f)
      },
    }

    expect(stampBranchOnTabs(counting, 'w1', '/repo', 'A')).toBe(false)
    expect(updates).toBe(0)
    expect(branchOf(state, 'a1')).toBe('A')
  })

  it('buildTree re-runs reactively when a tab\'s gitBranch changes', () => {
    createRoot((dispose) => {
      const { state, stamp } = target([makeAgentTab('a1'), makeAgentTab('a2')])

      const tree = createMemo(() => buildTree(state.tabs))

      // Initial: one group, branch label "A".
      expect(tree().groups).toHaveLength(1)
      expect(tree().groups[0].branches).toHaveLength(1)
      expect(tree().groups[0].branches[0].branchName).toBe('A')
      expect(tree().groups[0].branches[0].tabs.map(t => t.id).toSorted()).toEqual(['a1', 'a2'])

      // Switch both tabs to branch "B" the same way AppShell does.
      stampBranchOnTabs(stamp, 'w1', '/repo', 'B')

      // The memo must have re-run and produced a single group with the
      // new branch label.
      expect(tree().groups).toHaveLength(1)
      expect(tree().groups[0].branches).toHaveLength(1)
      expect(tree().groups[0].branches[0].branchName).toBe('B')
      expect(tree().groups[0].branches[0].tabs.map(t => t.id).toSorted()).toEqual(['a1', 'a2'])

      dispose()
    })
  })

  it('rejects a stamp with an empty repo path to avoid a cross-repo leak', () => {
    // Regression guard: `isSameRepo` used to treat an empty repoToplevel as a
    // wildcard via `(t.gitToplevel ?? '') === ''`, so a ChangeBranch on one
    // unstamped repo silently re-labeled tabs in a SIBLING unstamped repo on
    // the same worker. The stamp now spans EVERY workspace rather than just
    // the active one, so that leak would reach the whole account.
    const { state, stamp } = target([
      // Two tabs from DIFFERENT repos, neither with its gitToplevel stamped.
      makeAgentTab('a1', { gitToplevel: undefined, gitOriginUrl: 'https://github.com/o/r1.git' }),
      makeAgentTab('a2', { gitToplevel: undefined, gitOriginUrl: 'https://github.com/o/r2.git' }),
    ])

    expect(stampBranchOnTabs(stamp, 'w1', '', 'B')).toBe(false)
    expect(branchOf(state, 'a1')).toBe('A')
    expect(branchOf(state, 'a2')).toBe('A')
  })

  // The symmetric half of the empty-toplevel guard. `isSameRepo` compares
  // `(tab.workerId ?? '') === workerId`, so an empty workerId matches every tab
  // whose own worker has not resolved yet -- and the stamp is account-wide.
  it('refuses to stamp when the worker is unresolved', () => {
    const { state, stamp } = target([
      makeAgentTab('a1', { workerId: undefined, gitToplevel: '/repo' }),
      makeAgentTab('a2', { workerId: 'w1', gitToplevel: '/repo' }),
    ])

    expect(stampBranchOnTabs(stamp, '', '/repo', 'B')).toBe(false)
    expect(branchOf(state, 'a1')).toBe('A')
    expect(branchOf(state, 'a2')).toBe('A')
  })

  it('stamps a tab in each of two different workspaces in one call', () => {
    // The reach that replaced the registry fan-out: one repo's tabs can live
    // in several workspaces, and the dialog may be opened from any of them.
    const { state, stamp } = target([
      makeAgentTab('a1', { workspaceId: 'ws-1', tileId: 'tile-1' }),
      makeAgentTab('a2', { workspaceId: 'ws-2', tileId: 'tile-2' }),
    ])

    stampBranchOnTabs(stamp, 'w1', '/repo', 'B')

    expect(branchOf(state, 'a1')).toBe('B')
    expect(branchOf(state, 'a2')).toBe('B')
  })

  it('matches by tab key, not by array index', () => {
    // `update` receives a predicate rather than a list, and the two passes
    // (filter, then update) each walk `target.tabs` independently. Keying on
    // identity keeps them aligned even though the collection is live.
    const { state, stamp } = target([
      makeAgentTab('a1'),
      makeAgentTab('skip', { gitToplevel: '/elsewhere' }),
      makeAgentTab('a2'),
    ])

    stampBranchOnTabs(stamp, 'w1', '/repo', 'B')

    expect(state.tabs.map(t => [tabKey(t), t.gitBranch])).toEqual([
      [tabKey(state.tabs[0]), 'B'],
      [tabKey(state.tabs[1]), 'A'],
      [tabKey(state.tabs[2]), 'B'],
    ])
  })
})
