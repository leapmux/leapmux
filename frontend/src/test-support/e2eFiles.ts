import { dirname, join, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { collectFiles } from '~/test-support/sourceTree'

const frontendRoot = resolve(dirname(fileURLToPath(import.meta.url)), '..', '..')

/** The one sanctioned home for tests outside `src/`. */
export const e2eRoot = join(frontendRoot, 'tests', 'e2e')

/**
 * Every e2e spec and helper, recursively, as an absolute path.
 *
 * The enumeration three repo guards scan -- `noNetworkIdleWait.test.ts` (no
 * spec may wait for `networkidle`), `chatRowReads.test.ts` (no two-round-trip
 * read on a chat locator) and `visibleChatLocators.test.ts` (no unscoped chat
 * locator rooted at the page). All three carried a byte-identical copy of the
 * walk, so a change to what counts as an e2e file -- a `.mts` helper, a
 * fixtures directory to skip -- would have moved one guard and left the other
 * two scanning a different set.
 *
 * `.ts` rather than `.spec.ts`: a helper under `tests/e2e/helpers/` runs inside
 * the same page and breaks the same rules.
 *
 * NOT a `.test.ts`, so vitest does not collect this module as a suite of its
 * own.
 */
export function collectE2EFiles(): string[] {
  return collectFiles(e2eRoot, { matches: name => name.endsWith('.ts') })
}
