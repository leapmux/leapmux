/**
 * Unit tests for the background-task / session-goal E2E helper.
 *
 * A `.test.ts` under `tests/e2e/` runs under vitest, not Playwright: it needs
 * no browser and no hub, so it costs milliseconds and belongs to
 * `task test-frontend`. Both runner configs are pinned to that rule, and
 * `src/test-support/testFileNaming.test.ts` fails the suite if this file is
 * ever renamed to `.spec.ts`.
 *
 * It asserts over the helper's SOURCE and imports nothing from it, which is not
 * a shortcut: `./subagentRegistry.ts` imports the Playwright fixtures, so a
 * unit test that imported it would drag a browser-only module graph into
 * vitest and fail to load. The logic that CAN be imported lives in
 * `./goalTransitions.ts` and is tested beside itself.
 */
import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { describe, expect, it } from 'vitest'

/**
 * Every locator this helper builds for a DUPLICATED surface must be
 * `:visible`-scoped.
 *
 * The sidebar is mounted twice (the desktop tree and the mobile one both
 * render) and ChatView renders each unmeasured row twice, so a bare test id
 * matches two elements. Playwright's strict mode then throws on every call --
 * or, worse, `.first()` silently picks the off-screen copy and the assertion
 * reads state nobody can see.
 *
 * Asserted over the SOURCE rather than a rendered page, because that is what
 * makes it cheap enough to run on every commit; the alternative is discovering
 * it from a spec timeout.
 */
describe('registry locators', () => {
  /** Test ids that name a surface the app mounts more than once. */
  const DUPLICATED = ['bg-task-', 'goal-', 'section-header-']

  it('scopes every duplicated-surface locator to :visible', () => {
    const source = readFileSync(join(import.meta.dirname, 'subagentRegistry.ts'), 'utf-8')
    // The body excludes only the DELIMITER, via a lookahead, rather than every
    // quote character: a selector is `'[data-testid="x"]'`, so a class that
    // banned all three quotes stopped at the first inner `"` and matched
    // nothing at all -- which would have made this assertion vacuous.
    const locators = [...source.matchAll(/page\.locator\(\s*(['"`])((?:(?!\1).)*)\1/g)]
      .map(match => match[2])
    // The regex has to find something, or a rewrite of the helper -- or a
    // pattern that silently stops matching -- would make this pass by finding
    // nothing to check.
    expect(locators.length).toBeGreaterThan(5)

    const offenders = locators.filter(selector =>
      DUPLICATED.some(id => selector.includes(`data-testid="${id}`)) && !selector.includes(':visible'),
    )
    expect(offenders).toEqual([])
  })
})
