import { expect, test } from './fixtures'
import { loginViaAPI, TEST_ADMIN_PASSWORD, TEST_ADMIN_USERNAME } from './helpers/api'
import { loginViaToken, openSettingsAt } from './helpers/ui'

test.describe('Preferences administration groups', () => {
  test('admin sees the administration groups and can change session duration', async ({ page, leapmuxServer }) => {
    await loginViaToken(page, leapmuxServer.adminToken)
    await page.goto('/')
    const dialog = await openSettingsAt(page)

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

    await dialog.getByLabel('Close').click()
    await expect(dialog).not.toBeVisible()
    const reopened = await openSettingsAt(page, 'admin-general')
    await expect(reopened.locator('[data-setting-id="session_duration_seconds"] input[type="number"]')).toHaveValue('7200')

    // Reset back to the default so the change cannot leak into a later test
    // on this worker's shared hub.
    await reopened.locator('[data-testid="setting-reset-session_duration_seconds"]').click()
    await expect(reopened.locator('[data-setting-id="session_duration_seconds"] input[type="number"]')).toHaveValue('604800')
  })

  /**
   * A dev instance holds sign-up open while the hub stores no value, so
   * this row is where the configured value and the enforced value differ:
   * the switch carries the configured default (closed) and the note carries
   * what the hub applies (open). The note uses the switch's own
   * vocabulary -- "On", never the JSON literal `true` the wire carries.
   */
  test('states an enforced toggle in the words of its switch', async ({ page, leapmuxServer }) => {
    await loginViaToken(page, leapmuxServer.adminToken)
    await page.goto('/')
    const dialog = await openSettingsAt(page, 'admin-signup')

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
    const dialog = await openSettingsAt(page, 'admin-advanced')

    const row = dialog.locator('[data-setting-id="queue_budget.relay_bytes"]')
    await expect(row).toBeVisible()
    await expect(row.locator('input[type="number"]')).toHaveValue('0')
    await expect(row).toContainText(/Currently in effect: \d+ bytes/)
  })

  /**
   * A hub-settings write changes what GetSystemInfo answers, and the page
   * fetches that once at bootstrap.
   *
   * public_url is the sharpest case: it decides which browser origin the hub
   * runs passkey ceremonies for. An administrator who published the hub's URL
   * watched Add passkey stay disabled -- on the very screen that just
   * accepted the change -- until a page reload, with nothing to say why.
   *
   * The test drives the two sections the way a person does, because the store
   * is what re-reads the snapshot: a write made through the API would not
   * exercise the trigger at all. It NEVER reloads the page, which is the
   * whole assertion.
   */
  test('a public URL change reaches the passkey affordance without a reload', async ({ page, leapmuxServer }) => {
    await loginViaToken(page, leapmuxServer.adminToken)
    await page.goto('/')
    const dialog = await openSettingsAt(page, 'account')

    // Scoped to the passkeys row: the verified-session banner at the top of
    // the Account section is a `role="alert"` too, so an unscoped alert
    // locator matches two elements and fails strict mode.
    const passkeyRow = dialog.locator('[data-setting-id="account.passkeys"]')
    // The listing controls the button too, so settle on the enabled state
    // before changing anything.
    const addPasskey = passkeyRow.getByRole('button', { name: 'Add passkey' })
    await expect(addPasskey).toBeEnabled()

    await dialog.getByTestId('preferences-nav-admin-general').click()
    const row = dialog.locator('[data-setting-id="public_url"]')
    await expect(row).toBeVisible()
    const input = row.locator('input[type="text"]')
    // An address this browser is certainly not on. The control commits on
    // change (blur or Enter), never per keystroke.
    await input.fill('https://hub.invalid')
    await input.press('Enter')
    await expect(row.getByText('Customized')).toBeVisible()

    await dialog.getByTestId('preferences-nav-account').click()
    await expect(addPasskey).toBeDisabled()
    await expect(passkeyRow.getByRole('alert')).toContainText(/configured URL/i)

    // And back, so the affordance follows the setting in both directions --
    // and so the change cannot leak into a later test on this worker's
    // shared hub.
    await dialog.getByTestId('preferences-nav-admin-general').click()
    await dialog.getByTestId('setting-reset-public_url').click()
    await expect(row.getByText('Customized')).toBeHidden()

    await dialog.getByTestId('preferences-nav-account').click()
    await expect(addPasskey).toBeEnabled()
  })

  test('non-admins do not see the administration groups', async ({ page, leapmuxServer }) => {
    await loginViaToken(page, leapmuxServer.newuserToken)
    await page.goto('/')
    const dialog = await openSettingsAt(page)

    await expect(dialog.getByText('ADMINISTRATION')).not.toBeVisible()
    await expect(dialog.getByTestId('preferences-nav-admin-general')).not.toBeVisible()
    // User categories are still there.
    await expect(dialog.getByTestId('preferences-nav-appearance')).toBeVisible()
  })
})

