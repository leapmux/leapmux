import { motion } from '../../src/styles/tokens'
import { expect, test } from './fixtures'
import { loginViaToken, openPreferencesDialog } from './helpers/ui'

test.describe('Preferences navigation', () => {
  test('opens via the menu and via Cmd/Ctrl+Comma', async ({ page, leapmuxServer }) => {
    await loginViaToken(page, leapmuxServer.adminToken)
    await page.goto('/')

    // Via the app menu.
    await openPreferencesDialog(page)
    await expect(page.getByTestId('preferences-nav-appearance')).toBeVisible()
    await page.getByRole('dialog', { name: 'Preferences' }).getByLabel('Close').click()
    await expect(page.getByRole('dialog', { name: 'Preferences' })).not.toBeVisible()

    // Via the keyboard shortcut (the platform modifier + comma).
    const mod = process.platform === 'darwin' ? 'Meta' : 'Control'
    await page.keyboard.press(`${mod}+Comma`)
    await expect(page.getByRole('dialog', { name: 'Preferences' })).toBeVisible()
  })

  test('walks categories through the tab list', async ({ page, leapmuxServer }) => {
    await loginViaToken(page, leapmuxServer.adminToken)
    await page.goto('/')
    await openPreferencesDialog(page, 'appearance')
    const dialog = page.getByRole('dialog', { name: 'Preferences' })

    await dialog.getByTestId('preferences-nav-notifications').click()
    await expect(dialog.getByText('Turn-end sound', { exact: true })).toBeVisible()

    await dialog.getByTestId('preferences-nav-terminal').click()
    await expect(dialog.getByText('Terminal renderer')).toBeVisible()

    await dialog.getByTestId('preferences-nav-advanced').click()
    await expect(dialog.getByText('Debug logging')).toBeVisible()

    // Arrow keys move through the tab list (roving tabindex contract).
    await page.keyboard.press('ArrowDown')
    await expect(dialog.getByTestId('preferences-nav-account')).toHaveAttribute('aria-selected', 'true')
  })

  // Every entry point asks for the same category, and the open state is one
  // signal -- so a repeat request wrote the same value, notified nothing, and
  // left the dialog on whatever section the user had walked to.
  test('returns to the requested section when asked for again while open', async ({ page, leapmuxServer }) => {
    await loginViaToken(page, leapmuxServer.adminToken)
    await page.goto('/')
    await openPreferencesDialog(page, 'appearance')
    const dialog = page.getByRole('dialog', { name: 'Preferences' })

    await dialog.getByTestId('preferences-nav-advanced').click()
    await expect(dialog.getByTestId('preferences-nav-advanced')).toHaveAttribute('aria-selected', 'true')

    const mod = process.platform === 'darwin' ? 'Meta' : 'Control'
    await page.keyboard.press(`${mod}+Comma`)
    await expect(dialog.getByTestId('preferences-nav-appearance')).toHaveAttribute('aria-selected', 'true')
    await expect(dialog.getByTestId('preferences-nav-advanced')).toHaveAttribute('aria-selected', 'false')
  })

  test('searching "volume" shows the two notifications rows with breadcrumbs', async ({ page, leapmuxServer }) => {
    await loginViaToken(page, leapmuxServer.adminToken)
    await page.goto('/')
    await openPreferencesDialog(page, 'appearance')
    const dialog = page.getByRole('dialog', { name: 'Preferences' })

    const search = dialog.getByTestId('preferences-search')
    await search.fill('volume')

    // Navigation hides while searching; the results panel takes over.
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
    await openPreferencesDialog(page, 'appearance')
    const dialog = page.getByRole('dialog', { name: 'Preferences' })

    const search = dialog.getByTestId('preferences-search')
    await search.fill('volume')
    await expect(dialog.getByTestId('preferences-search-results')).toBeVisible()

    await search.press('Escape')
    await expect(search).toHaveValue('')
    // Outlast `Dialog`'s exit animation before reading the dialog: it defers
    // the unmount, so a check here also passes on a dialog that IS closing.
    await page.waitForTimeout(motion.fast * 3)
    await expect(dialog, 'Escape closed the dialog instead of clearing the search').toBeVisible()

    // An empty query holds nothing back, so the same key now reaches the dialog.
    await search.press('Escape')
    await expect(dialog).toBeHidden()
  })
})
