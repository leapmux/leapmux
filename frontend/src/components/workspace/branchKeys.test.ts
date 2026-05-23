import { describe, expect, it } from 'vitest'
import {
  branchKey,
  branchNameSegment,
  collapseKeyForBranch,
  isLocalRepoKey,
  repoKeyForLocal,
  repoKeyTooltip,
} from './branchKeys'

describe('branchNameSegment', () => {
  it('returns a sentinel for null', () => {
    const seg = branchNameSegment(null)
    expect(seg).not.toBe('')
    expect(seg).not.toBe('(no branch)')
  })

  it('does not collide with a real branch literally named "(no branch)"', () => {
    expect(branchNameSegment(null)).not.toBe(branchNameSegment('(no branch)'))
  })

  it('passes a real branch name through unchanged', () => {
    expect(branchNameSegment('main')).toBe('main')
    expect(branchNameSegment('feature/x')).toBe('feature/x')
  })
})

describe('branchKey', () => {
  it('keys distinct (branch, worker, toplevel) triples to distinct strings', () => {
    const a = branchKey('feature', 'w1', '/p')
    const b = branchKey('feature', 'w2', '/p')
    const c = branchKey('feature', 'w1', '/q')
    const d = branchKey('other', 'w1', '/p')
    const keys = new Set([a, b, c, d])
    expect(keys.size).toBe(4)
  })

  it('separates the null-branch bucket from a real branch literally named "(no branch)"', () => {
    expect(branchKey(null, 'w1', '/p')).not.toBe(branchKey('(no branch)', 'w1', '/p'))
  })

  it('keeps a colliding worker:path/branch:worker pair distinct', () => {
    // Legacy colon-joined keys would have collapsed these two:
    //   branch="feature:a", worker="b",   toplevel="/p"  → "feature:a:b:/p"
    //   branch="feature",   worker="a:b", toplevel="/p"  → "feature:a:b:/p"
    // (Branch names can't contain ':' per git-check-ref-format, but worker
    // ids and paths can.)
    const a = branchKey('feature', 'a:b', '/p')
    const b = branchKey('feature:a', 'b', '/p')
    expect(a).not.toBe(b)
  })
})

describe('repoKeyForLocal / isLocalRepoKey / repoKeyTooltip', () => {
  it('returns a local key that is recognised as local', () => {
    const key = repoKeyForLocal('/home/me/projects/alpha')
    expect(isLocalRepoKey(key)).toBe(true)
  })

  it('returns false for raw origin URLs', () => {
    expect(isLocalRepoKey('https://github.com/o/r.git')).toBe(false)
    expect(isLocalRepoKey('git@github.com:o/r.git')).toBe(false)
  })

  it('round-trips the toplevel path through the tooltip', () => {
    const toplevel = '/home/me/projects/alpha'
    expect(repoKeyTooltip(repoKeyForLocal(toplevel))).toBe(toplevel)
  })

  it('returns the origin URL unchanged for non-local keys', () => {
    expect(repoKeyTooltip('https://github.com/o/r.git')).toBe('https://github.com/o/r.git')
  })

  it('cannot collide with any real origin URL (control byte prefix)', () => {
    // Git origin URLs cannot begin with a control byte, so the prefix
    // guarantees a local key never matches a remote key by coincidence.
    const local = repoKeyForLocal('/x')
    expect(local.charCodeAt(0)).toBeLessThan(0x20)
  })
})

describe('collapseKeyForBranch', () => {
  it('composes repo + branch into a unique key', () => {
    const a = collapseKeyForBranch('repoA', branchKey('feature', 'w1', '/p'))
    const b = collapseKeyForBranch('repoB', branchKey('feature', 'w1', '/p'))
    expect(a).not.toBe(b)
  })

  it('cannot collide across (repo, branch) split (sentinel separator)', () => {
    // If the separator weren't a control byte, splitting at the first
    // ':' could ambiguously assign chars in one of the halves to the
    // other. The control-byte separator prevents the ambiguity entirely.
    const a = collapseKeyForBranch('foo', branchKey('bar', 'w', '/p'))
    const b = collapseKeyForBranch('foo:bar', branchKey('', 'w', '/p'))
    expect(a).not.toBe(b)
  })
})