/**
 * A hub setting is deployment-wide, and several of these keys ARE the hub's
 * security controls: sign-up, captcha, the rate limits, SMTP, and the
 * public_url the passkey relying party derives from. Renaming yourself asked
 * for a proven factor while opening the hub to the public internet asked for
 * nothing.
 *
 * Only a browser shows the half that matters to an operator: the refusal
 * arrives as a PROMPT they can answer, and the write they started succeeds
 * after they do.
 */
test.describe('administration settings need a verified session', () => {
  /**
   * A FRESH admin session, never elevated.
   *
   * The fixture elevates the worker's shared `adminToken` -- every other
   * spec here writes settings through it -- so a test about the gate has to
   * mint its own, exactly as 006-passkey and 143-cli-elevation do.
   */
  async function unelevatedAdminSession(hubUrl: string): Promise<string> {
    return loginViaAPI(hubUrl, TEST_ADMIN_USERNAME, TEST_ADMIN_PASSWORD)
  }

  test('prompts on the first write and applies it once the user verifies', async ({ page, leapmuxServer }) => {
    await loginViaToken(page, await unelevatedAdminSession(leapmuxServer.hubUrl))
    await page.goto('/')
    const dialog = await openSettingsAt(page, 'admin-general')

    const row = dialog.locator('[data-setting-id="session_duration_seconds"]')
    await expect(row).toBeVisible()
    // Nothing is verified yet, so the panel carries no verified-session state.
    await expect(dialog.getByTestId('elevation-status')).toHaveCount(0)

    const input = row.locator('input[type="number"]')
    await input.fill('7200')
    await input.press('Enter')

    // The hub refuses the un-elevated session; the transport prompts.
    const verify = page.getByRole('dialog', { name: 'Verify your identity' })
    await expect(verify).toBeVisible()
    await verify.getByTestId('elevate-password').fill(TEST_ADMIN_PASSWORD)
    await verify.getByTestId('elevate-password-submit').click()

    // The refused write ran again and succeeded, with no second click.
    await expect(row.getByText('Customized')).toBeVisible()
    await expect(page.getByRole('dialog', { name: 'Verify your identity' })).toHaveCount(0)

    // And the panel now says the session is verified, at the TOP of the group
    // -- the state used to live inside one account editor, so no
    // ADMINISTRATION panel ever showed it.
    await expect(dialog.getByTestId('elevation-status')).toBeVisible()

    // A SECOND write in the same window: no second prompt.
    await dialog.locator('[data-testid="setting-reset-session_duration_seconds"]').click()
    await expect(input).toHaveValue('604800')
    await expect(page.getByRole('dialog', { name: 'Verify your identity' })).toHaveCount(0)
  })

  // "End now" is the control the docs point at for "I am stepping away from a
  // shared machine", and it must be reachable from the panel the operator is
  // actually on.
  test('ends the window from the administration panel', async ({ page, leapmuxServer }) => {
    await loginViaToken(page, leapmuxServer.adminToken)
    await page.goto('/')
    const dialog = await openSettingsAt(page, 'admin-general')

    await expect(dialog.getByTestId('elevation-status')).toBeVisible()
    await dialog.getByTestId('elevation-drop').click()
    await expect(dialog.getByTestId('elevation-status')).toHaveCount(0)

    // Re-elevate, so this test leaves the worker's shared session as the
    // fixture made it, and the hub does not refuse a later spec's settings
    // write.
    const row = dialog.locator('[data-setting-id="session_duration_seconds"]')
    const input = row.locator('input[type="number"]')
    await input.fill('7200')
    await input.press('Enter')
    const verify = page.getByRole('dialog', { name: 'Verify your identity' })
    await expect(verify).toBeVisible()
    await verify.getByTestId('elevate-password').fill(TEST_ADMIN_PASSWORD)
    await verify.getByTestId('elevate-password-submit').click()
    await expect(row.getByText('Customized')).toBeVisible()
    await dialog.locator('[data-testid="setting-reset-session_duration_seconds"]').click()
    await expect(input).toHaveValue('604800')
  })
})
