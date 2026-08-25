import { dirname, join, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { collectFiles } from '~/test-support/sourceTree'

const frontendRoot = resolve(dirname(fileURLToPath(import.meta.url)), '..', '..')

/** The one sanctioned home for tests outside `src/`. */
export const e2eRoot = join(frontendRoot, 'tests', 'e2e')

/**
 * Every e2e spec, helper and co-located unit test, recursively, as an absolute
 * path.
 *
 * The enumeration four repo guards scan -- `noNetworkIdleWait.test.ts` (no
 * spec may wait for `networkidle`), `chatRowReads.test.ts` (no two-round-trip
 * read on a chat locator), `visibleChatLocators.test.ts` (no unscoped chat
 * locator rooted at the page) and `testFileNaming.test.ts` (a `.test.ts` here
 * names the module beside it). The first three carried a byte-identical copy
 * of the walk, so a change to what counts as an e2e file -- a `.mts` helper, a
 * fixtures directory to skip -- would have moved one guard and left the other
 * two scanning a different set.
 *
 * `.ts` rather than `.spec.ts`: a helper under `tests/e2e/helpers/` runs inside
 * the same page and breaks the same rules. That widened the set to the
 * `.test.ts` unit tests beside those helpers, which run under vitest and reach
 * no page at all. They stay in the set: the three page rules cost them nothing
 * to satisfy, and a scan that is too wide reports a file the author can fix,
 * where one that is too narrow reports nothing.
 *
 * NOT a `.test.ts`, so vitest does not collect this module as a suite of its
 * own.
 */
export function collectE2EFiles(): string[] {
  return collectFiles(e2eRoot, { matches: name => name.endsWith('.ts') })
}
