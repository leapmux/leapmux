import type { Page } from '@playwright/test'
import type { SoloServerHandle } from './helpers/devServer'
import { connect } from 'node:net'
import { expect, test } from './fixtures'
import { startSoloServer, stopSoloServer } from './helpers/devServer'
import { findFreePort } from './helpers/server'
import { logoutViaUI, openSettingsAt } from './helpers/ui'

/** A password the hub's own validator accepts. */
const SOLO_PASSWORD = 'correct-horse-battery-staple'
const SOLO_ENV = { LEAPMUX_HUB_DEV_FRONTEND: undefined }

async function completeSoloSetup(page: Page) {
  const gate = page.getByTestId('password-setup-gate')
  await expect(gate).toBeVisible()
  await gate.getByLabel('New Password').fill(SOLO_PASSWORD)
  await gate.getByLabel('Confirm Password').fill(SOLO_PASSWORD)
  await gate.getByRole('button', { name: 'Set Password' }).click()
  await expect(gate).toBeHidden()
}

/**
 * Whether an address ACCEPTS a TCP connection.
 *
 * A raw connect, never an HTTP client. Closing a listener stops it accepting
 * and leaves every connection it already accepted open -- which is right, and
 * which makes a POOLED connection answer long after the address stopped
 * serving. Playwright's request contexts share a pool, so an HTTP probe could
 * never observe a removal; accepting is also the exact property the listener
 * controls.
 */
async function accepts(port: number): Promise<boolean> {
  return new Promise((resolve) => {
    const socket = connect({ host: '127.0.0.1', port })
    const done = (answer: boolean) => {
      socket.destroy()
      resolve(answer)
    }
    socket.setTimeout(2000)
    socket.once('connect', () => done(true))
    socket.once('error', () => done(false))
    socket.once('timeout', () => done(false))
  })
}

/**
 * The Network access panel, on a real `leapmux solo` hub.
 *
 * A solo instance of its own, not the shared `leapmux dev` fixture. The
 * additional listen-address row exists only in solo mode. The trusted-proxy
 * row also appears in hub and dev modes.
 */
