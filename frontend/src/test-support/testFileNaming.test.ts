import { existsSync, readFileSync } from 'node:fs'
import { dirname, join, relative, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { describe, expect, it } from 'vitest'
import { collectE2EFiles } from '~/test-support/e2eFiles'
import { collectFiles } from '~/test-support/sourceTree'

// Repo layout guard: the file EXTENSION decides which runner collects a test,
// everywhere in the repo. `.spec.ts` is Playwright, `.test.ts` is vitest.
//
// The rule exists because the previous split was by DIRECTORY: vitest excluded
// `tests/e2e/**` wholesale, and Playwright's default `testMatch` took every
// `*.spec.ts` AND `*.test.ts` under it. A unit test beside the helper it tests
// therefore landed in the E2E suite, inside a browser worker with a whole
// `leapmux dev` instance behind it, where vitest's API does not exist. The two
// unit tests that needed a home were written into `src/test-support/` instead,
// reaching back across the tree with a relative import.
//
// Now the extension decides, and the two runners cannot both claim a file --
// or both skip one. Three cases below, one for each way back into that hole:
// a spec that strays out of `tests/e2e/`, a `.test.ts` under `tests/e2e/` that
// names no module it tests (a mistyped E2E spec, which vitest now collects and
// Playwright ignores), and a runner config that stops enforcing its half.

const frontendRoot = resolve(dirname(fileURLToPath(import.meta.url)), '..', '..')
const srcRoot = join(frontendRoot, 'src')

const UNIT_TEST = /\.test\.tsx?$/
const PLAYWRIGHT_SPEC = /\.spec\.tsx?$/

/** Every co-located unit test under `tests/e2e/`, as an absolute path. */
function collectE2EUnitTests(): string[] {
  return collectE2EFiles().filter(file => UNIT_TEST.test(file))
}

describe('test file naming', () => {
  it('keeps every .spec file under tests/e2e, where Playwright collects it', () => {
    const strays = collectFiles(srcRoot, { matches: name => PLAYWRIGHT_SPEC.test(name) })
      .map(file => relative(frontendRoot, file))

    expect(
      strays,
      'A `.spec` file names itself a Playwright spec, but `testDir` is `tests/e2e`, so '
      + 'Playwright never sees these. Rename them to `.test.ts` (vitest) or move them '
      + `under tests/e2e/:\n  ${strays.join('\n  ')}`,
    ).toEqual([])
  })

  it('co-locates every .test.ts under tests/e2e with the module it tests', () => {
    // `mail.test.ts` must sit beside `mail.ts`. A file that names no sibling is
    // almost always a spec whose author typed `.test.ts`: vitest would collect
    // it, its Playwright fixtures would be undefined, and Playwright itself
    // would never run it.
    const orphans = collectE2EUnitTests()
      .filter(file => !existsSync(file.replace(/\.test(\.tsx?)$/, '$1')))
      .map(file => relative(frontendRoot, file))

    expect(
      orphans,
      'A `.test.ts` under tests/e2e/ is a vitest unit test, and it must sit beside the '
      + 'module it tests. If you meant to write an E2E spec, rename it to `.spec.ts`:'
      + `\n  ${orphans.join('\n  ')}`,
    ).toEqual([])
  })

  it('pins both runner configs to the extension rule', () => {
    // Without this case the guard above still passes when someone widens the
    // vitest exclude back to `tests/e2e/**`: the co-located tests stay exactly
    // where they belong and stop running, silently, which is the failure the
    // whole rule exists to remove.
    const vitestConfig = readFileSync(join(frontendRoot, 'vitest.config.ts'), 'utf8')
    const playwrightConfig = readFileSync(join(frontendRoot, 'playwright.config.ts'), 'utf8')

    expect(
      vitestConfig,
      'vitest must exclude the Playwright specs BY NAME. Excluding `tests/e2e/**` also '
      + 'drops the unit tests co-located with the helpers, and nothing else runs them.',
    ).toContain('\'tests/e2e/**/*.spec.ts\'')
    expect(
      vitestConfig,
      'The whole-directory pattern is back in vitest.config.ts. If it is only quoted '
      + 'inside a comment, re-word the comment: this case reads the file as text and '
      + 'cannot tell a comment from the exclude list.',
    ).not.toContain('\'tests/e2e/**\'')

    expect(
      playwrightConfig,
      'Playwright must be pinned to `.spec.ts`. Its default `testMatch` also takes '
      + '`*.test.ts`, which drags the vitest unit tests into a browser worker.',
    ).toContain('testMatch: \'**/*.spec.ts\'')
  })

  it('finds the files it judges, so no case above passes vacuously', () => {
    // Both walks return an empty array for a directory that moved, and an
    // empty array satisfies every emptiness assertion above.
    expect(collectFiles(srcRoot, { matches: name => UNIT_TEST.test(name) }).length)
      .toBeGreaterThan(100)
    expect(collectE2EUnitTests().length).toBeGreaterThanOrEqual(2)
  })
})
