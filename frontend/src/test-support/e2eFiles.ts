import { join } from 'node:path'
import { collectFiles, frontendRoot } from '~/test-support/sourceTree'

/** The one sanctioned home for tests outside `src/`. */
export const e2eRoot = join(frontendRoot, 'tests', 'e2e')

/**
 * Every e2e spec, helper and co-located unit test, recursively, as an absolute
 * path.
 *
 * The enumeration that these repo guards scan:
 *
 * - `noNetworkIdleWait.test.ts`: no spec may wait for `networkidle`.
 * - `chatRowReads.test.ts`: no two-round-trip read on a chat locator.
 * - `visibleChatLocators.test.ts`: no unscoped chat locator rooted at the page.
 * - `testFileNaming.test.ts`: a `.test.ts` here identifies the module beside it.
 * - `throwingPollReads.test.ts`: no `expect.poll` waits on a Worker or Hub read.
 * - `fixedWaits.test.ts`: no spec or helper sizes a window with `waitForTimeout`.
 * - `platformModifierKeys.test.ts`: no key chord presses a fixed Meta in place
 *   of `PLATFORM_MOD`.
 * - `expectImports.test.ts`: no module passes the plain `expect` of Playwright
 *   on.
 * - `escapedIdSelectors.test.ts`: no spec or helper interpolates an unescaped
 *   value into the selector of an ID attribute.
 *
 * One walk serves every guard. A change to what counts as an e2e file -- a
 * `.mts` helper, a fixtures directory to skip -- thus moves every guard, and
 * no guard scans a different set.
 *
 * Scan `.ts` and `.tsx`, including helpers and tests. A helper under `tests/e2e/helpers/` runs inside
 * the same page and breaks the same rules. That widened the set to the
 * `.test.ts` unit tests beside those helpers, which run under vitest and reach
 * no page at all. They stay in the set: the page rules cost them nothing to
 * satisfy, and a scan that is too wide reports a file the author can fix,
 * where one that is too narrow reports nothing.
 *
 * NOT a `.test.ts`, so vitest does not collect this module as a suite of its
 * own.
 */
export function collectE2EFiles(): string[] {
  // A misplaced TSX spec must reach the layout guard even though Playwright does not collect it.
  return collectFiles(e2eRoot, {
    matches: name => name.endsWith('.ts') || name.endsWith('.tsx'),
  })
}
