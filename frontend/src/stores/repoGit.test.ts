import { create } from '@bufbuild/protobuf'
import { describe, expect, it } from 'vitest'
import { GitRepoStatusSchema } from '~/generated/leapmux/v1/common_pb'
import { GetGitFileStatusResponseSchema } from '~/generated/leapmux/v1/git_pb'
import {
  applyFullGitStatusUpsert,
  findCanonicalRepoKey,
  focusedRepoKeyFromTab,
  gitStatusProbePath,
  hasHealthyRepoForProbe,
  hasPreservableRepoGitState,
  isStampOnlyRepoGitState,
  migrateErrorHintFromForResolvedRepo,
  patchFromNonRepoGetGitFileStatus,
  protoToRepoGitPatch,
  repoGitView,
  repoKey,
  upsertRepoGitFromProtoStatus,
} from '~/stores/repoGit'
import { createRepoGitStore } from '~/stores/repoGit.store'

describe('gitStatusProbePath', () => {
  it('prefers gitToplevel over workingDir', () => {
    expect(gitStatusProbePath({ gitToplevel: '/repo', workingDir: '/repo/pkg' })).toBe('/repo')
  })

  it('falls back to workingDir when toplevel is unset', () => {
    expect(gitStatusProbePath({ workingDir: '/repo/pkg' })).toBe('/repo/pkg')
  })
})

describe('focusedRepoKeyFromTab', () => {
  it('falls back to the probe path when gitToplevel is unset', () => {
    expect(focusedRepoKeyFromTab({ workerId: 'w1', workingDir: '/repo/pkg' })).toBe('w1\x00/repo/pkg')
  })

  it('uses ctx probe path for file tabs before gitToplevel resolves', () => {
    const tab = { workerId: 'w1', workingDir: '/repo/pkg' }
    const ctx = { gitToplevel: '/repo', workingDir: '/repo/pkg' }
    expect(focusedRepoKeyFromTab(tab, ctx)).toBe('w1\x00/repo')
  })

  it('resolves the canonical toplevel key from store state', () => {
    const store = createRepoGitStore()
    store.upsert(repoKey('w1', '/repo'), {
      workerId: 'w1',
      toplevel: '/repo',
      branch: 'main',
    })
    const tab = { workerId: 'w1', workingDir: '/repo/pkg' }
    expect(focusedRepoKeyFromTab(tab, tab, store)).toBe('w1\x00/repo')
  })
})

describe('findCanonicalRepoKey', () => {
  it('returns the longest matching toplevel for a nested probe path', () => {
    const store = createRepoGitStore()
    store.upsert(repoKey('w1', '/repo'), { workerId: 'w1', toplevel: '/repo', branch: 'main' })
    expect(findCanonicalRepoKey(store, 'w1', '/repo/pkg')).toBe('w1\x00/repo')
  })
})

describe('hasPreservableRepoGitState', () => {
  it('treats metadata-only clean repos as preservable', () => {
    expect(hasPreservableRepoGitState({
      workerId: 'w1',
      toplevel: '/repo',
      branch: 'main',
      repoRoot: '',
      originUrl: '',
      isWorktree: false,
      ahead: 0,
      behind: 0,
      conflicted: false,
      stashed: false,
      deleted: false,
      renamed: false,
      modified: false,
      typeChanged: false,
      added: false,
      untracked: false,
      diffAdded: 0,
      diffDeleted: 0,
      diffUntracked: 0,
      files: [],
      errorHint: '',
      gitStatusSeen: true,
    })).toBe(true)
  })

  it('excludes stamp-only seeds', () => {
    const stampOnly = {
      workerId: 'w1',
      toplevel: '/repo',
      branch: 'feature',
      branchPinnedUntilRefresh: true,
      repoRoot: '',
      originUrl: '',
      isWorktree: false,
      ahead: 0,
      behind: 0,
      conflicted: false,
      stashed: false,
      deleted: false,
      renamed: false,
      modified: false,
      typeChanged: false,
      added: false,
      untracked: false,
      diffAdded: 0,
      diffDeleted: 0,
      diffUntracked: 0,
      files: [],
      errorHint: '',
    }
    expect(isStampOnlyRepoGitState(stampOnly)).toBe(true)
    expect(hasPreservableRepoGitState(stampOnly)).toBe(false)
  })
})

