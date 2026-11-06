import { expect } from '@playwright/test'
import { test } from './fixtures'
import { expandSidebarSection, openAppAs, sidebarSectionHeader } from './helpers/ui'

// The registration dialog's two-step CLI: the registration command and
// the start command are SEPARATE commands with separate copy actions,
// the shell pills switch the quoting, and the register command never
// starts the worker by itself.
test.describe('registration dialog CLI', () => {
  test('renders register then start commands for both shells', async ({ page, leapmuxServer }) => {
    await openAppAs(page, leapmuxServer.adminToken)
    const workersSection = sidebarSectionHeader(page, 'workers')
    await expandSidebarSection(workersSection)
    await page.getByTestId('sidebar-register-worker').click()

    const command = page.getByTestId('registration-command')
    await expect(command).toContainText('leapmux worker register --hub')
    await expect(command).toContainText('--registration-key')
    await expect(page.getByTestId('start-command')).toHaveText('leapmux worker start')

    // Registration and startup stay separate: the register command
    // never starts the worker by itself.
    await expect(command).not.toContainText(' start')

    // The shell pills switch the quoting.
    await page.getByRole('radio', { name: 'PowerShell' }).click()
    await expect(command).toContainText('--hub \'')
  })

  test('offers separate copy actions and keeps the key valid while open', async ({ page, leapmuxServer }) => {
    await openAppAs(page, leapmuxServer.adminToken)
    const workersSection = sidebarSectionHeader(page, 'workers')
    await expandSidebarSection(workersSection)
    await page.getByTestId('sidebar-register-worker').click()

    await expect(page.getByTestId('copy-registration-command')).toBeEnabled()
    await expect(page.getByTestId('copy-start-command')).toBeEnabled()
    await expect(commandLocator(page)).toContainText('--registration-key')
  })
})

function commandLocator(page: import('@playwright/test').Page) {
  return page.getByTestId('registration-command')
}