test.describe('Network access', () => {
  let solo: SoloServerHandle | undefined

  test.beforeEach(async () => {
    solo = await startSoloServer({ env: SOLO_ENV })
  })

  test.afterEach(async () => {
    await stopSoloServer(solo)
  })

  test('publishes an address, then asks every address for the password', async ({ page }) => {
    await page.goto(`${solo!.hubUrl}/`)
    await completeSoloSetup(page)

    const dialog = await openSettingsAt(page, 'admin-network')
    const row = dialog.locator('[data-setting-id="extra_listen_addresses"]')
    await expect(row).toBeVisible()

    // -listen is reported read-only: it is a command-line option, never a
    // setting, and the panel adds addresses BESIDE it.
    await expect(row.getByText(solo!.listen)).toBeVisible()
    await expect(row.getByText('from -listen')).toBeVisible()

    // A SECOND port, beside the one the hub already serves on. The row
    // defaults to every interface, which is what makes the hub reachable from
    // another machine -- and therefore what demands the password.
    const port = await findFreePort()
    await row.getByRole('button', { name: '+ Add address' }).click()
    await row.getByLabel('Port').fill(String(port))

    await expect(row.getByRole('button', { name: 'Apply' })).toBeEnabled()

    await row.getByRole('button', { name: 'Apply' }).click()
    await expect(row.getByText('Network access updated.')).toBeVisible()

    // The hub answers on the new address straight away -- no restart.
    expect(await accepts(port)).toBe(true)

    // The write that stored the password is what started demanding one, and
    // the page that made it must NOT be signed out of the form it is in.
    await expect(row.getByText(/Change it in Account → Password/)).toBeVisible()

    // Account → Password is where it changes from here. It is the one Account
    // row solo keeps; its neighbours stay hidden, because solo refuses the
    // RPCs behind them. Setting the FIRST password there is 194's case.
    const account = await openSettingsAt(page, 'account')
    await expect(account.locator('[data-setting-id="account.password"]')).toBeVisible()
    await expect(account.locator('[data-setting-id="account.profile"]')).toHaveCount(0)
    await expect(account.locator('[data-setting-id="account.passkeys"]')).toHaveCount(0)

    // And the rule is armed: a browser with NO session lands on the sign-in
    // form even at the -listen address, which is loopback. Loopback buys no
    // exemption once the account holds a password.
    //
    // The cookie has to go first: applying handed THIS browser a session, so a
    // bare reload would prove only that a signed-in browser stays signed in.
    await page.context().clearCookies()
    await page.reload()
    await expect(page.getByRole('button', { name: 'Sign in' })).toBeVisible()

    // The username is fixed: a solo hub has exactly one account.
    const username = page.getByLabel('Username')
    await expect(username).toHaveValue('solo')
    await expect(username).toHaveAttribute('readonly', '')

    await page.getByLabel('Password').fill(SOLO_PASSWORD)
    await page.getByRole('button', { name: 'Sign in' }).click()
    await expect(page.getByRole('button', { name: 'Sign in' })).toBeHidden()

    // The session can be ended. This browser holds a real TCP session, and the
    // user must be able to sign out again.
    //
    // The dialog is closed first because it is addressable -- its open state
    // lives in `?prefs=` -- so the reload above brought it back over the app
    // menu that carries the item.
    await dialog.getByRole('button', { name: 'Close' }).click()
    await expect(dialog).toBeHidden()
    await logoutViaUI(page)
  })

  test('removes an address and stops answering there', async ({ page }) => {
    await page.goto(`${solo!.hubUrl}/`)
    await completeSoloSetup(page)
    const dialog = await openSettingsAt(page, 'admin-network')
    const row = dialog.locator('[data-setting-id="extra_listen_addresses"]')

    const port = await findFreePort()
    await row.getByRole('button', { name: '+ Add address' }).click()
    await row.getByLabel('Port').fill(String(port))
    await row.getByRole('button', { name: 'Apply' }).click()
    await expect(row.getByText('Network access updated.')).toBeVisible()
    expect(await accepts(port)).toBe(true)

    // Remove it and apply again. The OUTCOME is the assertion -- the status
    // line already reads "updated" from the first apply, so re-reading it
    // would pass whether or not this second one ran.
    await row.getByRole('button', { name: /^Remove / }).click()
    await expect(row.getByTestId('network-address-row')).toHaveCount(0)
    await row.getByRole('button', { name: 'Apply' }).click()
    // The panel reports its own outcome on the STATUS LINE -- `apply` calls
    // `props.binding.set` directly, so the row's `setting-error-*` slot can
    // never fill for it -- and a refused write leaves the failure text there
    // instead of the success. Asserting the success is what waits for the
    // second write to land at all.
    await expect(row.getByText('Network access updated.')).toBeVisible()
    await expect(row.getByText(/Failed to apply/)).toBeHidden()
    await expect(async () => {
      expect(await accepts(port)).toBe(false)
    }).toPass()

    // And the -listen address is never dropped, whatever the list says: Apply
    // merges it back in every time.
    expect(await accepts(Number(solo!.listen.split(':').pop()))).toBe(true)
  })

  test('stores trusted proxy providers symbolically', async ({ page }) => {
    await page.goto(`${solo!.hubUrl}/`)
    await completeSoloSetup(page)
    const dialog = await openSettingsAt(page, 'admin-network')
    const row = dialog.locator('[data-setting-id="trusted_proxy_ranges"]')

    await expect(row).toBeVisible()
    await expect(row.getByRole('alert')).toContainText('shared provider infrastructure')
    await row.getByRole('button', { name: /^Add/ }).click()
    await page.getByRole('menuitemcheckbox', { name: /AWS CloudFront/ }).click()
    await expect(row.getByLabel('Trusted proxy selector')).toHaveValue('cloudfront')
    await row.getByRole('button', { name: 'Apply' }).click()
    await expect(row.getByText('Trusted reverse proxies updated.')).toBeVisible()

    await dialog.getByRole('button', { name: 'Close' }).click()
    await expect(dialog).toBeHidden()
    const reopened = await openSettingsAt(page, 'admin-network')
    await expect(reopened.locator('[data-setting-id="trusted_proxy_ranges"]')
      .getByLabel('Trusted proxy selector')).toHaveValue('cloudfront')
  })
})
