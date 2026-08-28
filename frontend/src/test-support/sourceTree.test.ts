import { existsSync } from 'node:fs'
import { isAbsolute, join, sep } from 'node:path'
import { describe, expect, it } from 'vitest'
import { collectFiles, frontendRoot, posixRelative, SKIP_DIRS } from '~/test-support/sourceTree'

// Eight repo guards read their whole verdict through this walk, so a hole here
// reads as a clean suite rather than as a failure. Pin the filter, the skip
// set a caller cannot drop, and the absent-directory result.

const testSupportRoot = join(frontendRoot, 'src', 'test-support')

/** The directory names on the way to `file`, so a skip test matches no substring. */
function directoriesOf(file: string): string[] {
  return file.split(sep).slice(0, -1)
}

describe('collectFiles', () => {
  it('returns the absolute path of every file the filter accepts', () => {
    const found = collectFiles(testSupportRoot, { matches: name => name.endsWith('.test.ts') })

    expect(found).toContain(join(testSupportRoot, 'sourceTree.test.ts'))
    expect(found.every(file => file.endsWith('.test.ts'))).toBe(true)
    expect(found.length).toBeGreaterThanOrEqual(10)
  })

  it('rejects a file the filter declines', () => {
    const found = collectFiles(testSupportRoot, { matches: name => name.endsWith('.test.ts') })
    expect(found).not.toContain(join(testSupportRoot, 'sourceTree.ts'))
  })

  it('skips the package install and the build outputs', () => {
    // A walk from the frontend root reaches node_modules unless the shared
    // skip set holds, and a thousand packages of `package.json` is what a hole
    // here looks like.
    const found = collectFiles(frontendRoot, { matches: name => name === 'package.json' })

    expect(found).toContain(join(frontendRoot, 'package.json'))
    expect(found.filter(file => directoriesOf(file).some(dir => SKIP_DIRS.has(dir)))).toEqual([])
  })

  it('adds alsoSkip to the shared set rather than replacing it', () => {
    const found = collectFiles(frontendRoot, {
      matches: name => name === 'package.json',
      alsoSkip: new Set(['src']),
    })

    expect(found).toContain(join(frontendRoot, 'package.json'))
    expect(found.filter(file => directoriesOf(file).includes('node_modules'))).toEqual([])
  })

  it('skips a directory by basename anywhere in the walk', () => {
    const srcRoot = join(frontendRoot, 'src')
    const matches = (name: string): boolean => name.endsWith('.ts')
    const all = collectFiles(srcRoot, { matches })
    const withoutTestSupport = collectFiles(srcRoot, { matches, alsoSkip: new Set(['test-support']) })

    expect(withoutTestSupport.length).toBeLessThan(all.length)
    expect(withoutTestSupport.filter(file => directoriesOf(file).includes('test-support'))).toEqual([])
  })

  // The distinction REMOVALS-9 turns on: a basename skip exempts every
  // directory with that name at every depth, an exact path skip exempts
  // exactly one location.
  it('skips a path exactly, so a same-named directory elsewhere is still walked', () => {
    const srcRoot = join(frontendRoot, 'src')
    const matches = (name: string): boolean => name.endsWith('.ts')

    // 'components' exists directly under src/, so an exact skip removes it.
    const exact = collectFiles(srcRoot, { matches, skipPaths: new Set(['components']) })
    expect(exact.filter(file => file.startsWith(join(srcRoot, 'components')))).toEqual([])

    // A nested directory that shares a basename with an exact skip entry
    // survives, where alsoSkip would have removed it too.
    const nestedName = 'test-support'
    const byPath = collectFiles(frontendRoot, { matches, skipPaths: new Set([nestedName]) })
    expect(byPath.some(file => directoriesOf(file).includes(nestedName))).toBe(true)
    const byName = collectFiles(frontendRoot, { matches, alsoSkip: new Set([nestedName]) })
    expect(byName.some(file => directoriesOf(file).includes(nestedName))).toBe(false)
  })

  it('returns nothing when the filter accepts no file', () => {
    expect(collectFiles(testSupportRoot, { matches: () => false })).toEqual([])
  })

  it('returns nothing for a directory that does not exist', () => {
    expect(collectFiles(join(frontendRoot, 'no-such-directory'), { matches: () => true })).toEqual([])
  })
})

describe('posixRelative', () => {
  // Windows CI failed five repo guards that compared path.relative output
  // to literals like 'src/app.tsx'. path.relative uses '\' on Windows, so
  // the walk found the file and the strings still disagreed. This case is
  // that comparison: join() builds a native path, and the result must be
  // the POSIX form every allow-list and toEqual() writes.
  it('returns a slash-separated path so Windows matches POSIX literals', () => {
    expect(posixRelative(frontendRoot, join(frontendRoot, 'src', 'app.tsx'))).toBe('src/app.tsx')
    expect(posixRelative(frontendRoot, join(frontendRoot, 'src', 'lib', 'systemInfo.ts')))
      .toBe('src/lib/systemInfo.ts')
    expect(posixRelative(
      frontendRoot,
      join(frontendRoot, 'src', 'components', 'chat', 'results', 'CollapsibleContent.tsx'),
    )).toBe('src/components/chat/results/CollapsibleContent.tsx')
  })

  it('returns an empty string when from and to are the same path', () => {
    expect(posixRelative(frontendRoot, frontendRoot)).toBe('')
  })

  it('returns a parent-relative path with slashes', () => {
    expect(posixRelative(join(frontendRoot, 'src'), join(frontendRoot, 'tests', 'e2e')))
      .toBe('../tests/e2e')
  })
})

describe('frontendRoot', () => {
  it('is the frontend package directory', () => {
    expect(isAbsolute(frontendRoot)).toBe(true)
    expect(existsSync(join(frontendRoot, 'package.json'))).toBe(true)
    expect(posixRelative(frontendRoot, join(frontendRoot, 'src', 'test-support', 'sourceTree.ts')))
      .toBe('src/test-support/sourceTree.ts')
  })
})
