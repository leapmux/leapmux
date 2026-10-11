import type { Browser, BrowserContext, Page } from '@playwright/test'
import { test } from '@playwright/test'
import { finishCleanup, withCleanup } from './cleanup'
import { attachToastLog, installToastRecorder } from './toast'

/**
 * Extra browser clients for a spec that drives more than one session of the same account.
 *
 * Each client is a page in a context of its own, so it has its own cookies, its own browser storage, and its own
 * CRDT client ID, as a second device does. A spec that needs two pages in ONE context, which share IndexedDB, opens
 * them itself (see `162-new-tab-seeds-from-sibling.spec.ts`).
 */

/** The number of extra clients that one call can open. */
export type ExtraClientCount = 1 | 2 | 3

/** A tuple of `N` pages, so that a caller can destructure every page with no `undefined` case. */
export type ExtraClientPages<N extends ExtraClientCount, Pages extends Page[] = []>
  = Pages['length'] extends N ? Pages : ExtraClientPages<N, [...Pages, Page]>

/**
 * Open `count` extra clients of the hub at `server.hubUrl`, run `use` with their pages, and close every client.
 *
 * - Each page records its toasts from the start, as the fixture page does, so `expectToastRecorded` and
 *   `dangerToasts` work on it.
 * - Before the close, the helper attaches the toasts of each page to the report as `toast-log-client-<n>`.
 * - The close runs after a failure also. It closes every context, also when an attachment or another close fails.
 *   When the test and the close both fail, the helper throws one `AggregateError` that holds both errors, the error
 *   of the test first.
 *
 * The pages are not signed in. The caller signs each one in, because the session under test differs between specs.
 */
export async function withExtraClients<N extends ExtraClientCount, R>(
  browser: Browser,
  server: { hubUrl: string },
  count: N,
  use: (pages: ExtraClientPages<N>) => Promise<R>,
): Promise<R> {
  if (!Number.isSafeInteger(count) || count < 1 || count > 3)
    throw new RangeError(`withExtraClients opens one to three clients, not ${count}.`)
  const contexts: BrowserContext[] = []
  const pages: Page[] = []
  return withCleanup(async () => {
    for (let index = 0; index < count; index++) {
      const context = await browser.newContext({ baseURL: server.hubUrl })
      contexts.push(context)
      const page = await context.newPage()
      pages.push(page)
      await installToastRecorder(page)
    }
    return use(pages as ExtraClientPages<N>)
  }, () => finishCleanup(contexts.map(async (context, index) => {
    try {
      const page = pages[index]
      if (page)
        await attachToastLog(page, test.info(), `toast-log-client-${index + 1}`)
    }
    finally {
      await context.close()
    }
  })))
}
