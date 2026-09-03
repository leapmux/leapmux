import { beforeEach, describe, expect, it } from 'vitest'
import { GitMode } from '~/hooks/useGitModeState'
import { localStorageClearForTests, localStorageSet, PREFIX_WORKSPACE_GIT_MODE, setStorageAccount } from '~/lib/browserStorage'
import {
  gitModeStickyKey,
  readStickyGitMode,
  rememberStickyGitMode,
  startPointDialogSetup,
} from './workspaceStartPoint'

/**
 * `assertNever` gives exhaustiveness for ADDING a variant; it gives no coverage
 * that an existing arm is right. These tests are that coverage: one case per
 * variant, asserting the whole returned setup.
 */
describe('startPointDialogSetup', () => {
  it('asks for nothing on a bare directory start point', () => {
    expect(startPointDialogSetup({ kind: 'directory' })).toEqual({ workerId: undefined })
  })

  it('preselects a worker without pre-filling a directory', () => {
    // The `?newWorkspace=true&workerId=` route names a machine and nothing
    // else, so the user still picks the directory.
    expect(startPointDialogSetup({ kind: 'directory', workerId: 'w1' }))
      .toEqual({ workerId: 'w1' })
  })

  it('pre-fills worker, directory, snapshot and sticky key for a repo root', () => {
    expect(startPointDialogSetup({
      kind: 'repo',
      workerId: 'w1',
      gitToplevel: '/home/me/leapmux',
      isWorktree: false,
      currentBranch: 'main',
    })).toEqual({
      workerId: 'w1',
      workingDir: '/home/me/leapmux',
      pathInfoSeed: {
        isGitRepo: true,
        isRepoRoot: true,
        isWorktreeRoot: false,
        repoRoot: '/home/me/leapmux',
        repoDirName: 'leapmux',
        currentBranch: 'main',
      },
      stickyKey: gitModeStickyKey('w1', '/home/me/leapmux'),
    })
  })

  it('leaves repoRoot EMPTY for a worktree, so the probe supplies the real one', () => {
    // Seeding `repoRoot` with a worktree toplevel makes
    // `GitOptions.worktreePath()` suggest a new worktree nested under the
    // existing worktree's parent instead of the repository's.
    const setup = startPointDialogSetup({
      kind: 'repo',
      workerId: 'w1',
      gitToplevel: '/home/me/wt/feature',
      isWorktree: true,
      currentBranch: 'feature',
    })
    expect(setup.pathInfoSeed).toEqual({
      isGitRepo: true,
      isRepoRoot: false,
      isWorktreeRoot: true,
      repoRoot: '',
      repoDirName: '',
      currentBranch: 'feature',
    })
    // The working directory is still the worktree: that IS where the agent
    // starts.
    expect(setup.workingDir).toBe('/home/me/wt/feature')
  })

  it('seeds an empty branch when the caller does not know one', () => {
    const setup = startPointDialogSetup({
      kind: 'repo',
      workerId: 'w1',
      gitToplevel: '/repo',
      isWorktree: false,
    })
    expect(setup.pathInfoSeed?.currentBranch).toBe('')
  })

  it('keys the worktree and its main repository separately', () => {
    // Both checkouts are real places to start, and they deserve different
    // remembered modes.
    const main = startPointDialogSetup({ kind: 'repo', workerId: 'w1', gitToplevel: '/repo', isWorktree: false })
    const wt = startPointDialogSetup({ kind: 'repo', workerId: 'w1', gitToplevel: '/repo-wt/x', isWorktree: true })
    expect(main.stickyKey).not.toBe(wt.stickyKey)
  })

  it('keys the same path on two workers separately', () => {
    const a = startPointDialogSetup({ kind: 'repo', workerId: 'w1', gitToplevel: '/repo', isWorktree: false })
    const b = startPointDialogSetup({ kind: 'repo', workerId: 'w2', gitToplevel: '/repo', isWorktree: false })
    expect(a.stickyKey).not.toBe(b.stickyKey)
  })
})

describe('sticky git mode', () => {
  beforeEach(() => {
    localStorageClearForTests()
    setStorageAccount('u-1')
  })

  it('round-trips a mode under its repository key', () => {
    const key = gitModeStickyKey('w1', '/repo')
    rememberStickyGitMode(key, GitMode.CreateWorktree)
    expect(readStickyGitMode(key)).toBe(GitMode.CreateWorktree)
  })

  it('remembers nothing for a repository never submitted', () => {
    expect(readStickyGitMode(gitModeStickyKey('w1', '/other'))).toBeUndefined()
  })

  it('keeps two repositories independent', () => {
    rememberStickyGitMode(gitModeStickyKey('w1', '/a'), GitMode.CreateBranch)
    rememberStickyGitMode(gitModeStickyKey('w1', '/b'), GitMode.UseWorktree)
    expect(readStickyGitMode(gitModeStickyKey('w1', '/a'))).toBe(GitMode.CreateBranch)
    expect(readStickyGitMode(gitModeStickyKey('w1', '/b'))).toBe(GitMode.UseWorktree)
  })

  it('is a no-op without a key, rather than writing a keyless entry', () => {
    rememberStickyGitMode(undefined, GitMode.CreateBranch)
    expect(readStickyGitMode(undefined)).toBeUndefined()
  })

  it('answers undefined for a stored value that no longer names a mode', () => {
    // The whole reason a TOKEN is stored: a value from a previous build must
    // fall back rather than select whichever mode now holds that slot.
    localStorageSet(`${PREFIX_WORKSPACE_GIT_MODE}w1:/repo`, 'rebase-onto')
    expect(readStickyGitMode(gitModeStickyKey('w1', '/repo'))).toBeUndefined()
  })

  it('answers undefined for a stored enum NUMBER', () => {
    localStorageSet(`${PREFIX_WORKSPACE_GIT_MODE}w1:/repo`, 3)
    expect(readStickyGitMode(gitModeStickyKey('w1', '/repo'))).toBeUndefined()
  })

  it('stores Current, which is a real answer and not "nothing remembered"', () => {
    const key = gitModeStickyKey('w1', '/repo')
    rememberStickyGitMode(key, GitMode.Current)
    expect(readStickyGitMode(key)).toBe(GitMode.Current)
  })
})
