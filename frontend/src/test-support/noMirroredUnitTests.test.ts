import { existsSync } from 'node:fs'
import { dirname, join, relative, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { describe, expect, it } from 'vitest'
import { collectFiles } from '~/test-support/sourceTree'

// Repo layout guard: unit tests are co-located next to the code they test
// (`foo.ts` -> `foo.test.ts` in the same directory under `src/`). The old
// `tests/unit/` mirror -- which duplicated the `src/` tree and let the same
// module be tested in two places that then drifted -- has been retired.
//
// This guard fails the suite if a `*.test.*` unit test reappears anywhere
// under `tests/`, closing the failure mode where a search scoped to `src/`
// misses a mirrored copy and a duplicate gets created. Only Playwright E2E
// specs (`*.spec.ts` under `tests/e2e/`) legitimately live outside `src/`,
// so they are ignored.

const frontendRoot = resolve(dirname(fileURLToPath(import.meta.url)), '..', '..')
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
  }).map(file => relative(frontendRoot, file))
}

describe('unit-test co-location', () => {
  it('has no mirrored unit tests under tests/ (they must live beside the code in src/)', () => {
    const stray = collectStrayUnitTests()
    expect(
      stray,
      `Unit tests must be co-located under src/ (foo.ts -> foo.test.ts). `
      + `Move these next to the module they test and delete the tests/ copy:\n  ${stray.join('\n  ')}`,
    ).toEqual([])
  })

  it('does not resurrect the retired tests/unit mirror', () => {
    expect(
      existsSync(join(testsRoot, 'unit')),
      'The tests/unit/ mirror was retired; do not recreate it. Co-locate unit tests under src/.',
    ).toBe(false)
  })
})
