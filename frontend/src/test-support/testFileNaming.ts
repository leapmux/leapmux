// The naming rule that decides which runner collects a test file, as three
// pure functions over a path. `testFileNaming.test.ts` beside this module
// applies them to the real tree.
//
// Separate from the guard because a guard built out of filesystem walks has a
// silent-pass mode: every case there asserts that a list is EMPTY, and a
// predicate that answers "fine" for every input produces an empty list too.
// The guard then passes forever and reports nothing. These functions take a
// string and return a value, so a test states what they must answer for a
// path that breaks the rule -- no fixture tree, no walk.
//
// NOT a `.test.ts`, so vitest does not collect this module as a suite of its
// own. Its cases live in `testFileNaming.test.ts` beside it.

/** Matches a vitest unit test: `foo.test.ts`, `foo.test.tsx`. */
const UNIT_TEST = /\.test\.tsx?$/

/** Matches a Playwright spec: `foo.spec.ts`, `foo.spec.tsx`. */
const PLAYWRIGHT_SPEC = /\.spec\.tsx?$/

/** Reports whether `path` is a vitest unit test. */
export function isUnitTest(path: string): boolean {
  return UNIT_TEST.test(path)
}

/** Reports whether `path` is a Playwright spec. */
export function isPlaywrightSpec(path: string): boolean {
  return PLAYWRIGHT_SPEC.test(path)
}

/**
 * The module a co-located unit test names: `mail.test.ts` -> `mail.ts`, and
 * `Button.test.tsx` -> `Button.tsx`.
 *
 * Anchored at the END of the path, so a directory that happens to hold
 * `.test.` in its name keeps it. Returns the path unchanged when it is not a
 * unit test, which is why every caller filters with `isUnitTest` first: an
 * unchanged path exists on disk, and a co-location check reads that as a
 * module found.
 */
export function siblingModulePath(testPath: string): string {
  return testPath.replace(/\.test(\.tsx?)$/, '$1')
}