describe('hasHealthyRepoForProbe', () => {
  it('matches hasPreservableRepoGitState for hint and canonical keys', () => {
    const store = createRepoGitStore()
    const key = repoKey('w1', '/repo')
    store.upsert(key, { workerId: 'w1', toplevel: '/repo', branch: 'main', gitStatusSeen: true })
    expect(hasHealthyRepoForProbe(store, 'w1', '/repo/pkg', key)).toBe(true)

    store.clear(key)
    store.upsert(key, {
      workerId: 'w1',
      toplevel: '/repo',
      branch: 'feature',
      branchPinnedUntilRefresh: true,
    })
    expect(hasHealthyRepoForProbe(store, 'w1', '/repo', key)).toBe(false)
  })
})

describe('applyFullGitStatusUpsert', () => {
  it('keeps a stamped branch when refresh returns a stale branch name', () => {
    const store = createRepoGitStore()
    const key = repoKey('w1', '/repo')
    store.upsert(key, {
      workerId: 'w1',
      toplevel: '/repo',
      branch: 'feature',
      branchPinnedUntilRefresh: true,
    })

    applyFullGitStatusUpsert(store, {
      key,
      patch: { workerId: 'w1', toplevel: '/repo', branch: 'main', diffAdded: 3 },
    })

    expect(store.get(key)?.branch).toBe('feature')
    expect(store.get(key)?.branchPinnedUntilRefresh).toBe(true)
    expect(store.get(key)?.diffAdded).toBe(3)
  })

  it('clears the pin when refresh confirms the stamped branch', () => {
    const store = createRepoGitStore()
    const key = repoKey('w1', '/repo')
    store.upsert(key, {
      workerId: 'w1',
      toplevel: '/repo',
      branch: 'feature',
      branchPinnedUntilRefresh: true,
    })

    applyFullGitStatusUpsert(store, {
      key,
      patch: { workerId: 'w1', toplevel: '/repo', branch: 'feature' },
    })

    expect(store.get(key)?.branch).toBe('feature')
    expect(store.get(key)?.branchPinnedUntilRefresh).toBe(false)
  })
})

describe('migrateErrorHintFromForResolvedRepo', () => {
  it('returns the orphan probe-path key when toplevel resolves to a different key', () => {
    const tab = { workingDir: '/repo/pkg' }
    const status = create(GitRepoStatusSchema, { toplevel: '/repo', branch: 'main' })
    expect(migrateErrorHintFromForResolvedRepo('w1', tab, status)).toBe('w1\x00/repo/pkg')
  })

  it('returns undefined when tab already has gitToplevel', () => {
    const tab = { gitToplevel: '/repo', workingDir: '/repo/pkg' }
    const status = create(GitRepoStatusSchema, { toplevel: '/repo', branch: 'main' })
    expect(migrateErrorHintFromForResolvedRepo('w1', tab, status)).toBeUndefined()
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
      gitStatusSeen: true,
      branchPinnedUntilRefresh: false,
    })
  })
})

describe('protoToRepoGitPatch', () => {
  it('maps metadata fields only', () => {
    const patch = protoToRepoGitPatch('w1', create(GitRepoStatusSchema, {
      toplevel: '/repo',
      branch: 'feature',
      ahead: 2,
    }))
    expect(patch).toEqual({
      workerId: 'w1',
      toplevel: '/repo',
      branch: 'feature',
      originUrl: '',
      isWorktree: false,
      ahead: 2,
      behind: 0,
      conflicted: false,
      stashed: false,
      deleted: false,
      renamed: false,
      modified: false,
      typeChanged: false,
      added: false,
      untracked: false,
    })
    expect(patch).not.toHaveProperty('files')
  })
})

