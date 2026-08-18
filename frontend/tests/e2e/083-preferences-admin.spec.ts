import { expect, test } from './fixtures'
import { loginViaToken, openPreferencesDialog } from './helpers/ui'

test.describe('Preferences administration groups', () => {
  test('admin sees the administration groups and can change session duration', async ({ page, leapmuxServer }) => {
    await loginViaToken(page, leapmuxServer.adminToken)
    await page.goto('/')
    await openPreferencesDialog(page)
    const dialog = page.getByRole('dialog', { name: 'Preferences' })

    await expect(dialog.getByText('ADMINISTRATION')).toBeVisible()
    await expect(dialog.getByTestId('preferences-nav-admin-general')).toBeVisible()
    await expect(dialog.getByTestId('preferences-nav-admin-email')).toBeVisible()

    await dialog.getByTestId('preferences-nav-admin-general').click()
    const row = dialog.locator('[data-setting-id="session_duration_seconds"]')
    await expect(row).toBeVisible()

    // The hub applies the write server-first: fill, wait for the store to
    // converge on the reply, then reopen the dialog and read it back.
    const input = row.locator('input[type="number"]')
    await input.fill('7200')
    await input.press('Enter') /* the control commits on the DOM change event */
    await expect(row.getByText('Customized')).toBeVisible()

    await page.getByRole('dialog', { name: 'Preferences' }).getByLabel('Close').click()
    await expect(page.getByRole('dialog', { name: 'Preferences' })).not.toBeVisible()
    await openPreferencesDialog(page, 'admin-general')
    const reopened = page.getByRole('dialog', { name: 'Preferences' })
    await expect(reopened.locator('[data-setting-id="session_duration_seconds"] input[type="number"]')).toHaveValue('7200')

    // Reset back to the default so the change cannot leak into a later test
    // on this worker's shared hub.
    await reopened.locator('[data-testid="setting-reset-session_duration_seconds"]').click()
    await expect(reopened.locator('[data-setting-id="session_duration_seconds"] input[type="number"]')).toHaveValue('604800')
  })

  /**
   * A dev instance holds sign-up open while nothing is stored, so this row
   * is where the configured value and the enforced value differ: the
   * switch carries the configured default (closed) and the note carries
   * what the hub applies (open). The note speaks the switch's own
   * vocabulary -- "On", never the JSON literal `true` the wire carries.
   */
  test('states an enforced toggle in the words of its switch', async ({ page, leapmuxServer }) => {
    await loginViaToken(page, leapmuxServer.adminToken)
    await page.goto('/')
    await openPreferencesDialog(page, 'admin-signup')
    const dialog = page.getByRole('dialog', { name: 'Preferences' })

    const row = dialog.locator('[data-setting-id="signup_enabled"]')
    await expect(row).toBeVisible()
    await expect(row.getByRole('switch', { name: 'Open sign-up' })).not.toBeChecked()
    await expect(row).toContainText('Currently in effect: On')
  })

  /**
   * The other half of the note: a number carries the unit its control
   * shows. A queue budget of 0 auto-sizes, so the configured field reads 0
   * and the hub reports the byte count it settled on at startup.
   *
   * The figure itself comes from the process memory limit of the machine
   * that runs the test, so the assertion pins the SHAPE -- a figure, a
   * space, and the unit -- and not the number.
   */
  test('states an enforced number with the unit of its control', async ({ page, leapmuxServer }) => {
    await loginViaToken(page, leapmuxServer.adminToken)
    await page.goto('/')
    await openPreferencesDialog(page, 'admin-advanced')
    const dialog = page.getByRole('dialog', { name: 'Preferences' })

    const row = dialog.locator('[data-setting-id="queue_budget.relay_bytes"]')
    await expect(row).toBeVisible()
    await expect(row.locator('input[type="number"]')).toHaveValue('0')
    await expect(row).toContainText(/Currently in effect: \d+ bytes/)
  })

  test('non-admins do not see the administration groups', async ({ page, leapmuxServer }) => {
    await loginViaToken(page, leapmuxServer.newuserToken)
    await page.goto('/')
    await openPreferencesDialog(page)
    const dialog = page.getByRole('dialog', { name: 'Preferences' })

    await expect(dialog.getByText('ADMINISTRATION')).not.toBeVisible()
    await expect(dialog.getByTestId('preferences-nav-admin-general')).not.toBeVisible()
    // User categories are still there.
    await expect(dialog.getByTestId('preferences-nav-appearance')).toBeVisible()
  })
})
