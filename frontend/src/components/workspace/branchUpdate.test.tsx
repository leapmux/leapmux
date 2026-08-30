/// <reference types="vitest/globals" />
/* eslint-disable solid/reactivity -- tests intentionally read memo values outside JSX */
import type { Tab } from '~/stores/tab.types'
import { createMemo, createRoot } from 'solid-js'
import { describe, expect, it } from 'vitest'
import { stampBranchOnRepo } from '~/components/shell/stampBranchOnTabs'
import { TabType } from '~/generated/proto/leapmux/v1/workspace_pb'
import { repoGitView, repoKey } from '~/stores/repoGit'
import { createRepoGitStore } from '~/stores/repoGit.store'
import { buildTree } from './WorkspaceTabTree'

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
      ...overrides,
    } as Tab
  }

  function seedBranch(
    store: ReturnType<typeof createRepoGitStore>,
    workerId: string,
    gitToplevel: string,
    branch: string,
    originUrl = 'https://github.com/o/r.git',
  ) {
    store.upsert(repoKey(workerId, gitToplevel), {
      workerId,
      toplevel: gitToplevel,
      branch,
      originUrl,
    })
  }

  function branchLabel(
    store: ReturnType<typeof createRepoGitStore>,
    tab: Tab,
  ) {
    return repoGitView(tab, store).branchLabel
  }

  it('restamps the branch in the repo-keyed store for the repo group', () => {
    const store = createRepoGitStore()
    const tabs = [
      makeAgentTab('a1'),
      makeAgentTab('a2'),
      makeAgentTab('other', { workerId: 'w2', gitToplevel: '/other' }),
    ]
    seedBranch(store, 'w1', '/repo', 'A')
    seedBranch(store, 'w2', '/other', 'A')

    expect(stampBranchOnRepo(store, { workerId: 'w1', gitToplevel: '/repo' }, 'B')).toBe(true)

    expect(branchLabel(store, tabs[0])).toBe('B')
    expect(branchLabel(store, tabs[1])).toBe('B')
    expect(branchLabel(store, tabs[2])).toBe('A')
  })

  it('seeds repo identity when stamping before hydration', () => {
    const store = createRepoGitStore()
    expect(stampBranchOnRepo(store, { workerId: 'w1', gitToplevel: '/repo' }, 'feature')).toBe(true)
    const state = store.get(repoKey('w1', '/repo'))
    expect(state?.workerId).toBe('w1')
    expect(state?.toplevel).toBe('/repo')
    expect(state?.branch).toBe('feature')
    expect(repoGitView(makeAgentTab('a1'), store).isGitRepo).toBe(true)
  })

  it('re-pins when the branch matches but the pin is clear', () => {
    const store = createRepoGitStore()
    seedBranch(store, 'w1', '/repo', 'A')

    expect(stampBranchOnRepo(store, { workerId: 'w1', gitToplevel: '/repo' }, 'A')).toBe(true)
    expect(store.get(repoKey('w1', '/repo'))?.branch).toBe('A')
    expect(store.get(repoKey('w1', '/repo'))?.branchPinnedUntilRefresh).toBe(true)
  })

  it('reports false when the branch is already stamped and pinned', () => {
    const store = createRepoGitStore()
    seedBranch(store, 'w1', '/repo', 'A')
    store.upsert(repoKey('w1', '/repo'), { branchPinnedUntilRefresh: true })

    expect(stampBranchOnRepo(store, { workerId: 'w1', gitToplevel: '/repo' }, 'A')).toBe(false)
    expect(store.get(repoKey('w1', '/repo'))?.branch).toBe('A')
  })

  it('buildTree re-runs reactively when the repo branch changes', () => {
    createRoot((dispose) => {
      const store = createRepoGitStore()
      const tabs = [makeAgentTab('a1'), makeAgentTab('a2')]
      seedBranch(store, 'w1', '/repo', 'A')

      const tree = createMemo(() => buildTree(tabs, store))

      expect(tree().groups).toHaveLength(1)
      expect(tree().groups[0].branches).toHaveLength(1)
      expect(tree().groups[0].branches[0].branchName).toBe('A')
      expect(tree().groups[0].branches[0].tabs.map(t => t.id).toSorted()).toEqual(['a1', 'a2'])

      stampBranchOnRepo(store, { workerId: 'w1', gitToplevel: '/repo' }, 'B')

      expect(tree().groups).toHaveLength(1)
      expect(tree().groups[0].branches).toHaveLength(1)
      expect(tree().groups[0].branches[0].branchName).toBe('B')
      expect(tree().groups[0].branches[0].tabs.map(t => t.id).toSorted()).toEqual(['a1', 'a2'])

      dispose()
    })
  })

  it('rejects a stamp with an empty repo path', () => {
    const store = createRepoGitStore()
    expect(stampBranchOnRepo(store, { workerId: 'w1', gitToplevel: '' }, 'B')).toBe(false)
  })

  it('refuses to stamp when the worker is unresolved', () => {
    const store = createRepoGitStore()
    seedBranch(store, 'w1', '/repo', 'A')
    expect(stampBranchOnRepo(store, { workerId: '', gitToplevel: '/repo' }, 'B')).toBe(false)
    expect(store.get(repoKey('w1', '/repo'))?.branch).toBe('A')
  })
})