describe('upsertRepoGitFromProtoStatus', () => {
  it('preserves files when branch and toplevel are unchanged', () => {
    const store = createRepoGitStore()
    const key = repoKey('w1', '/repo')
    const files = [{ path: 'a.txt' } as never]
    store.upsert(key, {
      workerId: 'w1',
      toplevel: '/repo',
      branch: 'main',
      diffAdded: 5,
      files,
    })

    upsertRepoGitFromProtoStatus(store, 'w1', create(GitRepoStatusSchema, {
      toplevel: '/repo',
      branch: 'main',
      ahead: 1,
    }))

    expect(store.get(key)?.ahead).toBe(1)
    expect(store.get(key)?.diffAdded).toBe(5)
    expect(store.get(key)?.files).toEqual(files)
  })

  it('clears file-derived fields when branch changes', () => {
    const store = createRepoGitStore()
    const key = repoKey('w1', '/repo')
    store.upsert(key, {
      workerId: 'w1',
      toplevel: '/repo',
      branch: 'main',
      diffAdded: 5,
      files: [{ path: 'a.txt' } as never],
    })

    upsertRepoGitFromProtoStatus(store, 'w1', create(GitRepoStatusSchema, {
      toplevel: '/repo',
      branch: 'feature',
    }))

    expect(store.get(key)?.branch).toBe('feature')
    expect(store.get(key)?.diffAdded).toBe(0)
    expect(store.get(key)?.files).toEqual([])
  })

  it('clears a probe-path orphan without copying its errorHint onto a resolved repo', () => {
    const store = createRepoGitStore()
    const orphanKey = repoKey('w1', '/repo/pkg')
    store.upsert(orphanKey, { workerId: 'w1', errorHint: 'not a git repository' })

    upsertRepoGitFromProtoStatus(store, 'w1', create(GitRepoStatusSchema, {
      toplevel: '/repo',
      branch: 'main',
    }), { migrateErrorHintFrom: orphanKey })

    expect(store.get(orphanKey)).toBeUndefined()
    expect(store.get(repoKey('w1', '/repo'))?.errorHint).toBe('')
    expect(store.get(repoKey('w1', '/repo'))?.gitStatusSeen).toBe(true)
  })

  it('clears a probe-path orphan even when it has no errorHint', () => {
    const store = createRepoGitStore()
    const orphanKey = repoKey('w1', '/repo/pkg')
    store.upsert(orphanKey, { workerId: 'w1', errorHint: '' })

    upsertRepoGitFromProtoStatus(store, 'w1', create(GitRepoStatusSchema, {
      toplevel: '/repo',
      branch: 'main',
    }), { migrateErrorHintFrom: orphanKey })

    expect(store.get(orphanKey)).toBeUndefined()
    expect(store.get(repoKey('w1', '/repo'))?.toplevel).toBe('/repo')
  })

  it('clears errorHint on identity-stable metadata upsert after recovery', () => {
    const store = createRepoGitStore()
    const key = repoKey('w1', '/repo')
    store.upsert(key, {
      workerId: 'w1',
      toplevel: '/repo',
      branch: 'main',
      errorHint: 'not a git repository',
    })

    upsertRepoGitFromProtoStatus(store, 'w1', create(GitRepoStatusSchema, {
      toplevel: '/repo',
      branch: 'main',
      ahead: 1,
    }))

    expect(store.get(key)?.errorHint).toBe('')
    expect(store.get(key)?.ahead).toBe(1)
  })

  it('preserves a hydrated-repo errorHint across identity-stable metadata', () => {
    const store = createRepoGitStore()
    const key = repoKey('w1', '/repo')
    store.upsert(key, {
      workerId: 'w1',
      toplevel: '/repo',
      branch: 'main',
      repoRoot: '/repo',
      errorHint: 'dubious ownership',
      gitStatusSeen: true,
    })

    upsertRepoGitFromProtoStatus(store, 'w1', create(GitRepoStatusSchema, {
      toplevel: '/repo',
      branch: 'main',
      ahead: 1,
    }))

    expect(store.get(key)?.errorHint).toBe('dubious ownership')
    expect(store.get(key)?.ahead).toBe(1)
  })

  it('does not keep a stale tip when the resolving status is otherwise hydrated', () => {
    const store = createRepoGitStore()
    const orphanKey = repoKey('w1', '/repo/pkg')
    store.upsert(orphanKey, { workerId: 'w1', errorHint: 'not a git repository' })

    upsertRepoGitFromProtoStatus(store, 'w1', create(GitRepoStatusSchema, {
      toplevel: '/repo',
      branch: 'main',
      originUrl: 'git@example.com:o/r.git',
      ahead: 2,
    }), { migrateErrorHintFrom: orphanKey })

    const key = repoKey('w1', '/repo')
    expect(store.get(key)?.errorHint).toBe('')
    expect(store.get(key)?.originUrl).toBe('git@example.com:o/r.git')

    upsertRepoGitFromProtoStatus(store, 'w1', create(GitRepoStatusSchema, {
      toplevel: '/repo',
      branch: 'main',
      originUrl: 'git@example.com:o/r.git',
      ahead: 2,
    }))

    expect(store.get(key)?.errorHint).toBe('')
  })

  it('clears errorHint when toplevel first resolves on a non-repo stub', () => {
    const store = createRepoGitStore()
    const key = repoKey('w1', '/repo')
    store.upsert(key, {
      workerId: 'w1',
      toplevel: '',
      branch: '',
      errorHint: 'not a git repository',
    })

    upsertRepoGitFromProtoStatus(store, 'w1', create(GitRepoStatusSchema, {
      toplevel: '/repo',
      branch: 'main',
    }))

    expect(store.get(key)?.errorHint).toBe('')
    expect(store.get(key)?.toplevel).toBe('/repo')
  })

  it('keeps pinned branch and files when a stale broadcast arrives', () => {
    const store = createRepoGitStore()
    const key = repoKey('w1', '/repo')
    const files = [{ path: 'a.txt' } as never]
    store.upsert(key, {
      workerId: 'w1',
      toplevel: '/repo',
      branch: 'feature',
      branchPinnedUntilRefresh: true,
      diffAdded: 5,
      files,
    })

    upsertRepoGitFromProtoStatus(store, 'w1', create(GitRepoStatusSchema, {
      toplevel: '/repo',
      branch: 'main',
      ahead: 1,
    }))

    expect(store.get(key)?.branch).toBe('feature')
    expect(store.get(key)?.diffAdded).toBe(5)
    expect(store.get(key)?.files).toEqual(files)
    expect(store.get(key)?.ahead).toBe(1)
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

  it('does not treat a probe-path key as toplevel when the store has no entry', () => {
    const store = createRepoGitStore()
    const view = repoGitView(
      { workerId: 'w1', workingDir: '/repo/pkg' },
      store,
      { workingDir: '/repo/pkg' },
    )

    expect(view.key).toBe('w1\x00/repo/pkg')
    expect(view.toplevel).toBeUndefined()
    expect(view.isGitRepo).toBe(false)
  })

  it('reads canonical repo state for a file tab without gitToplevel', () => {
    const store = createRepoGitStore()
    store.upsert(repoKey('w1', '/repo'), {
      workerId: 'w1',
      toplevel: '/repo',
      branch: 'feature',
      diffAdded: 4,
    })

    const view = repoGitView(
      { workerId: 'w1', workingDir: '/repo/pkg' },
      store,
      { workingDir: '/repo/pkg' },
    )

    expect(view.key).toBe('w1\x00/repo')
    expect(view.branchLabel).toBe('feature')
    expect(view.diffStats).toEqual({ added: 4, deleted: 0, untracked: 0 })
  })
})
