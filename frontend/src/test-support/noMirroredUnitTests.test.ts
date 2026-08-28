import { existsSync } from 'node:fs'
import { join } from 'node:path'
import { describe, expect, it } from 'vitest'
import { collectFiles, frontendRoot, posixRelative } from '~/test-support/sourceTree'

// Repo layout guard: unit tests are co-located next to the code they test
// (`foo.ts` -> `foo.test.ts` in the same directory). The old `tests/unit/`
// mirror -- which duplicated the `src/` tree and let the same module be
// tested in two places that then drifted -- has been retired.
//
// This guard fails the suite if a `*.test.*` unit test reappears anywhere
// under `tests/`, closing the failure mode where a search scoped to `src/`
// misses a mirrored copy and a duplicate gets created.
//
// `tests/e2e/` is exempt, because co-location applies there too: the E2E
// helpers carry their own `.test.ts` files, which run under vitest like any
// other unit test. That directory has its own rules, and
// `testFileNaming.test.ts` enforces them -- a `.test.ts` there must name the
// module beside it, so the exemption cannot become a second mirror.

const testsRoot = join(frontendRoot, 'tests')

const UNIT_TEST_FILE = /\.test\.(?:ts|tsx)$/

/** Every unit test that strayed under `tests/`, as a repo-relative path. */
function collectStrayUnitTests(): string[] {
  return collectFiles(testsRoot, {
    matches: name => UNIT_TEST_FILE.test(name),
    // `tests/e2e/` is the one sanctioned home for tests outside `src/`.
    // An EXACT path, not a basename: a basename skip would also exempt
    // `tests/unit/e2e/`, so the retired mirror this guard exists to keep
    // out could return under one directory name.
    skipPaths: new Set(['e2e']),
  }).map(file => posixRelative(frontendRoot, file))
}

describe('unit-test co-location', () => {
  it('has no mirrored unit tests under tests/ (they must live beside the module they test)', () => {
    const stray = collectStrayUnitTests()
    expect(
      stray,
      `Unit tests must be co-located with the module they test (foo.ts -> foo.test.ts). `
      + `Move these next to that module and delete the tests/ copy:\n  ${stray.join('\n  ')}`,
    ).toEqual([])
  })

  it('does not resurrect the retired tests/unit mirror', () => {
    expect(
      existsSync(join(testsRoot, 'unit')),
      'The tests/unit/ mirror was retired; do not recreate it. Co-locate a unit test '
      + 'with the module it tests.',
    ).toBe(false)
  })

  it('walks a tests/ tree that holds files, so the case above cannot pass vacuously', () => {
    // `collectFiles` answers an empty array for a directory that moved, and an
    // empty array satisfies the emptiness assertion above. Drop the skip and
    // the walk must still find the E2E specs.
    expect(collectFiles(testsRoot, { matches: name => name.endsWith('.spec.ts') }).length)
      .toBeGreaterThan(100)
  })
})
