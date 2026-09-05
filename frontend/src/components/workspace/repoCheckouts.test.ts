import type { BranchGroup } from './WorkspaceTabTree'
import { describe, expect, it } from 'vitest'
import { listRepoCheckouts } from './repoCheckouts'

function branch(overrides: Partial<BranchGroup> = {}): BranchGroup {
  return {
    branchName: 'main',
    workerId: 'w1',
    gitToplevel: '/home/me/leapmux',
    isWorktree: false,
    displayLabel: 'main',
    homeDir: '/home/me',
    flavor: undefined,
    workerLabel: '',
    tabs: [],
    diffAdded: 0,
    diffDeleted: 0,
    diffUntracked: 0,
    ...overrides,
  }
}

const ORIGIN = 'https://example.com/o/r.git'
const allLocal = () => true
const noneLocal = () => false

describe('listRepoCheckouts', () => {
  it('gives one checkout for one branch', () => {
    const got = listRepoCheckouts([branch()], ORIGIN, allLocal)

    expect(got).toHaveLength(1)
    expect(got[0]).toMatchObject({
      workerId: 'w1',
      gitToplevel: '/home/me/leapmux',
      originUrl: ORIGIN,
      isLocal: true,
      label: 'main',
    })
  })

  // Several branch rows can share one working tree: the tree keys its rows by
  // (branchName, workerId, gitToplevel), while a path-shaped action acts on
  // the pair alone.
  it('collapses branches that share one working tree', () => {
    const got = listRepoCheckouts(
      [branch({ branchName: 'main' }), branch({ branchName: 'feature', displayLabel: 'feature' })],
      ORIGIN,
      allLocal,
    )

    expect(got).toHaveLength(1)
    expect(got[0].label).toBe('main')
  })

  it('keeps a linked worktree apart from the main checkout, and marks it', () => {
    const got = listRepoCheckouts(
      [
        branch(),
        branch({ gitToplevel: '/home/me/wt', isWorktree: true, branchName: 'feature', displayLabel: 'feature' }),
      ],
      ORIGIN,
      allLocal,
    )

    expect(got.map(c => c.label)).toEqual(['main', 'feature (worktree)'])
  })

  // One repository can be checked out on two machines, and the two are
  // different directories that happen to share a path.
  it('keeps the same path on two workers apart', () => {
    const got = listRepoCheckouts(
      [branch(), branch({ workerId: 'w2' })],
      ORIGIN,
      workerId => workerId === 'w1',
    )

    expect(got).toHaveLength(2)
    expect(got.map(c => c.isLocal)).toEqual([true, false])
  })

  // Every item of the menu a checkout produces needs a path to act on.
  it('drops a branch group with no resolved toplevel', () => {
    const got = listRepoCheckouts([branch({ gitToplevel: '' }), branch()], ORIGIN, allLocal)

    expect(got).toHaveLength(1)
    expect(got[0].gitToplevel).toBe('/home/me/leapmux')
  })

  it('answers empty for no branches', () => {
    expect(listRepoCheckouts([], ORIGIN, allLocal)).toEqual([])
  })

  it('carries an empty origin through for a repository with no remote', () => {
    const got = listRepoCheckouts([branch()], '', noneLocal)

    expect(got[0].originUrl).toBe('')
    expect(got[0].isLocal).toBe(false)
  })

  // The tree's own disambiguation already lives in `displayLabel`; the mark is
  // the only thing this projection adds.
  it('keeps the tree\'s disambiguated label', () => {
    const got = listRepoCheckouts(
      [branch({ displayLabel: 'main (worker-a)' })],
      ORIGIN,
      allLocal,
    )

    expect(got[0].label).toBe('main (worker-a)')
  })
})
