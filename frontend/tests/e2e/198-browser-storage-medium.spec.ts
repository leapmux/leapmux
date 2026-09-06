import type { Page } from '@playwright/test'
import { accountStorageKey, KEY_BROWSER_PREFS, KEY_CLIENT_ID } from '../../src/lib/browserStorage'
import { expect, test } from './fixtures'
import { databaseStores, webStorageKeys } from './helpers/storage'
import { loginViaToken, openSettingsAt, pickTheme } from './helpers/ui'

/**
 * WHERE the browser keeps its state, asserted end to end.
 *
 * The `leapmux:` family moved off localStorage and onto IndexedDB. Every other
 * spec asserts a BEHAVIOUR that would survive either medium -- a theme that
 * sticks, an account that does not inherit another's overrides -- so none of
 * them notices a value that quietly went back to `setItem`. This one asserts
 * the medium itself, which is the only statement that catches that.
 *
 * It also drives the CROSS-TAB path with two real tabs. That path changed
 * mechanism entirely: localStorage raised a `storage` event on every write, and
 * IndexedDB raises nothing, so `~/lib/browserStorageDb` carries committed
 * changes over a BroadcastChannel instead. The unit tests cover each half --
 * the publisher, and `PreferencesContext`'s subscriber -- but only a second
 * real tab covers the join.
 */

/** The three databases this app declares, and the stores each one must hold. */
const DECLARED = {
  'leapmux-kv': ['entries'],
  'leapmux-crdt-state': ['checkpointChunks', 'checkpoints', 'opLog'],
} as const

/** Set the palette as a device-tier override, through the Preferences dialog. */
async function overrideThemeOnThisDevice(page: Page, palette: string) {
  const dialog = await openSettingsAt(page, 'appearance')

  const chip = dialog.getByTestId('scope-chip-appearance.theme')
  await chip.click()
  await page.getByRole('menuitemradio', { name: 'Override on this device' }).click()
  await expect(chip).toHaveText(/This device/)

  const themeRow = dialog.locator('[data-setting-id="appearance.theme"]')
  await pickTheme(themeRow, palette)
  await expect(page.locator('html')).toHaveAttribute('data-ui-theme', palette)

  await page.keyboard.press('Escape')
  await expect(dialog).toBeHidden()
}

test.describe('browser storage medium', () => {
  test('keeps the leapmux family in IndexedDB and nothing in localStorage', async ({ page, leapmuxServer }) => {
    await loginViaToken(page, leapmuxServer.adminToken)
    await page.goto('/')
    // A device-tier write, so there is certainly a row to find.
    await overrideThemeOnThisDevice(page, 'nord')

    const prefsKey = accountStorageKey(leapmuxServer.adminUserId, KEY_BROWSER_PREFS)
    // POLLED: writes go behind, so the row reaches disk a moment after the UI
    // settles.
    await expect.poll(
      async () => (await databaseStores(page, 'leapmux-kv')) ?? [],
      'the key-value database must exist with its declared store',
    ).toEqual([...DECLARED['leapmux-kv']])

    // THE HEADLINE. Every `leapmux:` key lives in IndexedDB now, and the
    // retirement sweep deletes anything the old build left in localStorage. A
    // key here means either a caller that bypassed the gateway, or a family
    // that was never migrated -- and both read as working until a second
    // account signs in to the same browser.
    await expect.poll(
      async () => (await webStorageKeys(page, 'local'))
        .filter(key => key.startsWith('leapmux:') || key.startsWith('leapmux-')),
      'no leapmux key may survive in localStorage',
    ).toEqual([])

    // sessionStorage KEEPS its family: it is per tab and dies with the tab, and
    // that lifetime is load-bearing for the CRDT client identity and every tab /
    // tile pointer. IndexedDB is per origin and cannot express it.
    const sessionKeys = await webStorageKeys(page, 'session')
    expect(
      sessionKeys,
      'the CRDT client identity must still be a per-tab sessionStorage key',
    ).toContain(accountStorageKey(leapmuxServer.adminUserId, KEY_CLIENT_ID))
    // And it is still SCOPED: nothing of the app's own sits at a flat name.
    expect(sessionKeys.filter(key => key.startsWith('leapmux:'))).not.toEqual([])

    // The row the theme override wrote, read back through the same layout the
    // app composes. `v` is the unwrapped value -- a structured clone, not a
    // JSON envelope -- so the palette is readable without a parse.
    const rows = await page.evaluate(dbName => new Promise<unknown[]>((resolve) => {
      const request = indexedDB.open(dbName)
      request.onerror = () => resolve([])
      request.onsuccess = () => {
        const db = request.result
        const all = db.transaction('entries', 'readonly').objectStore('entries').getAll()
        all.onsuccess = () => {
          db.close()
          resolve(all.result as unknown[])
        }
        all.onerror = () => {
          db.close()
          resolve([])
        }
      }
    }), 'leapmux-kv')
    const prefs = (rows as Array<{ k: string, v: { theme?: { name?: string } } }>)
      .find(row => row.k === prefsKey)
    expect(prefs?.v.theme?.name, 'the palette must be readable as a structured value').toBe('nord')
  })

  test('builds the CRDT checkpoint database with its declared stores', async ({ page, leapmuxServer }) => {
    await loginViaToken(page, leapmuxServer.adminToken)
    await page.goto('/')

    // The CRDT runtime opens this on its first checkpoint write, which follows
    // the projection the shell hydrates from -- hence the poll rather than a
    // read straight after the load.
    await expect.poll(
      async () => (await databaseStores(page, 'leapmux-crdt-state')) ?? [],
      'the checkpoint database must exist with all three declared stores',
    ).toEqual([...DECLARED['leapmux-crdt-state']])
  })

  test('carries a preference change to a second tab', async ({ page, leapmuxServer }) => {
    await loginViaToken(page, leapmuxServer.adminToken)
    await page.goto('/')
    await expect(page.locator('html')).toHaveAttribute('data-ui-theme', /.+/)

    // A SECOND REAL TAB on the same origin, which is what makes this a test of
    // the BroadcastChannel rather than of one tab talking to itself. It comes
    // from THIS page's context: that is the one the harness gave a `baseURL`
    // and the one holding the session cookie, so the second tab signs in as the
    // same account without repeating the login.
    const second = await page.context().newPage()
    await second.goto('/')
    await expect(second.locator('html')).toHaveAttribute('data-ui-theme', /.+/)

    await overrideThemeOnThisDevice(page, 'nord')

    // WITHOUT a reload. localStorage raised a `storage` event that carried this
    // for free; IndexedDB raises nothing, so a channel message published after
    // the transaction commits is the only thing that can move the second tab.
    await expect(
      second.locator('html'),
      'the second tab must follow the palette without reloading',
    ).toHaveAttribute('data-ui-theme', 'nord')

    await second.close()
  })
})
