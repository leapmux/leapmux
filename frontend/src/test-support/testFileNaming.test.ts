import { existsSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import { describe, expect, it } from 'vitest'
import { collectE2EFiles } from '~/test-support/e2eFiles'
import { collectFiles, frontendRoot, posixRelative } from '~/test-support/sourceTree'
import { isPlaywrightSpec, isUnitTest, siblingModulePath } from '~/test-support/testFileNaming'

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

const srcRoot = join(frontendRoot, 'src')

/** Every co-located unit test under `tests/e2e/`, as an absolute path. */
function collectE2EUnitTests(): string[] {
  return collectE2EFiles().filter(isUnitTest)
}

describe('test file naming', () => {
  it('keeps every .spec file under tests/e2e, where Playwright collects it', () => {
    const strays = collectFiles(srcRoot, { matches: isPlaywrightSpec })
      .map(file => posixRelative(frontendRoot, file))

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
      .filter(file => !existsSync(siblingModulePath(file)))
      .map(file => posixRelative(frontendRoot, file))

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
    expect(collectFiles(srcRoot, { matches: isUnitTest }).length).toBeGreaterThan(100)
    expect(collectE2EUnitTests().length).toBeGreaterThanOrEqual(2)
  })
})

// The predicates the guard above is built from, against paths rather than
// against the tree. A predicate that answered "fine" for every input would
// leave every case above asserting that an empty list is empty.
describe('isUnitTest', () => {
  it('accepts a .test.ts and a .test.tsx', () => {
    expect(isUnitTest('helpers/mail.test.ts')).toBe(true)
    expect(isUnitTest('components/Button.test.tsx')).toBe(true)
  })

  it('rejects the module under test and a near miss', () => {
    expect(isUnitTest('helpers/mail.ts')).toBe(false)
    expect(isUnitTest('helpers/testing.ts')).toBe(false)
    expect(isUnitTest('helpers/mail.test.ts.bak')).toBe(false)
  })

  it('rejects a Playwright spec, so the two sets never overlap', () => {
    expect(isUnitTest('001-auth.spec.ts')).toBe(false)
  })
})

describe('isPlaywrightSpec', () => {
  it('accepts a .spec.ts and a .spec.tsx', () => {
    expect(isPlaywrightSpec('001-auth.spec.ts')).toBe(true)
    expect(isPlaywrightSpec('001-auth.spec.tsx')).toBe(true)
  })

  it('rejects a unit test and a name that merely contains "spec"', () => {
    expect(isPlaywrightSpec('helpers/mail.test.ts')).toBe(false)
    expect(isPlaywrightSpec('lib/inspector.ts')).toBe(false)
    expect(isPlaywrightSpec('lib/spec.ts')).toBe(false)
  })
})

describe('siblingModulePath', () => {
  it('drops the .test segment and keeps the extension', () => {
    expect(siblingModulePath('tests/e2e/helpers/mail.test.ts'))
      .toBe('tests/e2e/helpers/mail.ts')
    expect(siblingModulePath('src/components/Button.test.tsx'))
      .toBe('src/components/Button.tsx')
  })

  it('does not answer the path it was given for a unit test', () => {
    // The silent-pass mode this whole module exists to close: an unchanged
    // path exists on disk, so the co-location case would find every orphan
    // "co-located" and could never fail again.
    const test = 'tests/e2e/helpers/mail.test.ts'
    expect(siblingModulePath(test)).not.toBe(test)
  })

  it('rewrites only the end of the path', () => {
    // A directory that carries `.test.` in its name is not the test file.
    expect(siblingModulePath('a/x.test.ts/mail.test.ts')).toBe('a/x.test.ts/mail.ts')
  })

  it('leaves a path that is not a unit test alone', () => {
    expect(siblingModulePath('helpers/mail.ts')).toBe('helpers/mail.ts')
    expect(siblingModulePath('001-auth.spec.ts')).toBe('001-auth.spec.ts')
  })
})
