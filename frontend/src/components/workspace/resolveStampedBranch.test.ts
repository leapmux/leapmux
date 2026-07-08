/// <reference types="vitest/globals" />
import type { GitBranchEntry } from '~/generated/leapmux/v1/git_pb'
import { describe, expect, it } from 'vitest'
import { resolveStampedBranch } from '~/components/workspace/branchStamp'

function entry(name: string, isRemote: boolean): GitBranchEntry {
  return { $typeName: 'leapmux.v1.GitBranchEntry', name, isRemote } as GitBranchEntry
}

// The dialog passes the chosen branch name back into stampBranchOnTabs;
// the worker's checkoutBranchInDir leaves the working directory on the
// LOCAL branch (creating a tracking branch from a remote ref, or
// checking out the local name verbatim). resolveStampedBranch decides
// which name to stamp.
describe('resolveStampedBranch', () => {
  it('returns the verbatim name for a local branch even when it contains "/"', () => {
    const branches = [entry('main', false), entry('feature/auth', false)]
    expect(resolveStampedBranch('feature/auth', branches)).toBe('feature/auth')
  })

  it('strips the prefix for a remote-tracking ref', () => {
    const branches = [entry('main', false), entry('origin/foo', true)]
    expect(resolveStampedBranch('origin/foo', branches)).toBe('foo')
  })

  it('strips the prefix when the branches list is null (pre-RPC fallback)', () => {
    // Race: the inspect RPC hasn't landed but the user already clicked
    // Apply on a remote-tracking ref the BranchSelect was paint-by-seed.
    // The conservative choice matches the worker's resolution.
    expect(resolveStampedBranch('origin/foo', null)).toBe('foo')
  })

  it('keeps the name verbatim when the selected entry is not in the branches list (race)', () => {
    // Race: refresh removed the entry between selection and Apply.
    // The earlier behavior stripped unconditionally, which mis-stamped
    // legitimate local branches like `feature/auth` as `auth`. With a
    // known branches list missing the target we have no positive
    // evidence the picked entry is remote, so stay verbatim — the
    // strip-on-missing path was the bug, not the contract.
    const branches = [entry('main', false)]
    expect(resolveStampedBranch('origin/foo', branches)).toBe('origin/foo')
  })

  it('keeps a local branch with "/" verbatim even when the branches list races out the entry', () => {
    // Regression for the strip-on-missing bug: a local branch like
    // `feature/auth` could be silently stamped as `auth` if the
    // branches list was momentarily filtered (e.g. by partitionBranches
    // dropping the doomed branch in DeleteBranchDialog). The new
    // contract is that an unknown entry never overrides verbatim
    // selection — the worker's resolution wins on the next refresh.
    const branches = [entry('main', false)]
    expect(resolveStampedBranch('feature/auth', branches)).toBe('feature/auth')
  })

  it('returns the verbatim name when neither side has a slash', () => {
    const branches = [entry('main', false)]
    expect(resolveStampedBranch('main', branches)).toBe('main')
  })

  it('handles a multi-segment local branch (e.g. team/feature/auth) verbatim', () => {
    const branches = [entry('team/feature/auth', false)]
    expect(resolveStampedBranch('team/feature/auth', branches)).toBe('team/feature/auth')
  })
})
