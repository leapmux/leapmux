import { create } from '@bufbuild/protobuf'
import { describe, expect, it } from 'vitest'
import { GetGitFileStatusResponseSchema } from '~/generated/leapmux/v1/git_pb'
import { gitStatusProbePath, patchFromNonRepoGetGitFileStatus, repoGitView, repoKey } from '~/stores/repoGit'
import { createRepoGitStore } from '~/stores/repoGit.store'

describe('gitStatusProbePath', () => {
  it('prefers gitToplevel over workingDir', () => {
    expect(gitStatusProbePath({ gitToplevel: '/repo', workingDir: '/repo/pkg' })).toBe('/repo')
  })

  it('falls back to workingDir when toplevel is unset', () => {
    expect(gitStatusProbePath({ workingDir: '/repo/pkg' })).toBe('/repo/pkg')
  })
})

describe('patchFromNonRepoGetGitFileStatus', () => {
  it('clears git fields and keeps errorHint on the hinted key', () => {
    const key = repoKey('w1', '/repo')
    const mapped = patchFromNonRepoGetGitFileStatus('w1', create(GetGitFileStatusResponseSchema, {
      repoRoot: '',
      files: [],
      errorHint: 'not a git repository',
    }), key)

    expect(mapped.key).toBe(key)
    expect(mapped.patch).toMatchObject({
      workerId: 'w1',
      toplevel: '',
      branch: '',
      errorHint: 'not a git repository',
      files: [],
      diffAdded: 0,
      diffDeleted: 0,
      diffUntracked: 0,
    })
  })
})

describe('repoGitView', () => {
  it('joins a tab repo identity to keyed store state', () => {
    const store = createRepoGitStore()
    store.upsert(repoKey('w1', '/repo'), {
      workerId: 'w1',
      toplevel: '/repo',
      branch: 'main',
      originUrl: 'git@example.com:o/r.git',
      isWorktree: true,
      ahead: 2,
      behind: 1,
      conflicted: true,
      diffAdded: 10,
      diffDeleted: 3,
      diffUntracked: 1,
    })

    const view = repoGitView({ workerId: 'w1', gitToplevel: '/repo' }, store)

    expect(view.key).toBe('w1\x00/repo')
    expect(view.branchLabel).toBe('main')
    expect(view.originUrl).toBe('git@example.com:o/r.git')
    expect(view.isWorktree).toBe(true)
    expect(view.ahead).toBe(2)
    expect(view.behind).toBe(1)
    expect(view.conflicted).toBe(true)
    expect(view.diffStats).toEqual({ added: 10, deleted: 3, untracked: 1 })
    expect(view.isGitRepo).toBe(true)
  })

  it('returns the empty view when the tab has no repo key', () => {
    const store = createRepoGitStore()
    store.upsert(repoKey('w1', '/repo'), { workerId: 'w1', toplevel: '/repo', branch: 'main' })

    const view = repoGitView({ workerId: 'w1' }, store)

    expect(view.key).toBeUndefined()
    expect(view.branchLabel).toBeUndefined()
    expect(view.isGitRepo).toBe(false)
    expect(view.diffStats).toEqual({ added: 0, deleted: 0, untracked: 0 })
  })

  it('returns a keyed empty view when the store has no entry yet', () => {
    const store = createRepoGitStore()

    const view = repoGitView({ workerId: 'w1', gitToplevel: '/repo' }, store)

    expect(view.key).toBe('w1\x00/repo')
    expect(view.toplevel).toBe('/repo')
    expect(view.branchLabel).toBeUndefined()
    expect(view.originUrl).toBeUndefined()
    expect(view.isGitRepo).toBe(false)
    expect(view.diffStats).toEqual({ added: 0, deleted: 0, untracked: 0 })
  })
})
