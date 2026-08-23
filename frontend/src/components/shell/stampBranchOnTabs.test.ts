/// <reference types="vitest/globals" />
import { describe, expect, it } from 'vitest'
import { repoKey } from '~/stores/repoGit'
import { createRepoGitStore } from '~/stores/repoGit.store'
import { stampBranchOnRepo } from './stampBranchOnTabs'

describe('stampBranchOnRepo', () => {
  it('clears a non-repo stub tip and reopens the probe path', () => {
    const store = createRepoGitStore()
    const key = repoKey('w1', '/repo')
    store.upsert(key, {
      workerId: 'w1',
      toplevel: '',
      branch: '',
      errorHint: 'not a git repository',
      gitStatusSeen: true,
    })

    expect(stampBranchOnRepo(store, { workerId: 'w1', gitToplevel: '/repo' }, 'feature')).toBe(true)

    expect(store.get(key)?.toplevel).toBe('/repo')
    expect(store.get(key)?.branch).toBe('feature')
    expect(store.get(key)?.errorHint).toBe('')
    expect(store.get(key)?.gitStatusSeen).toBe(false)
    expect(store.get(key)?.branchPinnedUntilRefresh).toBe(true)
  })

  it('re-pins when the branch label already matches but the pin is off', () => {
    const store = createRepoGitStore()
    const key = repoKey('w1', '/repo')
    store.upsert(key, {
      workerId: 'w1',
      toplevel: '/repo',
      branch: 'feature',
      branchPinnedUntilRefresh: false,
      gitStatusSeen: true,
    })

    expect(stampBranchOnRepo(store, { workerId: 'w1', gitToplevel: '/repo' }, 'feature')).toBe(true)
    expect(store.get(key)?.branchPinnedUntilRefresh).toBe(true)
  })

  it('returns false when the branch is already pinned to the same name', () => {
    const store = createRepoGitStore()
    const key = repoKey('w1', '/repo')
    store.upsert(key, {
      workerId: 'w1',
      toplevel: '/repo',
      branch: 'feature',
      branchPinnedUntilRefresh: true,
    })

    expect(stampBranchOnRepo(store, { workerId: 'w1', gitToplevel: '/repo' }, 'feature')).toBe(false)
  })
})
