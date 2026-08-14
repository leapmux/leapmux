import type { GitFileStatusEntry } from '~/generated/leapmux/v1/common_pb'
import { describe, expect, it } from 'vitest'
import { computeGitVisibility, flatEntryOpenTarget, isPathVisible } from './gitVisibility'

/**
 * The git filter tabs ("Changed" / "Staged" / "Unstaged") hide everything the
 * filter did not select. `isPathVisible` is the predicate behind that, and it
 * has to answer two shapes at once: exact entries (a changed file, and the
 * directories on the way down to it) and whole subtrees (git reports an
 * untracked directory as a single "build/" entry).
 */
describe('isPathVisible', () => {
  const unix = 'posix' as const
  const none = new Set<string>()

  it('shows a path that was selected exactly', () => {
    expect(isPathVisible('/repo/file_a.txt', new Set(['/repo/file_a.txt']), none, unix)).toBe(true)
  })

  it('hides a sibling of a selected file', () => {
    // The regression: `visible` also carries the repo root (so the root node
    // renders), and an ancestor walk over it matched EVERY top-level file. The
    // "Changed" tab listed unchanged files as a result.
    const visible = new Set(['/repo', '/repo/file_a.txt'])
    expect(isPathVisible('/repo/clean.txt', visible, none, unix)).toBe(false)
  })

  it('hides a sibling inside a directory that contains a change', () => {
    // Same leak one level down: `src` is in the set only as an ancestor of the
    // changed file, which must not make its other children visible.
    const visible = new Set(['/repo', '/repo/src', '/repo/src/changed.ts'])
    expect(isPathVisible('/repo/src/untouched.ts', visible, none, unix)).toBe(false)
    expect(isPathVisible('/repo/src', visible, none, unix)).toBe(true)
  })

  it('shows everything under a selected subtree', () => {
    // git emits an untracked directory as one entry; its contents were never
    // named individually and still have to render.
    const visible = new Set(['/repo', '/repo/build'])
    const subtrees = new Set(['/repo/build'])
    expect(isPathVisible('/repo/build', visible, subtrees, unix)).toBe(true)
    expect(isPathVisible('/repo/build/bin', visible, subtrees, unix)).toBe(true)
    expect(isPathVisible('/repo/build/bin/app', visible, subtrees, unix)).toBe(true)
  })

  it('does not let a subtree root leak to its own siblings', () => {
    const visible = new Set(['/repo', '/repo/build'])
    const subtrees = new Set(['/repo/build'])
    expect(isPathVisible('/repo/other.txt', visible, subtrees, unix)).toBe(false)
  })

  it('hides everything when nothing was selected', () => {
    expect(isPathVisible('/repo/file_a.txt', none, none, unix)).toBe(false)
  })

  it('walks Windows separators', () => {
    const visible = new Set(['C:\\repo', 'C:\\repo\\build'])
    const subtrees = new Set(['C:\\repo\\build'])
    expect(isPathVisible('C:\\repo\\build\\bin', visible, subtrees, 'win32')).toBe(true)
    expect(isPathVisible('C:\\repo\\clean.txt', visible, subtrees, 'win32')).toBe(false)
  })
})

/**
 * The PRODUCER half. `isPathVisible` above is tested against hand-built sets,
 * so it cannot catch a bug in what actually lands in them — and it was the
 * producer and consumer disagreeing about `subtrees` that made "Changed" list
 * unchanged files in the first place.
 */
