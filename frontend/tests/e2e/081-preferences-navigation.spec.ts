import { motion } from '../../src/styles/tokens'
import { expect, test } from './fixtures'
import { loginViaToken, openSettingsAt } from './helpers/ui'

test.describe('Preferences navigation', () => {
  test('opens via the menu and via Cmd/Ctrl+Comma', async ({ page, leapmuxServer }) => {
    await loginViaToken(page, leapmuxServer.adminToken)
    await page.goto('/')

    // Via the app menu.
    const dialog = await openSettingsAt(page)
    await expect(page.getByTestId('preferences-nav-appearance')).toBeVisible()
    await dialog.getByLabel('Close').click()
    await expect(dialog).not.toBeVisible()

    // Via the keyboard shortcut (the platform modifier + comma).
    const mod = process.platform === 'darwin' ? 'Meta' : 'Control'
    await page.keyboard.press(`${mod}+Comma`)
    await expect(page.getByRole('dialog', { name: 'Preferences' })).toBeVisible()
  })

  test('walks categories through the tab list', async ({ page, leapmuxServer }) => {
    await loginViaToken(page, leapmuxServer.adminToken)
    await page.goto('/')
    const dialog = await openSettingsAt(page, 'appearance')

    await dialog.getByTestId('preferences-nav-notifications').click()
    await expect(dialog.getByText('Turn-end sound', { exact: true })).toBeVisible()

    await dialog.getByTestId('preferences-nav-terminal').click()
    await expect(dialog.getByText('Terminal renderer')).toBeVisible()

    await dialog.getByTestId('preferences-nav-advanced').click()
    await expect(dialog.getByText('Debug logging')).toBeVisible()

    // Arrow keys move through the tab list (roving tabindex contract).
    // Advanced is the LAST user category, so the next tab is the first
    // ADMINISTRATION one for this admin session.
    await page.keyboard.press('ArrowDown')
    await expect(dialog.getByTestId('preferences-nav-admin-general')).toHaveAttribute('aria-selected', 'true')

    // ArrowUp from Appearance lands on APPS, and Apps on Account: the two
    // lead the list in that order, because Account holds the apps you
    // AUTHORIZED and Apps the ones you REGISTERED — the same errand one step
    // further out. Walking both steps is what pins the order rather than only
    // the head of the list.
    await dialog.getByTestId('preferences-nav-appearance').click()
    await page.keyboard.press('ArrowUp')
    await expect(dialog.getByTestId('preferences-nav-apps')).toHaveAttribute('aria-selected', 'true')
    await page.keyboard.press('ArrowUp')
    await expect(dialog.getByTestId('preferences-nav-account')).toHaveAttribute('aria-selected', 'true')
  })

  // Every entry point asks for the same category, and the open state is the
  // ADDRESS now -- so a repeat request replaces the `?prefs=` value, and the
  // dialog follows it back to the requested section rather than staying on
  // whatever section the user walked to. It used to be a private signal, where
  // a repeat wrote the same value and notified nothing.
  test('returns to the requested section when asked for again while open', async ({ page, leapmuxServer }) => {
    await loginViaToken(page, leapmuxServer.adminToken)
    await page.goto('/')
    const dialog = await openSettingsAt(page, 'appearance')

    await dialog.getByTestId('preferences-nav-advanced').click()
    await expect(dialog.getByTestId('preferences-nav-advanced')).toHaveAttribute('aria-selected', 'true')

    // The shortcut asks for the dialog with no section, which selects the
    // first one -- Account.
    const mod = process.platform === 'darwin' ? 'Meta' : 'Control'
    await page.keyboard.press(`${mod}+Comma`)
    await expect(dialog.getByTestId('preferences-nav-account')).toHaveAttribute('aria-selected', 'true')
    await expect(dialog.getByTestId('preferences-nav-advanced')).toHaveAttribute('aria-selected', 'false')
  })

  // Every entry point asks for the dialog and nothing more, so the section it
  // selects is the dialog's own default: the first one in the list.
  test('opens on Account', async ({ page, leapmuxServer }) => {
    await loginViaToken(page, leapmuxServer.adminToken)
    await page.goto('/')
    const dialog = await openSettingsAt(page)

    await expect(dialog.getByTestId('preferences-nav-account')).toHaveAttribute('aria-selected', 'true')
    await expect(dialog.locator('[data-setting-id="account.profile"]')).toBeVisible()
  })

  test('searching "volume" shows the two notifications rows with breadcrumbs', async ({ page, leapmuxServer }) => {
    await loginViaToken(page, leapmuxServer.adminToken)
    await page.goto('/')
    const dialog = await openSettingsAt(page, 'appearance')

    const search = dialog.getByTestId('preferences-search')
    await search.fill('volume')

    // The navigation hides during a search; the results panel replaces it.
    await expect(dialog.getByTestId('preferences-nav-appearance')).not.toBeVisible()
    await expect(dialog.getByTestId('preferences-search-results')).toBeVisible()

    // Turn-end sound (keyword match) and Turn-end volume (label match), each
    // with the Notifications breadcrumb.
    await expect(dialog.getByText('Notifications \u203A Turn-end sound')).toBeVisible()
    await expect(dialog.getByText('Notifications \u203A Turn-end volume')).toBeVisible()

    // Jumping to a hit clears the search and selects the category.
    await dialog.getByText('Notifications \u203A Turn-end volume').click()
    await expect(dialog.getByTestId('preferences-nav-notifications')).toBeVisible()
    await expect(dialog.getByText('Turn-end volume')).toBeVisible()
  })

  // Escape dismisses ONE layer, innermost first. The search box is the inner
  // one while it holds a query, and `PreferencesSearch` says so in code -- it
  // consumes the key and lets only an EMPTY query through to the dialog. A
  // global Escape binding used to run first and close the dialog either way,
  // so the search box never got the press it claimed.
  test('clears the search on Escape, and closes the dialog only once the query is empty', async ({ page, leapmuxServer }) => {
    await loginViaToken(page, leapmuxServer.adminToken)
    await page.goto('/')
    const dialog = await openSettingsAt(page, 'appearance')

    const search = dialog.getByTestId('preferences-search')
    await search.fill('volume')
    await expect(dialog.getByTestId('preferences-search-results')).toBeVisible()

    await search.press('Escape')
    await expect(search).toHaveValue('')
    // Outlast `Dialog`'s exit animation before reading the dialog: it defers
    // the unmount, so a check here also passes on a dialog whose exit
    // animation still runs.
    await page.waitForTimeout(motion.fast * 3)
    await expect(dialog, 'Escape closed the dialog instead of clearing the search').toBeVisible()

    // An empty query blocks nothing, so the same key now reaches the dialog.
    await search.press('Escape')
    await expect(dialog).toBeHidden()
  })
})
