import type { Page } from '@playwright/test'
import { expect, test } from './fixtures'
import { createWorkspaceViaAPI, deleteWorkspaceViaAPI } from './helpers/api'
import { gotoWorkspace, openWorkspace } from './helpers/ui'

/**
 * A new tab resumes from a sibling tab's persisted checkpoint (issue #358).
 *
 * ONE browser context, TWO pages, and that is the whole point: IndexedDB is
 * per-ORIGIN, so both tabs see the same checkpoint store, while sessionStorage
 * is per-TAB, so the second tab mints a fresh CRDT client id and finds no
 * checkpoint under it. Before this change that meant no resume cursor and a
 * full `UserMaterialized` snapshot on every new tab; now it seeds from the
 * sibling and RESUMEs.
 *
 * Two separate contexts -- which 150-crdt-convergence and 151-active-client-ding
 * use -- have separate IndexedDB and could never exercise this.
 *
 * What is asserted is that the new tab PRESENTS A CURSOR (`resume_after_hlc` on
 * its `/ws/userevents` URL). Whether the hub then honours it is
 * `SubscribeWithACL`'s decision and is covered by hub tests; the contract this
 * change owns is the client half.
 */

/** Collect every `/ws/userevents` URL this page opens, from now on. */
function userEventsUrls(page: Page): string[] {
  const urls: string[] = []
  page.on('websocket', (ws) => {
    if (ws.url().includes('/ws/userevents'))
      urls.push(ws.url())
  })
  return urls
}

/**
 * Wait until this origin's checkpoint store holds at least `n` owner rows.
 *
 * The write is asynchronous and off the interaction path, so a tab can be fully
 * interactive before its checkpoint lands. Polling the store beats a sleep: it
 * waits for the actual precondition (a sibling row exists to seed FROM) rather
 * than for a guess at how long that takes.
 */
async function waitForCheckpointOwners(page: Page, n: number): Promise<void> {
  await expect.poll(async () => page.evaluate(async () => {
    return await new Promise<number>((resolve) => {
      const open = indexedDB.open('leapmux-crdt-state')
      open.onerror = () => resolve(-1)
      open.onsuccess = () => {
        const db = open.result
        if (!db.objectStoreNames.contains('checkpoints')) {
          db.close()
          resolve(-1)
          return
        }
        const req = db.transaction('checkpoints', 'readonly').objectStore('checkpoints').count()
        req.onerror = () => {
          db.close()
          resolve(-1)
        }
        req.onsuccess = () => {
          db.close()
          resolve(req.result)
        }
      }
    })
  })).toBeGreaterThanOrEqual(n)
}

test.describe('new-tab checkpoint seeding', () => {
  test('a second tab in the same profile resumes from its sibling\'s checkpoint', async ({ browser, leapmuxServer }) => {
    const { hubUrl, adminToken } = leapmuxServer
    const wsId = await createWorkspaceViaAPI(hubUrl, adminToken, 'New Tab Seeding')

    const context = await browser.newContext({ baseURL: hubUrl })
    const pageA = await context.newPage()

    try {
      await gotoWorkspace(pageA, adminToken, wsId)
      await expect(pageA.locator('[data-testid="tile"]')).toHaveCount(1)
      // A mutation, so the CRDT holds something a seeded tab can be seen to
      // have received rather than rebuilt.
      await pageA.locator('[data-testid="split-horizontal"]').first().click()
      await expect(pageA.locator('[data-testid="tile"]')).toHaveCount(2)

      // Tab A's checkpoint must exist before tab B can seed from it.
      await waitForCheckpointOwners(pageA, 1)

      // Tab B: same context (shared IndexedDB), fresh sessionStorage.
      const pageB = await context.newPage()
      const urlsB = userEventsUrls(pageB)
      await openWorkspace(pageB, wsId)
      await expect(pageB.locator('[data-testid="tile"]')).toHaveCount(2)

      await expect.poll(() => urlsB.at(0) ?? '').toContain('resume_after_hlc=')

      // TWO owner rows, not one: B copied the checkpoint under its OWN client
      // id rather than adopting A's key, which is what keeps one writer per
      // owner and leaves A's own persistence untouched.
      await waitForCheckpointOwners(pageB, 2)

      // And A is unharmed -- it still converges with B afterwards.
      await pageB.locator('[data-testid="split-vertical"]').nth(1).click()
      await expect(pageB.locator('[data-testid="tile"]')).toHaveCount(3)
      await expect(pageA.locator('[data-testid="tile"]')).toHaveCount(3)
    }
    finally {
      await context.close()
      await deleteWorkspaceViaAPI(hubUrl, adminToken, wsId)
    }
  })

  test('a tab opened after its sibling closed still seeds from the dead row', async ({ browser, leapmuxServer }) => {
    // The browser-restart case: sessionStorage died with the tab, but the
    // checkpoint rows survive in IndexedDB. The issue's trigger table listed
    // this separately from "new tab"; one mechanism covers both.
    const { hubUrl, adminToken } = leapmuxServer
    const wsId = await createWorkspaceViaAPI(hubUrl, adminToken, 'Dead Sibling Seeding')

    const context = await browser.newContext({ baseURL: hubUrl })
    const pageA = await context.newPage()

    try {
      await gotoWorkspace(pageA, adminToken, wsId)
      await expect(pageA.locator('[data-testid="tile"]')).toHaveCount(1)
      await pageA.locator('[data-testid="split-horizontal"]').first().click()
      await expect(pageA.locator('[data-testid="tile"]')).toHaveCount(2)
      await waitForCheckpointOwners(pageA, 1)
      await pageA.close()

      const pageC = await context.newPage()
      const urlsC = userEventsUrls(pageC)
      await openWorkspace(pageC, wsId)
      await expect(pageC.locator('[data-testid="tile"]')).toHaveCount(2)

      await expect.poll(() => urlsC.at(0) ?? '').toContain('resume_after_hlc=')
    }
    finally {
      await context.close()
      await deleteWorkspaceViaAPI(hubUrl, adminToken, wsId)
    }
  })
})