describe('computeGitVisibility', () => {
  const unix = 'posix' as const
  const entry = (path: string) => ({ path }) as GitFileStatusEntry

  it('always shows the root, but never as a subtree', () => {
    // The root has to render so the tree is not empty. Making it a subtree
    // root instead would show every file in the repo — the exact leak.
    const { paths, subtrees } = computeGitVisibility([], '/repo', unix)
    expect(paths).toEqual(new Set(['/repo']))
    expect(subtrees.size).toBe(0)
  })

  it('selects a changed file and the directories on the way down to it', () => {
    const { paths, subtrees } = computeGitVisibility([entry('src/app/main.ts')], '/repo', unix)
    expect(paths).toEqual(new Set(['/repo', '/repo/src', '/repo/src/app', '/repo/src/app/main.ts']))
    expect(subtrees.size).toBe(0)
  })

  it('marks a trailing-slash entry as a whole subtree', () => {
    // Git collapses an untracked directory into one "build/" entry.
    const { paths, subtrees } = computeGitVisibility([entry('build/')], '/repo', unix)
    expect(subtrees).toEqual(new Set(['/repo/build']))
    expect(paths.has('/repo/build')).toBe(true)
  })

  it('does not mark a plain file as a subtree even under a matching name', () => {
    const { subtrees } = computeGitVisibility([entry('build')], '/repo', unix)
    expect(subtrees.size).toBe(0)
  })

  it('keeps a subtree root that is nested under another entry\'s ancestor', () => {
    // The ancestor walk breaks early once it meets a path another entry already
    // seeded. That early exit must not skip recording the subtree itself.
    const { paths, subtrees } = computeGitVisibility(
      [entry('src/app/main.ts'), entry('src/app/dist/')],
      '/repo',
      unix,
    )
    expect(subtrees).toEqual(new Set(['/repo/src/app/dist']))
    expect(paths.has('/repo/src/app/dist')).toBe(true)
  })

  it('skips an entry that is nothing but a separator', () => {
    const { paths } = computeGitVisibility([entry('/')], '/repo', unix)
    expect(paths).toEqual(new Set(['/repo']))
  })

  it('feeds isPathVisible so a sibling of a change stays hidden end to end', () => {
    const { paths, subtrees } = computeGitVisibility(
      [entry('src/changed.ts'), entry('build/')],
      '/repo',
      unix,
    )
    expect(isPathVisible('/repo/src/changed.ts', paths, subtrees, unix)).toBe(true)
    expect(isPathVisible('/repo/build/bin/app', paths, subtrees, unix)).toBe(true)
    expect(isPathVisible('/repo/src/clean.ts', paths, subtrees, unix)).toBe(false)
    expect(isPathVisible('/repo/untouched.md', paths, subtrees, unix)).toBe(false)
  })

  it('joins with the worker\'s separator', () => {
    const { paths } = computeGitVisibility([entry('src/main.ts')], 'C:\\repo', 'win32')
    expect(paths.has('C:\\repo\\src\\main.ts')).toBe(true)
  })
})

describe('flatEntryOpenTarget', () => {
  const unix = 'posix' as const

  it('resolves a file entry against the repo root', () => {
    expect(flatEntryOpenTarget({ path: 'src/main.ts', isDir: false }, '/repo', unix)).toBe('/repo/src/main.ts')
  })

  it('refuses an untracked-directory entry', () => {
    // Git lists a whole untracked directory as one "build/" entry beside the
    // files. Opening it as a file tab produced a permanently broken editor —
    // the worker answers ReadFile with "path is a directory".
    expect(flatEntryOpenTarget({ path: 'build/', isDir: false }, '/repo', unix)).toBeUndefined()
  })

  it('still opens a FILE whose name resembles a directory entry', () => {
    expect(flatEntryOpenTarget({ path: 'build', isDir: false }, '/repo', unix)).toBe('/repo/build')
  })

  /**
   * A submodule is a directory that git names WITHOUT a trailing slash, so the
   * slash alone never caught it and its row opened the same broken editor the
   * `build/` case exists to prevent. `isDir` is the worker's own answer.
   */
  it('refuses a directory that carries no trailing slash', () => {
    expect(flatEntryOpenTarget({ path: 'vendor/lib', isDir: true }, '/repo', unix)).toBeUndefined()
  })
})
